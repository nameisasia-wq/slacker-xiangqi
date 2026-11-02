package ai

import (
	"errors"
	"net/url"
	"strings"
)

// NormalizeEndpoint preserves complete API routes and defaults a base URL to Chat.
func NormalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("地址必须以 http:// 或 https:// 开头")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("接口地址不能包含查询参数、片段或账号信息")
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/chat/completions") {
		for _, unsupported := range []string{"/messages", "/embeddings", "/models", "/completions"} {
			if strings.HasSuffix(path, unsupported) {
				return "", errors.New("该完整接口尚未适配；请填 Base URL、/chat/completions 或 /responses 地址")
			}
		}
		path += "/chat/completions"
	}
	u.Path = path
	return u.String(), nil
}

func (a *Agent) usesResponses() bool {
	u, err := url.Parse(a.Endpoint)
	return err == nil && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/responses")
}

func (a *Agent) ProtocolLabel() string {
	if a.usesResponses() {
		return "Responses"
	}
	return "Chat Completions"
}

func (a *Agent) requestBody(settings map[string]any, messages []message) map[string]any {
	body := make(map[string]any, len(settings)+2)
	for k, v := range settings {
		body[k] = v
	}
	if !a.usesResponses() {
		body["messages"] = messages
		return body
	}
	body["input"] = messages
	body["store"] = false
	if limit, ok := body["max_tokens"]; ok {
		body["max_output_tokens"] = limit
		delete(body, "max_tokens")
	}
	mode, protocol, _ := a.ThinkingConfig()
	effort, _ := body["reasoning_effort"].(string)
	delete(body, "reasoning_effort")
	delete(body, "enable_search")
	delete(body, "enable_thinking")
	if protocol == "bailian" || protocol == "deepseek" {
		delete(body, "thinking")
		if mode == ThinkingOff {
			effort = "none"
		} else if mode == ThinkingOn && effort == "" {
			effort = "high"
		}
	}
	if effort != "" {
		body["reasoning"] = map[string]string{"effort": effort}
	}
	return body
}
