package ai

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ThinkingMode is independent of a provider's wire format.
type ThinkingMode string

const (
	ThinkingOff     ThinkingMode = "off"
	ThinkingOn      ThinkingMode = "on"
	ThinkingDefault ThinkingMode = "default"
)

// ThinkingConfig returns the effective mode and protocol without making a request.
// An empty mode preserves old behavior: known adapters turn thinking off,
// while unrecognized endpoints retain their provider's defaults.
func (a *Agent) ThinkingConfig() (ThinkingMode, string, error) {
	protocol := a.ThinkingAPI
	if protocol == "" || protocol == "auto" {
		protocol = ""
		if SupportsFastMode(a.Endpoint, a.Model) {
			protocol = "bailian"
		}
		u, _ := url.Parse(a.Endpoint)
		if u != nil && u.Scheme == "https" && u.Hostname() == "api.deepseek.com" && (a.Model == "deepseek-flash" || a.Model == "deepseek-v4-pro") {
			protocol = "deepseek"
		}
		if u != nil && u.Scheme == "https" && strings.HasPrefix(u.Hostname(), "ark.") && strings.HasSuffix(u.Hostname(), ".volces.com") {
			protocol = "ark"
		}

	}
	if protocol != "" && protocol != "bailian" && protocol != "deepseek" && protocol != "ark" {
		return "", "", fmt.Errorf("未知思考协议 %q；可选 auto、bailian、deepseek、ark", protocol)
	}
	mode := a.Thinking
	if mode == "" {
		mode = ThinkingDefault
		if protocol != "" {
			mode = ThinkingOff
		}
	}
	if mode != ThinkingOff && mode != ThinkingOn && mode != ThinkingDefault {
		return "", "", fmt.Errorf("未知思考模式 %q；可选 off、on、default", mode)
	}
	if protocol == "" && mode != ThinkingDefault {
		return "", "", fmt.Errorf("无法识别该接口/模型的思考开关；请选择百炼、DeepSeek 或豆包方舟思考协议，或选择服务商默认；接口格式兼容不代表支持相同思考参数")
	}
	if a.Timeout < 0 || a.MaxTokens < 0 {
		return "", "", fmt.Errorf("AI 等待时间和 Token 上限不能为负数")
	}
	if mode == ThinkingOn {
		if _, err := a.reasoningEffort(protocol); err != nil {
			return "", "", err
		}
	}
	return mode, protocol, nil
}

func (a *Agent) requestSettings(body map[string]any) (time.Duration, error) {
	mode, protocol, err := a.ThinkingConfig()
	if err != nil {
		return 0, err
	}
	if mode == ThinkingOn {
		effort, err := a.reasoningEffort(protocol)
		if err != nil {
			return 0, err
		}
		if effort != "" {
			body["reasoning_effort"] = effort
		}
	}
	if protocol == "bailian" {
		body["enable_search"] = false
		if mode != ThinkingDefault {
			body["enable_thinking"] = mode == ThinkingOn
		}
	}
	if (protocol == "deepseek" || protocol == "ark") && mode != ThinkingDefault {
		value := "disabled"
		if mode == ThinkingOn {
			value = "enabled"
		}
		body["thinking"] = map[string]string{"type": value}
	}
	timeout := 90 * time.Second
	if mode != ThinkingOff {
		timeout = 180 * time.Second
	}
	if a.Timeout > 0 {
		timeout = a.Timeout
	}
	if mode != ThinkingOff {
		body["stream"] = true
	}
	if protocol != "" {
		limit := 16384
		if mode == ThinkingOff {
			limit = 128
			if a.Explain {
				limit = 256
			}
		}
		body["max_tokens"] = limit
	}
	if a.MaxTokens > 0 {
		body["max_tokens"] = a.MaxTokens
	}
	return timeout, nil
}

func (a *Agent) ThinkingLabel() string {
	mode, protocol, err := a.ThinkingConfig()
	if err != nil {
		return err.Error()
	}
	label := map[ThinkingMode]string{ThinkingOff: "关闭", ThinkingOn: "开启（按协议请求）", ThinkingDefault: "服务商默认"}[mode]
	if mode == ThinkingOn {
		if effort, _ := a.reasoningEffort(protocol); effort != "" {
			label += "，推理力度 " + effort
		}
	}
	if protocol != "" {
		label += " / " + protocol
	}
	return label
}

// Suppress accidental whitespace in the CLI mode without silently accepting typos.
func ParseThinking(value string) ThinkingMode { return ThinkingMode(strings.TrimSpace(value)) }

// Only attach effort parameters for models whose support is documented.
func (a *Agent) reasoningEffort(protocol string) (string, error) {
	known := (protocol == "ark" && strings.HasPrefix(a.Model, "doubao-seed-2-")) || (protocol == "bailian" && (a.Model == "deepseek-v4.1-flash" || a.Model == "deepseek-v4-flash-0731" || a.Model == "deepseek-v4-pro")) || (protocol == "deepseek" && (a.Model == "deepseek-flash" || a.Model == "deepseek-v4-pro"))
	effort := a.ReasoningEffort
	if !known {
		if effort != "" {
			return "", fmt.Errorf("该模型尚未适配推理力度参数，请省略 --thinking-effort")
		}
		return "", nil
	}
	if effort == "" {
		effort = "low"
		if protocol == "bailian" && a.Model == "deepseek-v4-pro" {
			effort = "high"
		}
	}
	if effort != "low" && effort != "high" && effort != "max" {
		return "", fmt.Errorf("推理力度只能使用 low、high、max")
	}
	if protocol == "bailian" && a.Model == "deepseek-v4-pro" && effort == "low" {
		return "", fmt.Errorf("百炼 deepseek-v4-pro 不提供 low 力度，可使用 high 或 max")
	}
	return effort, nil
}

// TokenLimit is zero when the request leaves the provider's limit unchanged.
func (a *Agent) TokenLimit() int {
	body := map[string]any{}
	if _, err := a.requestSettings(body); err != nil {
		return 0
	}
	limit, _ := body["max_tokens"].(int)
	return limit
}
