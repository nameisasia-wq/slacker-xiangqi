package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moyu-xiangqi/internal/xiangqi"
)

func TestAgentRetriesIllegalMove(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing key")
		}
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !strings.Contains(request.Messages[1].Content, "0605") {
			t.Error("legal moves missing")
		}
		if strings.Contains(request.Messages[1].Content, "invalid-plan-marker") {
			t.Error("a plan attached to an invalid move must not be retained")
		}
		if calls == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"move\":\"0603\",\"plan\":\"invalid-plan-marker\",\"why\":\"\"}"}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"move\":\"0605\",\"why\":\"向前一步\"}"}}]}`))
	}))
	defer s.Close()
	a := Agent{Endpoint: s.URL, Model: "test-model", APIKey: "test-key", Explain: true}
	d, err := a.Choose(context.Background(), xiangqi.New())
	if err != nil {
		t.Fatal(err)
	}
	if d.Move.String() != "0605" || d.Invalid != 1 || calls != 2 {
		t.Fatalf("decision: %+v calls=%d", d, calls)
	}
	if len(a.history) != 1 || strings.Contains(a.history[0].reply, "invalid-plan-marker") {
		t.Fatal("only the accepted decision should enter conversation history")
	}
}

func TestAgentExplainsModelNotFound(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"Model not exist.","code":"model_not_found"}}`))
	}))
	defer s.Close()
	a := Agent{Endpoint: s.URL, Model: "deepseek-v4-flash", APIKey: "private-test-key"}
	_, err := a.Choose(context.Background(), xiangqi.New())
	if err == nil || !strings.Contains(err.Error(), "deepseek-v4-flash") || !strings.Contains(err.Error(), "模型 ID") {
		t.Fatalf("expected actionable model error, got %v", err)
	}
	if strings.Contains(err.Error(), a.APIKey) {
		t.Fatal("API key leaked into error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultFastRequest(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		fast           bool
	}{
		{"bailian", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", true},
		{"generic", "https://example.com/v1/chat/completions", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				value, exists := request["enable_thinking"]
				if exists != tc.fast || (tc.fast && string(value) != "false") {
					t.Errorf("enable_thinking = %s, exists = %v", value, exists)
				}
				search, exists := request["enable_search"]
				if exists != tc.fast || (tc.fast && string(search) != "false") {
					t.Errorf("enable_search = %s, exists = %v", search, exists)
				}
				if tc.fast && string(request["max_tokens"]) != "128" {
					t.Error("fast response must have a small output limit")
				}
				if _, exists := request["tools"]; exists {
					t.Error("chess requests must not enable tools")
				}
				var messages []struct {
					Content string `json:"content"`
				}
				if err := json.Unmarshal(request["messages"], &messages); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(messages[0].Content, "why") {
					t.Error("default prompt must not request the optional explanation")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"move\":\"0605\"}"}}]}`)), Header: make(http.Header)}, nil
			})}
			a := Agent{Endpoint: tc.endpoint, Model: "deepseek-v4.1-flash", APIKey: "test-key", Client: client}
			if _, err := a.Choose(context.Background(), xiangqi.New()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentCarriesPlanAcrossTurnsAndResets(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt := request.Messages[len(request.Messages)-1].Content
		move, plan := "0605", "控制中路并出动车马"
		switch calls {
		case 1:
			if len(request.Messages) != 2 {
				t.Error("new game must have only system and latest position")
			}
			if strings.Contains(prompt, "上一回合的阶段目标") {
				t.Error("new game must not have a previous plan")
			}
		case 2:
			if len(request.Messages) != 4 || !strings.Contains(request.Messages[2].Content, "控制中路并出动车马") {
				t.Error("previous accepted reply must be included as conversation context")
			}
			if !strings.Contains(prompt, "控制中路并出动车马") || !strings.Contains(prompt, "0304") {
				t.Error("next turn must carry the previous goal and opponent's reply")
			}
			move, plan = "2625", "转攻侧翼并保持中路牵制"
		case 3:
			if len(request.Messages) != 2 {
				t.Error("reset must discard conversation history")
			}
			if strings.Contains(prompt, "上一回合的阶段目标") {
				t.Error("reset must discard the previous game's plan")
			}
		default:
			t.Fatalf("unexpected request %d", calls)
		}
		content, _ := json.Marshal(map[string]string{"move": move, "plan": plan})
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	a := Agent{Endpoint: "https://example.com/v1/chat/completions", Model: "test-model", APIKey: "test-key", Client: client}
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
	if a.plan != "转攻侧翼并保持中路牵制" {
		t.Fatal("plan was not updated")
	}
	a.ResetGame()
	if _, err := a.Choose(context.Background(), xiangqi.New()); err != nil {
		t.Fatal(err)
	}
}

func TestConversationWindowAndSideIsolation(t *testing.T) {
	sequence := []string{"0605", "0304", "2625", "2324", "4645", "4344", "6665", "6364", "8685", "8384", "1927", "1022", "7967", "7062"}
	g := xiangqi.New()
	agents := map[xiangqi.Side]*Agent{}
	for _, side := range []xiangqi.Side{xiangqi.Red, xiangqi.Black} {
		side := side
		calls := 0
		ownPlan, otherPlan := "red-private-plan", "black-private-plan"
		if side == xiangqi.Black {
			ownPlan, otherPlan = otherPlan, ownPlan
		}
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Messages []message `json:"messages"`
				Thinking *bool     `json:"enable_thinking"`
				Search   *bool     `json:"enable_search"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) == 0 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, chessPrimer) {
				t.Fatal("chess briefing must stay available even when the conversation window rolls over")
			}
			if request.Thinking == nil || *request.Thinking || request.Search == nil || *request.Search {
				t.Error("thinking and web search must stay disabled with conversation history")
			}
			retained := calls
			if retained > historyRounds {
				retained = historyRounds
			}
			if len(request.Messages) != 2+2*retained {
				t.Fatalf("side %s turn %d: got %d messages, want %d", side, calls, len(request.Messages), 2+2*retained)
			}
			for i := 0; i < retained; i++ {
				user, assistant := request.Messages[1+2*i], request.Messages[2+2*i]
				if user.Role != "user" || assistant.Role != "assistant" {
					t.Fatal("historical messages must alternate user and assistant")
				}
				if strings.Contains(user.Content, "棋盘：") || strings.Contains(user.Content, "当前可选着法") {
					t.Error("old boards and move lists must not accumulate")
				}
				firstPly := 0
				if side == xiangqi.Black {
					firstPly = 1
				}
				wantMove := sequence[firstPly+2*(calls-retained+i)]
				var prior struct {
					Move string `json:"move"`
				}
				if err := json.Unmarshal([]byte(assistant.Content), &prior); err != nil {
					t.Fatal(err)
				}
				if prior.Move != wantMove {
					t.Errorf("history move %s, want %s", prior.Move, wantMove)
				}
			}
			latest := request.Messages[len(request.Messages)-1]
			if latest.Role != "user" || !strings.Contains(latest.Content, "棋盘：") {
				t.Error("latest message must include the authoritative board")
			}
			if len(g.History) > 0 && !strings.Contains(latest.Content, strings.Join(g.History, ",")) {
				t.Error("latest message must include the entire actual move record")
			}
			for _, msg := range request.Messages {
				if strings.Contains(msg.Content, otherPlan) {
					t.Error("opponent's private goal leaked")
				}
			}
			content, _ := json.Marshal(map[string]string{"move": sequence[len(g.History)], "plan": ownPlan})
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
			calls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
		})}
		agents[side] = &Agent{Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", Model: "deepseek-v4.1-flash", APIKey: "test-key", Client: client}
	}
	for range sequence {
		d, err := agents[g.Turn].Choose(context.Background(), g)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Apply(d.Move); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupportsFastMode(t *testing.T) {
	tests := []struct {
		endpoint string
		model    string
		want     bool
	}{
		{"https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-v4-flash-0731", true},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-v4.1-flash", true},
		{"https://abc.cn-beijing.maas.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-v4-pro", true},
		{"https://api.deepseek.com/chat/completions", "deepseek-v4-flash-0731", false},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "deepseek-r1", false},
		{"https://fake.aliyuncs.com.example/compatible-mode/v1/chat/completions", "deepseek-v4-pro", false},
	}
	for _, tc := range tests {
		if got := SupportsFastMode(tc.endpoint, tc.model); got != tc.want {
			t.Errorf("SupportsFastMode(%q, %q) = %v; want %v", tc.endpoint, tc.model, got, tc.want)
		}
	}
}

