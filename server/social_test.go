package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
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
	socialClient.Transport = exchangeTransport(func(r *http.Request) (*http.Response, error) {
		var result any
		switch r.URL.Host {
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
				body := fmt.Sprintf(`{"iss":"https://accounts.huawei.com","aud":"client","sub":"subject","exp":%d}`, time.Now().Add(time.Minute).Unix())
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
			got, err := p.subject(context.Background(), "code", proof)
			if err != nil || got != "subject" {
				t.Fatalf("subject=%q err=%v", got, err)
			}
			if p.key == "huawei" {
				if _, err = p.subject(context.Background(), "code", "different"); err == nil {
					t.Fatal("wrong PKCE verifier accepted")
				}
			}
		})
	}
}
