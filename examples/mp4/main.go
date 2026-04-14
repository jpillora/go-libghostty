// Example mp4 spawns a command in a pty, feeds its output into a
// libghostty VT, and records terminal frames as a directory of JPEGs
// with delta encoding and a JSON manifest. An embedded HTML/JS canvas
// player is also written for browser playback.
//
// No external tools — font rendering uses golang.org/x/image, JPEG
// encoding and the player use the Go stdlib.
//
// Output structure:
//
//	<outdir>/
//	  manifest.json    — frame list with timestamps, dimensions, patch coords
//	  000.jpg          — full keyframe
//	  001.jpg          — delta patch (only changed region)
//	  ...
//	  player.html      — self-contained browser player
//
// Usage:
//
//	go run ./examples/mp4 [-o rec] [-fps 10] [-cols 100] [-rows 30] <cmd> [args...]
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ghostty "github.com/mitchellh/go-libghostty"
	"github.com/creack/pty"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// manifestFrame is one entry in manifest.json.
type manifestFrame struct {
	Time float64 `json:"t"`           // seconds since start
	File string  `json:"file"`        // JPEG filename
	W    int     `json:"w,omitempty"` // full frame width (omitted for patches)
	H    int     `json:"h,omitempty"` // full frame height (omitted for patches)
	X    int     `json:"x,omitempty"` // patch X offset (0 for full frames)
	Y    int     `json:"y,omitempty"` // patch Y offset (0 for full frames)
}

