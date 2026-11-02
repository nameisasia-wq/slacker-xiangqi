package xiangqi

import (
	"errors"
	"fmt"
	"strings"
)

type Side uint8

const (
	None Side = iota
	Red
	Black
)

func (s Side) Other() Side {
	if s == Red {
		return Black
	}
	return Red
}

func (s Side) String() string {
	if s == Red {
		return "红方"
	}
	if s == Black {
		return "黑方"
	}
	return "无"
}

type Kind byte

const (
	General  Kind = 'G'
	Advisor  Kind = 'A'
	Elephant Kind = 'E'
	Horse    Kind = 'H'
	Rook     Kind = 'R'
	Cannon   Kind = 'P'
	Soldier  Kind = 'B'
)

type Piece struct {
	Side Side
	Kind Kind
	ID   uint8
}

func (p Piece) Letter() byte {
	if p.Side == None {
		return '.'
	}
	if p.Side == Black {
		return byte(p.Kind) + ('a' - 'A')
	}
	return byte(p.Kind)
}

func (p Piece) Label() string {
	if p.Side == None {
		return ".."
	}
	return fmt.Sprintf("%c%d", p.Letter(), p.ID)
}

func ParsePiece(label string) (Piece, error) {
	if label == ".." {
		return Piece{}, nil
	}
	if len(label) != 2 || label[1] < '1' || label[1] > '5' {
		return Piece{}, errors.New("无效棋子编号")
	}
	c := label[0]
	side := Red
	if c >= 'a' && c <= 'z' {
		side, c = Black, c-('a'-'A')
	}
	kind := Kind(c)
	maxID := byte('2')
	switch kind {
	case General:
		maxID = '1'
	case Advisor, Elephant, Horse, Rook, Cannon:
	case Soldier:
		maxID = '5'
	default:
		return Piece{}, errors.New("未知棋子")
	}
	if label[1] > maxID {
		return Piece{}, errors.New("无效棋子编号")
	}
	return Piece{Side: side, Kind: kind, ID: label[1] - '0'}, nil
}

type Pos struct{ X, Y int }

func (p Pos) Valid() bool { return p.X >= 0 && p.X < 9 && p.Y >= 0 && p.Y < 10 }

func (p Pos) String() string { return fmt.Sprintf("%d%d", p.X, p.Y) }

type Move struct{ From, To Pos }

func (m Move) String() string { return m.From.String() + m.To.String() }

func ParsePos(s string) (Pos, error) {
	if len(s) != 2 || s[0] < '0' || s[0] > '8' || s[1] < '0' || s[1] > '9' {
		return Pos{}, errors.New("位置格式为 00 到 89（横坐标在前）")
	}
	return Pos{int(s[0] - '0'), int(s[1] - '0')}, nil
}

func ParseMove(s string) (Move, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if len(s) != 4 {
		return Move{}, errors.New("走法格式为 0605（起点横纵坐标 + 终点横纵坐标）")
	}
	from, err := ParsePos(s[:2])
	if err != nil {
		return Move{}, errors.New("走法格式为 0605（起点横纵坐标 + 终点横纵坐标）")
	}
	to, err := ParsePos(s[2:])
	if err != nil {
		return Move{}, errors.New("走法格式为 0605（起点横纵坐标 + 终点横纵坐标）")
	}
	return Move{from, to}, nil
}

type Game struct {
	Board   [10][9]Piece
	Turn    Side
	Winner  Side
	History []string
}

func New() *Game {
	g := &Game{Turn: Red}
	back := []Kind{Rook, Horse, Elephant, Advisor, General, Advisor, Elephant, Horse, Rook}
	counts := map[Kind]uint8{}
	for x, kind := range back {
		counts[kind]++
		g.Board[0][x] = Piece{Black, kind, counts[kind]}
		g.Board[9][x] = Piece{Red, kind, counts[kind]}
	}
	for i, x := range []int{1, 7} {
		g.Board[2][x] = Piece{Black, Cannon, uint8(i + 1)}
		g.Board[7][x] = Piece{Red, Cannon, uint8(i + 1)}
	}
	for x := 0; x < 9; x += 2 {
		g.Board[3][x] = Piece{Black, Soldier, uint8(x/2 + 1)}
		g.Board[6][x] = Piece{Red, Soldier, uint8(x/2 + 1)}
	}
	return g
}

func (g *Game) Clone() *Game {
	c := *g
	c.History = append([]string(nil), g.History...)
	return &c
}

func (g *Game) At(p Pos) Piece {
	if !p.Valid() {
		return Piece{}
	}
	return g.Board[p.Y][p.X]
}

