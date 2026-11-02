package xiangqi

import "testing"

func mustMove(t *testing.T, s string) Move {
	t.Helper()
	m, err := ParseMove(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func bare() *Game {
	g := &Game{Turn: Red}
	g.Board[9][4] = Piece{Red, General, 1}
	g.Board[0][4] = Piece{Black, General, 1}
	g.Board[5][4] = Piece{Red, Soldier, 1}
	return g
}

func TestInitialPositionAndTurn(t *testing.T) {
	g := New()
	if g.Rows()[0] != "r1 h1 e1 a1 g1 a2 e2 h2 r2" || g.Rows()[9] != "R1 H1 E1 A1 G1 A2 E2 H2 R2" {
		t.Fatalf("bad initial rows: %v", g.Rows())
	}
	if err := g.Apply(mustMove(t, "0605")); err != nil {
		t.Fatal(err)
	}
	if g.Turn != Black || g.Board[5][0].Label() != "B1" {
		t.Fatal("turn or board not updated")
	}
	if err := g.Apply(mustMove(t, "2625")); err == nil {
		t.Fatal("red moved twice")
	}
}

func TestHorseLegElephantRiverAndCannonScreen(t *testing.T) {
	g := bare()
	g.Board[7][1] = Piece{Red, Horse, 1}
	if !g.legalFor(Red, mustMove(t, "1738")) {
		t.Fatal("horse should move")
	}
	g.Board[7][2] = Piece{Red, Soldier, 2}
	if g.legalFor(Red, mustMove(t, "1738")) {
		t.Fatal("blocked horse moved")
	}
	g.Board[5][2] = Piece{Red, Elephant, 1}
	if g.legalFor(Red, mustMove(t, "2543")) {
		t.Fatal("elephant crossed river")
	}
	g.Board[9][0] = Piece{Red, Cannon, 1}
	g.Board[5][0] = Piece{Black, Rook, 1}
	if g.legalFor(Red, mustMove(t, "0905")) {
		t.Fatal("cannon captured without screen")
	}
	g.Board[7][0] = Piece{Red, Soldier, 2}
	if !g.legalFor(Red, mustMove(t, "0905")) {
		t.Fatal("cannon could not capture over one screen")
	}
}

func TestGeneralFacingAndWin(t *testing.T) {
	g := bare()
	if g.legalFor(Red, mustMove(t, "4555")) {
		t.Fatal("exposing generals was allowed")
	}
	g.Board[5][4] = Piece{}
	if !g.InCheck(Red) || !g.InCheck(Black) {
		t.Fatal("facing generals must check each other")
	}
	if err := g.Apply(mustMove(t, "4940")); err != nil {
		t.Fatal(err)
	}
	if g.Winner != Red {
		t.Fatalf("expected red win, got %v", g.Winner)
	}
}

func TestMoveParser(t *testing.T) {
	for _, s := range []string{"z909", "9109", "090", "09a9"} {
		if _, err := ParseMove(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if got := mustMove(t, "09 08").String(); got != "0908" {
		t.Fatal(got)
	}
}

func TestStablePieceIDAndLegalDestinations(t *testing.T) {
	g := New()
	from, ok := g.Find("R2", Red)
	if !ok || from != (Pos{8, 9}) {
		t.Fatal("R2 missing")
	}
	if got := g.LegalMovesFrom(from); len(got) != 2 || got[0].To != (Pos{8, 7}) || got[1].To != (Pos{8, 8}) {
		t.Fatalf("R2 destinations: %v", got)
	}
	if err := g.Apply(mustMove(t, "8988")); err != nil {
		t.Fatal(err)
	}
	if moved, ok := g.Find("R2", Red); !ok || moved != (Pos{8, 8}) {
		t.Fatal("R2 was renumbered")
	}
}

func TestElephantEyeBlocksOnlyItsDiagonal(t *testing.T) {
	g := New()
	from, ok := g.Find("E2", Red)
	if !ok || from != (Pos{6, 9}) {
		t.Fatal("E2 missing")
	}
	check := func(want ...Pos) {
		t.Helper()
		moves := g.LegalMovesFrom(from)
		if len(moves) != len(want) {
			t.Fatalf("destinations: %v", moves)
		}
		for i, m := range moves {
			if m.To != want[i] {
				t.Fatalf("destinations: %v", moves)
			}
		}
	}
	check(Pos{4, 7}, Pos{8, 7})
	g.Board[8][7] = Piece{Red, Soldier, 4}
	check(Pos{4, 7})
	g.Board[8][5] = Piece{Red, Soldier, 3}
	check()
}

func TestRookStopsBeforeOwnPieceAndCapturesEnemy(t *testing.T) {
	g := bare()
	g.Board[5][2] = Piece{Red, Rook, 2}
	g.Board[3][2] = Piece{Red, Soldier, 2}
	g.Board[7][2] = Piece{Black, Horse, 1}
	from := Pos{2, 5}
	vertical := map[Pos]bool{}
	for _, move := range g.LegalMovesFrom(from) {
		if move.To.X == 2 {
			vertical[move.To] = true
		}
	}
	for _, p := range []Pos{{2, 4}, {2, 6}, {2, 7}} {
		if !vertical[p] {
			t.Fatalf("missing legal target %s: %v", p, vertical)
		}
	}
	for _, p := range []Pos{{2, 3}, {2, 2}, {2, 8}} {
		if vertical[p] {
			t.Fatalf("passed blocker at %s: %v", p, vertical)
		}
	}
}
