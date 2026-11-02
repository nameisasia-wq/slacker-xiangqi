package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"moyu-xiangqi/internal/ai"
	"moyu-xiangqi/internal/xiangqi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryTokenLimitKeepsCurrentGame(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Thinking bool              `json:"enable_thinking"`
			Messages []json.RawMessage `json:"messages"`
			Tokens   int               `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Messages) != 2 {
			t.Error("failed reply must not pollute conversation context")
		}
		if calls == 1 {
			if !body.Thinking {
				t.Error("first attempt must use thinking")
			}
			w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`))
			return
		}
		if body.Thinking || body.Tokens != 128 {
			t.Error("retry must honor the user's choice to turn thinking off")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"move\":\"0605\",\"plan\":\"争中\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	agent := &ai.Agent{Endpoint: server.URL, Model: "test-model", APIKey: "test-key", ThinkingAPI: "bailian", Thinking: ai.ThinkingOn}
	var output bytes.Buffer
	ui := &screen{out: &output, rows: 24, cols: 100}
	_, err := playOne(map[xiangqi.Side]*ai.Agent{xiangqi.Red: agent, xiangqi.Black: &ai.Agent{}}, false, 1, ui, bufio.NewReader(strings.NewReader("2\n")), "test")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(output.String(), "0605") {
		t.Fatal("game did not continue after the retry")
	}
}

func TestTokenRetryPreservesBoardAndCredentials(t *testing.T) {
	for _, choice := range []string{"1\n", "q\n"} {
		a := &ai.Agent{Endpoint: "https://example.com/v1/chat/completions", Model: "test", APIKey: "private-key", ThinkingAPI: "bailian", Thinking: ai.ThinkingOn}
		g := xiangqi.New()
		before := strings.Join(g.Rows(), "\n")
		var output bytes.Buffer
		err := retryTokenLimit(a, g, &screen{out: &output, rows: 24, cols: 100}, bufio.NewReader(strings.NewReader(choice)), time.Second)
		if choice == "q\n" && !errors.Is(err, errQuit) {
			t.Fatal("quit choice lost")
		}
		if choice == "1\n" && (err != nil || a.MaxTokens != 32768) {
			t.Fatal("budget increase failed")
		}
		if strings.Join(g.Rows(), "\n") != before || len(g.History) != 0 {
			t.Fatal("retry changed board")
		}
		if a.APIKey != "private-key" || strings.Contains(output.String(), a.APIKey) {
			t.Fatal("credentials changed or leaked")
		}
	}
}