func (g *Game) Find(label string, side Side) (Pos, bool) {
	for y := range g.Board {
		for x, p := range g.Board[y] {
			if p.Side == side && p.Label() == label {
				return Pos{x, y}, true
			}
		}
	}
	return Pos{}, false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (g *Game) blockers(a, b Pos) int {
	if a.X != b.X && a.Y != b.Y {
		return -1
	}
	dx, dy := 0, 0
	if a.X < b.X {
		dx = 1
	} else if a.X > b.X {
		dx = -1
	}
	if a.Y < b.Y {
		dy = 1
	} else if a.Y > b.Y {
		dy = -1
	}
	n := 0
	for p := (Pos{a.X + dx, a.Y + dy}); p != b; p.X, p.Y = p.X+dx, p.Y+dy {
		if g.At(p).Side != None {
			n++
		}
	}
	return n
}

func inPalace(p Pos, side Side) bool {
	if p.X < 3 || p.X > 5 {
		return false
	}
	if side == Red {
		return p.Y >= 7 && p.Y <= 9
	}
	return p.Y >= 0 && p.Y <= 2
}

// attacks checks the piece's movement geometry. The caller handles own-piece occupancy.
func (g *Game) attacks(from, to Pos) bool {
	p := g.At(from)
	if p.Side == None || !to.Valid() || from == to {
		return false
	}
	dx, dy := to.X-from.X, to.Y-from.Y
	ax, ay := abs(dx), abs(dy)
	switch p.Kind {
	case General:
		if g.At(to).Kind == General && from.X == to.X && g.blockers(from, to) == 0 {
			return true
		}
		return inPalace(to, p.Side) && ax+ay == 1
	case Advisor:
		return inPalace(to, p.Side) && ax == 1 && ay == 1
	case Elephant:
		if ax != 2 || ay != 2 {
			return false
		}
		if p.Side == Red && to.Y < 5 || p.Side == Black && to.Y > 4 {
			return false
		}
		return g.At(Pos{from.X + dx/2, from.Y + dy/2}).Side == None
	case Horse:
		if ax == 2 && ay == 1 {
			step := dx / 2
			return g.At(Pos{from.X + step, from.Y}).Side == None
		}
		if ax == 1 && ay == 2 {
			step := dy / 2
			return g.At(Pos{from.X, from.Y + step}).Side == None
		}
		return false
	case Rook:
		return g.blockers(from, to) == 0
	case Cannon:
		n := g.blockers(from, to)
		if g.At(to).Side == None {
			return n == 0
		}
		return n == 1
	case Soldier:
		forward := -1
		crossed := from.Y <= 4
		if p.Side == Black {
			forward, crossed = 1, from.Y >= 5
		}
		return dx == 0 && dy == forward || crossed && ay == 0 && ax == 1
	}
	return false
}

func (g *Game) general(side Side) (Pos, bool) {
	for y := range g.Board {
		for x, p := range g.Board[y] {
			if p.Side == side && p.Kind == General {
				return Pos{x, y}, true
			}
		}
	}
	return Pos{}, false
}

func (g *Game) InCheck(side Side) bool {
	king, ok := g.general(side)
	if !ok {
		return true
	}
	for y := range g.Board {
		for x, p := range g.Board[y] {
			if p.Side == side.Other() && g.attacks(Pos{x, y}, king) {
				return true
			}
		}
	}
	return false
}

func (g *Game) legalFor(side Side, m Move) bool {
	if !m.From.Valid() || !m.To.Valid() {
		return false
	}
	p, dest := g.At(m.From), g.At(m.To)
	if p.Side != side || dest.Side == side || !g.attacks(m.From, m.To) {
		return false
	}
	c := *g
	c.Board[m.To.Y][m.To.X] = p
	c.Board[m.From.Y][m.From.X] = Piece{}
	return !c.InCheck(side)
}

func (g *Game) LegalMoves(side Side) []Move {
	moves := make([]Move, 0, 50)
	for fy := range g.Board {
		for fx, p := range g.Board[fy] {
			if p.Side != side {
				continue
			}
			for ty := range g.Board {
				for tx := range g.Board[ty] {
					m := Move{Pos{fx, fy}, Pos{tx, ty}}
					if g.legalFor(side, m) {
						moves = append(moves, m)
					}
				}
			}
		}
	}
	return moves
}

func (g *Game) LegalMovesFrom(from Pos) []Move {
	p := g.At(from)
	if p.Side == None {
		return nil
	}
	moves := make([]Move, 0, 12)
	for y := range g.Board {
		for x := range g.Board[y] {
			m := Move{From: from, To: Pos{x, y}}
			if g.legalFor(p.Side, m) {
				moves = append(moves, m)
			}
		}
	}
	return moves
}

func (g *Game) Apply(m Move) error {
	if g.Winner != None {
		return errors.New("棋局已结束")
	}
	if !g.legalFor(g.Turn, m) {
		return fmt.Errorf("%s 不是合法走法", m)
	}
	side := g.Turn
	g.Board[m.To.Y][m.To.X] = g.Board[m.From.Y][m.From.X]
	g.Board[m.From.Y][m.From.X] = Piece{}
	g.History = append(g.History, m.String())
	g.Turn = side.Other()
	if _, ok := g.general(g.Turn); !ok || len(g.LegalMoves(g.Turn)) == 0 {
		g.Winner = side
	}
	return nil
}

// Rows preserves stable piece identities in the AI prompt and wire protocol.
func (g *Game) Rows() []string {
	rows := make([]string, 10)
	for y := range g.Board {
		cells := make([]string, 9)
		for x, p := range g.Board[y] {
			cells[x] = p.Label()
		}
		rows[y] = strings.Join(cells, " ")
	}
	return rows
}
