// Example render-cmd spawns a command in a pty, feeds its output into
// a libghostty VT, waits until it goes idle (no output for a short
// window), and rasterizes the resulting terminal grid into a PNG.
//
// Font + PNG pipeline is pure Go (golang.org/x/image/font/opentype +
// stdlib image/png). Pty handling is pure Go (github.com/creack/pty).
// The only cgo is what go-libghostty itself links against libghostty-vt.
//
// Usage:
//
//	render-cmd [-o out.png] [-cols N] [-rows N] [-idle 1s] <cmd> [args...]
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
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

func main() {
	// Ghostty's own bundled defaults. These land in the Zig build cache
	// during `make build` (see build/_deps/.../SharedDeps.zig). We locate
	// them by basename inside .zig-global-cache/p/ to avoid hard-coding
	// the hash-based subdirectory.
	defaultFont, err := findGhosttyFont("JetBrainsMono[wght].ttf")
	check(err)

	// Fallback chain for glyphs not in the primary. We start with
	// ghostty's own bundled Symbols Nerd Font (icons / powerline / etc),
	// then fall back to system symbol fonts that cover broader Unicode
	// ranges like Miscellaneous Technical (U+2300–U+23FF) which includes
	// the ⏵ / ⏴ media-control symbols claude uses.
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
		outPath   = flag.String("o", "out.png", "output PNG path")
		cols      = flag.Int("cols", 100, "terminal columns")
		rows      = flag.Int("rows", 30, "terminal rows")
		idle      = flag.Duration("idle", 1500*time.Millisecond, "consider command idle after this long with no output")
		deadline  = flag.Duration("deadline", 15*time.Second, "hard deadline on waiting for idle")
		fontPath  = flag.String("font", defaultFont, "primary monospace font — ghostty default is JetBrains Mono")
		fallbacks = flag.String("font-fallback", strings.Join(defaultFallbacks, ":"), "colon-separated fallback font chain for glyphs missing from the primary")
		fontSize  = flag.Float64("size", 13, "font size in points")
	)
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: render-cmd [flags] <cmd> [args...]")
		os.Exit(2)
	}

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

	// Reasonable dark defaults so empty cells aren't white.
	check(term.SetColorBackground(&ghostty.ColorRGB{R: 0x1e, G: 0x1e, B: 0x2e}))
	check(term.SetColorForeground(&ghostty.ColorRGB{R: 0xcd, G: 0xd6, B: 0xf4}))

	// --- Spawn in a pty
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

	// --- Read until idle or deadline
	start := time.Now()
	lastOut := time.Now()
	buf := make([]byte, 32*1024)
	total := 0
	for {
		check1(ptmx.SetReadDeadline(time.Now().Add(100 * time.Millisecond)))
		n, err := ptmx.Read(buf)
		if n > 0 {
			term.VTWrite(buf[:n])
			total += n
			lastOut = time.Now()
		}
		if err != nil {
			var ne interface{ Timeout() bool }
			if errors.As(err, &ne) && ne.Timeout() {
				// keep polling
			} else {
				// EOF / other error — process probably ended
				break
			}
		}
		if time.Since(lastOut) >= *idle && total > 0 {
			break
		}
		if time.Since(start) >= *deadline {
			break
		}
	}
	fmt.Fprintf(os.Stderr, "read %d bytes, idle after %s\n", total, time.Since(start).Round(10*time.Millisecond))

	// --- Render one frame and time it.
	renderStart := time.Now()

	rs, err := ghostty.NewRenderState()
	check(err)
	defer rs.Close()
	check(rs.Update(term))
	colors, err := rs.Colors()
	check(err)
	tUpdate := time.Since(renderStart)

	// Canvas
	padding := 8
	imgW := *cols*cellW + 2*padding
	imgH := *rows*cellH + 2*padding
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: rgbaFrom(colors.Background)}, image.Point{}, draw.Src)

	// Walk cells + draw glyphs
	drawStart := time.Now()
	missing := map[rune]int{}
	ri, err := ghostty.NewRenderStateRowIterator()
	check(err)
	defer ri.Close()
	rc, err := ghostty.NewRenderStateRowCells()
	check(err)
	defer rc.Close()
	check(rs.RowIterator(ri))

	row := 0
	for ri.Next() {
		if row >= *rows {
			break
		}
		check(ri.Cells(rc))

		for col := 0; col < *cols; col++ {
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

			// Font fallback: try primary face first, then walk the
			// fallback chain looking for one that has the glyph.
			drawFace := face
			if _, ok := face.GlyphAdvance(r); !ok {
				found := false
				for _, fb := range fallbackFaces {
					if _, ok := fb.GlyphAdvance(r); ok {
						drawFace = fb
						found = true
						break
					}
				}
				if !found {
					missing[r]++
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
	tDraw := time.Since(drawStart)

	// PNG encode to memory
	encodeStart := time.Now()
	var pngBuf bytes.Buffer
	check(png.Encode(&pngBuf, img))
	tEncode := time.Since(encodeStart)

	// Write to disk (not timed — not part of the render path)
	check(os.WriteFile(*outPath, pngBuf.Bytes(), 0644))

	tTotal := time.Since(renderStart)

	fmt.Printf("wrote %s (%dx%d, %d bytes)\n", *outPath, imgW, imgH, pngBuf.Len())
	fmt.Fprintf(os.Stderr, "render: %s total  (update=%s  draw=%s  encode=%s)\n",
		tTotal.Round(time.Microsecond),
		tUpdate.Round(time.Microsecond),
		tDraw.Round(time.Microsecond),
		tEncode.Round(time.Microsecond),
	)
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing glyphs (%d unique):\n", len(missing))
		for r, n := range missing {
			fmt.Fprintf(os.Stderr, "  U+%04X %q ×%d\n", r, string(r), n)
		}
	}
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

func rgbaFrom(c ghostty.ColorRGB) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff}
}

// findGhosttyFont locates a font by basename inside the repo's
// .zig-global-cache/p/ tree, where `make build` placed ghostty's
// bundled fonts (JetBrains Mono, Symbols Nerd Font, etc).
func findGhosttyFont(basename string) (string, error) {
	roots := []string{".zig-global-cache/p", "build/_deps/ghostty-src/.zig-cache/p"}
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, "*", "**", basename))
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
		// filepath.Glob doesn't do "**"; walk manually.
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
	return "", fmt.Errorf("ghostty font %q not found under .zig-global-cache/p — run `make build` first", basename)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func check1(err error) { check(err) }
