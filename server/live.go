package main

import (
	"context"
	"encoding/json"
	"math"
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

type playbackSample struct {
	Asset    string  `json:"asset"`
	Position float64 `json:"position"`
	Phase    string  `json:"phase"`
}
type participant struct {
	access
	Playback *playbackSample `json:"playback"`
	Name     string          `json:"name"`
	RTT      *int            `json:"rtt"`
	asOf     time.Time
}

type peer struct {
	ws         *websocket.Conn
	out        chan any
	done       chan struct{}
	member     participant
	subscribed bool
	credential string
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
	state   state
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
			m.Playback = nil
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
	credential := hash(auth.Token)
	u := identity{token: credential}
	if a.identify(c, &u) != nil {
		return
	}
	room := u.room
	p := &peer{subscribed: auth.Presence, credential: credential, ws: ws, out: make(chan any, 32), done: make(chan struct{})}
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
	u.token = credential
	e = a.identify(c, &u)
	if e == nil {
		p.member.access = u.access
		p.member.Name = u.name
		e = a.db.QueryRow(c, "select state from rooms where id=$1", room).Scan(&lr.state)
	}
	if e == nil {
		p.send(map[string]any{"type": "state", "state": lr.state, "now": time.Now().UnixMilli()})
		if p.subscribed {
			p.send(map[string]any{"type": "access", "self": u.access})
		}
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
	if a.touch(c, room) != nil {
		return
	}
	lastCheck := time.Now()
	var lastProbe int64
	var lastSample time.Time
	for {
		var m struct {
			Type, Text, Action string
			Playback           *playbackSample
			Replay             bool
			State              state
			Probe              int64
			RTT                *int
		}
		ws.SetReadDeadline(time.Now().Add(150 * time.Second))
		if ws.ReadJSON(&m) != nil {
			return
		}
		if time.Since(lastCheck) >= sessionCheckInterval {
			current := identity{token: credential}
			if a.identify(c, &current) != nil || a.touch(c, room) != nil {
				return
			}
			lastCheck = time.Now()
		}
		if m.Type == "ping" {
			lastProbe = m.Probe
			p.send(map[string]any{"type": "pong", "probe": m.Probe, "now": time.Now().UnixMilli()})
			continue
		}
		lr.Lock()
		switch m.Type {
		case "progress":
			if time.Since(lastSample) >= 300*time.Millisecond {
				lastSample = time.Now()
				p.sample(m.Playback, lr.state.Asset)
				lr.presence()
			}
		case "latency":
			if m.Probe > 0 && m.Probe == lastProbe && m.RTT != nil && *m.RTT >= 0 && *m.RTT <= 120000 {
				p.sample(m.Playback, lr.state.Asset)
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
			if !p.member.allows("chat") {
				p.denied()
				break
			}
			if len([]rune(m.Text)) > 0 && len([]rune(m.Text)) <= 200 {
				lr.broadcast(map[string]string{"type": "chat", "name": u.name, "text": m.Text})
			}
		case "control":
			if p.member.Role != "owner" && p.member.Role != "legacy" {
				p.denied()
				break
			}
			a.control(c, lr, room, p, m.State)
		case "command":
			a.command(c, lr, room, p, m.Action, m.State, m.Replay)
		}
		lr.Unlock()
	}
}
func (lr *liveRoom) broadcast(m any) {
	for p := range lr.clients {
		p.send(m)
	}
}
func (a *app) control(c context.Context, lr *liveRoom, room string, p *peer, s state) {
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
		p.send(map[string]string{"type": "error", "message": "房间状态已变化，请重试"})
		return
	}
	lr.state = s
	lr.broadcast(map[string]any{"type": "state", "state": s, "now": time.Now().UnixMilli()})
}

func (p *peer) denied() {
	p.send(map[string]string{"type": "error", "message": "房主尚未授权此操作"})
}

// Derive all unchanged fields from the authoritative room clock. A play or
// pause permission alone must not allow a client to seek or replace media.
func (a *app) command(c context.Context, lr *liveRoom, room string, p *peer, action string, intent state, replay bool) {
	s := lr.state
	if intent.Version != s.Version {
		p.send(map[string]any{"type": "state", "state": s, "now": time.Now().UnixMilli()})
		return
	}
	if s.Playing {
		s.Position += float64(max(int64(0), time.Now().UnixMilli()-s.At)) * s.Rate / 1000
	}
	if action == "seek" {
		if intent.Position >= s.Position {
			action = "forward"
		} else {
			action = "back"
		}
	}
	if !p.member.allows(action) {
		p.denied()
		return
	}
	switch action {
	case "play":
		if replay {
			if !p.member.allows("back") {
				p.denied()
				return
			}
			s.Position = 0
		}
		s.Playing = true
	case "pause":
		s.Playing = false
	case "forward", "back":
		if (action == "forward" && intent.Position < s.Position) || (action == "back" && intent.Position > s.Position) {
			p.denied()
			return
		}
		s.Position = intent.Position
	case "rate":
		s.Rate = intent.Rate
	case "select":
		if intent.Asset == s.Asset && (!p.member.allows("back") || !p.member.allows("pause")) {
			p.denied()
			return
		}
		s.Asset = intent.Asset
		s.Position = 0
		s.Playing = false
	default:
		return
	}
	s.Position = math.Min(s.Position, 604800)
	a.control(c, lr, room, p, s)
}

func (p *peer) sample(q *playbackSample, asset string) {
	p.member.Playback = nil
	if q != nil && q.Asset == asset && q.Asset != "" && q.Position >= 0 && q.Position <= 604800 && (q.Phase == "playing" || q.Phase == "paused" || q.Phase == "buffering") {
		p.member.Playback = q
	}
}

func (a *app) disconnectCredential(room, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if lr := a.rooms[room]; lr != nil {
		lr.Lock()
		defer lr.Unlock()
		for p := range lr.clients {
			if p.credential == key {
				p.ws.Close()
			}
		}
	}
}
