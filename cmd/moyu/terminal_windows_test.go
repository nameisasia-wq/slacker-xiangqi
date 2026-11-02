package main

import (
	"errors"
	"syscall"
	"testing"
)

func fakeConsole(t *testing.T, original uint32) *[]uint32 {
	t.Helper()
	oldGet, oldSet := getConsoleMode, setConsoleMode
	modes := []uint32{}
	getConsoleMode = func(_ syscall.Handle, mode *uint32) error { *mode = original; return nil }
	setConsoleMode = func(_ syscall.Handle, mode uint32) error { modes = append(modes, mode); return nil }
	t.Cleanup(func() { getConsoleMode, setConsoleMode = oldGet, oldSet })
	return &modes
}

func TestWindowsSecretInputRestoresConsole(t *testing.T) {
	original := winEcho | winLine | winQuickEdit | winExtended
	modes := fakeConsole(t, original)
	restore, err := terminalInputMode(false)
	if err != nil {
		t.Fatal(err)
	}
	if (*modes)[0] != original&^winEcho {
		t.Fatal("secret input must disable echo while retaining line editing")
	}
	restore()
	restore()
	if len(*modes) != 2 || (*modes)[1] != original {
		t.Fatal("original input mode was not restored exactly once")
	}
}

func TestWindowsRawInputAndOutputModes(t *testing.T) {
	original := winEcho | winLine | winQuickEdit
	modes := fakeConsole(t, original)
	restore, err := terminalInputMode(true)
	if err != nil {
		t.Fatal(err)
	}
	raw := (*modes)[0]
	if raw&(winEcho|winLine|winQuickEdit) != 0 || raw&(winVTInput|winExtended|winMouse) != (winVTInput|winExtended|winMouse) {
		t.Fatal("incorrect VT input flags")
	}
	restore()
	ok, restoreOutput := initializeConsole()
	if !ok {
		t.Fatal("VT output was not enabled")
	}
	if (*modes)[2] != (original | winVTOutput | winProcessedOutput) {
		t.Fatal("incorrect VT output flags")
	}
	restoreOutput()
	if (*modes)[3] != original {
		t.Fatal("output mode not restored")
	}
}

func TestWindowsUnsupportedConsoleDoesNotPretendSuccess(t *testing.T) {
	_ = fakeConsole(t, 0)
	setConsoleMode = func(syscall.Handle, uint32) error { return errors.New("unsupported") }
	ok, restore := initializeConsole()
	restore()
	if ok {
		t.Fatal("unsupported output must use plain-text fallback")
	}
	if _, err := terminalInputMode(false); err == nil {
		t.Fatal("failed echo disabling must prevent reading a secret")
	}
}
