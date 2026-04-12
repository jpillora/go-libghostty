// Example png walks the terminal grid via libghostty's RenderState API
// and rasterizes it into a PNG — no external tools, no cgo beyond what
// go-libghostty itself already needs. Font and PNG encoding are pure
// Go (golang.org/x/image + stdlib image/png).
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"

	ghostty "github.com/mitchellh/go-libghostty"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	cols    = 40
	rows    = 6
	cellW   = 7  // basicfont.Face7x13 advance
	cellH   = 14 // one pixel of leading
	padding = 8
)

func main() {
	// --- Terminal setup + content.
	term, err := ghostty.NewTerminal(
		ghostty.WithSize(cols, rows),
		ghostty.WithMaxScrollback(100),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer term.Close()

	// Catppuccin-ish default fg/bg so the image has baseline colors
	// even when cells themselves don't set them.
	if err := term.SetColorBackground(&ghostty.ColorRGB{R: 0x1e, G: 0x1e, B: 0x2e}); err != nil {
		log.Fatal(err)
	}
	if err := term.SetColorForeground(&ghostty.ColorRGB{R: 0xcd, G: 0xd6, B: 0xf4}); err != nil {
		log.Fatal(err)
	}

	// Write a variety of styled content.
	term.VTWrite([]byte("Hello, \033[1;32mworld\033[0m!\r\n"))
	term.VTWrite([]byte("\033[4munderlined\033[0m text\r\n"))
	term.VTWrite([]byte("\033[38;2;255;128;0morange\033[0m fg\r\n"))
	term.VTWrite([]byte("\033[48;2;40;40;80m bg block \033[0m trailing\r\n"))
	term.VTWrite([]byte("\033[1;38;2;137;180;250mbold blue\033[0m done"))

	// --- Render state snapshot of the terminal.
	rs, err := ghostty.NewRenderState()
	if err != nil {
		log.Fatal(err)
	}
	defer rs.Close()
	if err := rs.Update(term); err != nil {
		log.Fatal(err)
	}
	colors, err := rs.Colors()
	if err != nil {
		log.Fatal(err)
	}

	// --- Image canvas.
	imgW := cols*cellW + 2*padding
	imgH := rows*cellH + 2*padding
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))

	// Fill with terminal default background.
	bg := rgbaFrom(colors.Background)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	// --- Walk rows and cells.
	ri, err := ghostty.NewRenderStateRowIterator()
	if err != nil {
		log.Fatal(err)
	}
	defer ri.Close()
	rc, err := ghostty.NewRenderStateRowCells()
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()
	if err := rs.RowIterator(ri); err != nil {
		log.Fatal(err)
	}

	row := 0
	for ri.Next() {
		if row >= rows {
			break
		}
		if err := ri.Cells(rc); err != nil {
			log.Fatal(err)
		}

		for col := 0; col < cols; col++ {
			if err := rc.Select(uint16(col)); err != nil {
				log.Fatal(err)
			}

			raw, err := rc.Raw()
			if err != nil {
				log.Fatal(err)
			}
			wide, err := raw.Wide()
			if err != nil {
				log.Fatal(err)
			}
			// The tail of a wide cell is already painted by the base cell.
			if wide == ghostty.CellWideSpacerTail {
				continue
			}
			cellCols := 1
			if wide == ghostty.CellWideWide {
				cellCols = 2
			}

			// Resolve colors, falling back to terminal defaults.
			cellBg := colors.Background
			if c, err := rc.BgColor(); err == nil && c != nil {
				cellBg = *c
			}
			cellFg := colors.Foreground
			if c, err := rc.FgColor(); err == nil && c != nil {
				cellFg = *c
			}

			// Cell rect in pixel space.
			x0 := padding + col*cellW
			y0 := padding + row*cellH
			x1 := x0 + cellCols*cellW
			y1 := y0 + cellH
			rect := image.Rect(x0, y0, x1, y1)

			// Background.
			if cellBg != colors.Background {
				draw.Draw(img, rect, &image.Uniform{C: rgbaFrom(cellBg)}, image.Point{}, draw.Src)
			}

			// Grapheme text (only base codepoint; basicfont is ASCII-ish anyway).
			graphemes, err := rc.Graphemes()
			if err != nil {
				log.Fatal(err)
			}
			if len(graphemes) == 0 {
				continue
			}
			r := rune(graphemes[0])

			// Style for underline.
			style, err := rc.Style()
			if err != nil {
				log.Fatal(err)
			}

			// Draw the glyph in fg color.
			drawer := &font.Drawer{
				Dst:  img,
				Src:  &image.Uniform{C: rgbaFrom(cellFg)},
				Face: basicfont.Face7x13,
				Dot:  fixed.P(x0, y0+basicfont.Face7x13.Ascent),
			}
			drawer.DrawString(string(r))

			// Underline: draw a 1px line across the cell near the baseline.
			if style.Underline() != ghostty.UnderlineNone {
				uy := y0 + basicfont.Face7x13.Ascent + 1
				for x := x0; x < x1; x++ {
					img.Set(x, uy, rgbaFrom(cellFg))
				}
			}
		}
		row++
	}

	// --- Encode.
	out := "terminal.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%dx%d)\n", out, imgW, imgH)
}

func rgbaFrom(c ghostty.ColorRGB) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff}
}