func main() {
	defaultFont, err := findGhosttyFont("JetBrainsMono[wght].ttf")
	check(err)

	var defaultFallbacks []string
	if p, err := findGhosttyFont("SymbolsNerdFontMono-Regular.ttf"); err == nil {
		defaultFallbacks = append(defaultFallbacks, p)
	}
	for _, p := range []string{
		"/usr/share/fonts/truetype/ancient-scripts/Symbola_hint.ttf",
		"/usr/share/fonts/opentype/unifont/unifont.otf",
	} {
		if _, err := os.Stat(p); err == nil {
			defaultFallbacks = append(defaultFallbacks, p)
		}
	}

	var (
		outDir    = flag.String("o", "rec", "output directory")
		cols      = flag.Int("cols", 100, "terminal columns")
		rows      = flag.Int("rows", 30, "terminal rows")
		fps       = flag.Int("fps", 10, "frames per second")
		idle      = flag.Duration("idle", 5*time.Second, "stop after this long with no pty output")
		deadline  = flag.Duration("deadline", 120*time.Second, "hard deadline for recording")
		fontPath  = flag.String("font", defaultFont, "primary monospace font")
		fallbacks = flag.String("font-fallback", strings.Join(defaultFallbacks, ":"), "colon-separated fallback font chain")
		fontSize  = flag.Float64("size", 13, "font size in points")
		padding   = flag.Int("pad", 8, "pixel padding around terminal")
		quality   = flag.Int("quality", 75, "JPEG quality (1-100)")
	)
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: mp4 [flags] <cmd> [args...]")
		os.Exit(2)
	}

	check(os.MkdirAll(*outDir, 0755))

	face, cellW, cellH, ascent := loadFont(*fontPath, *fontSize)
	var fallbackFaces []font.Face
	for _, p := range strings.Split(*fallbacks, ":") {
		if p == "" {
			continue
		}
		f, _, _, _ := loadFont(p, *fontSize)
		fallbackFaces = append(fallbackFaces, f)
	}

	// --- Terminal
	term, err := ghostty.NewTerminal(
		ghostty.WithSize(uint16(*cols), uint16(*rows)),
		ghostty.WithMaxScrollback(1000),
	)
	check(err)
	defer term.Close()

	check(term.SetColorBackground(&ghostty.ColorRGB{R: 0x1e, G: 0x1e, B: 0x2e}))
	check(term.SetColorForeground(&ghostty.ColorRGB{R: 0xcd, G: 0xd6, B: 0xf4}))

	// --- Image canvases.
	imgW := *cols*cellW + 2*(*padding)
	imgH := *rows*cellH + 2*(*padding)
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	prevImg := image.NewRGBA(image.Rect(0, 0, imgW, imgH)) // previous frame for delta

	// --- Spawn command in a pty.
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		fmt.Sprintf("COLUMNS=%d", *cols),
		fmt.Sprintf("LINES=%d", *rows),
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(*rows), Cols: uint16(*cols)})
	check(err)
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// --- Read pty output in a goroutine.
	ptyData := make(chan []byte, 256)
	ptyDone := make(chan struct{})
	go func() {
		defer close(ptyDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				ptyData <- cp
			}
			if err != nil {
				return
			}
		}
	}()

	// --- Render iterators.
	rs, err := ghostty.NewRenderState()
	check(err)
	defer rs.Close()

	ri, err := ghostty.NewRenderStateRowIterator()
	check(err)
	defer ri.Close()

	rc, err := ghostty.NewRenderStateRowCells()
	check(err)
	defer rc.Close()

	// --- Frame loop.
	frameDur := time.Duration(float64(time.Second) / float64(*fps))
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()

	var manifest []manifestFrame
	var jpegBuf bytes.Buffer
	startTime := time.Now()
	lastOutput := time.Now()
	totalPtyBytes := 0
	totalFileBytes := 0
	frameIdx := 0
	hasFirst := false
	ptyFinished := false

	for range ticker.C {
		// Drain pending pty data.
		draining := true
		for draining {
			select {
			case data := <-ptyData:
				term.VTWrite(data)
				totalPtyBytes += len(data)
				lastOutput = time.Now()
			case <-ptyDone:
				ptyFinished = true
				draining = false
			default:
				draining = false
			}
		}
		if ptyFinished {
			for {
				select {
				case data := <-ptyData:
					term.VTWrite(data)
					totalPtyBytes += len(data)
					lastOutput = time.Now()
				default:
					goto drained
				}
			}
		drained:
		}

		// Render frame.
		check(rs.Update(term))
		colors, err := rs.Colors()
		check(err)
		renderFrame(img, rs, ri, rc, colors, *cols, *rows, *padding, cellW, cellH, ascent, face, fallbackFaces)

		t := time.Since(startTime).Seconds()

		if !hasFirst {
			// First frame is always a full keyframe.
			fname := fmt.Sprintf("%03d.jpg", frameIdx)
			jpegBuf.Reset()
			check(jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: *quality}))
			check(os.WriteFile(filepath.Join(*outDir, fname), jpegBuf.Bytes(), 0644))
			manifest = append(manifest, manifestFrame{
				Time: t, File: fname, W: imgW, H: imgH,
			})
			totalFileBytes += jpegBuf.Len()
			frameIdx++
			copy(prevImg.Pix, img.Pix)
			hasFirst = true
		} else if pixelsEqual(img.Pix, prevImg.Pix) {
			// Identical frame — skip.
		} else {
			// Compute changed bounding box.
			x0, y0, x1, y1 := diffBounds(prevImg, img)
			// Encode full frame JPEG for comparison.
			jpegBuf.Reset()
			check(jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: *quality}))
			fullSize := jpegBuf.Len()
			fullData := make([]byte, fullSize)
			copy(fullData, jpegBuf.Bytes())

			// Encode delta patch JPEG.
			patch := img.SubImage(image.Rect(x0, y0, x1, y1))
			jpegBuf.Reset()
			check(jpeg.Encode(&jpegBuf, patch, &jpeg.Options{Quality: *quality}))
			patchSize := jpegBuf.Len()

			fname := fmt.Sprintf("%03d.jpg", frameIdx)

			// Use delta if it saves >30% over full frame.
			if patchSize < fullSize*70/100 {
				check(os.WriteFile(filepath.Join(*outDir, fname), jpegBuf.Bytes(), 0644))
				manifest = append(manifest, manifestFrame{
					Time: t, File: fname,
					X: x0, Y: y0,
				})
				totalFileBytes += patchSize
			} else {
				check(os.WriteFile(filepath.Join(*outDir, fname), fullData, 0644))
				manifest = append(manifest, manifestFrame{
					Time: t, File: fname,
					W: imgW, H: imgH,
				})
				totalFileBytes += fullSize
			}

			// Periodically emit a full keyframe for seek support.
			// Every 5 seconds of wall time, force a full frame.
			if len(manifest) > 1 {
				last := manifest[len(manifest)-1]
				if last.W == 0 { // was a patch
					prevFull := 0.0
					for i := len(manifest) - 2; i >= 0; i-- {
						if manifest[i].W > 0 {
							prevFull = manifest[i].Time
							break
						}
					}
					if t-prevFull >= 5.0 {
						// Re-emit as full keyframe.
						check(os.WriteFile(filepath.Join(*outDir, fname), fullData, 0644))
						manifest[len(manifest)-1] = manifestFrame{
							Time: t, File: fname,
							W: imgW, H: imgH,
						}
						totalFileBytes += fullSize - patchSize
					}
				}
			}

			frameIdx++
			copy(prevImg.Pix, img.Pix)
		}

		if frameIdx%(*fps) == 0 && frameIdx > 0 {
			elapsed := time.Since(startTime).Round(time.Second)
			fmt.Fprintf(os.Stderr, "\r%s  %d files  %dKB  %d pty bytes",
				elapsed, frameIdx, totalFileBytes/1024, totalPtyBytes)
		}

		// Exit conditions.
		elapsed := time.Since(startTime)
		sinceOutput := time.Since(lastOutput)
		if ptyFinished && sinceOutput >= *idle {
			break
		}
		if !ptyFinished && totalPtyBytes > 0 && sinceOutput >= *idle {
			break
		}
		if elapsed >= *deadline {
			break
		}
	}

	// Write manifest.
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(*outDir, "manifest.json"), manifestData, 0644))

	// Write self-contained HTML player.
	check(writePlayer(*outDir, manifest, *quality))

	duration := time.Duration(len(manifest)) * frameDur
	if len(manifest) > 1 {
		duration = time.Duration(manifest[len(manifest)-1].Time * float64(time.Second))
	}
	fmt.Fprintf(os.Stderr, "\nwrote %d frames (%s, %dKB total, %d pty bytes) to %s/\n",
		frameIdx, duration.Round(time.Millisecond), totalFileBytes/1024, totalPtyBytes, *outDir)
}

