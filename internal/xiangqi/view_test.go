package xiangqi

import (
	"strings"
	"testing"
)

func TestChineseViewAndRotation(t *testing.T) {
	g := New()
	red := View{Language: Chinese, Bottom: Red}
	black := View{Language: Chinese, Bottom: Black}
	redBoard := g.RenderView(false, red, true)
	if !strings.Contains(redBoard, "车") || !strings.Contains(redBoard, "兵") || strings.Contains(redBoard, "车1") || strings.Contains(redBoard, "兵1") {
		t.Fatal("Chinese board labels should omit piece numbers")
	}
	board := g.RenderView(false, black, true)
	lines := strings.Split(board, "\n")
	if !strings.HasPrefix(lines[0], "     8  7  6") || !strings.HasPrefix(lines[1], " 9   车") || strings.Contains(board, "车2") {
		t.Fatalf("black view not rotated: %q %q", lines[0], lines[1])
	}
	for _, view := range []View{red, black} {
		for _, compact := range []bool{false, true} {
			layout := NewBoardLayout(view, compact)
			for y := 0; y < 10; y++ {
				for x := 0; x < 9; x++ {
					want := Pos{x, y}
					col, row := layout.Cell(want)
					got, ok := layout.Hit(col+1, row)
					if !ok || got != want {
						t.Fatalf("view=%v compact=%v %v -> %v %v", view, compact, want, got, ok)
					}
				}
			}
		}
	}
	p, err := ParseDisplayPiece("马2", Black)
	if err != nil || p.Label() != "h2" {
		t.Fatalf("Chinese input: %v %v", p, err)
	}
}
