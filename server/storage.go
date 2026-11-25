package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const partSize int64 = 16 << 20

type storeConfig struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey string
	LocalHTTP, VirtualHost                         bool
	Provider, UploadHost, DownloadHost             string
}
type uploadPart struct {
	PartNumber int
	ETag       string
}
type destination struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}
type storage interface {
	protocolName() string
	begin(context.Context, string) (string, error)
	part(context.Context, string, string, int) (destination, error)
	parts(context.Context, string, string) ([]uploadPart, error)
	finish(context.Context, string, string, []uploadPart) error
	abort(context.Context, string, string) error
	size(context.Context, string) (int64, error)
	readURL(context.Context, string) (string, error)
	open(context.Context, string) (io.ReadCloser, error)
	put(context.Context, string, io.Reader, int64) error
}
type s3 struct {
	c      *minio.Core
	bucket string
}

func storesFromEnv() (map[string]storage, error) {
	var configs map[string]storeConfig
	if err := json.Unmarshal([]byte(os.Getenv("STORES_JSON")), &configs); err != nil {
		return nil, err
	}
	out := map[string]storage{}
	for id, v := range configs {
		lookup := minio.BucketLookupPath
		if v.VirtualHost || v.Provider == "qiniu" {
			lookup = minio.BucketLookupDNS
		}
		c, e := minio.NewCore(v.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(v.AccessKey, v.SecretKey, ""), Secure: !v.LocalHTTP, Region: v.Region, BucketLookup: lookup})
		if e != nil {
			return nil, e
		}
		base := s3{c, v.Bucket}
		switch v.Provider {
		case "", "s3", "r2":
			out[id] = base
		case "qiniu":
			if !strings.HasPrefix(v.UploadHost, "https://") || !strings.HasPrefix(v.DownloadHost, "https://") {
				return nil, errors.New("qiniu requires HTTPS upload and private download hosts")
			}
			out[id] = newKodo(base, v)
		default:
			return nil, fmt.Errorf("unknown provider %s", v.Provider)
		}
	}
	return out, nil
}
func (s s3) protocolName() string { return protocol }
func (s s3) begin(c context.Context, k string) (string, error) {
	return s.c.NewMultipartUpload(c, s.bucket, k, minio.PutObjectOptions{ContentType: "video/mp4"})
}
func (s s3) part(c context.Context, k, id string, n int) (destination, error) {
	u, e := s.c.Presign(c, "PUT", s.bucket, k, 15*time.Minute, url.Values{"uploadId": {id}, "partNumber": {strconv.Itoa(n)}})
	if e != nil {
		return destination{}, e
	}
	return destination{URL: u.String()}, nil
}
func (s s3) parts(c context.Context, k, id string) ([]uploadPart, error) {
	result := []uploadPart{}
	marker := 0
	for {
		v, e := s.c.ListObjectParts(c, s.bucket, k, id, marker, 1000)
		if e != nil {
			return nil, e
		}
		for _, p := range v.ObjectParts {
			result = append(result, uploadPart{PartNumber: p.PartNumber, ETag: p.ETag})
		}
		if !v.IsTruncated {
			return result, nil
		}
		marker = v.NextPartNumberMarker
	}
}
func (s s3) finish(c context.Context, k, id string, parts []uploadPart) error {
	p := make([]minio.CompletePart, len(parts))
	for i, v := range parts {
		p[i] = minio.CompletePart{PartNumber: v.PartNumber, ETag: v.ETag}
	}
	_, e := s.c.CompleteMultipartUpload(c, s.bucket, k, id, p, minio.PutObjectOptions{})
	return e
}
func (s s3) abort(c context.Context, k, id string) error {
	return s.c.AbortMultipartUpload(c, s.bucket, k, id)
}
func (s s3) size(c context.Context, k string) (int64, error) {
	v, e := s.c.StatObject(c, s.bucket, k, minio.StatObjectOptions{})
	return v.Size, e
}
func (s s3) readURL(c context.Context, k string) (string, error) {
	u, e := s.c.PresignedGetObject(c, s.bucket, k, time.Hour, nil)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}
func (s s3) open(c context.Context, k string) (io.ReadCloser, error) {
	return s.c.Client.GetObject(c, s.bucket, k, minio.GetObjectOptions{})
}
func (s s3) put(c context.Context, k string, r io.Reader, n int64) error {
	_, e := s.c.Client.PutObject(c, s.bucket, k, r, n, minio.PutObjectOptions{ContentType: "video/mp4"})
	return e
}
func copyChecked(c context.Context, src, dst storage, k string, n int64) (string, error) {
	r, e := src.open(c, k)
	if e != nil {
		return "", e
	}
	defer r.Close()
	sum := sha256.New()
	if e = dst.put(c, k, io.TeeReader(r, sum), n); e != nil {
		return "", e
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	return digest, verifyLocation(c, dst, k, n, digest)
}
func verifyLocation(c context.Context, dst storage, k string, n int64, digest string) error {
	if e := verifyFile(c, dst, k, n, digest); e != nil {
		return e
	}
	u, e := dst.readURL(c, k)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(c, "GET", u, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-0/%d", n) {
		return errors.New("range validation failed")
	}
	return nil
}
