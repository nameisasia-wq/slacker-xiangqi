package main

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"moyu-xiangqi/internal/xiangqi"
)

func TestRawInputMouseAndChineseKeyboard(t *testing.T) {
	in := newRawTerminalInput(strings.NewReader("\x1b[<0;6;14M\x1b[<0;6;14m马1\r"))
	var mouse inputEvent
	for {
		event, err := in.nextEvent()
		if err != nil {
			t.Fatal(err)
		}
		if event.mouse {
			mouse = event
			break
		}
	}
	if mouse.col != 6 || mouse.row != 14 {
		t.Fatalf("mouse=%+v", mouse)
	}
	for {
		event, err := in.nextEvent()
		if err != nil {
			t.Fatal(err)
		}
		if event.line != "" {
			if event.line != "马1\n" {
				t.Fatalf("line=%q", event.line)
			}
			break
		}
	}
	if _, err := in.ReadString('\n'); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestMouseSelectSwitchAndCapture(t *testing.T) {
	g := xiangqi.New()
	// Put an enemy piece on 05 so the red soldier can capture it.
	g.Board[5][0] = xiangqi.Piece{Side: xiangqi.Black, Kind: xiangqi.Soldier, ID: 1}
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, color: true, rows: 24, cols: 80, view: xiangqi.View{Language: xiangqi.Chinese, Bottom: xiangqi.Red}}
	layout := xiangqi.NewBoardLayout(ui.view, false)
	click := func(p xiangqi.Pos) inputEvent { c, r := layout.Cell(p); return inputEvent{mouse: true, col: c, row: r} }
	in := &terminalInput{events: make(chan inputEvent, 8)}
	in.events <- click(xiangqi.Pos{X: 2, Y: 6})
	in.events <- click(xiangqi.Pos{X: 0, Y: 6})
	in.events <- click(xiangqi.Pos{X: 0, Y: 5})
	move, err := readMove(in, g, ui, "开始", "", "")
	if err != nil || move.String() != "0605" {
		t.Fatalf("move=%s err=%v", move, err)
	}
	if !strings.Contains(output.String(), "\x1b[1;33m") {
		t.Fatal("selected piece not gold")
	}
	if !strings.Contains(output.String(), "兵 在 06") {
		t.Fatal("selection did not switch")
	}
}

func TestChineseKeyboardMove(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80, view: xiangqi.View{Language: xiangqi.Chinese, Bottom: xiangqi.Red}}
	move, err := readMove(bufio.NewReader(strings.NewReader("车2\n88\n")), xiangqi.New(), ui, "", "", "")
	if err != nil || move.String() != "8988" {
		t.Fatalf("move=%s err=%v", move, err)
	}
}
