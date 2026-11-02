package netplay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moyu-xiangqi/internal/xiangqi"
)

func TestHostGuestRoundTrip(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h.Handler())
	defer s.Close()
	guest, err := NewGuest(s.URL, h.Token())
	if err != nil {
		t.Fatal(err)
	}
	state, err := guest.State(context.Background())
	if err != nil || state.Version != 0 {
		t.Fatalf("initial state: %+v %v", state, err)
	}
	if state.GuestJoined {
		t.Fatal("reading state must not count as a player joining")
	}
	state, err = guest.Join(context.Background())
	if err != nil || !state.GuestJoined || !h.State().GuestJoined {
		t.Fatalf("join: %+v %v", state, err)
	}
	move, _ := xiangqi.ParseMove("0304")
	if _, err := guest.Move(context.Background(), move, state.Version); err == nil {
		t.Fatal("guest moved on red turn")
	}
	red, _ := xiangqi.ParseMove("0605")
	if err := h.HostMove(red); err != nil {
		t.Fatal(err)
	}
	state, err = guest.State(context.Background())
	if err != nil || state.Version != 1 || state.Turn != xiangqi.Black {
		t.Fatalf("red move: %+v %v", state, err)
	}
	loaded, err := state.Game()
	if err != nil || loaded.Board[5][0].Label() != "B1" || loaded.Board[9][8].Label() != "R2" {
		t.Fatalf("piece IDs lost on network: %v", err)
	}
	if _, err := guest.Move(context.Background(), move, 0); err == nil {
		t.Fatal("stale move accepted")
	}
	state, err = guest.Move(context.Background(), move, state.Version)
	if err != nil || state.Version != 2 || state.Turn != xiangqi.Red {
		t.Fatalf("guest move: %+v %v", state, err)
	}
	bad, _ := NewGuest(s.URL, "wrong-token")
	if _, err := bad.State(context.Background()); err == nil {
		t.Fatal("bad token accepted")
	}
	res, err := http.Get(s.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatal(res.StatusCode)
	}
	if err := guest.Ack(context.Background()); err == nil {
		t.Fatal("guest acknowledged an unfinished game")
	}
	h.mu.Lock()
	h.game.Winner = xiangqi.Red
	h.mu.Unlock()
	if err := guest.Ack(context.Background()); err != nil || !h.GuestFinished() {
		t.Fatalf("winner acknowledgement failed: %v", err)
	}
	if err := guest.Leave(context.Background()); err != nil || !h.State().GuestLeft {
		t.Fatalf("guest leave not reported: %v", err)
	}
	h.MarkHostLeft()
	state, err = guest.State(context.Background())
	if err != nil || !state.HostLeft {
		t.Fatalf("host leave not reported: %+v %v", state, err)
	}
}

func TestGuestTimeoutAfterUnacknowledgedFinish(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatal(err)
	}
	h.guestJoined = true
	h.guestSeen = time.Now().Add(-21 * time.Second)
	h.game.Winner = xiangqi.Red
	state := h.State()
	if !state.GuestLeft || state.GuestJoined {
		t.Fatalf("finished game should report missing guest: %+v", state)
	}
}
