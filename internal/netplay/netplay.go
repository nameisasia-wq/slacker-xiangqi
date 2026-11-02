package netplay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"moyu-xiangqi/internal/xiangqi"
)

type State struct {
	Rows        []string     `json:"rows"`
	Turn        xiangqi.Side `json:"turn"`
	Winner      xiangqi.Side `json:"winner"`
	History     []string     `json:"history"`
	Version     int          `json:"version"`
	GuestJoined bool         `json:"guestJoined"`
	GuestLeft   bool         `json:"guestLeft"`
	HostLeft    bool         `json:"hostLeft"`
}

func (s State) Game() (*xiangqi.Game, error) {
	if len(s.Rows) != 10 {
		return nil, errors.New("收到无效棋盘")
	}
	g := &xiangqi.Game{Turn: s.Turn, Winner: s.Winner, History: append([]string(nil), s.History...)}
	for y, row := range s.Rows {
		cells := strings.Fields(row)
		if len(cells) != 9 {
			return nil, errors.New("收到无效棋盘行")
		}
		for x := 0; x < 9; x++ {
			piece, err := xiangqi.ParsePiece(cells[x])
			if err != nil {
				return nil, err
			}
			g.Board[y][x] = piece
		}
	}
	return g, nil
}

type Host struct {
	mu            sync.Mutex
	game          *xiangqi.Game
	version       int
	token         string
	guestFinished bool
	guestJoined   bool
	guestLeft     bool
	guestSeen     time.Time
	hostLeft      bool
}

func NewHost() (*Host, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &Host{game: xiangqi.New(), token: hex.EncodeToString(b)}, nil
}

func (h *Host) Token() string { return h.token }

func (h *Host) GuestFinished() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.guestFinished
}

func (h *Host) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.guestJoined && !h.guestLeft && !h.guestFinished && time.Since(h.guestSeen) > 20*time.Second {
		h.guestJoined = false
		h.guestLeft = true
	}
	return h.stateLocked()
}

func (h *Host) stateLocked() State {
	return State{Rows: h.game.Rows(), Turn: h.game.Turn, Winner: h.game.Winner, History: append([]string(nil), h.game.History...), Version: h.version, GuestJoined: h.guestJoined, GuestLeft: h.guestLeft, HostLeft: h.hostLeft}
}

func (h *Host) MarkHostLeft() {
	h.mu.Lock()
	h.hostLeft = true
	h.mu.Unlock()
}

func (h *Host) HostMove(m xiangqi.Move) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.game.Turn != xiangqi.Red {
		return errors.New("当前不是房主回合")
	}
	if err := h.game.Apply(m); err != nil {
		return err
	}
	h.version++
	return nil
}

func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.mu.Lock()
		if h.guestJoined && !h.guestLeft {
			h.guestSeen = time.Now()
		}
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(h.State())
	})
	mux.HandleFunc("POST /join", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.hostLeft || h.guestLeft || h.game.Winner != xiangqi.None {
			http.Error(w, "棋局已结束", http.StatusConflict)
			return
		}
		h.guestJoined = true
		h.guestSeen = time.Now()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.stateLocked())
	})
	mux.HandleFunc("POST /leave", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.guestJoined = false
		h.guestLeft = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.stateLocked())
	})
	mux.HandleFunc("POST /move", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Move    string `json:"move"`
			Version int    `json:"version"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		m, err := xiangqi.ParseMove(body.Move)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.guestJoined || h.guestLeft || h.hostLeft {
			http.Error(w, "对局未连接或已结束", http.StatusConflict)
			return
		}
		h.guestSeen = time.Now()
		if body.Version != h.version {
			http.Error(w, "棋局已更新，请刷新", http.StatusConflict)
			return
		}
		if h.game.Turn != xiangqi.Black {
			http.Error(w, "不是客方回合", http.StatusConflict)
			return
		}
		if err := h.game.Apply(m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.version++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.stateLocked())
	})
	mux.HandleFunc("POST /ack", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.game.Winner == xiangqi.None {
			http.Error(w, "棋局尚未结束", http.StatusConflict)
			return
		}
		h.guestFinished = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.stateLocked())
	})
	return mux
}

func (h *Host) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+h.token
}

type Guest struct {
	base   *url.URL
	token  string
	client *http.Client
}

func NewGuest(rawURL, token string) (*Guest, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("邀请地址必须以 http:// 或 https:// 开头")
	}
	if token == "" {
		return nil, errors.New("缺少邀请口令")
	}
	return &Guest{base: u, token: token, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (g *Guest) call(ctx context.Context, method, path string, input any, output any) error {
	u := *g.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	var body io.Reader
	if input != nil {
		b, _ := json.Marshal(input)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("房主返回 %d：%s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(output)
}

func (g *Guest) State(ctx context.Context) (State, error) {
	var state State
	err := g.call(ctx, http.MethodGet, "/state", nil, &state)
	return state, err
}

func (g *Guest) Join(ctx context.Context) (State, error) {
	var state State
	err := g.call(ctx, http.MethodPost, "/join", nil, &state)
	return state, err
}

func (g *Guest) Leave(ctx context.Context) error {
	var state State
	return g.call(ctx, http.MethodPost, "/leave", nil, &state)
}

func (g *Guest) Move(ctx context.Context, move xiangqi.Move, version int) (State, error) {
	var state State
	err := g.call(ctx, http.MethodPost, "/move", map[string]any{"move": move.String(), "version": version}, &state)
	return state, err
}

func (g *Guest) Ack(ctx context.Context) error {
	var state State
	return g.call(ctx, http.MethodPost, "/ack", nil, &state)
}
