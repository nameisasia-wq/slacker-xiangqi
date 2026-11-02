package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"moyu-xiangqi/internal/xiangqi"
)

// Agent speaks Chat Completions and Responses HTTP protocols. Other providers can
// be added behind the same Choose method without changing the chess engine.
type Agent struct {
	Endpoint        string
	Model           string
	APIKey          string
	Strict          bool // omit legal moves, so invalid moves are observable
	Explain         bool
	Thinking        ThinkingMode
	ThinkingAPI     string // auto, bailian, deepseek, or ark; explicit selection supports gateways
	Timeout         time.Duration
	ReasoningEffort string // empty selects light reasoning for supported models
	MaxTokens       int
	Client          *http.Client
	plan            string // concise strategic goal carried between turns in one game
	history         []exchange
}

const historyRounds = 4

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type exchange struct {
	position string // compact turn context; old boards and candidate lists are omitted
	reply    string
}

// ResetGame discards all game context, including after colors swap.
func (a *Agent) ResetGame() {
	a.plan = ""
	a.history = nil
}

func (a *Agent) messages(system, prompt string) []message {
	messages := []message{{Role: "system", Content: system}}
	for _, turn := range a.history {
		messages = append(messages, message{Role: "user", Content: turn.position}, message{Role: "assistant", Content: turn.reply})
	}
	return append(messages, message{Role: "user", Content: prompt})
}

func (a *Agent) remember(g *xiangqi.Game, move xiangqi.Move, why string) {
	position := fmt.Sprintf("你执%s，当前已完成 %d 个半回合。", g.Turn, len(g.History))
	if len(g.History) > 0 {
		position += fmt.Sprintf("对手%s刚走了 %s，现在轮到你。", g.Turn.Other(), g.History[len(g.History)-1])
	} else {
		position += "现在由红方开局。"
	}
	// Preserve accepted decisions as assistant messages, without carrying raw
	// provider text, invalid attempts, or unbounded explanations into later turns.
	reply := map[string]string{"move": move.String()}
	if a.plan != "" {
		reply["plan"] = a.plan
	}
	if a.Explain && why != "" {
		runes := []rune(why)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		reply["why"] = string(runes)
	}
	data, _ := json.Marshal(reply)
	a.history = append(a.history, exchange{position: position, reply: string(data)})
	if len(a.history) > historyRounds {
		a.history = append([]exchange(nil), a.history[len(a.history)-historyRounds:]...)
	}
}

// SupportsFastMode limits Alibaba Cloud's non-standard enable_thinking field
// to endpoints and DeepSeek models documented to support disabling thinking.
func SupportsFastMode(endpoint, model string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".aliyuncs.com") || !strings.Contains(u.Path, "/compatible-mode/") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-flash-0731":
		return true
	default:
		return false
	}
}

type Decision struct {
	Move        xiangqi.Move
	Explanation string
	Invalid     int
}

var ErrInvalidMoves = errors.New("AI 连续给出非法走法")

