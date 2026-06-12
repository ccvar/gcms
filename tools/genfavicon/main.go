// Command genfavicon 把内置 C 标记栅格化为多尺寸 favicon.ico（16/32/48，32 位 PNG 帧）。
// 几何与 assets/favicon.svg 保持一致。用法：go run ./tools/genfavicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

var (
	accent = color.RGBA{0x9a, 0x3b, 0x2f, 0xff}
	white  = color.RGBA{0xff, 0xff, 0xff, 0xff}
	clear  = color.RGBA{0, 0, 0, 0}
)

func hypot(x, y float64) float64 { return math.Hypot(x, y) }

// sample 在 64 单位空间内取某点颜色（与 favicon.svg 一致）。
func sample(x, y float64) color.RGBA {
	const n, r = 64.0, 15.0
	dx, dy := math.Max(math.Max(r-x, x-(n-r)), 0), math.Max(math.Max(r-y, y-(n-r)), 0)
	if dx*dx+dy*dy > r*r { // 圆角矩形之外
		return clear
	}
	c := accent
	const cx, cy, ri, ro = 32.5, 32.0, 10.5, 17.5
	d := hypot(x-cx, y-cy)
	ang := math.Atan2(y-cy, x-cx)
	gap := 34.8 * math.Pi / 180
	if d >= ri && d <= ro && math.Abs(ang) >= gap { // C 环（右侧开口）
		c = white
	}
	if hypot(x-44, y-24) <= 3.5 || hypot(x-44, y-40) <= 3.5 { // 圆头端帽
		c = white
	}
	if hypot(x-45.5, y-42) <= 4.6 { // 收尾的点
		c = white
	}
	return c
}

func render(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // 超采样抗锯齿
	scale := 64.0 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var pr, pg, pb, pa float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := (float64(px) + (float64(sx)+0.5)/ss) * scale
					fy := (float64(py) + (float64(sy)+0.5)/ss) * scale
					c := sample(fx, fy)
					a := float64(c.A) / 255
					pr += float64(c.R) * a
					pg += float64(c.G) * a
					pb += float64(c.B) * a
					pa += a
				}
			}
			cnt := float64(ss * ss)
			out := color.RGBA{A: uint8(pa / cnt * 255)}
			if pa > 0 {
				out.R = uint8(pr / pa)
				out.G = uint8(pg / pa)
				out.B = uint8(pb / pa)
			}
			img.Set(px, py, out)
		}
	}
	return img
}

func main() {
	sizes := []int{16, 32, 48}
	var pngs [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			log.Fatal(err)
		}
		pngs = append(pngs, buf.Bytes())
	}
	// 组装 ICO（PNG 帧）
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))}) // reserved, type=icon, count
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		b := byte(s)
		if s >= 256 {
			b = 0
		}
		out.Write([]byte{b, b, 0, 0})                                 // w,h,colorCount,reserved
		binary.Write(&out, binary.LittleEndian, []uint16{1, 32})      // planes, bitCount
		binary.Write(&out, binary.LittleEndian, uint32(len(pngs[i]))) // bytesInRes
		binary.Write(&out, binary.LittleEndian, uint32(offset))       // imageOffset
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	if err := os.WriteFile("assets/favicon.ico", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("已生成 assets/favicon.ico（%d 字节，尺寸 %v）", out.Len(), sizes)
}
