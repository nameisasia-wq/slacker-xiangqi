package main

import (
	"strings"
	"testing"
	"time"
)

func TestEarlyMoveIsHeldUntilTurn(t *testing.T) {
	in := newTerminalInput(strings.NewReader("0304\nq\n"))
	line, err := in.wait(time.Second)
	if err != nil || line != "0304" {
		t.Fatalf("waiting: %q %v", line, err)
	}
	in.hold(line)
	line, err = in.ReadString('\n')
	if err != nil || line != "0304\n" {
		t.Fatalf("held move: %q %v", line, err)
	}
	line, err = in.wait(time.Second)
	if err != nil || line != "q" {
		t.Fatalf("quit while waiting: %q %v", line, err)
	}
}
