package main

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	winEcho            uint32 = 0x0004
	winLine            uint32 = 0x0002
	winQuickEdit       uint32 = 0x0040
	winExtended        uint32 = 0x0080
	winVTInput         uint32 = 0x0200
	winMouse           uint32 = 0x0010
	winProcessedOutput uint32 = 0x0001
	winVTOutput        uint32 = 0x0004
)

var consoleDLL = syscall.NewLazyDLL("kernel32.dll")
var setConsoleModeProc = consoleDLL.NewProc("SetConsoleMode")
var screenBufferInfoProc = consoleDLL.NewProc("GetConsoleScreenBufferInfo")
var getConsoleMode = syscall.GetConsoleMode
var setConsoleMode = func(handle syscall.Handle, mode uint32) error {
	result, _, err := setConsoleModeProc.Call(uintptr(handle), uintptr(mode))
	if result == 0 {
		if err == syscall.Errno(0) {
			return syscall.EINVAL
		}
		return err
	}
	return nil
}

func initializeConsole() (bool, func()) {
	handle := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if err := getConsoleMode(handle, &mode); err != nil {
		return false, func() {}
	}
	if err := setConsoleMode(handle, mode|winProcessedOutput|winVTOutput); err != nil {
		return false, func() {}
	}
	return true, onceRestore(func() { _ = setConsoleMode(handle, mode) })
}

func terminalInputMode(raw bool) (func(), error) {
	handle := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := getConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	next := mode &^ winEcho
	if raw {
		next = (next | winVTInput | winExtended | winMouse) &^ (winLine | winQuickEdit)
	}
	if err := setConsoleMode(handle, next); err != nil {
		return nil, err
	}
	return onceRestore(func() { _ = setConsoleMode(handle, mode) }), nil
}

func terminalSelection(enabled bool) error {
	handle := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := getConsoleMode(handle, &mode); err != nil {
		return err
	}
	mode |= winExtended
	if enabled {
		mode |= winQuickEdit
	} else {
		mode &^= winQuickEdit
	}
	return setConsoleMode(handle, mode)
}

type consoleCoord struct{ X, Y int16 }
type consoleBufferInfo struct {
	Size       consoleCoord
	Cursor     consoleCoord
	Attributes uint16
	Window     struct{ Left, Top, Right, Bottom int16 }
	Maximum    consoleCoord
}

func terminalSize() (int, int, error) {
	var info consoleBufferInfo
	result, _, err := screenBufferInfoProc.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		if err == syscall.Errno(0) {
			return 0, 0, syscall.EINVAL
		}
		return 0, 0, err
	}
	// Use the visible window, not the scrollback buffer's larger dimensions.
	return int(info.Window.Bottom-info.Window.Top) + 1, int(info.Window.Right-info.Window.Left) + 1, nil
}
