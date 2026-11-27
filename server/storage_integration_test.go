package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStorageSwitch(t *testing.T) {
	dsn, host := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_S3_ENDPOINT")
	if dsn == "" || host == "" {
		t.Skip("requires disposable PostgreSQL and S3 endpoint")
	}
	c := context.Background()
	db, e := pgxpool.New(c, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(c, schema); e != nil {
		t.Fatal(e)
	}
	configs := map[string]storeConfig{}
	for _, id := range []string{"first", "second", "missing"} {
		configs[id] = storeConfig{Endpoint: host, Region: "us-east-1", AccessKey: "test", SecretKey: "test", Bucket: id + token()[:10], LocalHTTP: true}
	}
	conf, _ := json.Marshal(configs)
	t.Setenv("STORES_JSON", string(conf))
	stores, e := storesFromEnv()
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"first", "second"} {
		s := stores[id].(s3)
		if e = s.c.MakeBucket(c, s.bucket, minio.MakeBucketOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	a := app{db: db, stores: stores, primary: "first", fallback: "first", rooms: map[string]*liveRoom{}}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	call := func(path, tok string, p any, status int) map[string]any {
		t.Helper()
		m := "GET"
		var body io.Reader
		if p != nil {
			m = "POST"
			b, _ := json.Marshal(p)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(m, srv.URL+"/api/v1/"+path, body)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s status %d %s", path, res.StatusCode, b)
		}
		var out map[string]any
		json.Unmarshal(b, &out)
		return out
	}
	room := call("rooms", "", map[string]string{"name": "房主"}, 200)
	owner := room["token"].(string)
	peer := call("join", "", map[string]any{"name": "朋友", "invite": room["invite"]}, 200)["token"].(string)
	other := call("rooms", "", map[string]string{"name": "另一房间"}, 200)["token"].(string)
	guestInfo := call("room", peer, nil, 200)["self"].(map[string]any)
	if guestInfo["role"] != "member" || call("room", owner, nil, 200)["self"].(map[string]any)["role"] != "owner" {
		t.Fatal("unexpected room roles")
	}
	grant := map[string]any{"id": guestInfo["id"], "action": "upload", "allow": true}
	call("room/permission", peer, grant, 403)
	call("room/permission", other, grant, 400)
	call("uploads", peer, map[string]any{}, 403)
	call("room/permission", owner, grant, 200)
	if !call("room", peer, nil, 200)["self"].(map[string]any)["permissions"].(map[string]any)["upload"].(bool) {
		t.Fatal("upload grant not persisted")
	}
	grant["allow"] = false
	call("room/permission", owner, grant, 200)
	call("uploads", peer, map[string]any{}, 403)
	props := map[string]any{"title": "测试放映厅", "listed": true}
	call("room/settings", peer, props, 403)
	call("room/settings", owner, props, 200)
	appears := func() bool {
		for _, item := range call("rooms", "", nil, 200)["rooms"].([]any) {
			if item.(map[string]any)["invite"] == room["invite"] {
				return true
			}
		}
		return false
	}
	if !appears() {
		t.Fatal("public room missing")
	}
	props["listed"] = false
	call("room/settings", owner, props, 200)
	if appears() {
		t.Fatal("private room listed")
	}
	create := func() string {
		return call("uploads", owner, map[string]any{"name": "movie.mp4", "bytes": 6, "protocols": []string{protocol}}, 200)["id"].(string)
	}
	id := create()
	// The default provider can have a protocol the installed client does not speak.
	a.stores["native"] = newKodo(stores["second"].(s3), storeConfig{})
	a.primary = "native"
	legacy := create()
	var legacyStore string
	if err := db.QueryRow(c, "select store from assets where id=$1", legacy).Scan(&legacyStore); err != nil || legacyStore != "first" {
		t.Fatal("legacy protocol fallback", err)
	}
	call("uploads/"+legacy+"/cancel", owner, map[string]any{}, 200)
	a.primary = "second"
	id2 := create()
	put := func(id, store string) {
		t.Helper()
		p := "uploads/" + id + "/"
		d := call(p+"part", owner, map[string]int{"number": 1}, 200)
		url := d["url"].(string)
		if !strings.Contains(url, configs[store].Bucket) {
			t.Fatal("wrong upload target")
		}
		req, _ := http.NewRequest("PUT", url, strings.NewReader("sample"))
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status)
		}
		call(p+"parts", owner, struct{}{}, 200)
		call(p+"complete", owner, struct{}{}, 200)
		call(p+"complete", owner, struct{}{}, 200)
	}
	put(id, "first")
	put(id2, "second")
	play := "assets/" + id + "/play"
	old := call(play, peer, nil, 200)["url"].(string)
	call(play, other, nil, 404)
	if e = a.migrate(c, id, "missing"); e == nil {
		t.Fatal("missing bucket migration succeeded")
	}
	if !strings.Contains(call(play, peer, nil, 200)["url"].(string), configs["first"].Bucket) {
		t.Fatal("failed migration changed read target")
	}
	if e = a.migrate(c, id, "second"); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(call(play, peer, nil, 200)["url"].(string), configs["second"].Bucket) {
		t.Fatal("cutover failed")
	}
	resp, e := http.Get(old)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "sample" {
		t.Fatal("retained URL expired early")
	}
	// Rollback must work while the active provider is unavailable.
	saved := a.stores["second"]
	delete(a.stores, "second")
	if e = a.migrate(c, id, "first"); e != nil {
		t.Fatal(e)
	}
	a.stores["second"] = saved
	dial := func(tok string) *websocket.Conn {
		w, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/live", nil)
		if e != nil {
			t.Fatal(e)
		}
		w.SetReadDeadline(time.Now().Add(5 * time.Second))
		w.WriteJSON(map[string]any{"token": tok, "bufferSync": true})
		if _, _, e = w.ReadMessage(); e != nil {
			t.Fatal(e)
		}
		return w
	}
	w1, w2 := dial(owner), dial(peer)
	defer w1.Close()
	defer w2.Close()
	w1.WriteJSON(map[string]any{"type": "control", "state": state{Asset: id, Rate: 1.5, Position: 8}})
	var event struct {
		Type  string
		State state
		Text  string
	}
	if e = w2.ReadJSON(&event); e != nil || event.State.Position != 8 || event.State.Version != 1 {
		t.Fatalf("sync: %+v %v", event, e)
	}
	w1.WriteJSON(map[string]string{"type": "chat", "text": "暂停弹幕"})
	if e = w2.ReadJSON(&event); e != nil || event.Text != "暂停弹幕" {
		t.Fatal("chat", e)
	}
	read := func(kind string) state {
		t.Helper()
		for {
			if err := w2.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			if event.Type == kind {
				return event.State
			}
		}
	}
	command := func(action string, version int64, pos float64) {
		w1.WriteJSON(map[string]any{"type": "command", "action": action, "state": state{Version: version, Position: pos}})
	}
	ready := func(w *websocket.Conn, v int64) {
		w.WriteJSON(map[string]any{"type": "ready", "state": state{Version: v}})
	}
	command("play", 1, 8)
	start := read("state")
	read("bufferWait")
	ready(w1, start.Version)
	read("bufferWait")
	ready(w2, start.Version)
	running := read("state")
	if !running.Playing {
		t.Fatal("did not resume")
	}
	command("seek", running.Version, 55)
	held := read("state")
	if held.Playing || held.Position != 55 {
		t.Fatal("seek did not freeze", held)
	}
	read("bufferWait")
	ready(w1, held.Version)
	read("bufferWait")
	ready(w2, start.Version)
	w2.WriteJSON(map[string]any{"type": "ping"})
	read("pong")
	var persisted state
	if err := db.QueryRow(c, "select state from rooms where id=$1", room["room"]).Scan(&persisted); err != nil || persisted.Playing || persisted.Version != held.Version {
		t.Fatal("slow member was skipped", persisted, err)
	}
	ready(w2, held.Version)
	resumed := read("state")
	if !resumed.Playing || resumed.Position != 55 {
		t.Fatal("resume target", resumed)
	}
	// An account claims the pre-existing member and keeps its grants across sessions.
	user := map[string]string{"handle": "test_" + token()[:12], "password": token()}
	first := call("account/register", "", user, 200)["token"].(string)
	call("account/claim", first, map[string]string{"guest": peer}, 200)
	call("room", peer, nil, 401)
	second := call("account/login", "", user, 200)["token"].(string)
	joinData := map[string]any{"invite": room["invite"]}
	sessionRoom := call("join", second, joinData, 200)["token"].(string)
	if call("room", sessionRoom, nil, 200)["self"].(map[string]any)["id"] != guestInfo["id"] {
		t.Fatal("cross-device membership changed")
	}
	grant["allow"] = true
	call("room/permission", owner, grant, 200)
	if !call("room", sessionRoom, nil, 200)["self"].(map[string]any)["permissions"].(map[string]any)["upload"].(bool) {
		t.Fatal("account member grant lost")
	}
	call("account/logout", second, struct{}{}, 200)
	call("room", sessionRoom, nil, 401)
	protected := call("rooms", first, map[string]string{"title": "长期保留"}, 200)
	_, e = db.Exec(c, "update rooms set last_used=now()-interval '31 days' where id=$1 or id=$2", room["room"], protected["room"])
	if e != nil {
		t.Fatal(e)
	}
	w1.Close()
	w2.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		n := len(a.rooms)
		a.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("peers not disconnected")
		}
		time.Sleep(time.Millisecond * 10)
	}
	if more, err := a.expireRoom(c); !more || err != nil {
		t.Fatalf("expire: %v %v", more, err)
	}
	call("room", owner, nil, 401)
	call("room", protected["token"].(string), nil, 200)
	for {
		more, err := a.deleteMedia(c)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for _, dst := range []string{"first", "second"} {
		if _, err := a.stores[dst].size(c, "rooms/"+room["room"].(string)+"/"+id); err == nil {
			t.Fatal("retained media copy after expiry", dst)
		}
	}

}
