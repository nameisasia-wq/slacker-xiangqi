package main

import "sync"

var consoleVisual = true
var restoreConsole = func() {}

func onceRestore(restore func()) func() {
	var once sync.Once
	return func() { once.Do(restore) }
}
