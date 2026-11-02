package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"moyu-xiangqi/internal/xiangqi"
)

func TestScreenReusesOneFrameAndReadsCommands(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80}
	g := xiangqi.New()
	input := bufio.NewReader(strings.NewReader("moves\nmoves 06\n0605\n"))
	move, err := readMove(input, g, ui, "开始走棋", "", "")
	if err != nil || move.String() != "0605" {
		t.Fatalf("move=%s err=%v", move, err)
	}
	if strings.Count(output.String(), "\x1b[H\x1b[2J") != 1 {
		t.Fatal("the screen should be cleared only once")
	}
	if strings.Count(output.String(), "\x1b[1;1H") != 1 {
		t.Fatal("unchanged board header should not be redrawn")
	}
	if strings.Count(output.String(), "\x1b[23;1H\x1b[2K走棋 > ") != 3 {
		t.Fatal("input line should be redrawn after each command")
	}
}

func TestCompactFrameForShortTerminal(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 16, cols: 30}
	ui.Show(xiangqi.New(), "等待对手", true)
	frame := output.String()
	if !strings.Contains(frame, "0 1 2 3 4 5 6 7 8") {
		t.Fatal("compact board was not selected")
	}
	if !strings.Contains(frame, "\x1b[15;1H\x1b[2K走棋 > ") {
		t.Fatal("compact prompt was not placed at a fixed row")
	}
}

func TestMoveChangesOnlyBoardRowsAndStatus(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80}
	g := xiangqi.New()
	ui.Show(g, "开始走棋", true)
	output.Reset()
	move, _ := xiangqi.ParseMove("0605")
	if err := g.Apply(move); err != nil {
		t.Fatal(err)
	}
	ui.Show(g, "已走 0605", true)
	delta := output.String()
	if strings.Contains(delta, "\x1b[2J") || strings.Contains(delta, "\x1b[1;1H") {
		t.Fatal("move cleared or redrew the full board")
	}
	if !strings.Contains(delta, "\x1b[12;1H") || !strings.Contains(delta, "\x1b[14;1H") {
		t.Fatal("changed piece rows were not updated")
	}
}

func TestSelectPieceThenDestination(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80}
	g := xiangqi.New()
	input := bufio.NewReader(strings.NewReader("R2\n86\n88\n"))
	move, err := readMove(input, g, ui, "开始走棋", "", "")
	if err != nil || move.String() != "8988" {
		t.Fatalf("move=%s err=%v", move, err)
	}
	if !strings.Contains(output.String(), "R2 在 89，可走：87~88") || !strings.Contains(output.String(), "不能走到 86") {
		t.Fatal("missing piece selection guidance")
	}
	if got := formatTargets([]xiangqi.Move{{To: xiangqi.Pos{X: 0, Y: 5}}, {To: xiangqi.Pos{X: 0, Y: 1}}, {To: xiangqi.Pos{X: 0, Y: 2}}, {To: xiangqi.Pos{X: 0, Y: 3}}}); got != "01~03 05" {
		t.Fatal(got)
	}
}

func TestShareDetailsAreCompleteInsideFixedPanel(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80}
	url := "https://long-random-quick-tunnel-name.trycloudflare.com"
	token := strings.Repeat("a", 48)
	ui.SetShare(url, token)
	ui.Show(xiangqi.New(), "等待对手", true)
	if !ui.layout.Compact {
		t.Fatal("share footer should use compact board at 24 rows")
	}
	if !strings.Contains(output.String(), url) || !strings.Contains(output.String(), token) {
		t.Fatal("public address or token was hidden or truncated")
	}
	ui.cols = 33
	lines := ui.shareLines()
	if len(lines) < 4 {
		t.Fatalf("expected wrapped invitation, got %v", lines)
	}
	if !strings.Contains(strings.Join(lines, ""), url) || !strings.Contains(strings.Join(lines, ""), token) {
		t.Fatal("wrapped invitation lost characters")
	}
}

func TestInviteAndTunnelCommandsRedisplayDetails(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: false, rows: 24, cols: 80, view: xiangqi.View{Bottom: xiangqi.Red}}
	invite := "邀请口令：test-secret"
	address := "公网地址：https://sample.trycloudflare.com"
	input := bufio.NewReader(strings.NewReader("t\ni\nq\n"))
	_, err := readMove(input, xiangqi.New(), ui, "开始走棋", invite, address)
	if err != errQuit {
		t.Fatalf("expected quit, got %v", err)
	}
	if !strings.Contains(output.String(), invite) || !strings.Contains(output.String(), address) {
		t.Fatal("i/t did not redisplay the invitation")
	}
}

func TestMouseCanPauseForInvitationCopyAndResume(t *testing.T) {
	var output bytes.Buffer
	ui := &screen{out: &output, interactive: true, rows: 24, cols: 80, termState: "saved", mouse: true, view: xiangqi.View{Bottom: xiangqi.Red}}
	ui.SetShare("https://sample.trycloudflare.com", "test-secret")
	input := bufio.NewReader(strings.NewReader("m\nm\n0605\n"))
	move, err := readMove(input, xiangqi.New(), ui, "开始走棋", "邀请口令：test-secret", "公网地址：https://sample.trycloudflare.com")
	if err != nil || move.String() != "0605" {
		t.Fatalf("move=%s err=%v", move, err)
	}
	frame := output.String()
	if !strings.Contains(frame, "\x1b[?1000l\x1b[?1006l") || !strings.Contains(frame, "\x1b[?1000h\x1b[?1006h") {
		t.Fatal("mouse reporting was not disabled and restored")
	}
	if !strings.Contains(frame, "复制模式：拖选地址和口令") || !strings.Contains(frame, "已恢复棋盘鼠标操作") {
		t.Fatal("copy mode guidance was not shown")
	}
	if !ui.mouse {
		t.Fatal("mouse was not restored")
	}
}
