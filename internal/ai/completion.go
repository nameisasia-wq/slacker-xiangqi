package ai

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

var ErrTokenLimit = errors.New("AI 思考或输出达到 Token 上限，本步未执行")

func readCompletion(res *http.Response) (string, error) {
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&response); err != nil {
			return "", err
		}
		if len(response.Choices) == 0 {
			return "", errors.New("AI 没有返回走法")
		}
		if response.Choices[0].Finish == "length" {
			return "", ErrTokenLimit
		}
		return response.Choices[0].Message.Content, nil
	}
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var content strings.Builder
	var event []string
	finished := false
	consume := func() error {
		if len(event) == 0 {
			return nil
		}
		data := strings.Join(event, "\n")
		event = nil
		if data == "[DONE]" {
			finished = true
			return nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return errors.New("AI 流式接口返回错误，请检查模型、协议和额度")
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Finish == "length" {
				return ErrTokenLimit
			}
			content.WriteString(choice.Delta.Content)
			if choice.Finish != "" {
				finished = true
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return "", err
			}
			if finished {
				return content.String(), nil
			}
		} else if strings.HasPrefix(line, "data:") {
			event = append(event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if err := consume(); err != nil {
		return "", err
	}
	if !finished {
		return "", errors.New("AI 流式回复意外中断，未执行本步走棋")
	}
	return content.String(), nil
}
