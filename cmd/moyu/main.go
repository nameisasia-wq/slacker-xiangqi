package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"moyu-xiangqi/internal/ai"
	"moyu-xiangqi/internal/netplay"
	"moyu-xiangqi/internal/tunnel"
	"moyu-xiangqi/internal/xiangqi"
)

var errQuit = errors.New("退出")
var errPeerLeft = errors.New("对方已退出")

func main() {
	if len(os.Args) < 2 {
		if err := wizard(); err != nil && !errors.Is(err, errQuit) {
			fmt.Fprintln(os.Stderr, "错误：", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "play":
		err = play(os.Args[2:])
	case "host":
		err = host(os.Args[2:])
	case "join":
		err = join(os.Args[2:])
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, errQuit) {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`摸鱼象棋 — 终端中国象棋

  moyu                            交互菜单：选择玩法、房间和 AI 配置

  moyu play                       本机双人轮流对弈
  moyu play --black-model MODEL   人对 AI
  moyu play --red-model A --black-model B   AI 对 AI
  moyu host                       本机创建联机棋局，房主执红
  moyu host --tunnel              自动创建临时公网地址，无需另开终端
  moyu join --url URL             加入房主棋局，客方执黑

启动时可选择英文或中文棋子，也可用 --lang en|zh 指定，默认英文。
走法：鼠标点击己方棋子，再点击落点；或输入 0605 从 06 走到 05。
输入 m 回车可暂停鼠标以拖选复制文字，再输入 m 回车恢复。
还可输入棋子编号 R2 / 车2，再输入提示中的两位落点。
输入 moves 查看合法走法总数，moves 06 查看指定位置，q 退出。
菜单中的 AI Key 隐藏输入，不需要写进命令。使用 play 命令时，
AI Key 从 MOYU_RED_API_KEY、MOYU_BLACK_API_KEY 读取；也可共用 OPENAI_API_KEY。
基础地址默认 Chat Completions；完整 /responses 自动选择 Responses 协议。
可用 --red-url / --black-url 指定地址；菜单支持相同规则。
--thinking on|off|default 统一选择深度思考；菜单中也可选择。
--red-thinking / --black-thinking 可分别覆盖；未识别代理需指定思考协议。
--ai-timeout 300s 设置每步总等待上限，--ai-max-tokens 设置输出/推理上限。
--thinking-effort low|high|max 选择已适配模型的思考力度。
`)
}

func colorEnabled() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func keyFor(side xiangqi.Side) string {
	name := "MOYU_RED_API_KEY"
	if side == xiangqi.Black {
		name = "MOYU_BLACK_API_KEY"
	}
	if key := os.Getenv(name); key != "" {
		return key
	}
	return os.Getenv("OPENAI_API_KEY")
}

func readDirectLine() (string, error) {
	return readLineFrom(os.Stdin)
}

func chooseLanguage(value string) (xiangqi.Language, error) {
	if value == "" {
		info, err := os.Stdin.Stat()
		outInfo, outErr := os.Stdout.Stat()
		if err == nil && outErr == nil && info.Mode()&os.ModeCharDevice != 0 && outInfo.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb" {
			fmt.Print("棋子显示：1 英文（默认），2 中文 > ")
			value, err = readDirectLine()
			if err != nil {
				return "", err
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "en":
		return xiangqi.English, nil
	case "2", "zh":
		return xiangqi.Chinese, nil
	default:
		return "", errors.New("--lang 只能使用 en 或 zh")
	}
}

func startInput(ui *screen) *terminalInput {
	if ui.EnableMouse() {
		return newRawTerminalInput(os.Stdin)
	}
	return newTerminalInput(os.Stdin)
}

func readMove(in lineReader, g *xiangqi.Game, ui *screen, initialMessage, invite, tunnel string) (xiangqi.Move, error) {
	message := initialMessage
	var selected *xiangqi.Pos
	var targets []xiangqi.Move
	ui.Select(nil)
	for {
		ui.Show(g, message, true)
		var line string
		var event inputEvent
		var err error
		if terminal, ok := in.(*terminalInput); ok {
			event, err = terminal.nextEvent()
			if err == nil && event.draftChanged {
				ui.draft = event.draft
				continue
			}
			if err == nil && event.mouse {
				pos, hit := ui.Hit(event.col, event.row)
				if !hit {
					message = "请点击棋盘上的交叉点"
					continue
				}
				piece := g.At(pos)
				if piece.Side == g.Turn {
					moves := g.LegalMovesFrom(pos)
					selected, targets = &pos, moves
					ui.Select(selected)
					message = fmt.Sprintf("%s 在 %s，可走：%s；点击落点或输入两位坐标", piece.DisplayLabel(ui.view.Language), pos, formatTargets(moves))
					continue
				}
				if selected == nil {
					message = "请先点击己方棋子"
					continue
				}
				for _, move := range targets {
					if move.To == pos {
						ui.Select(nil)
						return move, nil
					}
				}
				message = fmt.Sprintf("%s 不能走到 %s；可走：%s", g.At(*selected).DisplayLabel(ui.view.Language), pos, formatTargets(targets))
				continue
			}
			line = event.line
		} else {
			line, err = in.ReadString('\n')
		}
		if err != nil {
			return xiangqi.Move{}, err
		}
		if line == "" {
			continue
		}
		ui.draft = ""
		line = strings.TrimSpace(line)
		command := strings.ToLower(line)
		switch command {
		case "q", "quit", "exit":
			return xiangqi.Move{}, errQuit
		case "help", "?":
			message = "点击棋子再点落点；0605 走棋；R2 / 车2 选棋；m 切换复制模式；q 退出"
		case "m", "mouse":
			message = ui.ToggleMouse()
		case "i", "invite":
			if invite != "" {
				message = invite
			} else {
				message = "本局没有联机邀请口令"
			}
		case "t", "tunnel":
			if tunnel != "" {
				message = tunnel
			} else {
				message = "本局不需要隧道"
			}
		case "moves":
			moves := g.LegalMoves(g.Turn)
			message = fmt.Sprintf("当前有 %d 步合法棋；输入棋子编号如 R2，或 moves 06 查看落点", len(moves))
		default:
			if strings.HasPrefix(command, "moves ") {
				arg := strings.TrimSpace(line[len("moves "):])
				from, err := xiangqi.ParsePos(arg)
				if err != nil {
					if piece, parseErr := xiangqi.ParseDisplayPiece(arg, g.Turn); parseErr == nil && piece.Side == g.Turn {
						var ok bool
						from, ok = g.Find(piece.Label(), g.Turn)
						if !ok {
							message = arg + " 已不在棋盘上"
							continue
						}
					} else {
						message = "请输入当前方棋子编号或两位坐标"
						continue
					}
				}
				if g.At(from).Side != g.Turn {
					message = from.String() + " 没有当前方棋子"
					continue
				}
				message = from.String() + "：" + formatTargets(g.LegalMovesFrom(from))
				continue
			}
			if piece, err := xiangqi.ParseDisplayPiece(line, g.Turn); err == nil {
				if piece.Side != g.Turn {
					message = line + " 不是当前方棋子"
					continue
				}
				from, ok := g.Find(piece.Label(), g.Turn)
				if !ok {
					message = line + " 已不在棋盘上"
					continue
				}
				moves := g.LegalMovesFrom(from)
				selected, targets = &from, moves
				ui.Select(selected)
				message = fmt.Sprintf("%s 在 %s，可走：%s；输入两位落点", line, from, formatTargets(moves))
				continue
			}
			if selected != nil && len(line) == 2 {
				to, err := xiangqi.ParsePos(line)
				if err != nil {
					message = err.Error()
					continue
				}
				for _, move := range targets {
					if move.To == to {
						ui.Select(nil)
						return move, nil
					}
				}
				message = fmt.Sprintf("%s 不能走到 %s；可走：%s", g.At(*selected).Label(), to, formatTargets(targets))
				continue
			}
			move, err := xiangqi.ParseMove(line)
			if err != nil {
				message = err.Error()
				continue
			}
			ui.Select(nil)
			return move, nil
		}
	}
}

// formatTargets groups consecutive ranks on each file while preserving exact legal destinations.
func formatTargets(moves []xiangqi.Move) string {
	if len(moves) == 0 {
		return "无合法落点"
	}
	byFile := [9][]int{}
	for _, m := range moves {
		byFile[m.To.X] = append(byFile[m.To.X], m.To.Y)
	}
	parts := make([]string, 0, len(moves))
	for x, ranks := range byFile {
		sort.Ints(ranks)
		for i := 0; i < len(ranks); {
			j := i
			for j+1 < len(ranks) && ranks[j+1] == ranks[j]+1 {
				j++
			}
			if j > i {
				parts = append(parts, fmt.Sprintf("%d%d~%d%d", x, ranks[i], x, ranks[j]))
			} else {
				parts = append(parts, fmt.Sprintf("%d%d", x, ranks[i]))
			}
			i = j + 1
		}
	}
	return strings.Join(parts, " ")
}

func play(args []string) error {
	fs := flag.NewFlagSet("play", flag.ContinueOnError)
	langFlag := fs.String("lang", "", "棋子显示语言：en 或 zh；交互启动时可选择")
	redModel := fs.String("red-model", "", "红方 AI 模型，空为人类")
	blackModel := fs.String("black-model", "", "黑方 AI 模型，空为人类")
	redURL := fs.String("red-url", "", "红方Base URL 或完整 Chat / Responses 接口地址")
	blackURL := fs.String("black-url", "", "黑方Base URL 或完整 Chat / Responses 接口地址")
	strict := fs.Bool("strict-ai", false, "不给 AI 合法走法列表，记录非法走法")
	explain := fs.Bool("explain", false, "显示 AI 每步的简短解释")
	fastAI := fs.Bool("fast-ai", false, "兼容旧参数：支持的百炼 DeepSeek V4 模型已默认快速走棋")
	thinking := fs.String("thinking", "", "深度思考：off、on、default；省略时已识别接口默认关闭")
	redThinking := fs.String("red-thinking", "", "覆盖红方深度思考模式")
	blackThinking := fs.String("black-thinking", "", "覆盖黑方深度思考模式")
	redThinkingAPI := fs.String("red-thinking-api", "auto", "红方思考协议：auto、bailian、deepseek、ark")
	blackThinkingAPI := fs.String("black-thinking-api", "auto", "黑方思考协议：auto、bailian、deepseek、ark")
	aiTimeout := fs.Duration("ai-timeout", 0, "AI 每步总等待上限，例如 180s；默认关闭思考 90s，其他模式 180s")
	aiMaxTokens := fs.Int("ai-max-tokens", 0, "覆盖输出 Token 上限；开启思考时包含推理消耗，默认 16384")
	thinkingEffort := fs.String("thinking-effort", "", "开启思考时的力度：low、high、max；已适配模型默认选择支持的最低力度")
	maxPlies := fs.Int("max-plies", 200, "AI 对 AI 最大半回合数")
	games := fs.Int("games", 1, "双 AI 对弈局数；每局交换红黑")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lang, err := chooseLanguage(*langFlag)
	if err != nil {
		return err
	}
	if *games < 1 {
		return errors.New("--games 至少为 1")
	}
	agents := map[xiangqi.Side]*ai.Agent{}
	if *redModel != "" {
		agents[xiangqi.Red] = &ai.Agent{Endpoint: *redURL, Model: *redModel, APIKey: keyFor(xiangqi.Red), Strict: *strict, Explain: *explain}
	}
	if *blackModel != "" {
		agents[xiangqi.Black] = &ai.Agent{Endpoint: *blackURL, Model: *blackModel, APIKey: keyFor(xiangqi.Black), Strict: *strict, Explain: *explain}
	}
	for side, a := range agents {
		if a.Endpoint != "" {
			normalized, err := ai.NormalizeEndpoint(a.Endpoint)
			if err != nil {
				return err
			}
			a.Endpoint = normalized
		}
		selected, protocol := *redThinking, *redThinkingAPI
		if side == xiangqi.Black {
			selected, protocol = *blackThinking, *blackThinkingAPI
		}
		if selected == "" {
			selected = *thinking
		}
		a.Thinking, a.ThinkingAPI = ai.ParseThinking(selected), protocol
		a.Timeout, a.MaxTokens = *aiTimeout, *aiMaxTokens
		a.ReasoningEffort = *thinkingEffort
	}
	for side, a := range agents {
		if a.APIKey == "" {
			return fmt.Errorf("%s缺少 API Key，请设置 MOYU_RED_API_KEY、MOYU_BLACK_API_KEY 或 OPENAI_API_KEY", side)
		}
		if *fastAI {
			if a.Thinking == ai.ThinkingOn || a.Thinking == ai.ThinkingDefault {
				return errors.New("--fast-ai 不能与开启思考或服务商默认同时使用")
			}
			a.Thinking = ai.ThinkingOff
			if !ai.SupportsFastMode(a.Endpoint, a.Model) {
				return fmt.Errorf("%s的 %s 不支持 --fast-ai；当前仅支持阿里云百炼的 DeepSeek V4 混合思考模型", side, a.Model)
			}
		}
		if _, _, err := a.ThinkingConfig(); err != nil {
			return fmt.Errorf("%s AI：%w", side, err)
		}

	}
	if *fastAI && len(agents) == 0 {
		return errors.New("--fast-ai 需要先配置 AI 模型")
	}
	if *games > 1 && len(agents) != 2 {
		return errors.New("--games 只适用于双 AI 对弈")
	}
	return playSession(lang, agents, *explain, *maxPlies, *games)
}

func playSession(lang xiangqi.Language, agents map[xiangqi.Side]*ai.Agent, explain bool, maxPlies, games int) error {
	redAgent, blackAgent := agents[xiangqi.Red], agents[xiangqi.Black]
	scores := [2]int{}
	draws := 0
	ui := newScreen()
	ui.view.Language = lang
	if agents[xiangqi.Red] != nil && agents[xiangqi.Black] == nil {
		ui.view.Bottom = xiangqi.Black
	}
	defer ui.Close()
	in := startInput(ui)
	lastWinner := xiangqi.None
	for gameNo := 0; gameNo < games; gameNo++ {
		if gameNo%2 == 0 {
			agents[xiangqi.Red], agents[xiangqi.Black] = redAgent, blackAgent
		} else {
			agents[xiangqi.Red], agents[xiangqi.Black] = blackAgent, redAgent
		}
		label := "点击棋子再点落点；也可输入 0605 或 R2 / 车2；help 看操作"
		if games > 1 {
			label = fmt.Sprintf("第 %d/%d 局：红方 %s，黑方 %s", gameNo+1, games, agents[xiangqi.Red].Model, agents[xiangqi.Black].Model)
		}
		winner, err := playOne(agents, explain, maxPlies, ui, in, label)
		if err != nil {
			return err
		}
		lastWinner = winner
		if winner == xiangqi.None {
			draws++
		} else if (winner == xiangqi.Red) == (gameNo%2 == 0) {
			scores[0]++
		} else {
			scores[1]++
		}
	}
	ui.Close()
	if games > 1 {
		fmt.Printf("\n赛果：%s %d 胜，%s %d 胜，未分胜负 %d 局。\n", redAgent.Model, scores[0], blackAgent.Model, scores[1], draws)
	} else if lastWinner != xiangqi.None {
		fmt.Printf("%s获胜。\n", lastWinner)
	} else {
		fmt.Println("对局结束，未分胜负。")
	}
	return nil
}

func playOne(agents map[xiangqi.Side]*ai.Agent, explain bool, maxPlies int, ui *screen, in lineReader, initialMessage string) (xiangqi.Side, error) {
	g := xiangqi.New()
	for _, agent := range agents {
		if agent != nil {
			agent.ResetGame()
		}
	}
	invalid := map[xiangqi.Side]int{}
	message := initialMessage
	for g.Winner == xiangqi.None {
		if len(agents) == 2 && maxPlies > 0 && len(g.History) >= maxPlies {
			ui.Show(g, fmt.Sprintf("已到 %d 个半回合；红方非法 %d 次，黑方非法 %d 次", maxPlies, invalid[xiangqi.Red], invalid[xiangqi.Black]), false)
			return xiangqi.None, nil
		}
		side := g.Turn
		var move xiangqi.Move
		if a := agents[side]; a != nil {
			ui.Show(g, fmt.Sprintf("%s AI（%s）思考中...", side, a.Model), false)
			started := time.Now()
			decision, err := chooseWithProgress(a, g, side, ui, started)
			elapsed := time.Since(started)
			invalid[side] += decision.Invalid
			if err != nil {
				if errors.Is(err, ai.ErrInvalidMoves) {
					g.Winner = side.Other()
					message = fmt.Sprintf("%s AI 连续走出非法棋，判负", side)
					break
				}
				if errors.Is(err, ai.ErrTokenLimit) {
					if retryErr := retryTokenLimit(a, g, ui, in, elapsed); retryErr != nil {
						return xiangqi.None, retryErr
					}
					continue
				}
				return xiangqi.None, fmt.Errorf("%s AI 等待 %.1f 秒后失败：%w", side, elapsed.Seconds(), err)
			}
			move = decision.Move
			message = fmt.Sprintf("%s AI 走 %s（用时 %.1f 秒）", side, move, elapsed.Seconds())
			if explain && decision.Explanation != "" {
				message += "；" + decision.Explanation
			}
		} else {
			var err error
			move, err = readMove(in, g, ui, message, "", "")
			if err != nil {
				return xiangqi.None, err
			}
		}
		if err := g.Apply(move); err != nil {
			message = err.Error()
			continue
		}
		if agents[side] == nil {
			message = "已走 " + move.String()
		}
	}
	ui.Show(g, fmt.Sprintf("%s；红方非法 %d 次，黑方非法 %d 次", message, invalid[xiangqi.Red], invalid[xiangqi.Black]), false)
	return g.Winner, nil
}

// Keep the single terminal panel responsive while a model request is pending.
func chooseWithProgress(a *ai.Agent, g *xiangqi.Game, side xiangqi.Side, ui *screen, started time.Time) (ai.Decision, error) {
	type result struct {
		decision ai.Decision
		err      error
	}
	done := make(chan result, 1)
	position := g.Clone()
	go func() {
		decision, err := a.Choose(context.Background(), position)
		done <- result{decision: decision, err: err}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			return r.decision, r.err
		case <-ticker.C:
			if ui.interactive {
				ui.Show(g, fmt.Sprintf("%s AI（%s）思考中...已等待 %.0f 秒", side, a.Model, time.Since(started).Seconds()), false)
			}
		}
	}
}

func host(args []string) error {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	langFlag := fs.String("lang", "", "棋子显示语言：en 或 zh；交互启动时可选择")
	listen := fs.String("listen", "127.0.0.1:8765", "监听地址；使用隧道时保持默认值")
	autoTunnel := fs.Bool("tunnel", false, "自动启动 Cloudflare Quick Tunnel")
	tunnelBin := fs.String("tunnel-bin", "", "指定已有 cloudflared 程序路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tunnelBin != "" && !*autoTunnel {
		return errors.New("--tunnel-bin 需要同时使用 --tunnel")
	}
	lang, err := chooseLanguage(*langFlag)
	if err != nil {
		return err
	}
	h, err := netplay.NewHost()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	server := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())
	fmt.Printf("房间已在本机启动：http://%s\n", ln.Addr())
	fmt.Printf("邀请口令：%s\n", h.Token())
	invite := "邀请口令：" + h.Token()
	tunnelInfo := "另开终端运行 cloudflared tunnel --url http://" + ln.Addr().String()
	var managedTunnel *tunnel.Process
	if *autoTunnel {
		tunnelCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		fmt.Println("正在准备公网隧道，请稍候...")
		firstProgress := true
		binary, err := (tunnel.Resolver{Binary: *tunnelBin, Status: func(message string) {
			fmt.Println(message)
		}, Progress: func(downloaded, total int64) {
			if firstProgress {
				fmt.Println("未找到 cloudflared，首次下载会保存到用户缓存；中断后再次运行会续传。")
				firstProgress = false
			}
			if total > 0 {
				fmt.Printf("cloudflared 下载进度：%.1f / %.1f MiB\n", float64(downloaded)/(1<<20), float64(total)/(1<<20))
			} else {
				fmt.Printf("cloudflared 已下载：%.1f MiB，正在连接下载源...\n", float64(downloaded)/(1<<20))
			}
		}}).Resolve(tunnelCtx)
		if err != nil {
			return fmt.Errorf("准备公网隧道失败：%w；仍可运行 host 本机联机", err)
		}
		fmt.Println("cloudflared 已就绪，正在建立隧道并验证公网地址...")
		host, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			return err
		}
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		if host == "::" {
			host = "::1"
		}
		origin := "http://" + net.JoinHostPort(host, port)
		managedTunnel, err = tunnel.Start(tunnelCtx, binary, origin, h.Token(), tunnel.StartOptions{})
		if err != nil {
			return fmt.Errorf("公网隧道启动失败：%w；仍可运行 host 本机联机", err)
		}
		defer managedTunnel.Close()
		tunnelInfo = "公网地址：" + managedTunnel.URL()
		fmt.Println(tunnelInfo)
		fmt.Println("把公网地址和邀请口令发给朋友；房主执红，朋友执黑。")
	} else {
		fmt.Printf("跨网络对弈：%s\n", tunnelInfo)
		fmt.Println("把生成的 https 地址和邀请口令发给朋友；房主执红，朋友执黑。")
	}
	defer func() {
		state := h.State()
		if state.GuestJoined && state.Winner == xiangqi.None {
			h.MarkHostLeft()
			time.Sleep(1500 * time.Millisecond)
		}
	}()
	ui := newScreen()
	ui.view.Language = lang
	if managedTunnel != nil {
		ui.SetShare(managedTunnel.URL(), h.Token())
		ui.cleanup = managedTunnel.Close
	}
	defer ui.Close()
	in := startInput(ui)
	alerts := make(chan error, 2)
	in.alerts = alerts
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				state := h.State()
				if state.GuestLeft {
					select {
					case alerts <- errPeerLeft:
					case <-watchCtx.Done():
					}
					return
				}
			}
		}
	}()
	if managedTunnel != nil {
		go func() {
			select {
			case err := <-managedTunnel.Alert():
				select {
				case alerts <- err:
				case <-watchCtx.Done():
				}
			case <-watchCtx.Done():
			}
		}()
	}
	version := -1
	message := invite
	resultShown := false
	joinedShown := false
	for {
		state := h.State()
		g, err := state.Game()
		if err != nil {
			return err
		}
		if state.GuestLeft {
			return finishHostPeerLeft(ui, g)
		}
		if state.Version != version {
			if !state.GuestJoined && state.Winner == xiangqi.None {
				ui.Show(g, "等待客方加入；"+invite+"；输入 q 退出", false)
			} else if state.Turn == xiangqi.Black {
				ui.Show(g, "等待客方走棋；输入 q 退出", false)
			} else {
				ui.Show(g, invite, false)
			}
			version = state.Version
		}
		if state.GuestJoined && !joinedShown {
			ui.Show(g, "客方已加入，红方先走", false)
			joinedShown = true
		}
		if state.Winner != xiangqi.None {
			if !resultShown {
				ui.Show(g, "棋局结束，等待客方确认结果；输入 q 可退出", false)
				resultShown = true
			}
			if h.GuestFinished() {
				ui.Close()
				fmt.Printf("%s获胜。\n", state.Winner)
				return nil
			}
			command, err := in.wait(time.Second)
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					return finishHostPeerLeft(ui, g)
				}
				return err
			}
			switch strings.ToLower(command) {
			case "q", "quit", "exit":
				return errQuit
			case "m", "mouse":
				ui.Show(g, ui.ToggleMouse(), false)
			}
			continue
		}
		if !state.GuestJoined {
			command, err := in.wait(time.Second)
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					return finishHostPeerLeft(ui, g)
				}
				return err
			}
			switch strings.ToLower(command) {
			case "q", "quit", "exit":
				return errQuit
			case "m", "mouse":
				ui.Show(g, ui.ToggleMouse(), false)
			case "i", "invite":
				ui.Show(g, invite, false)
			case "t", "tunnel":
				ui.Show(g, tunnelInfo, false)
			default:
				in.hold(command)
			}
			continue
		}
		if state.Turn == xiangqi.Red {
			move, err := readMove(in, g, ui, message, invite, tunnelInfo)
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					return finishHostPeerLeft(ui, g)
				}
				return err
			}
			if err := h.HostMove(move); err != nil {
				message = err.Error()
			} else {
				message = invite
			}
		} else {
			command, err := in.wait(time.Second)
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					return finishHostPeerLeft(ui, g)
				}
				return err
			}
			switch strings.ToLower(command) {
			case "q", "quit", "exit":
				return errQuit
			case "m", "mouse":
				ui.Show(g, ui.ToggleMouse(), false)
			case "i", "invite":
				ui.Show(g, invite, false)
			case "t", "tunnel":
				ui.Show(g, tunnelInfo, false)
			default:
				in.hold(command)
			}
		}
	}
}

