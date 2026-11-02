package xiangqi

import (
	"fmt"
	"strings"
)

type Language string

const (
	English Language = "en"
	Chinese Language = "zh"
)

type View struct {
	Language Language
	Bottom   Side
	Selected *Pos
}

func (v View) canonical(x, y int) Pos {
	if v.Bottom == Black {
		return Pos{8 - x, 9 - y}
	}
	return Pos{x, y}
}

func (v View) display(p Pos) Pos {
	if v.Bottom == Black {
		return Pos{8 - p.X, 9 - p.Y}
	}
	return p
}

type BoardLayout struct {
	Compact bool
	Step    int
	View    View
}

func NewBoardLayout(v View, compact bool) BoardLayout {
	step := 4
	if compact {
		step = 2
		if v.Language == Chinese {
			step = 3
		}
	}
	return BoardLayout{Compact: compact, Step: step, View: v}
}

func (l BoardLayout) MinWidth() int { return 5 + 8*l.Step + l.labelWidth() }
func (l BoardLayout) MinHeight() int {
	if l.Compact {
		return 15
	}
	return 23
}
func (l BoardLayout) labelWidth() int { return 2 }
func (l BoardLayout) Cell(p Pos) (col, row int) {
	d := l.View.display(p)
	col = 6 + l.Step*d.X
	row = 2 + d.Y
	if l.Compact {
		if d.Y >= 5 {
			row++
		}
	} else {
		row += d.Y
	}
	return
}
func (l BoardLayout) Hit(col, row int) (Pos, bool) {
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			p := l.View.canonical(x, y)
			c, r := l.Cell(p)
			if row == r && col >= c && col < c+l.labelWidth() {
				return p, true
			}
		}
	}
	return Pos{}, false
}

func (p Piece) DisplayLabel(lang Language) string {
	if lang != Chinese || p.Side == None {
		return p.Label()
	}
	var word string
	switch p.Kind {
	case General:
		if p.Side == Red {
			word = "帅"
		} else {
			word = "将"
		}
	case Advisor:
		if p.Side == Red {
			word = "仕"
		} else {
			word = "士"
		}
	case Elephant:
		if p.Side == Red {
			word = "相"
		} else {
			word = "象"
		}
	case Horse:
		word = "马"
	case Rook:
		word = "车"
	case Cannon:
		word = "炮"
	case Soldier:
		if p.Side == Red {
			word = "兵"
		} else {
			word = "卒"
		}
	}
	return word
}

func ParseDisplayPiece(label string, side Side) (Piece, error) {
	if p, err := ParsePiece(label); err == nil {
		return p, nil
	}
	for _, kind := range []Kind{General, Advisor, Elephant, Horse, Rook, Cannon, Soldier} {
		max := uint8(2)
		if kind == General {
			max = 1
		}
		if kind == Soldier {
			max = 5
		}
		for id := uint8(1); id <= max; id++ {
			p := Piece{Side: side, Kind: kind, ID: id}
			if fmt.Sprintf("%s%d", p.DisplayLabel(Chinese), p.ID) == label {
				return p, nil
			}
		}
	}
	return Piece{}, fmt.Errorf("无效棋子编号")
}

// Render uses two terminal columns per intersection; pieces replace the crossing glyph.
func (g *Game) Render(color bool) string { return g.RenderView(color, View{Bottom: Red}, false) }

// RenderCompact keeps the whole game visible in a short terminal panel.
func (g *Game) RenderCompact(color bool) string { return g.RenderView(color, View{Bottom: Red}, true) }

func (g *Game) RenderView(color bool, view View, compact bool) string {
	l := NewBoardLayout(view, compact)
	var b strings.Builder
	b.WriteString("     ")
	for x := 0; x < 9; x++ {
		p := view.canonical(x, 0)
		fmt.Fprintf(&b, "%d", p.X)
		if x < 8 {
			b.WriteString(strings.Repeat(" ", l.Step-1))
		}
	}
	b.WriteByte('\n')
	for y := 0; y < 10; y++ {
		p0 := view.canonical(0, y)
		fmt.Fprintf(&b, " %d   ", p0.Y)
		for x := 0; x < 9; x++ {
			pos := view.canonical(x, y)
			p := g.At(pos)
			label := "┼ "
			if p.Side != None {
				label = p.DisplayLabel(view.Language)
			}
			if color && view.Selected != nil && *view.Selected == pos {
				fmt.Fprintf(&b, "\x1b[1;33m%s\x1b[0m", label)
			} else if color && p.Side == Red {
				fmt.Fprintf(&b, "\x1b[1;31m%s\x1b[0m", label)
			} else {
				b.WriteString(label)
			}
			if x < 8 {
				width := 2
				if compact {
					b.WriteString(strings.Repeat(" ", l.Step-width))
				} else {
					b.WriteString(strings.Repeat("─", l.Step-width))
				}
			}
		}
		b.WriteByte('\n')
		if y == 9 {
			break
		}
		if y == 4 {
			b.WriteString("     │" + strings.Repeat(" ", l.Step*8) + "│\n")
		} else if !compact {
			b.WriteString("     │   │   │   │   │   │   │   │   │\n")
		}
	}
	return b.String()
}

func (g *Game) Status() string {
	var b strings.Builder
	if g.Winner != None {
		fmt.Fprintf(&b, "%s获胜", g.Winner)
	} else {
		fmt.Fprintf(&b, "轮到%s", g.Turn)
		if g.InCheck(g.Turn) {
			b.WriteString("（被将军）")
		}
	}
	if n := len(g.History); n > 0 {
		fmt.Fprintf(&b, "  上一步：%s", g.History[n-1])
	}
	return b.String()
}
