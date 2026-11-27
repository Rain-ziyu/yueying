package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type state struct {
	Asset    string  `json:"asset"`
	Position float64 `json:"position"`
	Rate     float64 `json:"rate"`
	Playing  bool    `json:"playing"`
	Version  int64   `json:"version"`
	At       int64   `json:"at"`
}

type participant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	RTT  *int   `json:"rtt"`
	asOf time.Time
}

type peer struct {
	ws         *websocket.Conn
	out        chan any
	done       chan struct{}
	member     participant
	subscribed bool
}

// A slow socket cannot hold up the other players or a room heartbeat.
func (p *peer) send(message any) {
	select {
	case p.out <- message:
	default:
		p.ws.Close()
	}
}
func (p *peer) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case message := <-p.out:
			p.ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if p.ws.WriteJSON(message) != nil {
				p.ws.Close()
				return
			}
		}
	}
}

type liveRoom struct {
	sync.Mutex
	clients map[*peer]bool
}

func (lr *liveRoom) roster() []participant {
	members := map[string]participant{}
	for p := range lr.clients {
		m := p.member
		if prior, ok := members[m.ID]; ok && !prior.asOf.Before(m.asOf) {
			continue
		}
		if time.Since(m.asOf) > 20*time.Second {
			m.RTT = nil
		}
		members[m.ID] = m
	}
	list := make([]participant, 0, len(members))
	for _, m := range members {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}
func (lr *liveRoom) presence() {
	message := map[string]any{"type": "presence", "members": lr.roster()}
	for p := range lr.clients {
		if p.subscribed {
			p.send(message)
		}
	}
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
	var auth struct {
		Token    string
		Presence bool
	}
	if ws.ReadJSON(&auth) != nil {
		return
	}
	c := r.Context()
	var room, name string
	if a.db.QueryRow(c, "select room,nickname from members where token=$1", hash(auth.Token)).Scan(&room, &name) != nil {
		return
	}
	p := &peer{subscribed: auth.Presence, ws: ws, out: make(chan any, 32), done: make(chan struct{}), member: participant{ID: hash("presence:" + auth.Token)[:16], Name: name}}
	defer close(p.done)
	go p.writeLoop()
	a.mu.Lock()
	lr := a.rooms[room]
	if lr == nil {
		lr = &liveRoom{clients: map[*peer]bool{}}
		a.rooms[room] = lr
	}
	lr.Lock()
	lr.clients[p] = true
	a.mu.Unlock()
	var snapshot state
	e = a.db.QueryRow(c, "select state from rooms where id=$1", room).Scan(&snapshot)
	if e == nil {
		p.send(map[string]any{"type": "state", "state": snapshot, "now": time.Now().UnixMilli()})
		lr.presence()
	}
	lr.Unlock()
	defer func() {
		a.mu.Lock()
		lr.Lock()
		delete(lr.clients, p)
		if len(lr.clients) == 0 {
			delete(a.rooms, room)
		} else {
			lr.presence()
		}
		lr.Unlock()
		a.mu.Unlock()
	}()
	if e != nil {
		return
	}
	var lastProbe int64
	for {
		var m struct {
			Type, Text string
			State      state
			Probe      int64
			RTT        *int
		}
		ws.SetReadDeadline(time.Now().Add(150 * time.Second))
		if ws.ReadJSON(&m) != nil {
			return
		}
		if m.Type == "ping" {
			lastProbe = m.Probe
			p.send(map[string]any{"type": "pong", "probe": m.Probe, "now": time.Now().UnixMilli()})
			continue
		}
		lr.Lock()
		switch m.Type {
		case "latency":
			if m.Probe > 0 && m.Probe == lastProbe && m.RTT != nil && *m.RTT >= 0 && *m.RTT <= 120000 {
				first := p.member.RTT == nil
				p.member.RTT = m.RTT
				p.member.asOf = time.Now()
				lastProbe = 0
				if first {
					lr.presence()
				} else {
					p.send(map[string]any{"type": "presence", "members": lr.roster()})
				}
			}
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
	for p := range lr.clients {
		p.send(m)
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
	lr.broadcast(map[string]any{"type": "state", "state": s, "now": time.Now().UnixMilli()})
}
