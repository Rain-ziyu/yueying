package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/qiniu/go-sdk/v7/auth"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Kodo uses its native resumable upload API for clients. The server uses the
// bucket's S3-compatible endpoint for stat, server-side copies and checksums.
type kodo struct {
	s3
	cfg    storeConfig
	signer *auth.Credentials
}

func newKodo(s s3, c storeConfig) *kodo { return &kodo{s, c, auth.New(c.AccessKey, c.SecretKey)} }
func (k *kodo) protocolName() string    { return "kodo-multipart-v2" }
func (k *kodo) path(key string) string {
	return strings.TrimRight(k.cfg.UploadHost, "/") + "/buckets/" + url.PathEscape(k.bucket) + "/objects/" + base64.URLEncoding.EncodeToString([]byte(key)) + "/uploads"
}
func (k *kodo) authorization(key string) string {
	data, _ := json.Marshal(map[string]any{"scope": k.bucket + ":" + key, "deadline": time.Now().Add(15 * time.Minute).Unix(), "insertOnly": 1, "fsizeLimit": int64(8) << 30})
	return "UpToken " + k.signer.SignWithData(data)
}
func (k *kodo) request(c context.Context, key, method, path string, input, result any) error {
	var data io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(c, method, path, data)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", k.authorization(key))
	req.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Kodo %s returned HTTP %d", method, resp.StatusCode)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result)
}
func (k *kodo) begin(c context.Context, key string) (string, error) {
	var v struct{ UploadId string }
	e := k.request(c, key, "POST", k.path(key), nil, &v)
	if e == nil && v.UploadId == "" {
		return "", fmt.Errorf("Kodo omitted uploadId")
	}
	return v.UploadId, e
}
func (k *kodo) part(c context.Context, key, id string, n int) (destination, error) {
	return destination{URL: k.path(key) + "/" + url.PathEscape(id) + "/" + strconv.Itoa(n), Headers: map[string]string{"Authorization": k.authorization(key), "Content-Type": "application/octet-stream"}}, nil
}
func (k *kodo) parts(c context.Context, key, id string) ([]uploadPart, error) {
	var v struct {
		Parts            []uploadPart
		PartNumberMarker int
	}
	e := k.request(c, key, "GET", k.path(key)+"/"+url.PathEscape(id)+"?max-parts=1000", nil, &v)
	// 8 GiB / 16 MiB = 512 parts, so accepted uploads always fit one page.
	if e == nil && v.PartNumberMarker != 0 {
		return nil, fmt.Errorf("too many parts")
	}
	if v.Parts == nil {
		v.Parts = []uploadPart{}
	}
	return v.Parts, e
}
func (k *kodo) finish(c context.Context, key, id string, parts []uploadPart) error {
	items := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		items = append(items, map[string]any{"partNumber": p.PartNumber, "etag": p.ETag})
	}
	return k.request(c, key, "POST", k.path(key)+"/"+url.PathEscape(id), map[string]any{"parts": items}, nil)
}
func (k *kodo) abort(c context.Context, key, id string) error {
	return k.request(c, key, "DELETE", k.path(key)+"/"+url.PathEscape(id), nil, nil)
}
func (k *kodo) readURL(c context.Context, key string) (string, error) {
	u := strings.TrimRight(k.cfg.DownloadHost, "/") + "/" + key + "?e=" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	return u + "&token=" + k.signer.Sign([]byte(u)), nil
}
