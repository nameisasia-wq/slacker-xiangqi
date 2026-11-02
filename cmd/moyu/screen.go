package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"moyu-xiangqi/internal/xiangqi"
)

type screen struct {
	out          io.Writer
	interactive  bool
	color        bool
	rows         int
	cols         int
	lastRows     int
	lastCols     int
	lastFrame    []string
	initialized  bool
	view         xiangqi.View
	layout       xiangqi.BoardLayout
	draft        string
	inputRestore func()
	mouse        bool
	shareURL     string
	shareToken   string
	cleanup      func()
	once         sync.Once
	done         chan struct{}
}

func newScreen() *screen {
	outInfo, outErr := os.Stdout.Stat()
	inInfo, inErr := os.Stdin.Stat()
	interactive := consoleVisual && outErr == nil && inErr == nil && outInfo.Mode()&os.ModeCharDevice != 0 && inInfo.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
	s := &screen{out: os.Stdout, interactive: interactive, color: colorEnabled(), rows: 24, cols: 80, done: make(chan struct{}), view: xiangqi.View{Bottom: xiangqi.Red}}
	if interactive {
		s.size()
		fmt.Fprint(s.out, "\x1b[?1049h")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			s.Close()
			restoreConsole()
			os.Exit(130)
		case <-s.done:
			signal.Stop(sig)
		}
	}()
	return s
}

func (s *screen) Close() {
	s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup()
		}
		if s.interactive {
			if s.mouse {
				fmt.Fprint(s.out, "\x1b[?1000l\x1b[?1006l")
			}
			if s.inputRestore != nil {
				s.inputRestore()
			}
			fmt.Fprint(s.out, "\x1b[?25h\x1b[?1049l")
		}
		close(s.done)
	})
}

func (s *screen) SetShare(url, token string) {
	s.shareURL, s.shareToken = url, token
}

