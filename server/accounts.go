package main

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type login struct {
	account
	Token string `json:"token"`
}

func bearer(r *http.Request) string {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return raw
}
func (a *app) account(r *http.Request) (account, error) {
	var u account
	err := a.db.QueryRow(r.Context(), "select a.id::text,a.display_name from account_sessions s join accounts a on a.id=s.account_id where s.token=$1 and s.expires_at>now()", hash(bearer(r))).Scan(&u.ID, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, fault{401, "请重新登录账号"}
	}
	return u, err
}
func newSession(c context.Context, tx pgx.Tx, u account) (login, error) {
	t := token()
	_, e := tx.Exec(c, "insert into account_sessions values($1,$2,now()+interval '30 days')", hash(t), u.ID)
	return login{u, t}, e
}

var handlePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
var dummyPassword, _ = bcrypt.GenerateFromPassword([]byte(token()), bcrypt.DefaultCost)
var passwordSlots = make(chan struct{}, 4)

func (a *app) limit(c context.Context, key string, max int) error {
	var count int
	err := a.db.QueryRow(c, `insert into auth_limits values($1,1,now()+interval '5 minutes')
 on conflict(bucket) do update set hits=case when auth_limits.until_at<=now() then 1 else least(auth_limits.hits+1,10000) end,
 until_at=case when auth_limits.until_at<=now() then excluded.until_at else auth_limits.until_at end returning hits`, hash(key)).Scan(&count)
	if err != nil {
		return err
	}
	if count > max {
		return fault{429, "尝试过于频繁，请稍后再试"}
	}
	return nil
}
func (a *app) password(w http.ResponseWriter, r *http.Request, _ identity) error {
	var p struct{ Handle, Password string }
	if e := body(r, &p); e != nil {
		return e
	}
	p.Handle = strings.ToLower(strings.TrimSpace(p.Handle))
	if !handlePattern.MatchString(p.Handle) || len(p.Password) < 10 || len(p.Password) > 72 {
		return bad("账号需为 3–32 位字母、数字或下划线，密码需为 10–72 字节")
	}
	c := r.Context()
	if e := a.limit(c, "password:"+p.Handle, 12); e != nil {
		return e
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return fault{429, "请稍后再试"}
	}
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	var u account
	if strings.HasSuffix(r.URL.Path, "/register") {
		digest, e := bcrypt.GenerateFromPassword([]byte(p.Password), bcrypt.DefaultCost)
		if e != nil {
			return e
		}
		e = tx.QueryRow(c, "insert into accounts(handle,password_hash,display_name) values($1,$2,$1) on conflict(handle) do nothing returning id::text,display_name", p.Handle, digest).Scan(&u.ID, &u.Name)
		if errors.Is(e, pgx.ErrNoRows) {
			return fault{409, "此账号名称已被使用"}
		}
		if e != nil {
			return e
		}
	} else {
		var digest []byte
		e = tx.QueryRow(c, "select id::text,display_name,password_hash from accounts where handle=$1", p.Handle).Scan(&u.ID, &u.Name, &digest)
		if errors.Is(e, pgx.ErrNoRows) {
			digest = dummyPassword
		} else if e != nil {
			return e
		}
		if bcrypt.CompareHashAndPassword(digest, []byte(p.Password)) != nil || u.ID == "" {
			return fault{401, "账号或密码有误"}
		}
	}
	session, e := newSession(c, tx, u)
	if e != nil {
		return e
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	return respond(w, session)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request, _ identity) error {
	c := r.Context()
	key := hash(bearer(r))
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	rows, e := tx.Query(c, "select m.room,rs.token from room_sessions rs join members m on m.token=rs.member_key where rs.session_key=$1", key)
	if e != nil {
		return e
	}
	type active struct{ Room, Key string }
	sockets, e := pgx.CollectRows(rows, pgx.RowToStructByPos[active])
	if e != nil {
		return e
	}
	if _, e = tx.Exec(c, "delete from account_sessions where token=$1", key); e != nil {
		return e
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	for _, p := range sockets {
		a.disconnectCredential(p.Room, p.Key)
	}
	return respond(w, true)
}
func (a *app) myRooms(w http.ResponseWriter, r *http.Request, _ identity) error {
	u, e := a.account(r)
	if e != nil {
		return e
	}
	rows, e := a.db.Query(r.Context(), "select r.title,r.invite,coalesce(r.owner_token=m.token,false),m.nickname from members m join rooms r on r.id=m.room where m.account_id=$1 order by r.last_used desc,r.id", u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	type card struct {
		roomCard
		Owner bool   `json:"owner"`
		Name  string `json:"name"`
	}
	list := []card{}
	for rows.Next() {
		var v card
		if e = rows.Scan(&v.Title, &v.Invite, &v.Owner, &v.Name); e != nil {
			return e
		}
		list = append(list, v)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return respond(w, list)
}
func (a *app) claim(w http.ResponseWriter, r *http.Request, _ identity) error {
	u, e := a.account(r)
	if e != nil {
		return e
	}
	var p struct{ Guest string }
	if e = body(r, &p); e != nil {
		return e
	}
	c := r.Context()
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	var room string
	key := hash(p.Guest)
	e = tx.QueryRow(c, "select r.id from rooms r join members m on m.room=r.id where m.token=$1 and (m.account_id is null or m.account_id=$2) for update of r", key, u.ID).Scan(&room)
	if errors.Is(e, pgx.ErrNoRows) {
		return fault{403, "本机身份已失效或已绑定其他账号"}
	}
	if e != nil {
		return e
	}
	var other, otherID string
	e = tx.QueryRow(c, "select token,public_id::text from members where room=$1 and account_id=$2 and token<>$3 for update", room, u.ID, key).Scan(&other, &otherID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if other != "" {
		// Preserve the account membership and its live session keys on other devices.
		// An owner credential can upgrade it; a prior grant can populate a fresh membership.
		res, err := tx.Exec(c, `update members dest set permissions=case when r.owner_token=dest.token then dest.permissions else src.permissions end
   from members src,rooms r where src.token=$1 and src.account_id is null and dest.token=$2 and r.id=src.room
   and (r.owner_token=src.token or r.owner_token=dest.token or dest.permissions='{}')`, key, other)
		if err != nil {
			return err
		}
		if res.RowsAffected() != 1 {
			return fault{409, "此账号已有独立授权，请使用已有房间身份"}
		}
		if _, e = tx.Exec(c, "update rooms set owner_token=$1 where id=$2 and owner_token=$3", other, room, key); e != nil {
			return e
		}
		if _, e = tx.Exec(c, "update assets set owner_token=$1 where owner_token=$2", other, key); e != nil {
			return e
		}
		if _, e = tx.Exec(c, "delete from members where token=$1 and account_id is null", key); e != nil {
			return e
		}
	} else {
		res, e := tx.Exec(c, "update members set account_id=$1 where token=$2 and (account_id is null or account_id=$1)", u.ID, key)
		if e != nil {
			return e
		}
		if res.RowsAffected() != 1 {
			return forbidden()
		}
	}
	if _, e = tx.Exec(c, "update rooms set last_used=now() where id=$1", room); e != nil {
		return e
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	a.disconnectCredential(room, key)
	if otherID != "" {
		a.mu.Lock()
		if lr := a.rooms[room]; lr != nil {
			lr.Lock()
			for p := range lr.clients {
				if p.member.ID == otherID {
					p.ws.Close()
				}
			}
			lr.Unlock()
		}
		a.mu.Unlock()
	}
	return respond(w, true)
}

// Every device receives a room-scoped bearer. The durable member key never leaves the database.
func roomSession(c context.Context, tx pgx.Tx, session, member string) (string, error) {
	// Session and membership keys are already high-entropy and session is supplied only by its holder.
	raw := hash("room-access:" + session + ":" + member)
	_, e := tx.Exec(c, "insert into room_sessions(token,member_key,session_key) values($1,$2,$3) on conflict(member_key,session_key) do nothing", hash(raw), member, hash(session))
	return raw, e
}
func (a *app) touch(c context.Context, room string) error {
	_, e := a.db.Exec(c, "update rooms set last_used=now() where id=$1 and last_used<now()-interval '1 minute'", room)
	return e
}

const sessionCheckInterval = time.Minute

func (a *app) guestCards(w http.ResponseWriter, r *http.Request, _ identity) error {
	var in struct{ Tokens []string }
	if e := body(r, &in); e != nil {
		return e
	}
	if len(in.Tokens) > 100 {
		return bad("请求房间过多")
	}
	keys := []string{}
	for _, t := range in.Tokens {
		keys = append(keys, hash(t))
	}
	rows, e := a.db.Query(r.Context(), "select r.title,r.invite from members m join rooms r on m.room=r.id where m.account_id is null and m.token=any($1)", keys)
	if e != nil {
		return e
	}
	defer rows.Close()
	cards, e := pgx.CollectRows(rows, pgx.RowToStructByPos[roomCard])
	if e != nil {
		return e
	}
	return respond(w, cards)
}