// ---------------------------------------------------------------------------
// Delta detection
// ---------------------------------------------------------------------------

func pixelsEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffBounds returns the bounding box of pixels that differ between a and b.
func diffBounds(a, b *image.RGBA) (x0, y0, x1, y1 int) {
	w := a.Bounds().Dx()
	h := a.Bounds().Dy()
	x0, y0 = w, h
	x1, y1 = 0, 0

	for y := 0; y < h; y++ {
		off := y * a.Stride
		for x := 0; x < w; x++ {
			i := off + x*4
			if a.Pix[i] != b.Pix[i] ||
				a.Pix[i+1] != b.Pix[i+1] ||
				a.Pix[i+2] != b.Pix[i+2] ||
				a.Pix[i+3] != b.Pix[i+3] {
				if x < x0 {
					x0 = x
				}
				if x+1 > x1 {
					x1 = x + 1
				}
				if y < y0 {
					y0 = y
				}
				if y+1 > y1 {
					y1 = y + 1
				}
			}
		}
	}
	if x0 >= x1 || y0 >= y1 {
		return 0, 0, w, h // shouldn't happen if not equal
	}
	return
}

// ---------------------------------------------------------------------------
// HTML player — reads JPEGs as base64 data URIs for a self-contained file.
// ---------------------------------------------------------------------------

