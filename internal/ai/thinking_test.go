package ai

import (
	"context"
	"errors"
	"io"
	"moyu-xiangqi/internal/xiangqi"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThinkingAdapters(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, model, api string
		mode                       ThinkingMode
		wantErr                    bool
	}{
		{"bailian-on", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-v4.1-flash", "", ThinkingOn, false},
		{"deepseek-off", "https://api.deepseek.com/chat/completions", "deepseek-flash", "", ThinkingOff, false},
		{"deepseek-on", "https://api.deepseek.com/chat/completions", "deepseek-v4-pro", "", ThinkingOn, false},
		{"provider-default", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-v4.1-flash", "", ThinkingDefault, false},
		{"gateway-explicit", "https://example.com/v1/chat/completions", "custom", "bailian", ThinkingOn, false},
		{"unknown-on", "https://example.com/v1/chat/completions", "custom", "", ThinkingOn, true},
		{"unknown-off", "https://example.com/v1/chat/completions", "custom", "", ThinkingOff, true},
		{"invalid-mode", "https://example.com/v1/chat/completions", "custom", "", ThinkingMode("oops"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Agent{Endpoint: tc.endpoint, Model: tc.model, ThinkingAPI: tc.api, Thinking: tc.mode}
			body := map[string]any{}
			timeout, err := a.requestSettings(body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v", err)
			}
			if err != nil {
				return
			}
			_, protocol, _ := a.ThinkingConfig()
			if tc.mode == ThinkingDefault {
				if _, ok := body["enable_thinking"]; ok {
					t.Error("default must omit provider toggle")
				}
				if _, ok := body["thinking"]; ok {
					t.Error("default must omit provider toggle")
				}
			} else if protocol == "bailian" {
				if body["enable_thinking"] != (tc.mode == ThinkingOn) || body["enable_search"] != false {
					t.Errorf("wrong settings: %v", body)
				}
			} else if protocol == "deepseek" {
				want := "disabled"
				if tc.mode == ThinkingOn {
					want = "enabled"
				}
				if body["thinking"].(map[string]string)["type"] != want {
					t.Error("wrong DeepSeek toggle")
				}
				if _, ok := body["enable_thinking"]; ok {
					t.Error("Bailian toggle sent to DeepSeek")
				}
			}
			if tc.mode == ThinkingOn && (body["stream"] != true || body["max_tokens"] != 16384 || timeout != 180*time.Second) {
				t.Error("thinking needs streaming, a reasoning budget and longer timeout")
			}
		})
	}
}

func TestThinkingStreamChoosesOnlyFinalContent(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"private-reasoning-marker\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"move\\\":\\\"0605\\\",\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\\\"plan\\\":\\\"争中\\\"}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	a := Agent{Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", Model: "deepseek-v4.1-flash", APIKey: "test-key", Thinking: ThinkingOn}
	a.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	d, err := a.Choose(context.Background(), xiangqi.New())
	if err != nil {
		t.Fatal(err)
	}
	if d.Move.String() != "0605" || len(a.history) != 1 {
		t.Fatal("missing streamed move or conversation context")
	}
	if strings.Contains(a.history[0].reply, "private-reasoning-marker") {
		t.Error("raw reasoning must not enter history")
	}
}

func TestStreamRejectsInterruptedOrLengthLimitedReply(t *testing.T) {
	for _, data := range []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"move\\\":\\\"0605\\\"}\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"move\\\":\\\"0605\\\"}\"},\"finish_reason\":\"length\"}]}\n\n",
	} {
		res := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(data))}
		if _, err := readCompletion(res); err == nil {
			t.Fatal("incomplete or truncated replies must not be executed")
		}
	}
}

func TestThinkingDeadline(t *testing.T) {
	a := Agent{Endpoint: "https://example.com/v1/chat/completions", Model: "custom", APIKey: "test-key", Thinking: ThinkingOn, ThinkingAPI: "deepseek", Timeout: 10 * time.Millisecond}
	a.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	_, err := a.Choose(context.Background(), xiangqi.New())
	if !errors.Is(err, context.DeadlineExceeded) || len(a.history) != 0 {
		t.Fatalf("deadline/history: %v", err)
	}
}

func TestReasoningEffortDefaultsAndOverrides(t *testing.T) {
	a := Agent{Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", Model: "deepseek-v4.1-flash", Thinking: ThinkingOn}
	body := map[string]any{}
	if _, err := a.requestSettings(body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" {
		t.Fatal("flash thinking should default to low effort")
	}
	a.ReasoningEffort = "high"
	a.MaxTokens = 32768
	body = map[string]any{}
	if _, err := a.requestSettings(body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" || body["max_tokens"] != 32768 {
		t.Fatal("explicit budgets must be honored")
	}
	a.Model = "deepseek-v4-pro"
	a.ReasoningEffort = "low"
	if _, _, err := a.ThinkingConfig(); err == nil {
		t.Fatal("unsupported low effort must not be advertised for Bailian Pro")
	}
	a.ReasoningEffort = ""
	body = map[string]any{}
	if _, err := a.requestSettings(body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatal("Bailian Pro minimum effort is high")
	}
	a.Thinking = ThinkingOff
	body = map[string]any{}
	if _, err := a.requestSettings(body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("effort must not turn thinking back on")
	}
}
