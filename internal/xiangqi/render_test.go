package xiangqi

import (
	"strings"
	"testing"
)

func TestBoardHasPlainRiverAndCompactFrame(t *testing.T) {
	g := New()
	full := g.Render(false)
	if !strings.Contains(full, "r1") || !strings.Contains(full, "R2") {
		t.Fatal("piece numbers are missing")
	}
	if strings.Contains(full, "楚") || strings.Contains(full, "汉") {
		t.Fatal("river label is still displayed")
	}
	if !strings.Contains(full, "│"+strings.Repeat(" ", 32)+"│") {
		t.Fatal("river boundary is not blank")
	}
	if lines := strings.Count(full, "\n"); lines != 20 {
		t.Fatalf("expected 20 board lines, got %d", lines)
	}
	if lines := strings.Count(g.RenderCompact(false), "\n"); lines != 12 {
		t.Fatalf("expected 12 compact lines, got %d", lines)
	}
}
