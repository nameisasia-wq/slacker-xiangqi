package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"moyu-xiangqi/internal/xiangqi"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesProvidersAndHistory(t *testing.T) {
	for _, provider := range []string{"deepseek", "ark", "bailian"} {
		for _, mode := range []ThinkingMode{ThinkingOff, ThinkingOn, ThinkingDefault} {
			t.Run(provider+"/"+string(mode), func(t *testing.T) {
				a := Agent{Endpoint: "https://api.deepseek.com/responses", Model: "deepseek-flash", Thinking: mode, APIKey: "test-key"}
				if provider == "ark" {
					a.Endpoint = "https://ark.cn-beijing.volces.com/api/v3/responses"
					a.Model = "doubao-seed-2-1-pro-260915"
				}
				if provider == "bailian" {
					a.Endpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/responses"
					a.Model = "deepseek-v4.1-flash"
				}
				calls := 0
				a.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if !strings.HasSuffix(r.URL.Path, "/responses") {
						t.Error("wrong route")
					}
					if r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("missing auth")
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					for _, key := range []string{"messages", "max_tokens", "reasoning_effort", "enable_thinking", "enable_search", "tools", "previous_response_id"} {
						if _, ok := body[key]; ok {
							t.Errorf("wrong field for Responses: %s", key)
						}
					}
					if string(body["store"]) != "false" {
						t.Error("unexpected server-side storage")
					}
					var input []message
					if err := json.Unmarshal(body["input"], &input); err != nil {
						t.Fatal(err)
					}
					if len(input) != calls*2 || input[0].Role != "system" || !strings.Contains(input[0].Content, chessPrimer) {
						t.Fatal("missing context or briefing")
					}
					if calls == 2 && (input[2].Role != "assistant" || !strings.Contains(input[len(input)-1].Content, "0605,0304")) {
						t.Error("lost multi-turn context")
					}
					var reasoning map[string]string
					_ = json.Unmarshal(body["reasoning"], &reasoning)
					if mode == ThinkingDefault {
						if _, ok := body["reasoning"]; ok {
							t.Error("default must not override effort")
						}
						if _, ok := body["thinking"]; ok {
							t.Error("default must not override toggle")
						}
					} else if provider == "ark" {
						var thinking map[string]string
						_ = json.Unmarshal(body["thinking"], &thinking)
						want := "disabled"
						if mode == ThinkingOn {
							want = "enabled"
						}
						if thinking["type"] != want {
							t.Error("wrong Ark thinking toggle")
						}
						if mode == ThinkingOff && len(reasoning) > 0 {
							t.Error("Ark off must not send a conflicting effort")
						}
					} else {
						want := "none"
						if mode == ThinkingOn {
							want = "low"
						}
						if reasoning["effort"] != want {
							t.Errorf("wrong Responses reasoning: %v", reasoning)
						}
						if _, ok := body["thinking"]; ok {
							t.Error("Chat-only toggle sent to Responses")
						}
					}
					if mode == ThinkingOff && string(body["max_output_tokens"]) != "128" {
						t.Error("wrong short budget")
					}
					if mode != ThinkingOff && string(body["max_output_tokens"]) != "16384" {
						t.Error("wrong reasoning budget")
					}
					move := "0605"
					if calls == 2 {
						move = "2625"
					}
					content, _ := json.Marshal(map[string]string{"move": move, "plan": "争中"})
					completed := map[string]any{"status": "completed", "output": []any{
						map[string]any{"type": "reasoning", "content": []any{map[string]string{"type": "reasoning_text", "text": "private-thought"}}},
						map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": string(content)}}},
					}}
					data, _ := json.Marshal(completed)
					headers := make(http.Header)
					if mode != ThinkingOff {
						event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": completed})
						data = []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ignored-until-complete\"}\n\nevent: response.completed\ndata: " + string(event) + "\n\n")
						headers.Set("Content-Type", "text/event-stream")
					}
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				})}
				g := xiangqi.New()
				d, err := a.Choose(context.Background(), g)
				if err != nil {
					t.Fatal(err)
				}
				if err := g.Apply(d.Move); err != nil {
					t.Fatal(err)
				}
				opponent, _ := xiangqi.ParseMove("0304")
				if err := g.Apply(opponent); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Choose(context.Background(), g); err != nil {
					t.Fatal(err)
				}
				for _, turn := range a.history {
					if strings.Contains(turn.reply, "private-thought") {
						t.Error("reasoning leaked into history")
					}
				}
			})
		}
	}
}

func TestResponsesRejectIncompleteAndFailed(t *testing.T) {
	for _, tc := range []struct {
		data            string
		stream, limited bool
	}{
		{`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, false, true},
		{`{"status":"failed"}`, false, false},
		{"data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"move\\\":\\\"0605\\\"}\"}\n\n", true, false},
		{"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", true, true},
		{"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", true, false},
	} {
		headers := make(http.Header)
		if tc.stream {
			headers.Set("Content-Type", "text/event-stream")
		}
		_, err := readResponse(&http.Response{Header: headers, Body: io.NopCloser(strings.NewReader(tc.data))})
		if err == nil || errors.Is(err, ErrTokenLimit) != tc.limited {
			t.Fatalf("error: %v", err)
		}
	}
}
