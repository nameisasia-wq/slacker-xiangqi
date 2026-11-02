package ai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type responseObject struct {
	Status     string `json:"status"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func responseText(response responseObject) (string, error) {
	if response.Status == "incomplete" && response.Incomplete.Reason == "max_output_tokens" {
		return "", ErrTokenLimit
	}
	if response.Status != "completed" {
		return "", fmt.Errorf("Responses 未正常完成（status=%q, reason=%q），本步未执行", response.Status, response.Incomplete.Reason)
	}
	var text strings.Builder
	for _, item := range response.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("Responses 没有返回最终走棋文本")
	}
	return text.String(), nil
}

// Responses uses semantic events; neither text deltas nor [DONE] prove success.
// Only a completed response object may become a move, preventing execution of
// text produced before a later incomplete/failed event.
func readResponse(res *http.Response) (string, error) {
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		var response responseObject
		if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&response); err != nil {
			return "", err
		}
		return responseText(response)
	}
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	consume := func() (string, bool, error) {
		if len(data) == 0 {
			return "", false, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		var event struct {
			Type     string         `json:"type"`
			Response responseObject `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return "", false, err
		}
		switch event.Type {
		case "response.completed", "response.incomplete", "response.failed":
			if event.Type != "response.completed" && event.Response.Status == "completed" {
				return "", true, fmt.Errorf("Responses 终止事件与状态不一致，本步未执行")
			}
			text, err := responseText(event.Response)
			return text, true, err
		case "error":
			return "", true, fmt.Errorf("Responses 流式接口返回错误，本步未执行")
		}
		return "", false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			text, done, err := consume()
			if done || err != nil {
				return text, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	text, done, err := consume()
	if done || err != nil {
		return text, err
	}
	return "", fmt.Errorf("Responses 流式回复在完成事件前中断，本步未执行")
}
