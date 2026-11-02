package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAPIEndpointAcceptsBaseAndFullRoutes(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.deepseek.com/responses":                  "https://api.deepseek.com/responses",
		"https://ark.cn-beijing.volces.com/api/v3/responses/": "https://ark.cn-beijing.volces.com/api/v3/responses",
		"https://ark.cn-beijing.volces.com/api/v3":            "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		"https://example.com/custom/responses":                "https://example.com/custom/responses",
		"https://api.deepseek.com":                            "https://api.deepseek.com/chat/completions",
		"https://example.com/v1/":                             "https://example.com/v1/chat/completions",
		"https://example.com/v1/chat/completions":             "https://example.com/v1/chat/completions",
	} {
		got, err := apiEndpoint(input)
		if err != nil || got != want {
			t.Errorf("apiEndpoint(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, invalid := range []string{"https://example.com/messages", "https://example.com/responses?api_key=secret", "https://user:pass@example.com/responses", "ftp://example.com/responses"} {
		if _, err := apiEndpoint(invalid); err == nil {
			t.Errorf("invalid endpoint accepted: %s", invalid)
		}
	}
}

func TestAgentSetupKeepsKeyOutOfOutput(t *testing.T) {
	var output bytes.Buffer
	p := setupPrompt{
		in:  strings.NewReader("https://api.deepseek.com\ndeepseek-flash\n\n"),
		out: &output,
		secret: func(string) (string, error) {
			return "private-test-key", nil
		},
	}
	agent, err := p.agent("黑方")
	if err != nil {
		t.Fatal(err)
	}
	if agent.APIKey != "private-test-key" || agent.Model != "deepseek-flash" || agent.Endpoint != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("wrong agent configuration: %+v", agent)
	}
	if strings.Contains(output.String(), agent.APIKey) {
		t.Fatal("API key appeared in setup output")
	}
}

func TestAgentSetupEnablesThinking(t *testing.T) {
	var output bytes.Buffer
	p := setupPrompt{
		in:     strings.NewReader("https://dashscope.aliyuncs.com/compatible-mode/v1\ndeepseek-v4.1-flash\n2\n"),
		out:    &output,
		secret: func(string) (string, error) { return "test-key", nil },
	}
	agent, err := p.agent("黑方")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Model != "deepseek-v4.1-flash" {
		t.Fatal("wrong model")
	}
	if string(agent.Thinking) != "on" || !strings.Contains(output.String(), "棋理简报已启用") {
		t.Fatal("thinking choice and context status missing")
	}
}

func TestGatewayThinkingProtocolChoice(t *testing.T) {
	var output bytes.Buffer
	p := setupPrompt{in: strings.NewReader("https://example.com/v1\ncustom-model\n2\n2\n"), out: &output, secret: func(string) (string, error) { return "private-test-key", nil }}
	agent, err := p.agent("红方")
	if err != nil {
		t.Fatal(err)
	}
	if string(agent.Thinking) != "on" || agent.ThinkingAPI != "deepseek" {
		t.Fatal("gateway protocol choice lost")
	}
	if strings.Contains(output.String(), agent.APIKey) {
		t.Fatal("key leaked")
	}
}

func TestArkResponsesSetup(t *testing.T) {
	var output bytes.Buffer
	p := setupPrompt{in: strings.NewReader("https://ark.cn-beijing.volces.com/api/v3/responses\ndoubao-seed-2-1-pro-260915\n2\n"), out: &output, secret: func(string) (string, error) { return "test-key", nil }}
	a, err := p.agent("黑方")
	if err != nil {
		t.Fatal(err)
	}
	_, protocol, err := a.ThinkingConfig()
	if err != nil || protocol != "ark" || a.ProtocolLabel() != "Responses" || !strings.Contains(output.String(), "接口 Responses") {
		t.Fatal("Ark Responses was not recognized")
	}
}
