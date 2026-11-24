package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	sizes := []int{16, 32, 48, 64, 256}
	frames := make([][]byte, 0, len(sizes))
	if err := os.MkdirAll("assets", 0o755); err != nil {
		panic(err)
	}
	for _, sz := range sizes {
		img := renderLogo(sz)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			panic(err)
		}
		pngPath := filepath.Join("assets", fmt.Sprintf("icon_%d.png", sz))
		if err := os.WriteFile(pngPath, buf.Bytes(), 0o644); err != nil {
			panic(err)
		}
		frames = append(frames, buf.Bytes())
	}

	outPath := filepath.Join("assets", "icon.ico")
	if err := writeICO(outPath, sizes, frames); err != nil {
		panic(err)
	}
	fmt.Printf("Generated PNGs and %s (%d sizes)\n", outPath, len(sizes))
}

func renderLogo(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // 4x4 supersampling for clean anti-aliased edges
	invScale := 64.0 / float64(size)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var rSum, gSum, bSum, aSum float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/float64(ss)) * invScale
					py := (float64(y) + (float64(sy)+0.5)/float64(ss)) * invScale
					c := sampleLogo(px, py)
					a := float64(c.A) / 255.0
					rSum += float64(c.R) * a
					gSum += float64(c.G) * a
					bSum += float64(c.B) * a
					aSum += float64(c.A)
				}
			}
			samples := float64(ss * ss)
			avgA := aSum / samples
			if avgA < 0.5 {
				continue
			}
			img.SetRGBA(x, y, color.RGBA{
				R: clampByte(rSum / avgA),
				G: clampByte(gSum / avgA),
				B: clampByte(bSum / avgA),
				A: clampByte(avgA),
			})
		}
	}
	return img
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}

func sampleLogo(x, y float64) color.RGBA {
	bgDist := roundedBoxSDF(x-32, y-32, 30, 30, 14)
	if bgDist > 0 {
		return color.RGBA{}
	}

	out := color.RGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0xff} // Slate #0f172a
	if bgDist > -2.0 {
		out = color.RGBA{R: 0x1e, G: 0x29, B: 0x3b, A: 0xff} // Subtle border #1e293b
	}

	// Lens ring centered at (32, 32), radius 20, thickness 4.5 (inner 17.75, outer 22.25)
	dx, dy := x-32, y-32
	dist := math.Hypot(dx, dy)
	if math.Abs(dist-20.0) <= 2.35 {
		if pyAngle := math.Atan2(dy, dx); pyAngle < -0.55 && pyAngle > -2.55 {
			out = color.RGBA{R: 0x14, G: 0xb8, B: 0xa6, A: 0xff} // Teal highlight #14b8a6
		} else {
			out = color.RGBA{R: 0x0f, G: 0x76, B: 0x6e, A: 0xff} // Teal #0f766e
		}
	}

	// Top arrow pointing right (y=26, x=21..41, head at 40)
	if segmentDist(x, y, 21.5, 26, 39.0, 26) <= 1.95 ||
		segmentDist(x, y, 34, 21, 40, 26) <= 1.95 ||
		segmentDist(x, y, 34, 31, 40, 26) <= 1.95 {
		out = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	}

	// Bottom arrow pointing left (y=38, x=23..42.5, head at 24)
	if segmentDist(x, y, 25.0, 38, 42.5, 38) <= 1.95 ||
		segmentDist(x, y, 30, 33, 24, 38) <= 1.95 ||
		segmentDist(x, y, 30, 43, 24, 38) <= 1.95 {
		out = color.RGBA{R: 0x5e, G: 0xea, B: 0xd4, A: 0xff}
	}

	return out
}

func roundedBoxSDF(px, py, bx, by, r float64) float64 {
	qx := math.Abs(px) - bx + r
	qy := math.Abs(py) - by + r
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	in := math.Min(math.Max(qx, qy), 0)
	return out + in - r
}

func segmentDist(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	wx, wy := px-ax, py-ay
	denom := vx*vx + vy*vy
	if denom == 0 {
		return math.Hypot(wx, wy)
	}
	t := (wx*vx + wy*vy) / denom
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}

func writeICO(path string, sizes []int, frames [][]byte) error {
	var buf bytes.Buffer
	// ICONDIR header: reserved(0), type(1=icon), count
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(frames)))

	offset := uint32(6 + 16*len(frames))
	for i, sz := range sizes {
		w := uint8(sz)
		h := uint8(sz)
		if sz >= 256 {
			w, h = 0, 0
		}
		buf.WriteByte(w)
		buf.WriteByte(h)
		buf.WriteByte(0) // color palette
		buf.WriteByte(0) // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(frames[i])))
		_ = binary.Write(&buf, binary.LittleEndian, offset)
		offset += uint32(len(frames[i]))
	}
	for _, f := range frames {
		buf.Write(f)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
