package mockgame

import (
	"image/color"
	"time"

	"goplay/internal/domain"
)

var (
	bgColors = [10]color.RGBA{
		{28, 30, 42, 255}, {34, 30, 44, 255}, {30, 36, 46, 255},
		{38, 32, 40, 255}, {26, 38, 42, 255}, {40, 34, 34, 255},
		{30, 34, 52, 255}, {36, 40, 30, 255}, {42, 28, 38, 255},
		{24, 24, 30, 255},
	}
	barBG     = color.RGBA{45, 45, 52, 255}
	barEdge   = color.RGBA{190, 190, 200, 255}
	hpFill    = color.RGBA{220, 45, 45, 255}
	enemyFill = color.RGBA{240, 150, 40, 255}
	white     = color.RGBA{250, 250, 250, 255}
	blue      = color.RGBA{60, 120, 240, 255}
	green     = color.RGBA{40, 220, 80, 255}
	deadRed   = color.RGBA{190, 20, 30, 255}
)

const (
	BarX, BarW, BarH = 8, 120, 8
	HPBarY           = 8
	EnemyBarY        = 24
	RoomDigitsX      = 294
	RoomDigitsY      = 158
	DigitW, DigitH   = 3, 5
	DigitScale       = 2
)

var digitFont = [10][5][3]byte{
	{{1, 1, 1}, {1, 0, 1}, {1, 0, 1}, {1, 0, 1}, {1, 1, 1}},
	{{0, 1, 0}, {1, 1, 0}, {0, 1, 0}, {0, 1, 0}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {1, 1, 1}, {1, 0, 0}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
	{{1, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 0, 1}, {0, 0, 1}},
	{{1, 1, 1}, {1, 0, 0}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {1, 0, 0}, {1, 1, 1}, {1, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}},
	{{1, 1, 1}, {1, 0, 1}, {1, 1, 1}, {1, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {1, 0, 1}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
}

type canvas struct {
	pix  []byte
	w, h int
}

func newCanvas(w, h int, bg color.RGBA) *canvas {
	c := &canvas{pix: make([]byte, w*h*4), w: w, h: h}
	for i := 0; i < w*h; i++ {
		c.pix[i*4] = bg.R
		c.pix[i*4+1] = bg.G
		c.pix[i*4+2] = bg.B
		c.pix[i*4+3] = 255
	}
	return c
}

func (c *canvas) set(x, y int, col color.RGBA) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	i := (y*c.w + x) * 4
	c.pix[i] = col.R
	c.pix[i+1] = col.G
	c.pix[i+2] = col.B
	c.pix[i+3] = 255
}

func (c *canvas) rect(x, y, w, h int, col color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			c.set(xx, yy, col)
		}
	}
}

func (c *canvas) digit(d int, x, y int) {
	if d < 0 || d > 9 {
		return
	}
	for row := 0; row < DigitH; row++ {
		for col := 0; col < DigitW; col++ {
			if digitFont[d][row][col] == 0 {
				continue
			}
			c.rect(x+col*DigitScale, y+row*DigitScale, DigitScale, DigitScale, white)
		}
	}
}

// Render draws a frame using only what a screen capture would show.
func Render(g *Game) domain.Frame {
	g.mu.Lock()
	snap := struct {
		hp, maxhp, ehp, emax, potions, room int
		dead, won                           bool
	}{g.HP, g.MaxHP, g.EnemyHP, g.EnemyMaxHP, g.Potions, g.Room, g.Dead, g.Won}
	g.mu.Unlock()

	bg := bgColors[(snap.room-1)%len(bgColors)]
	c := newCanvas(W, H, bg)

	// Wall texture: deterministic stripes so the frame is not a flat color.
	for y := 0; y < H; y += 12 {
		col := color.RGBA{bg.R + 6, bg.G + 6, bg.B + 8, 255}
		c.rect(0, y, W, 2, col)
	}

	// HP bar.
	c.rect(BarX-2, HPBarY-2, BarW+4, BarH+4, barEdge)
	c.rect(BarX, HPBarY, BarW, BarH, barBG)
	if snap.hp > 0 && snap.maxhp > 0 {
		w := snap.hp * BarW / snap.maxhp
		c.rect(BarX, HPBarY, w, BarH, hpFill)
	}

	// Enemy bar.
	c.rect(BarX-2, EnemyBarY-2, BarW+4, BarH+4, barEdge)
	c.rect(BarX, EnemyBarY, BarW, BarH, barBG)
	if snap.ehp > 0 && snap.emax > 0 {
		w := snap.ehp * BarW / snap.emax
		c.rect(BarX, EnemyBarY, w, BarH, enemyFill)
	}

	// Potion icons.
	for i := 0; i < snap.potions && i < 6; i++ {
		c.rect(10+i*10, 168, 7, 7, blue)
	}

	// Room number, always two digits, top-right of the lower band.
	c.digit(snap.room/10%10, RoomDigitsX, RoomDigitsY)
	c.digit(snap.room%10, RoomDigitsX+DigitW*DigitScale+2, RoomDigitsY)

	// Status marker.
	switch {
	case snap.dead:
		c.rect(300, 8, 12, 12, deadRed)
	case snap.won:
		c.rect(300, 8, 12, 12, green)
	}

	return domain.Frame{Width: W, Height: H, RGBA: c.pix, At: time.Now(), Source: "mock"}
}