func wrapCells(value string, width int) []string {
	if width < 2 {
		width = 2
	}
	var lines []string
	var b strings.Builder
	used := 0
	for _, r := range value {
		w := cellWidth(r)
		if used+w > width && used > 0 {
			lines = append(lines, b.String())
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += w
	}
	lines = append(lines, b.String())
	return lines
}

func (s *screen) shareLines() []string {
	if s.shareURL == "" && s.shareToken == "" {
		return nil
	}
	width := s.cols - 1
	var lines []string
	if s.shareURL != "" {
		lines = append(lines, wrapCells("公网地址："+s.shareURL, width)...)
	} else {
		lines = append(lines, "公网隧道已断开，地址失效")
	}
	if s.shareToken != "" {
		lines = append(lines, wrapCells("邀请口令："+s.shareToken, width)...)
	}
	if s.inputRestore != nil {
		if s.mouse {
			lines = append(lines, wrapCells("输入 m 回车暂停鼠标，即可拖选复制；再输入 m 回车恢复", width)...)
		} else {
			lines = append(lines, wrapCells("复制模式：拖选地址和口令；输入 m 回车恢复鼠标", width)...)
		}
	}
	return lines
}

// EnableMouse uses xterm click reports and restores the exact prior tty state on Close.
func (s *screen) EnableMouse() bool {
	if !s.interactive || s.out != os.Stdout {
		return false
	}
	restore, err := terminalInputMode(true)
	if err != nil {
		return false
	}
	s.inputRestore = restore
	s.mouse = true
	fmt.Fprint(s.out, "\x1b[?1000h\x1b[?1006h")
	return true
}

// ToggleMouse lets the terminal handle text selection while the board remains visible.
func (s *screen) ToggleMouse() string {
	if !s.interactive || s.inputRestore == nil {
		return "当前终端没有启用棋盘鼠标操作，可直接拖选文字"
	}
	if s.out == os.Stdout {
		if err := terminalSelection(s.mouse); err != nil {
			return "无法切换终端选择模式：" + err.Error()
		}
	}
	if s.mouse {
		fmt.Fprint(s.out, "\x1b[?1000l\x1b[?1006l")
		s.mouse = false
		return "已暂停棋盘鼠标操作；现在可拖选复制地址和口令，输入 m 回车恢复"
	}
	fmt.Fprint(s.out, "\x1b[?1000h\x1b[?1006h")
	s.mouse = true
	return "已恢复棋盘鼠标操作"
}

func (s *screen) Hit(col, row int) (xiangqi.Pos, bool) { return s.layout.Hit(col, row) }

func (s *screen) Select(p *xiangqi.Pos) {
	s.view.Selected = p
}

func (s *screen) size() {
	rows, cols, err := terminalSize()
	if err == nil && rows > 0 && cols > 0 {
		s.rows, s.cols = rows, cols
	}
}

func cellWidth(r rune) int {
	if r >= 0x1100 && (r <= 0x115f || r >= 0x2329 && r <= 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300) {
		return 2
	}
	return 1
}

func fit(s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if width < 2 {
		return ""
	}
	total := 0
	for _, r := range s {
		total += cellWidth(r)
	}
	if total <= width {
		return s
	}
	used := 0
	var b strings.Builder
	for _, r := range s {
		w := cellWidth(r)
		if used+w > width-1 {
			return b.String() + "…"
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

// Show updates changed terminal rows in the alternate screen. A pipe or log
// receives ordinary successive frames instead.
func (s *screen) Show(g *xiangqi.Game, message string, prompt bool) {
	if s.interactive {
		if s.out == os.Stdout {
			s.size()
		}
	}
	full := xiangqi.NewBoardLayout(s.view, false)
	compact := xiangqi.NewBoardLayout(s.view, true)
	share := s.shareLines()
	s.layout = full
	if s.interactive && (s.rows < full.MinHeight()+len(share) || s.cols < full.MinWidth()) {
		s.layout = compact
	}
	board := g.RenderView(s.color, s.view, s.layout.Compact)
	if s.interactive && (s.rows < s.layout.MinHeight()+len(share) || s.cols < s.layout.MinWidth()) {
		board = fmt.Sprintf("终端太小：请调整到至少 %d 列 × %d 行\n", compact.MinWidth(), compact.MinHeight()+len(share))
	}
	status := g.Status()
	if !s.interactive {
		fmt.Fprint(s.out, board)
		for _, line := range share {
			fmt.Fprintln(s.out, line)
		}
		fmt.Fprintln(s.out, status)
		fmt.Fprintln(s.out, message)
		if prompt {
			fmt.Fprint(s.out, "走棋 > "+s.draft)
		}
		return
	}
	status = fit(status, s.cols-1)
	message = fit(message, s.cols-1)
	frame := strings.Split(strings.TrimSuffix(board, "\n"), "\n")
	frame = append(frame, share...)
	frame = append(frame, status, message)
	displayedDraft := fit(s.draft, s.cols-8)
	if prompt {
		frame = append(frame, "走棋 > "+displayedDraft)
	} else {
		frame = append(frame, "")
	}
	if !s.initialized || s.rows != s.lastRows || s.cols != s.lastCols {
		fmt.Fprint(s.out, "\x1b[H\x1b[2J")
		s.lastFrame = nil
		s.initialized = true
		s.lastRows, s.lastCols = s.rows, s.cols
	}
	fmt.Fprint(s.out, "\x1b[?25l")
	for i, line := range frame {
		// Canonical mode echoes input; redraw the prompt after each command.
		if i < len(s.lastFrame) && line == s.lastFrame[i] && (i != len(frame)-1 || s.mouse) {
			continue
		}
		fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", i+1, line)
	}
	for i := len(frame); i < len(s.lastFrame); i++ {
		fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K", i+1)
	}
	col := 1
	if prompt {
		col = 8
		for _, r := range displayedDraft {
			col += cellWidth(r)
		}
	}
	fmt.Fprintf(s.out, "\x1b[%d;%dH", len(frame), col)
	fmt.Fprint(s.out, "\x1b[?25h")
	s.lastFrame = frame
}
