//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func initializeConsole() (bool, func()) { return true, func() {} }

func terminalInputMode(raw bool) (func(), error) {
	get := exec.Command("stty", "-g")
	get.Stdin = os.Stdin
	state, err := get.Output()
	if err != nil {
		return nil, err
	}
	args := []string{"-echo"}
	if raw {
		args = append(args, "-icanon", "min", "1", "time", "0")
	}
	set := exec.Command("stty", args...)
	set.Stdin = os.Stdin
	if err := set.Run(); err != nil {
		return nil, err
	}
	return onceRestore(func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = os.Stdin
		if err := restore.Run(); err != nil {
			fallback := exec.Command("stty", "echo", "icanon")
			fallback.Stdin = os.Stdin
			_ = fallback.Run()
		}
	}), nil
}

func terminalSize() (int, int, error) {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(string(output))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid terminal size")
	}
	rows, e1 := strconv.Atoi(parts[0])
	cols, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || rows <= 0 || cols <= 0 {
		return 0, 0, fmt.Errorf("invalid terminal size")
	}
	return rows, cols, nil
}

func terminalSelection(bool) error { return nil }
