package main

import (
	"fmt"
	"moyu-xiangqi/internal/ai"
	"moyu-xiangqi/internal/xiangqi"
	"strings"
	"time"
)

func retryTokenLimit(a *ai.Agent, g *xiangqi.Game, ui *screen, in lineReader, elapsed time.Duration) error {
	current := a.TokenLimit()
	next := current
	if current == 0 {
		next = 32768
	} else if current < 65536 {
		next = current * 2
		if next > 65536 {
			next = 65536
		}
	}
	budget := fmt.Sprintf("%d Token", current)
	if current == 0 {
		budget = "服务商预算"
	}
	label := fmt.Sprintf("AI %.0fs/%s耗尽；1 用%d重试；2 关思考重试；q退出", elapsed.Seconds(), budget, next)

	ui.Select(nil)
	for {
		ui.Show(g, label, true)
		var line string
		var err error
		if terminal, ok := in.(*terminalInput); ok {
			var event inputEvent
			event, err = terminal.nextEvent()
			if err == nil && event.draftChanged {
				ui.draft = event.draft
				continue
			}
			if err == nil && event.mouse {
				continue
			}
			line = event.line
		} else {
			line, err = in.ReadString('\n')
		}
		if err != nil {
			return fmt.Errorf("%w；未选择重试：%v", ai.ErrTokenLimit, err)
		}
		ui.draft = ""
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "1":
			a.MaxTokens = next
			return nil
		case "2":
			a.Thinking = ai.ThinkingOff
			a.MaxTokens = 0
			return nil
		case "q", "quit", "exit":
			return errQuit
		}
	}
}
