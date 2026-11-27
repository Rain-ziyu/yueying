package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
)

type exchangeTransport func(*http.Request) (*http.Response, error)

func (t exchangeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func TestSocialSubject(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("LOGIN_CALLBACK_URL", "https://watch.example.test/")
	t.Setenv("ALIPAY_PRIVATE_KEY", base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)))
	t.Setenv("ALIPAY_PUBLIC_KEY", base64.StdEncoding.EncodeToString(pub))
	previousTransport, previousKeys := socialClient.Transport, huaweiKeys
	defer func() { socialClient.Transport = previousTransport; huaweiKeys = previousKeys }()
	huaweiKeys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), socialClient), "https://oauth-login.cloud.huawei.com/oauth2/v3/certs")
	sign := func(s string) []byte {
		sum := sha256.Sum256([]byte(s))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	b64 := base64.RawURLEncoding.EncodeToString
	proof := strings.Repeat("a", 64)
	avatarURL := "https://upfile-drcn.platform.hicloud.com/avatar.jpg"
	profileCalls := 0
	fixtureSubject := "test-" + token()
	socialClient.Transport = exchangeTransport(func(r *http.Request) (*http.Response, error) {
		var result any
		switch r.URL.Host {
		case "upfile-drcn.platform.hicloud.com":
			var img bytes.Buffer
			if err := jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(&img)}, nil
		case "account.cloud.huawei.com":
			profileCalls++
			if r.ParseForm() != nil || r.Form.Get("getNickName") != "1" || r.Form.Get("access_token") != "fixture" {
				t.Fatal("missing Huawei profile request parameters")
			}
			result = map[string]string{"openID": fixtureSubject, "displayName": "月下", "headPictureURL": avatarURL}
		case "api.weixin.qq.com", "graph.qq.com":
			if r.URL.Query().Get("code") != "code" {
				t.Fatal("missing code")
			}
			result = map[string]string{"access_token": "fixture", "openid": "subject"}
		case "openapi.alipay.com":
			if r.ParseForm() != nil || r.Form.Get("method") != "alipay.system.oauth.token" || r.Form.Get("sign") == "" {
				t.Fatal("missing signed request")
			}
			payload := `{"access_token":"fixture","open_id":"subject"}`
			result = map[string]any{"alipay_system_oauth_token_response": json.RawMessage(payload), "sign": base64.StdEncoding.EncodeToString(sign(payload))}
		case "oauth-login.cloud.huawei.com":
			if strings.HasSuffix(r.URL.Path, "/certs") {
				result = map[string]any{"keys": []map[string]string{{"kid": "test", "alg": "RS256", "kty": "RSA", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}}
			} else {
				if r.ParseForm() != nil || r.Form.Get("code") != "code" {
					t.Fatal("missing form code")
				}
				if r.Form.Get("code_verifier") != proof {
					return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))}, nil
				}
				body := fmt.Sprintf(`{"iss":"https://accounts.huawei.com","aud":"client","sub":"%s","exp":%d}`, fixtureSubject, time.Now().Add(time.Minute).Unix())
				jwt := b64([]byte(`{"alg":"RS256","kid":"test"}`)) + "." + b64([]byte(body))
				result = map[string]string{"access_token": "fixture", "id_token": jwt + "." + b64(sign(jwt))}
			}
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Host)
		}
		raw, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	for _, p := range platforms {
		t.Run(p.key, func(t *testing.T) {
			t.Setenv(strings.ToUpper(p.key)+"_APP_ID", "client")
			t.Setenv(strings.ToUpper(p.key)+"_APP_SECRET", "secret")
			if !p.enabled() {
				t.Fatal("provider disabled")
			}
			got, err := p.identify(context.Background(), "code", proof)
			want := "subject"
			if p.key == "huawei" {
				want = fixtureSubject
			}
			if err != nil || got.Subject != want {
				t.Fatalf("subject=%q err=%v", got.Subject, err)
			}
			if p.key == "huawei" {
				got = huaweiProfile(context.Background(), got.Subject, got.accessToken)
				if got.Name != "月下" || got.Avatar != avatarURL || !strings.Contains(p.scope, "profile") {
					t.Fatal("missing Huawei profile")
				}
				if _, err = p.identify(context.Background(), "code", "different"); err == nil {
					t.Fatal("wrong PKCE verifier accepted")
				}
				t.Run("profile survives next login", func(t *testing.T) {
					before := profileCalls
					testOwnedProfile(t, proof)
					if profileCalls != before+1 {
						t.Fatal("provider profile must be fetched only once")
					}
				})
			}
		})
	}
}

func testOwnedProfile(t *testing.T, proof string) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	app := app{db: db}
	router := app.routes()
	call := func(path, bearer string, body map[string]string, status int) map[string]string {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/v1/account/"+path, bytes.NewReader(raw))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s: status %d", path, res.Code)
		}
		if status != 200 {
			return nil
		}
		var data map[string]string
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	login := func() map[string]string {
		state := call("oauth/start", "", map[string]string{"provider": "huawei", "challenge": hash(proof)}, 200)["state"]
		return call("oauth/finish", "", map[string]string{"state": state, "code": "code", "verifier": proof}, 200)
	}
	first := login()
	defer func() {
		for _, table := range []string{"account_sessions", "social_identities"} {
			_, _ = db.Exec(ctx, "delete from "+table+" where account_id=$1", first["id"])
		}
		_, _ = db.Exec(ctx, "delete from accounts where id=$1", first["id"])
	}()
	if first["name"] != "月下" || !strings.HasPrefix(first["avatarUrl"], avatarPrefix) {
		t.Fatal("profile not imported into account")
	}
	call("me", "", map[string]string{"name": "游客"}, 401)
	call("me", first["token"], map[string]string{"name": "  "}, 400)
	call("me", first["token"], map[string]string{"name": "月映昵称", "avatar": ""}, 200)
	second := login()
	if second["id"] != first["id"] || second["name"] != "月映昵称" || second["avatarUrl"] != "" {
		t.Fatal("repeat OAuth login overwrote local profile")
	}
}
