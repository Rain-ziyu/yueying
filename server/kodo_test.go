package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKodoMultipartContract(t *testing.T) {
	var methods []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "UpToken test:") {
			t.Error("unsigned request")
		}
		if !strings.HasPrefix(r.URL.Path, "/buckets/media/objects/YS9i/uploads") {
			t.Error(r.URL.Path)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "uploads"):
			io.WriteString(w, `{"uploadId":"session"}`)
		case r.Method == "GET":
			io.WriteString(w, `{"parts":[{"partNumber":1,"etag":"tag"}]}`)
		case r.Method == "POST":
			var v struct{ Parts []map[string]any }
			if e := json.NewDecoder(r.Body).Decode(&v); e != nil || len(v.Parts) != 1 || v.Parts[0]["etag"] != "tag" {
				t.Error("wrong completion payload")
			}
		}
	}))
	defer endpoint.Close()
	q := newKodo(s3{bucket: "media"}, storeConfig{Bucket: "media", AccessKey: "test", SecretKey: "test-secret", UploadHost: endpoint.URL, DownloadHost: "https://media.example"})
	c := context.Background()
	id, e := q.begin(c, "a/b")
	if e != nil || id != "session" {
		t.Fatal(id, e)
	}
	part, e := q.part(c, "a/b", id, 1)
	if e != nil || !strings.HasSuffix(part.URL, "/session/1") || part.Headers["Authorization"] == "" {
		t.Fatal("invalid client instruction")
	}
	parts, e := q.parts(c, "a/b", id)
	if e != nil || len(parts) != 1 || parts[0].PartNumber != 1 {
		t.Fatal(parts, e)
	}
	if e = q.finish(c, "a/b", id, parts); e != nil {
		t.Fatal(e)
	}
	if e = q.abort(c, "a/b", id); e != nil {
		t.Fatal(e)
	}
	u, e := q.readURL(c, "a/b")
	if e != nil || !strings.HasPrefix(u, "https://media.example/a/b?e=") || !strings.Contains(u, "&token=test:") {
		t.Fatal("private playback URL")
	}
	if strings.Join(methods, ",") != "POST,GET,POST,DELETE" {
		t.Fatal(methods)
	}
}