func (a *Agent) Choose(ctx context.Context, g *xiangqi.Game) (Decision, error) {
	if a.Model == "" || a.APIKey == "" {
		return Decision{}, errors.New("缺少 AI 模型或 API Key")
	}
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/chat/completions"
	}
	var err error
	endpoint, err = NormalizeEndpoint(endpoint)
	if err != nil {
		return Decision{}, err
	}
	settings := map[string]any{"model": a.Model}
	timeout, err := a.requestSettings(settings)
	if err != nil {
		return Decision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	var legal []string
	for _, move := range g.LegalMoves(g.Turn) {
		legal = append(legal, move.String())
	}
	rows := g.Rows()
	for y := range rows {
		rows[y] = fmt.Sprintf("y=%d: %s", y, rows[y])
	}
	prompt := fmt.Sprintf("你执%s，现在轮到你走。坐标固定，不随执棋方翻转：横坐标 x 从左到右为 0-8，纵坐标 y 从上到下为 0-9。红方在下，兵向 y 减小方向前进；黑方在上，卒向 y 增大方向前进。每个棋子带固定编号，例如 R1、r2；大写为红方，小写为黑方，.. 是空位。G/g 将，A/a 士，E/e 象，H/h 马，R/r 车，P/p 炮，B/b 兵。棋盘：\n     0  1  2  3  4  5  6  7  8  (x)\n%s\n四位走法是起点横纵坐标加终点横纵坐标，例如 0605。", g.Turn, strings.Join(rows, "\n"))
	if len(g.History) > 0 {
		prompt += "\n整局实际棋谱（红方先行，双方交替，按时间顺序）：" + strings.Join(g.History, ",")
	} else {
		prompt += "\n整局实际棋谱：尚未走棋。"
	}
	if !a.Strict {
		prompt += "\n当前可选着法：" + strings.Join(legal, ",")
	}
	if a.plan != "" {
		prompt += "\n你上一回合的阶段目标：" + a.plan + "\n结合对手刚才的回应，继续推进该目标；若局势已改变，则调整目标。以当前棋盘为准。"
	}
	if a.Explain {
		prompt += "\n请在 why 中用一句话解释这步棋。"
	}
	strategy := "你是一位经验丰富的中国象棋大师，擅长布局、中局攻杀和残局。你执当前轮到的一方，目标是战胜对手。每一步都应服务于赢棋：判断双方的将帅安全、子力得失、棋子活跃度和攻防形势，预判对手的有力回应，选择最有利于取胜的着法。发现将杀机会时把握战机，面临威胁时准确防守；吃子时考虑回吃，弃子时应有明确补偿或后续攻势，避免无意义往返。"
	strategy += "围绕明确的阶段目标组织连续着法，让本步为后续棋子配合和攻防布局创造条件。plan 用不超过 20 个字概括当前目标（如出动双车、控制中路），供下回合参考；只写目标摘要。"
	strategy += "历史消息记录你之前的着法和目标，可用于延续布局；实际棋子位置和当前可选着法以最新消息中的棋盘为准。"
	strategy += chessPrimer
	system := strategy + "只输出一个 JSON 对象，格式为 {\"move\":\"0605\",\"plan\":\"阶段目标\"}。不要输出 Markdown、说明或内部思考过程。"
	if a.Explain {
		system = strategy + "只输出一个 JSON 对象，格式为 {\"move\":\"0605\",\"plan\":\"阶段目标\",\"why\":\"一句简短理由\"}。why 不超过 30 个字。不要输出 Markdown 或内部思考过程。"
	}
	invalid := 0
	for attempt := 0; attempt < 3; attempt++ {
		body, _ := json.Marshal(a.requestBody(settings, a.messages(system, prompt)))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return Decision{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
		res, err := client.Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return Decision{}, fmt.Errorf("AI 接口等待超时（本次上限 %s）；可调整 --ai-timeout 或关闭深度思考：%w", timeout, err)
			}
			return Decision{}, err
		}

		if res.StatusCode != http.StatusOK {
			data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if readErr != nil {
				return Decision{}, readErr
			}
			var providerError struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if res.StatusCode == http.StatusNotFound && json.Unmarshal(data, &providerError) == nil && providerError.Error.Code == "model_not_found" {
				return Decision{}, fmt.Errorf("模型 ID %q 在当前接口不可用；请核对服务商、地域和控制台中的模型 ID（接口返回 404）", a.Model)
			}
			return Decision{}, fmt.Errorf("AI 接口返回 %d：%s", res.StatusCode, strings.TrimSpace(string(data)))
		}
		var content string
		if a.usesResponses() {
			content, err = readResponse(res)
		} else {
			content, err = readCompletion(res)
		}
		res.Body.Close()
		if err != nil {
			if errors.Is(err, ErrTokenLimit) {
				return Decision{}, fmt.Errorf("%w（当前上限 %v Token）", err, settings["max_tokens"])
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return Decision{}, fmt.Errorf("AI 接口等待超时（本次上限 %s）；可调整 --ai-timeout：%w", timeout, err)
			}
			return Decision{}, err
		}
		start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
		if start < 0 || end < start {
			invalid++
			prompt += "\n上次回复不是 JSON，请重试。"
			continue
		}
		var choice struct {
			Move string `json:"move"`
			Why  string `json:"why"`
			Plan string `json:"plan"`
		}
		if err := json.Unmarshal([]byte(content[start:end+1]), &choice); err != nil {
			invalid++
			prompt += "\n上次 JSON 无法解析，请重试。"
			continue
		}
		move, err := xiangqi.ParseMove(choice.Move)
		if err == nil {
			for _, allowed := range legal {
				if move.String() == allowed {
					if plan := strings.Join(strings.Fields(choice.Plan), " "); plan != "" {
						runes := []rune(plan)
						if len(runes) > 40 {
							runes = runes[:40]
						}
						a.plan = string(runes)
					}
					a.remember(g, move, choice.Why)
					return Decision{Move: move, Explanation: choice.Why, Invalid: invalid}, nil
				}
			}
		}
		invalid++
		prompt += "\n上次走法 " + choice.Move + " 不合法，请重新选择。"
	}
	return Decision{Invalid: invalid}, fmt.Errorf("%w（%d 次）", ErrInvalidMoves, invalid)
}
