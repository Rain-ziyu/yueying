package main

import (
	"context"
	"net/http"
	"strings"
)

var capabilities = map[string]bool{"play": true, "pause": true, "forward": true, "back": true, "chat": true, "upload": true, "select": true, "rate": true}

type access struct {
	ID          string          `json:"id"`
	Role        string          `json:"role"`
	Permissions map[string]bool `json:"permissions"`
}

func (u access) allows(action string) bool {
	return capabilities[action] && (u.Role == "owner" || u.Role == "legacy" || u.Permissions[action])
}
func forbidden() error { return fault{403, "房主尚未授权此操作"} }

const roleSQL = "case when r.owner_token=m.token then 'owner' when r.owner_token is null then 'legacy' else 'member' end"

func (a *app) identify(c context.Context, u *identity) error {
	return a.db.QueryRow(c, `select m.token,m.room,m.nickname,m.public_id::text,`+roleSQL+`,m.permissions from members m join rooms r on r.id=m.room
 where (m.token=$1 and m.account_id is null) or m.token=(select rs.member_key from room_sessions rs join account_sessions s on s.token=rs.session_key join members bound on bound.token=rs.member_key and bound.account_id=s.account_id where rs.token=$1 and s.expires_at>now())`, u.token).Scan(&u.token, &u.room, &u.name, &u.ID, &u.Role, &u.Permissions)
}

type roomCard struct {
	Title  string `json:"title"`
	Invite string `json:"invite"`
}

func (a *app) directory(w http.ResponseWriter, r *http.Request, _ identity) error {
	params := r.URL.Query()
	query := strings.TrimSpace(params.Get("q"))
	rows, e := a.db.Query(r.Context(), `select title,invite from rooms
		where listed and invite > $1 and
		($2::text = '' or strpos(lower(title),lower($2)) > 0 or strpos(lower(invite),lower($2)) > 0)
		order by invite limit 51`, params.Get("after"), query)
	if e != nil {
		return e
	}
	defer rows.Close()
	rooms := []roomCard{}
	for rows.Next() {
		var card roomCard
		if e = rows.Scan(&card.Title, &card.Invite); e != nil {
			return e
		}
		rooms = append(rooms, card)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	more := len(rooms) > 50
	if more {
		rooms = rooms[:50]
	}
	w.Header().Set("Cache-Control", "no-store")
	return respond(w, map[string]any{"rooms": rooms, "more": more})
}
func (a *app) roomInfo(w http.ResponseWriter, r *http.Request, u identity) error {
	var title string
	var listed bool
	if e := a.db.QueryRow(r.Context(), "select title,listed from rooms where id=$1", u.room).Scan(&title, &listed); e != nil {
		return e
	}
	w.Header().Set("Cache-Control", "no-store")
	return respond(w, map[string]any{"self": u.access, "title": title, "listed": listed})
}
func (a *app) settings(w http.ResponseWriter, r *http.Request, u identity) error {
	if u.Role != "owner" {
		return forbidden()
	}
	var p struct {
		Title  string
		Listed bool
	}
	if e := body(r, &p); e != nil {
		return e
	}
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len([]rune(p.Title)) > 60 {
		return bad("房间名称为 1–60 字")
	}
	if _, e := a.db.Exec(r.Context(), "update rooms set title=$1,listed=$2 where id=$3 and owner_token=$4", p.Title, p.Listed, u.room, u.token); e != nil {
		return e
	}
	a.mu.Lock()
	if lr := a.rooms[u.room]; lr != nil {
		lr.Lock()
		lr.broadcast(map[string]string{"type": "settings"})
		lr.Unlock()
	}
	a.mu.Unlock()
	return respond(w, true)
}

type memberDetails struct {
	access
	Name string `json:"name"`
}

func (a *app) roomMembers(w http.ResponseWriter, r *http.Request, u identity) error {
	if u.Role != "owner" {
		return forbidden()
	}
	rows, e := a.db.Query(r.Context(), "select public_id::text,nickname,permissions from members where room=$1 and token<>$2 order by nickname,public_id", u.room, u.token)
	if e != nil {
		return e
	}
	defer rows.Close()
	list := []memberDetails{}
	for rows.Next() {
		m := memberDetails{}
		m.Role = "member"
		if e = rows.Scan(&m.ID, &m.Name, &m.Permissions); e != nil {
			return e
		}
		list = append(list, m)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	w.Header().Set("Cache-Control", "no-store")
	return respond(w, list)
}
func (a *app) permission(w http.ResponseWriter, r *http.Request, u identity) error {
	if u.Role != "owner" {
		return forbidden()
	}
	var p struct {
		ID, Action string
		Allow      *bool
	}
	if e := body(r, &p); e != nil {
		return e
	}
	if !capabilities[p.Action] || p.Allow == nil {
		return bad("未知权限")
	}
	// Serialize permission changes with joins and commands in this room.
	a.mu.Lock()
	lr := a.rooms[u.room]
	if lr != nil {
		lr.Lock()
		a.mu.Unlock()
		defer lr.Unlock()
	} else {
		defer a.mu.Unlock()
	}
	var perms map[string]bool
	err := a.db.QueryRow(r.Context(), "update members set permissions=jsonb_set(permissions,array[$1::text],to_jsonb($2::boolean)) where room=$3 and public_id::text=$4 and token<>$5 returning permissions", p.Action, *p.Allow, u.room, p.ID, u.token).Scan(&perms)
	if err != nil {
		return fault{400, "房间成员不可用"}
	}
	if lr != nil {
		for client := range lr.clients {
			if client.member.ID == p.ID {
				client.member.Permissions = perms
				client.send(map[string]any{"type": "access", "self": client.member.access})
			}
		}
	}
	return respond(w, perms)
}