func writePlayer(dir string, manifest []manifestFrame, quality int) error {
	type inlineFrame struct {
		Time    float64 `json:"t"`
		DataURI string  `json:"d"`
		W       int     `json:"w,omitempty"`
		H       int     `json:"h,omitempty"`
		X       int     `json:"x,omitempty"`
		Y       int     `json:"y,omitempty"`
	}

	frames := make([]inlineFrame, len(manifest))
	for i, m := range manifest {
		data, err := os.ReadFile(filepath.Join(dir, m.File))
		if err != nil {
			return err
		}
		frames[i] = inlineFrame{
			Time:    m.Time,
			DataURI: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
			W:       m.W,
			H:       m.H,
			X:       m.X,
			Y:       m.Y,
		}
	}

	framesJSON, err := json.Marshal(frames)
	if err != nil {
		return err
	}

	html := `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>terminal recording</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { background: #11111b; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 100vh; font-family: monospace; color: #cdd6f4; }
canvas { border: 1px solid #45475a; border-radius: 6px; max-width: 95vw; }
.controls { margin-top: 12px; display: flex; gap: 12px; align-items: center; font-size: 13px; }
button { background: #313244; color: #cdd6f4; border: 1px solid #45475a; border-radius: 4px; padding: 4px 12px; cursor: pointer; font-family: monospace; font-size: 13px; }
button:hover { background: #45475a; }
input[type=range] { width: 200px; }
.time { color: #6c7086; min-width: 80px; text-align: center; }
</style>
</head>
<body>
<canvas id="c"></canvas>
<div class="controls">
  <button id="play">pause</button>
  <input id="scrub" type="range" min="0" max="1000" value="0">
  <span class="time" id="time">0:00</span>
  <button id="speed">1x</button>
</div>
<script>
const F = ` + string(framesJSON) + `;
const canvas = document.getElementById('c');
const ctx = canvas.getContext('2d');
const playBtn = document.getElementById('play');
const scrub = document.getElementById('scrub');
const timeEl = document.getElementById('time');
const speedBtn = document.getElementById('speed');

// Preload all images.
const imgs = new Array(F.length);
let loaded = 0;
F.forEach((f, i) => {
  const img = new Image();
  img.onload = () => { loaded++; if (loaded === F.length) start(); };
  img.src = f.d;
  imgs[i] = img;
});

// Find canvas size from first full frame.
let cw = 0, ch = 0;
for (const f of F) { if (f.w) { cw = f.w; ch = f.h; break; } }
canvas.width = cw;
canvas.height = ch;

const duration = F.length > 0 ? F[F.length - 1].t + 0.5 : 0;
let playing = true;
let startT = 0;
let pausedAt = 0;
let playbackRate = 1;
const speeds = [1, 2, 4, 0.5];
let speedIdx = 0;

speedBtn.onclick = () => {
  speedIdx = (speedIdx + 1) % speeds.length;
  playbackRate = speeds[speedIdx];
  speedBtn.textContent = playbackRate + 'x';
  if (playing) startT = performance.now() - (pausedAt / playbackRate);
};

playBtn.onclick = () => {
  playing = !playing;
  playBtn.textContent = playing ? 'pause' : 'play';
  if (playing) startT = performance.now() - (pausedAt / playbackRate);
};

scrub.oninput = () => {
  const t = (scrub.value / 1000) * duration;
  pausedAt = t * 1000;
  if (playing) startT = performance.now() - (pausedAt / playbackRate);
  drawAt(t);
};

function drawAt(t) {
  // Find the latest keyframe at or before t, then apply patches forward.
  let keyIdx = 0;
  for (let i = F.length - 1; i >= 0; i--) {
    if (F[i].w && F[i].t <= t) { keyIdx = i; break; }
  }
  // Draw keyframe.
  const kf = F[keyIdx];
  if (kf.w !== cw || kf.h !== ch) { cw = kf.w; ch = kf.h; canvas.width = cw; canvas.height = ch; }
  ctx.drawImage(imgs[keyIdx], 0, 0);
  // Apply patches from keyIdx+1 up to current time.
  for (let i = keyIdx + 1; i < F.length && F[i].t <= t; i++) {
    const f = F[i];
    if (f.w) { ctx.drawImage(imgs[i], 0, 0); }
    else { ctx.drawImage(imgs[i], f.x, f.y); }
  }
  const m = Math.floor(t / 60);
  const s = Math.floor(t % 60);
  timeEl.textContent = m + ':' + String(s).padStart(2, '0');
  scrub.value = Math.floor((t / duration) * 1000);
}

function start() {
  startT = performance.now();
  function tick() {
    if (playing) {
      const elapsed = (performance.now() - startT) * playbackRate;
      pausedAt = elapsed;
      let t = elapsed / 1000;
      if (t >= duration) { t = 0; startT = performance.now(); pausedAt = 0; }
      drawAt(t);
    }
    requestAnimationFrame(tick);
  }
  requestAnimationFrame(tick);
}
</script>
</body>
</html>`

	return os.WriteFile(filepath.Join(dir, "player.html"), []byte(html), 0644)
}

// ---------------------------------------------------------------------------
// Terminal frame renderer
// ---------------------------------------------------------------------------

