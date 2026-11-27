package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed schema.sql
var schema string

type app struct {
	db                *pgxpool.Pool
	stores            map[string]storage
	primary, fallback string
	origins           map[string]bool
	mu                sync.Mutex
	rooms             map[string]*liveRoom
}
type identity struct {
	room, name, token string
	access
}
type handler func(http.ResponseWriter, *http.Request, identity) error
type fault struct {
	code int
	text string
}

func (f fault) Error() string { return f.text }
func bad(s string) error      { return fault{400, s} }
func token() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func respond(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}
func body(r *http.Request, v any) error {
	if json.NewDecoder(r.Body).Decode(v) != nil {
		return bad("请求格式错误")
	}
	return nil
}
func (a *app) wrap(auth bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		var u identity
		var e error
		if auth {
			u.token = hash(bearer(r))
			e = a.identify(r.Context(), &u)
			if e != nil {
				http.Error(w, "请重新加入房间", 401)
				return
			}
		}
		if auth {
			if e = a.touch(r.Context(), u.room); e != nil {
				http.Error(w, "房间暂不可用", 503)
				return
			}
		}
		if e = h(w, r, u); e != nil {
			var f fault
			if errors.As(e, &f) {
				http.Error(w, f.text, f.code)
			} else {
				log.Printf("%s failed (%T)", r.URL.Path, e)
				http.Error(w, "操作未完成，请重试", 500)
			}
		}
	}
}
func (a *app) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if !a.origins[origin] {
					http.Error(w, "origin", 403)
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
			}
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if a.db.Ping(r.Context()) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	r.Post("/api/v1/account/register", a.wrap(false, a.password))
	r.Post("/api/v1/account/login", a.wrap(false, a.password))
	r.Post("/api/v1/account/logout", a.wrap(false, a.logout))
	r.Post("/api/v1/account/guests", a.wrap(false, a.guestCards))
	r.Get("/api/v1/account/rooms", a.wrap(false, a.myRooms))
	r.Post("/api/v1/account/claim", a.wrap(false, a.claim))
	r.Get("/api/v1/account/me", a.wrap(false, a.accountProfile))
	r.Get("/api/v1/account/providers", a.wrap(false, a.providers))
	r.Post("/api/v1/account/oauth/start", a.wrap(false, a.socialStart))
	r.Post("/api/v1/account/oauth/finish", a.wrap(false, a.socialFinish))
	r.Get("/api/v1/rooms", a.wrap(false, a.directory))
	r.Get("/api/v1/room", a.wrap(true, a.roomInfo))
	r.Post("/api/v1/room/settings", a.wrap(true, a.settings))
	r.Get("/api/v1/room/members", a.wrap(true, a.roomMembers))
	r.Post("/api/v1/room/permission", a.wrap(true, a.permission))
	r.Post("/api/v1/rooms", a.wrap(false, a.join))
	r.Post("/api/v1/join", a.wrap(false, a.join))
	r.Post("/api/v1/uploads", a.wrap(true, a.begin))
	r.Post("/api/v1/uploads/{id}/{op}", a.wrap(true, a.upload))
	r.Post("/api/v1/assets/{id}/transcode", a.wrap(true, a.queueTranscode))
	r.Get("/api/v1/assets", a.wrap(true, a.list))
	r.Get("/api/v1/assets/{id}/play", a.wrap(true, a.play))
	r.Get("/api/v1/live", a.live)
	return r
}
func (a *app) join(w http.ResponseWriter, r *http.Request, _ identity) error {
	var p struct {
		Name, Invite, Title string
		Listed              bool
	}
	if e := body(r, &p); e != nil {
		return e
	}
	var user account
	session := bearer(r)
	if r.Header.Get("Authorization") != "" {
		var err error
		user, err = a.account(r)
		if err != nil {
			return err
		}
		if p.Name == "" {
			p.Name = user.Name
		}
	}
	if len([]rune(p.Name)) < 1 || len([]rune(p.Name)) > 40 {
		return bad("请输入 1–40 字昵称")
	}
	c := r.Context()
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	id := token()
	t := token()
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		p.Title = "观影房间"
	}
	if len([]rune(p.Title)) > 60 {
		return bad("房间名称最多 60 字")
	}
	if strings.HasSuffix(r.URL.Path, "/rooms") {
		p.Invite = token()[:16]
		_, e = tx.Exec(c, "insert into rooms(id,invite,title,listed,owner_token)values($1,$2,$3,$4,$5)", id, p.Invite, p.Title, p.Listed, hash(t))
	} else {
		e = tx.QueryRow(c, "update rooms set last_used=now() where invite=$1 returning id", p.Invite).Scan(&id)
		if e != nil {
			return bad("房间码无效")
		}
	}
	if e != nil {
		return e
	}
	if user.ID == "" {
		_, e = tx.Exec(c, "insert into members(token,room,nickname) values($1,$2,$3)", hash(t), id, p.Name)
	} else {
		var key string
		e = tx.QueryRow(c, `insert into members(token,room,nickname,account_id) values($1,$2,$3,$4)
 on conflict(room,account_id) where account_id is not null do update set account_id=excluded.account_id returning token,nickname`, hash(t), id, p.Name, user.ID).Scan(&key, &p.Name)
		if e == nil {
			t, e = roomSession(c, tx, session, key)
		}
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	return respond(w, map[string]string{"token": t, "invite": p.Invite, "room": id, "name": p.Name})
}
func (a *app) migrate(c context.Context, id, target string) error {
	dst := a.stores[target]
	if dst == nil {
		return errors.New("unknown target")
	}
	var source, k string
	var n int64
	e := a.db.QueryRow(c, "select store,object_key,bytes from assets where id=$1 and ready", id).Scan(&source, &k, &n)
	if e != nil || source == target {
		return e
	}
	var digest string
	e = a.db.QueryRow(c, "select sha256 from locations where asset=$1 and store=$2 and sha256 is not null", id, target).Scan(&digest)
	if errors.Is(e, pgx.ErrNoRows) {
		src := a.stores[source]
		if src == nil {
			return errors.New("source configuration missing")
		}
		digest, e = copyChecked(c, src, dst, k, n)
	} else if e == nil {
		// A verified retained copy permits rollback even during a source outage.
		e = verifyLocation(c, dst, k, n, digest)
	}
	if e != nil {
		return e
	}
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	res, e := tx.Exec(c, "update assets set store=$1,sha256=$2 where id=$3 and store=$4", target, digest, id, source)
	if e != nil {
		return e
	}
	if res.RowsAffected() != 1 {
		return errors.New("source changed; retry")
	}
	_, e = tx.Exec(c, "update locations set sha256=$1 where asset=$2 and store=$3", digest, id, source)
	if e != nil {
		return e
	}
	_, e = tx.Exec(c, "insert into locations values($1,$2,$3,$4,$5) on conflict(asset,store) do update set sha256=excluded.sha256", id, target, k, n, digest)
	if e != nil {
		return e
	}
	return tx.Commit(c)
}
func main() {
	c := context.Background()
	db, e := pgxpool.New(c, os.Getenv("DATABASE_URL"))
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(c, schema); e != nil {
		log.Fatal(e)
	}
	stores, e := storesFromEnv()
	if e != nil {
		log.Fatal(e)
	}
	a := &app{db: db, stores: stores, primary: os.Getenv("DEFAULT_STORE"), fallback: os.Getenv("LEGACY_STORE"), origins: map[string]bool{}, rooms: map[string]*liveRoom{}}
	if stores[a.primary] == nil {
		log.Fatal("invalid DEFAULT_STORE")
	}
	for _, v := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		a.origins[strings.TrimSpace(v)] = true
	}
	if len(os.Args) == 4 && os.Args[1] == "migrate" {
		if e = a.migrate(c, os.Args[2], os.Args[3]); e != nil {
			log.Fatal(e)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "transcode-worker" {
		if e = a.worker(c); e != nil {
			log.Fatal(e)
		}
		return
	}
	go a.maintain(c)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	s := http.Server{Addr: os.Getenv("LISTEN_HOST") + ":" + port, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(s.ListenAndServe())
}
