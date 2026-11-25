package main

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
	"time"
)

type state struct {
	Asset    string  `json:"asset"`
	Position float64 `json:"position"`
	Rate     float64 `json:"rate"`
	Playing  bool    `json:"playing"`
	Version  int64   `json:"version"`
	At       int64   `json:"at"`
}
type liveRoom struct {
	sync.Mutex
	clients map[*websocket.Conn]bool
}

func write(ws *websocket.Conn, v any) error {
	ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return ws.WriteJSON(v)
}
func (a *app) live(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" || a.origins[r.Header.Get("Origin")] }}
	ws, e := up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(4096)
	ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	var auth struct{ Token string }
	if ws.ReadJSON(&auth) != nil {
		return
	}
	c := r.Context()
	var room, name string
	if a.db.QueryRow(c, "select room,nickname from members where token=$1", hash(auth.Token)).Scan(&room, &name) != nil {
		return
	}
	ws.SetReadDeadline(time.Time{})
	a.mu.Lock()
	lr := a.rooms[room]
	if lr == nil {
		lr = &liveRoom{clients: map[*websocket.Conn]bool{}}
		a.rooms[room] = lr
	}
	lr.Lock()
	lr.clients[ws] = true
	a.mu.Unlock()
	var snapshot state
	e = a.db.QueryRow(c, "select state from rooms where id=$1", room).Scan(&snapshot)
	if e == nil {
		e = write(ws, map[string]any{"type": "state", "state": snapshot, "now": time.Now().UnixMilli()})
	}
	lr.Unlock()
	defer func() {
		a.mu.Lock()
		lr.Lock()
		delete(lr.clients, ws)
		if len(lr.clients) == 0 {
			delete(a.rooms, room)
		}
		lr.Unlock()
		a.mu.Unlock()
	}()
	if e != nil {
		return
	}
	for {
		var m struct {
			Type, Text string
			State      state
		}
		if ws.ReadJSON(&m) != nil {
			return
		}
		lr.Lock()
		switch m.Type {
		case "ping":
			write(ws, map[string]any{"type": "pong", "now": time.Now().UnixMilli()})
		case "chat":
			if len([]rune(m.Text)) > 0 && len([]rune(m.Text)) <= 200 {
				lr.broadcast(map[string]string{"type": "chat", "name": name, "text": m.Text})
			}
		case "control":
			a.control(c, lr, room, m.State)
		}
		lr.Unlock()
	}
}
func (lr *liveRoom) broadcast(m any) {
	for ws := range lr.clients {
		if write(ws, m) != nil {
			ws.Close()
		}
	}
}
func (a *app) control(c context.Context, lr *liveRoom, room string, s state) {
	if s.Position < 0 || s.Position > 604800 || s.Rate < 0.25 || s.Rate > 4 {
		return
	}
	s.At = time.Now().UnixMilli()
	prior := s.Version
	s.Version++
	raw, e := json.Marshal(s)
	if e != nil {
		return
	}
	res, e := a.db.Exec(c, "update rooms set state=$1 where id=$2 and (state->>'version')::bigint=$3 and exists(select 1 from assets where id=$4 and assets.room=$2 and ready)", raw, room, prior, s.Asset)
	if e != nil || res.RowsAffected() != 1 {
		return
	}
	lr.broadcast(map[string]any{"type": "state", "state": s, "now": s.At})
}