func renderFrame(
	img *image.RGBA,
	rs *ghostty.RenderState,
	ri *ghostty.RenderStateRowIterator,
	rc *ghostty.RenderStateRowCells,
	colors *ghostty.RenderStateColors,
	cols, rows, padding, cellW, cellH, ascent int,
	face font.Face,
	fallbackFaces []font.Face,
) {
	bg := rgbaFrom(colors.Background)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	check(rs.RowIterator(ri))

	row := 0
	for ri.Next() {
		if row >= rows {
			break
		}
		check(ri.Cells(rc))

		for col := 0; col < cols; col++ {
			check(rc.Select(uint16(col)))

			raw, err := rc.Raw()
			check(err)
			wide, err := raw.Wide()
			check(err)
			if wide == ghostty.CellWideSpacerTail {
				continue
			}
			cellCols := 1
			if wide == ghostty.CellWideWide {
				cellCols = 2
			}

			cellBg := colors.Background
			if c, err := rc.BgColor(); err == nil && c != nil {
				cellBg = *c
			}
			cellFg := colors.Foreground
			if c, err := rc.FgColor(); err == nil && c != nil {
				cellFg = *c
			}

			x0 := padding + col*cellW
			y0 := padding + row*cellH
			x1 := x0 + cellCols*cellW
			y1 := y0 + cellH

			if cellBg != colors.Background {
				draw.Draw(img, image.Rect(x0, y0, x1, y1), &image.Uniform{C: rgbaFrom(cellBg)}, image.Point{}, draw.Src)
			}

			graphemes, err := rc.Graphemes()
			check(err)
			if len(graphemes) == 0 {
				continue
			}
			r := rune(graphemes[0])

			style, err := rc.Style()
			check(err)

			drawFace := face
			if _, ok := face.GlyphAdvance(r); !ok {
				for _, fb := range fallbackFaces {
					if _, ok := fb.GlyphAdvance(r); ok {
						drawFace = fb
						break
					}
				}
			}

			drawer := &font.Drawer{
				Dst:  img,
				Src:  &image.Uniform{C: rgbaFrom(cellFg)},
				Face: drawFace,
				Dot:  fixed.P(x0, y0+ascent),
			}
			drawer.DrawString(string(r))

			if style.Underline() != ghostty.UnderlineNone {
				uy := y0 + ascent + 1
				if uy >= y1 {
					uy = y1 - 1
				}
				for x := x0; x < x1; x++ {
					img.Set(x, uy, rgbaFrom(cellFg))
				}
			}
		}
		row++
	}

	// Cursor overlay.
	cursorVis, err := rs.CursorVisible()
	if err == nil && cursorVis {
		if hasVal, err := rs.CursorViewportHasValue(); err == nil && hasVal {
			cx, _ := rs.CursorViewportX()
			cy, _ := rs.CursorViewportY()
			x0 := padding + int(cx)*cellW
			y0 := padding + int(cy)*cellH
			cursorColor := colors.Foreground
			if colors.CursorHasValue {
				cursorColor = colors.Cursor
			}
			cc := rgbaFrom(cursorColor)
			cc.A = 0x80
			draw.Draw(img, image.Rect(x0, y0, x0+cellW, y0+cellH),
				&image.Uniform{C: cc}, image.Point{}, draw.Over)
		}
	}
}

func rgbaFrom(c ghostty.ColorRGB) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff}
}

func loadFont(path string, size float64) (face font.Face, cellW, cellH, ascent int) {
	data, err := os.ReadFile(path)
	check(err)
	f, err := opentype.Parse(data)
	check(err)
	face, err = opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	check(err)
	m := face.Metrics()
	ascent = m.Ascent.Ceil()
	cellH = ascent + m.Descent.Ceil()
	adv, ok := face.GlyphAdvance('M')
	if !ok {
		adv = fixed.I(int(size * 0.6))
	}
	cellW = adv.Ceil()
	return
}

func findGhosttyFont(basename string) (string, error) {
	roots := []string{".zig-global-cache/p", "build/_deps/ghostty-src/.zig-cache/p"}
	for _, root := range roots {
		var found string
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !info.IsDir() && filepath.Base(p) == basename {
				found = p
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("ghostty font %q not found — run `make build` first", basename)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
