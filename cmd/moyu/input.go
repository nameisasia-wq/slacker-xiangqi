package main

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type lineReader interface{ ReadString(byte) (string, error) }

type inputEvent struct {
	line         string
	draft        string
	draftChanged bool
	col          int
	row          int
	mouse        bool
	err          error
}

// One goroutine owns stdin throughout the game, including while waiting for a peer.
type terminalInput struct {
	events  chan inputEvent
	pending []string
	alerts  <-chan error
}

func newTerminalInput(r io.Reader) *terminalInput {
	in := &terminalInput{events: make(chan inputEvent, 64)}
	go func() {
		defer close(in.events)
		reader := bufio.NewReader(r)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				in.events <- inputEvent{line: line}
			}
			if err != nil {
				in.events <- inputEvent{err: err}
				return
			}
		}
	}()
	return in
}

func newRawTerminalInput(r io.Reader) *terminalInput {
	in := &terminalInput{events: make(chan inputEvent, 64)}
	go func() {
		defer close(in.events)
		reader := bufio.NewReader(r)
		var draft []byte
		previousCR := false
		for {
			ch, err := reader.ReadByte()
			if err != nil {
				in.events <- inputEvent{err: err}
				return
			}
			if ch == '\n' && previousCR {
				previousCR = false
				continue
			}
			previousCR = ch == '\r'
			switch ch {
			case 3, 4:
				in.events <- inputEvent{err: io.EOF}
				return
			case 27:
				if event, ok := readEscape(reader); ok {
					in.events <- event
				}
			case 13, 10:
				in.events <- inputEvent{line: string(draft) + "\n"}
				draft = draft[:0]
				in.events <- inputEvent{draft: "", draftChanged: true}
			case 8, 127:
				if len(draft) > 0 {
					_, n := utf8.DecodeLastRune(draft)
					draft = draft[:len(draft)-n]
					in.events <- inputEvent{draft: string(draft), draftChanged: true}
				}
			default:
				if ch >= 32 {
					draft = append(draft, ch)
					if utf8.Valid(draft) {
						in.events <- inputEvent{draft: string(draft), draftChanged: true}
					}
				}
			}
		}
	}()
	return in
}

func readEscape(reader *bufio.Reader) (inputEvent, bool) {
	ch, err := reader.ReadByte()
	if err != nil || ch != '[' {
		return inputEvent{}, false
	}
	ch, err = reader.ReadByte()
	if err != nil {
		return inputEvent{}, false
	}
	if ch == 'M' {
		var report [3]byte
		if _, err := io.ReadFull(reader, report[:]); err != nil {
			return inputEvent{}, false
		}
		button, col, row := int(report[0])-32, int(report[1])-32, int(report[2])-32
		if button&3 != 0 || button&32 != 0 || button&64 != 0 || button&128 != 0 || col < 1 || row < 1 {
			return inputEvent{}, false
		}
		return inputEvent{mouse: true, col: col, row: row}, true
	}
	if ch != '<' {
		for i := 0; i < 32 && ch < 0x40; i++ {
			ch, err = reader.ReadByte()
			if err != nil {
				break
			}
		}
		return inputEvent{}, false
	}
	var seq []byte
	for len(seq) < 32 {
		ch, err = reader.ReadByte()
		if err != nil {
			return inputEvent{}, false
		}
		if ch == 'M' || ch == 'm' {
			if ch != 'M' {
				return inputEvent{}, false
			}
			parts := strings.Split(string(seq), ";")
			if len(parts) != 3 {
				return inputEvent{}, false
			}
			button, e1 := strconv.Atoi(parts[0])
			col, e2 := strconv.Atoi(parts[1])
			row, e3 := strconv.Atoi(parts[2])
			if e1 != nil || e2 != nil || e3 != nil || button&3 != 0 || button&32 != 0 || button&64 != 0 || button&128 != 0 || col < 1 || row < 1 {
				return inputEvent{}, false
			}
			return inputEvent{mouse: true, col: col, row: row}, true
		}
		seq = append(seq, ch)
	}
	return inputEvent{}, false
}

func (in *terminalInput) nextEvent() (inputEvent, error) {
	select {
	case err := <-in.alerts:
		return inputEvent{}, err
	default:
	}
	if len(in.pending) > 0 {
		line := in.pending[0]
		in.pending = in.pending[1:]
		return inputEvent{line: line + "\n"}, nil
	}
	select {
	case err := <-in.alerts:
		return inputEvent{}, err
	case event, ok := <-in.events:
		if !ok {
			return inputEvent{}, io.EOF
		}
		return event, event.err
	}
}

func (in *terminalInput) ReadString(byte) (string, error) {
	for {
		event, err := in.nextEvent()
		if err != nil {
			return "", err
		}
		if event.line != "" {
			return event.line, nil
		}
	}
}

func (in *terminalInput) hold(line string) {
	if line != "" {
		in.pending = append(in.pending, line)
	}
}

func (in *terminalInput) wait(delay time.Duration) (string, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case err := <-in.alerts:
			return "", err
		case event, ok := <-in.events:
			if !ok {
				return "", io.EOF
			}
			if event.err != nil {
				return "", event.err
			}
			if event.line != "" {
				return strings.TrimSpace(event.line), nil
			}
		case <-timer.C:
			return "", nil
		}
	}
}
