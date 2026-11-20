package radar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"time"

	"golang.org/x/image/draw"
)

// Smooth radar. The mosaic is 1 km, which is about one source pixel at z7; NOAA's own picture at z9 and finer is that pixel repeated into blocks.
// With Config.Smooth the zooms past the source zoom are cut from the source tiles here and upscaled with a smooth kernel, so the picture is soft
// instead of blocky. It is the same data at the same time: nothing here adds detail, and a derived tile lives no longer than its source tiles.
const (
	// SmoothKey is the design-preview key (RIVOLT_DESIGN_PREVIEW) that turns the smoothing on; prod does not list it.
	SmoothKey           = "radar_smooth"
	DefaultSmoothSource = 7 // about native: the mosaic is 1 km, a pixel at z7; a z9 source keeps the blocks, see the shots in the PR
	SmoothMaxZoom       = 12
	smoothMargin        = 2 // source pixels read around the window, so the kernel and the blur see the neighbours across a tile edge
)

func (s *Service) smooth() bool {
	if s.cfg.SmoothFn != nil {
		return s.cfg.SmoothFn()
	}
	return s.cfg.Smooth
}

func (s *Service) maxZoom() int {
	if s.smooth() {
		return SmoothMaxZoom
	}
	return MaxZoom
}

func (s *Service) smoothSource() int {
	if s.cfg.SmoothSource > 0 && s.cfg.SmoothSource < SmoothMaxZoom {
		return s.cfg.SmoothSource
	}
	return DefaultSmoothSource
}

// ClientMaxZoom is the deepest zoom clients are told to ask for.
func (s *Service) ClientMaxZoom() int {
	if s.smooth() {
		return SmoothMaxZoom
	}
	return ClientMaxZoom
}

// derived returns the tile (z, x, y) cut from its ancestor at the source zoom and upscaled. Derived tiles are kept in process only: they are cheap to
// make from source tiles that are themselves shared across pods.
func (s *Service) derived(ctx context.Context, frame time.Time, z, x, y int) ([]byte, error) {
	key := fmt.Sprintf("s:%d:%d:%d:%d", frame.Unix(), z, x, y)
	if b, ok := s.localGet(key); ok {
		return b, nil
	}
	v, err, _ := s.sf.Do("tile:"+key, func() (any, error) {
		return s.deriveTile(ctx, frame, z, x, y)
	})
	if err != nil {
		return nil, err
	}
	b := v.([]byte)
	s.localPut(key, b)
	return b, nil
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func (s *Service) deriveTile(ctx context.Context, frame time.Time, z, x, y int) ([]byte, error) {
	src := s.smoothSource()
	k := z - src
	n := 1 << k
	sz := TileSize / n // the window's size in source pixels
	m := smoothMargin
	ax, ay := x>>k, y>>k
	ox, oy := (x&(n-1))*sz, (y&(n-1))*sz
	side := sz + 2*m
	in := image.NewRGBA(image.Rect(0, 0, side, side))
	tiles := map[[2]int]*image.NRGBA{}
	last := 1 << src
	tileAt := func(dx, dy int) (*image.NRGBA, error) {
		if img, ok := tiles[[2]int{dx, dy}]; ok {
			return img, nil
		}
		tx, ty := ax+dx, ay+dy
		var img *image.NRGBA
		if tx >= 0 && ty >= 0 && tx < last && ty < last {
			b, err := s.baseTile(ctx, frame, src, tx, ty)
			if err != nil {
				return nil, err
			}
			p, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			img = toNRGBA(p)
		}
		tiles[[2]int{dx, dy}] = img
		return img, nil
	}
	for j := 0; j < side; j++ {
		sy := oy - m + j
		dy := floorDiv(sy, TileSize)
		for i := 0; i < side; i++ {
			sx := ox - m + i
			dx := floorDiv(sx, TileSize)
			img, err := tileAt(dx, dy)
			if err != nil {
				return nil, err
			}
			if img == nil {
				continue
			}
			c := img.NRGBAAt(sx-dx*TileSize, sy-dy*TileSize)
			if c.A == 0 {
				continue
			}
			r, g, b, a := c.RGBA()
			in.SetRGBA(i, j, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
		}
	}
	big := image.NewRGBA(image.Rect(0, 0, side*n, side*n))
	draw.CatmullRom.Scale(big, big.Bounds(), in, in.Bounds(), draw.Src, nil)
	if r := s.cfg.SmoothBlur; r > 0 {
		boxBlur(big, r)
		boxBlur(big, r)
	}
	out := image.NewNRGBA(image.Rect(0, 0, TileSize, TileSize))
	off := m * n
	for j := 0; j < TileSize; j++ {
		for i := 0; i < TileSize; i++ {
			p := big.RGBAAt(off+i, off+j)
			if p.A == 0 {
				continue
			}
			a := int(p.A)
			out.SetNRGBA(i, j, color.NRGBA{uint8(min(255, int(p.R)*255/a)), uint8(min(255, int(p.G)*255/a)), uint8(min(255, int(p.B)*255/a)), p.A})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// boxBlur blurs a premultiplied image in place with a (2r+1) box, horizontally then vertically; outside the image counts as transparent.
func boxBlur(img *image.RGBA, r int) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	tmp := make([]uint8, len(img.Pix))
	d := 2*r + 1
	pass := func(dst, srcPix []uint8, n, lines, step, lineStep int) {
		for l := 0; l < lines; l++ {
			base := l * lineStep
			for c := 0; c < 4; c++ {
				sum := 0
				for i := -r; i <= r; i++ {
					if i >= 0 && i < n {
						sum += int(srcPix[base+i*step+c])
					}
				}
				for i := 0; i < n; i++ {
					dst[base+i*step+c] = uint8(sum / d)
					if o := i - r; o >= 0 {
						sum -= int(srcPix[base+o*step+c])
					}
					if a := i + r + 1; a < n {
						sum += int(srcPix[base+a*step+c])
					}
				}
			}
		}
	}
	pass(tmp, img.Pix, w, h, 4, img.Stride)
	pass(img.Pix, tmp, h, w, img.Stride, 4)
}
