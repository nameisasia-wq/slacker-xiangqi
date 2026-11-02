package main

import (
	"io"
	"strings"
	"testing"
)

func TestMenuCRLFDoesNotSkipChoices(t *testing.T) {
	in := strings.NewReader("2\r\n马2\r\n\r\n1\n")
	for _, want := range []string{"2", "马2", "", "1"} {
		got, err := readLineFrom(in)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readLineFrom(in); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestRawCRLFDoesNotSubmitTwoCommands(t *testing.T) {
	in := newRawTerminalInput(strings.NewReader("马2\r\n0605\r\nq\r"))
	for _, want := range []string{"马2\n", "0605\n", "q\n"} {
		got, err := in.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := in.ReadString('\n'); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestTerminalRestoreIsIdempotent(t *testing.T) {
	calls := 0
	restore := onceRestore(func() { calls++ })
	restore()
	restore()
	if calls != 1 {
		t.Fatal("console should be restored exactly once")
	}
}
