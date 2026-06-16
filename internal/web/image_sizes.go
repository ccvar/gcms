package web

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"math"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type ImageSize struct {
	Width  int
	Height int
}

var (
	svgWidthRE   = regexp.MustCompile(`(?i)\bwidth=["']([0-9.]+)`)
	svgHeightRE  = regexp.MustCompile(`(?i)\bheight=["']([0-9.]+)`)
	svgViewBoxRE = regexp.MustCompile(`(?i)\bviewBox=["']([^"']+)["']`)
)

func scanAssetImageSizes(fsys fs.FS) map[string]ImageSize {
	out := map[string]ImageSize{}
	if fsys == nil {
		return out
	}
	_ = fs.WalkDir(fsys, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(path.Ext(p))
		if ext != ".webp" && ext != ".svg" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		size, ok := imageSizeFromBytes(ext, b)
		if !ok || size.Width <= 0 || size.Height <= 0 {
			return nil
		}
		key := "/" + p
		out[key] = size
		out[p] = size
		return nil
	})
	return out
}

func imageSizeFromBytes(ext string, b []byte) (ImageSize, bool) {
	switch ext {
	case ".webp":
		return webpSize(b)
	case ".svg":
		return svgSize(b)
	default:
		cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return ImageSize{}, false
		}
		return ImageSize{Width: cfg.Width, Height: cfg.Height}, true
	}
}

func lookupImageSize(src string, sizes map[string]ImageSize) (ImageSize, bool) {
	if len(sizes) == 0 {
		return ImageSize{}, false
	}
	src = strings.TrimSpace(src)
	if src == "" {
		return ImageSize{}, false
	}
	if size, ok := sizes[src]; ok {
		return size, true
	}
	if u, err := url.Parse(src); err == nil && u.Path != "" {
		if size, ok := sizes[u.Path]; ok {
			return size, true
		}
		if strings.HasPrefix(u.Path, "/") {
			if size, ok := sizes[strings.TrimPrefix(u.Path, "/")]; ok {
				return size, true
			}
		}
	}
	if strings.HasPrefix(src, "/") {
		size, ok := sizes[strings.TrimPrefix(src, "/")]
		return size, ok
	}
	return ImageSize{}, false
}

func svgSize(b []byte) (ImageSize, bool) {
	s := string(b)
	root := s
	if end := strings.Index(root, ">"); end >= 0 {
		root = root[:end+1]
	}
	w, wok := svgLength(svgWidthRE.FindStringSubmatch(root))
	h, hok := svgLength(svgHeightRE.FindStringSubmatch(root))
	if wok && hok {
		return ImageSize{Width: w, Height: h}, true
	}
	if m := svgViewBoxRE.FindStringSubmatch(root); len(m) == 2 {
		fields := strings.Fields(strings.ReplaceAll(m[1], ",", " "))
		if len(fields) == 4 {
			vw, errW := strconv.ParseFloat(fields[2], 64)
			vh, errH := strconv.ParseFloat(fields[3], 64)
			if errW == nil && errH == nil && vw > 0 && vh > 0 {
				return ImageSize{Width: int(math.Round(vw)), Height: int(math.Round(vh))}, true
			}
		}
	}
	return ImageSize{}, false
}

func svgLength(m []string) (int, bool) {
	if len(m) != 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return int(math.Round(v)), true
}

func webpSize(b []byte) (ImageSize, bool) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return ImageSize{}, false
	}
	for off := 12; off+8 <= len(b); {
		chunk := string(b[off : off+4])
		n := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		start := off + 8
		end := start + n
		if end > len(b) {
			return ImageSize{}, false
		}
		data := b[start:end]
		switch chunk {
		case "VP8X":
			if len(data) >= 10 {
				return ImageSize{Width: int(le24(data[4:7])) + 1, Height: int(le24(data[7:10])) + 1}, true
			}
		case "VP8L":
			if len(data) >= 5 && data[0] == 0x2f {
				bits := uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16 | uint32(data[4])<<24
				return ImageSize{Width: int(bits&0x3fff) + 1, Height: int((bits>>14)&0x3fff) + 1}, true
			}
		case "VP8 ":
			if len(data) >= 10 && data[3] == 0x9d && data[4] == 0x01 && data[5] == 0x2a {
				w := int(binary.LittleEndian.Uint16(data[6:8]) & 0x3fff)
				h := int(binary.LittleEndian.Uint16(data[8:10]) & 0x3fff)
				return ImageSize{Width: w, Height: h}, true
			}
		}
		off = end
		if n%2 == 1 {
			off++
		}
	}
	return ImageSize{}, false
}

func le24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}