// The model must see the engine's current position even after captures and a
// black-side display rotation; screen coordinates must not leak into the API.
func TestPositionSentToBlackAfterCaptureAndRotation(t *testing.T) {
	g := xiangqi.New()
	capture, err := xiangqi.ParseMove("1710")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Apply(capture); err != nil {
		t.Fatal(err)
	}
	if g.Turn != xiangqi.Black || g.At(xiangqi.Pos{X: 1, Y: 0}).Label() != "P1" || g.At(xiangqi.Pos{X: 1, Y: 7}).Side != xiangqi.None {
		t.Fatal("capture fixture invalid")
	}
	expected := g.Rows()
	for _, side := range []xiangqi.Side{xiangqi.Red, xiangqi.Black} {
		_ = g.RenderView(false, xiangqi.View{Language: xiangqi.Chinese, Bottom: side}, true)
	}
	a := Agent{Endpoint: "https://example.com/v1/chat/completions", Model: "test-model", APIKey: "test-key"}
	a.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt := request.Messages[len(request.Messages)-1].Content
		if !strings.Contains(prompt, "你执黑方") || !strings.Contains(prompt, "1710") {
			t.Error("wrong side or missing actual capture")
		}
		boardStart := strings.Index(prompt, "y=0: ")
		if boardStart < 0 {
			t.Fatal("missing current board")
		}
		rows := strings.Split(prompt[boardStart:], "\n")
		if len(rows) < 10 {
			t.Fatal("board has fewer than ten rows")
		}
		for y, want := range expected {
			prefix := "y=" + string(rune('0'+y)) + ": "
			if rows[y] != prefix+want {
				t.Errorf("row %d: got %q, want %q", y, rows[y], prefix+want)
			}
		}
		if strings.Contains(strings.Join(rows[:10], " "), "h1") {
			t.Error("captured horse still present in model board")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"move\":\"0304\"}"},"finish_reason":"stop"}]}`))}, nil
	})}
	if _, err := a.Choose(context.Background(), g.Clone()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.Rows(), "\n") != strings.Join(expected, "\n") {
		t.Error("rendering or prompting mutated live position")
	}
}