func finishHostPeerLeft(ui *screen, g *xiangqi.Game) error {
	ui.Show(g, "客方已退出，游戏结束", false)
	ui.Close()
	if g.Winner != xiangqi.None {
		fmt.Printf("客方已退出；%s获胜。\n", g.Winner)
	} else {
		fmt.Println("客方已退出，游戏结束。")
	}
	return nil
}

func join(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	langFlag := fs.String("lang", "", "棋子显示语言：en 或 zh；交互启动时可选择")
	inviteURL := fs.String("url", "", "房主分享的 http(s) 邀请地址")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inviteURL == "" {
		fmt.Print("房主分享的网址 > ")
		value, err := readDirectLine()
		if err != nil {
			return err
		}
		*inviteURL = strings.TrimSpace(value)
		if *inviteURL == "" {
			return errors.New("缺少房主分享的网址")
		}
	}
	lang, err := chooseLanguage(*langFlag)
	if err != nil {
		return err
	}
	token := os.Getenv("MOYU_JOIN_TOKEN")
	if token == "" {
		var err error
		token, err = readHiddenLine("邀请口令（输入时不显示） > ")
		if err != nil {
			return err
		}
		token = strings.TrimSpace(token)
	}
	guest, err := netplay.NewGuest(*inviteURL, token)
	if err != nil {
		return err
	}
	if _, err := guest.Join(context.Background()); err != nil {
		return fmt.Errorf("加入房间失败：%w", err)
	}
	finished := false
	defer func() {
		if !finished {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = guest.Leave(ctx)
		}
	}()
	ui := newScreen()
	ui.view.Language = lang
	ui.view.Bottom = xiangqi.Black
	defer ui.Close()
	in := startInput(ui)
	alerts := make(chan error, 1)
	in.alerts = alerts
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				state, err := guest.State(watchCtx)
				if err == nil {
					failures = 0
					if !state.HostLeft {
						continue
					}
				} else {
					failures++
					if failures < 3 {
						continue
					}
				}
				select {
				case alerts <- errPeerLeft:
				case <-watchCtx.Done():
				}
				return
			}
		}
	}()
	version := -1
	message := "等待房主走棋"
	var lastGame *xiangqi.Game
	var pendingMove string
	pendingVersion := -1
	for {
		state, err := guestStateWithRetry(guest, in, ui, lastGame)
		if err != nil {
			if errors.Is(err, errPeerLeft) {
				ui.Close()
				fmt.Println("房主已退出或连接已断开，游戏结束。")
				return nil
			}
			return err
		}
		g, err := state.Game()
		if err != nil {
			return err
		}
		lastGame = g
		if state.HostLeft {
			ui.Show(g, "房主已退出，游戏结束", false)
			ui.Close()
			fmt.Println("房主已退出，游戏结束。")
			return nil
		}
		if pendingMove != "" {
			if state.Version > pendingVersion && len(state.History) > 0 && state.History[len(state.History)-1] == pendingMove {
				message = "已确认走棋 " + pendingMove + "（刚才的响应中断）"
			} else if state.Version == pendingVersion {
				message = "走棋未提交，请重新输入"
			} else {
				message = "棋局已更新，请核对上一步"
			}
			pendingMove = ""
		}
		if state.Version != version {
			if strings.HasPrefix(message, "已确认走棋") || strings.HasPrefix(message, "已提交") {
				// Keep the submission result visible with the new board.
			} else if state.Turn == xiangqi.Black {
				message = "轮到你走棋"
			} else {
				message = "等待房主走棋；输入 q 退出"
			}
			ui.Show(g, message, false)
			version = state.Version
		}
		if state.Winner != xiangqi.None {
			if err := guestAckWithRetry(guest); err != nil {
				// The guest has the final state; the host can still exit with q.
				ui.Show(g, "终局已收到，但确认回执失败："+err.Error(), false)
			}
			ui.Close()
			finished = true
			fmt.Printf("%s获胜。\n", state.Winner)
			return nil
		}
		if state.Turn == xiangqi.Black {
			move, err := readMove(in, g, ui, message, "", "")
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					ui.Close()
					fmt.Println("房主已退出或连接已断开，游戏结束。")
					return nil
				}
				return err
			}
			if _, err := guest.Move(context.Background(), move, state.Version); err != nil {
				pendingMove = move.String()
				pendingVersion = state.Version
				message = "走棋响应中断，正在核对房主棋局：" + err.Error()
			} else {
				message = "已提交 " + move.String()
			}
		} else {
			command, err := in.wait(time.Second)
			if err != nil {
				if errors.Is(err, errPeerLeft) {
					ui.Close()
					fmt.Println("房主已退出或连接已断开，游戏结束。")
					return nil
				}
				return err
			}
			switch strings.ToLower(command) {
			case "q", "quit", "exit":
				return errQuit
			case "m", "mouse":
				ui.Show(g, ui.ToggleMouse(), false)
			default:
				in.hold(command)
			}
		}
	}
}

func guestAckWithRetry(guest *netplay.Guest) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := guest.Ack(context.Background()); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func guestStateWithRetry(guest *netplay.Guest, in *terminalInput, ui *screen, lastGame *xiangqi.Game) (netplay.State, error) {
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		state, err := guest.State(context.Background())
		if err == nil {
			return state, nil
		}
		lastErr = err
		if lastGame != nil {
			ui.Show(lastGame, "连接暂时中断，正在重试："+err.Error(), false)
		}
		command, waitErr := in.wait(time.Second)
		if waitErr != nil {
			return netplay.State{}, waitErr
		}
		switch strings.ToLower(command) {
		case "q", "quit", "exit":
			return netplay.State{}, errQuit
		case "m", "mouse":
			if lastGame != nil {
				ui.Show(lastGame, ui.ToggleMouse(), false)
			} else {
				ui.ToggleMouse()
			}
		default:
			in.hold(command)
		}
	}
	return netplay.State{}, fmt.Errorf("多次无法连接房主：%w", lastErr)
}
