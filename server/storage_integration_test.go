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
		req.Header.Set("Authorization", "Bearer "+tok)
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
		w.WriteJSON(map[string]string{"token": tok})
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
}
