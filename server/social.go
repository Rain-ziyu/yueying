package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/smartwalle/alipay/v3"
)

type platform struct{ key, label, authorize, tokenURL, scope string }

var platforms = []platform{
	{"wechat", "微信", "https://open.weixin.qq.com/connect/qrconnect", "https://api.weixin.qq.com/sns/oauth2/access_token", "snsapi_login"},
	{"qq", "QQ", "https://graph.qq.com/oauth2.0/authorize", "https://graph.qq.com/oauth2.0/token", "get_user_info"},
	{"alipay", "支付宝", "https://openauth.alipay.com/oauth2/publicAppAuthorize.htm", "", "auth_base"},
	{"huawei", "华为账号", "https://oauth-login.cloud.huawei.com/oauth2/v3/authorize", "https://oauth-login.cloud.huawei.com/oauth2/v3/token", "openid profile"},
}

func (p platform) config(key string) string { return os.Getenv(strings.ToUpper(p.key) + "_" + key) }
func (p platform) enabled() bool {
	redirect, err := url.Parse(os.Getenv("LOGIN_CALLBACK_URL"))
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || p.config("APP_ID") == "" {
		return false
	}
	if p.key == "alipay" {
		return p.config("PRIVATE_KEY") != "" && p.config("PUBLIC_KEY") != ""
	}
	return p.config("APP_SECRET") != ""
}
func getPlatform(key string) (platform, error) {
	for _, p := range platforms {
		if p.key == key && p.enabled() {
			return p, nil
		}
	}
	return platform{}, fault{503, "登录平台尚未配置"}
}
func (a *app) providers(w http.ResponseWriter, r *http.Request, _ identity) error {
	type option struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	list := []option{}
	for _, p := range platforms {
		list = append(list, option{p.key, p.label, p.enabled()})
	}
	return respond(w, list)
}

var hexProof = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *app) socialStart(w http.ResponseWriter, r *http.Request, _ identity) error {
	var input struct{ Provider, Challenge string }
	if e := body(r, &input); e != nil {
		return e
	}
	p, e := getPlatform(input.Provider)
	if e != nil {
		return e
	}
	if !hexProof.MatchString(input.Challenge) {
		return bad("登录校验无效")
	}
	var session any
	if bearer(r) != "" {
		if _, e = a.account(r); e != nil {
			return e
		}
		session = hash(bearer(r))
	}
	state := token()
	if p.key == "huawei" {
		state = "pkce1." + state
	}
	_, e = a.db.Exec(r.Context(), "insert into oauth_flows values($1,$2,$3,$4,now()+interval '10 minutes')", hash(state), p.key, input.Challenge, session)
	if e != nil {
		return e
	}
	q := url.Values{"redirect_uri": {os.Getenv("LOGIN_CALLBACK_URL")}, "state": {state}, "scope": {p.scope}, "response_type": {"code"}}
	switch p.key {
	case "wechat":
		q.Set("appid", p.config("APP_ID"))
	case "alipay":
		q.Del("response_type")
		q.Set("app_id", p.config("APP_ID"))
	default:
		q.Set("client_id", p.config("APP_ID"))
	}
	if p.key == "huawei" {
		digest, _ := hex.DecodeString(input.Challenge) // validated by hexProof above
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(digest))
		q.Set("code_challenge_method", "S256")
	}
	target := p.authorize + "?" + q.Encode()
	if p.key == "wechat" {
		target += "#wechat_redirect"
	}
	return respond(w, map[string]string{"url": target, "state": state})
}

var socialClient = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var huaweiKeys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), socialClient), "https://oauth-login.cloud.huawei.com/oauth2/v3/certs")

type identityProfile struct {
	Subject string
	Name    string
	Avatar  string
}

func huaweiProfile(c context.Context, subject, accessToken string) identityProfile {
	profile := identityProfile{Subject: subject}
	form := url.Values{"access_token": {accessToken}, "getNickName": {"1"}}
	req, err := http.NewRequestWithContext(c, http.MethodPost, "https://account.cloud.huawei.com/rest.php?nsp_svc=GOpen.User.getInfo", strings.NewReader(form.Encode()))
	if err != nil {
		return profile
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := socialClient.Do(req)
	if err != nil {
		return profile
	}
	defer resp.Body.Close()
	var data struct{ OpenID, UnionID, DisplayName, HeadPictureURL string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data) != nil || (data.OpenID != subject && data.UnionID != subject) {
		return profile
	}
	profile.Name = strings.TrimSpace(data.DisplayName)
	if u, err := url.Parse(data.HeadPictureURL); err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil {
		profile.Avatar = u.String()
	}
	return profile
}

func (p platform) identify(c context.Context, code, proof string) (identityProfile, error) {
	rejected := bad("平台未确认登录，请重新授权")
	if p.key == "alipay" {
		client, e := alipay.New(p.config("APP_ID"), p.config("PRIVATE_KEY"), true, alipay.WithHTTPClient(socialClient))
		if e != nil {
			return identityProfile{}, e
		}
		if e = client.LoadAliPayPublicKey(p.config("PUBLIC_KEY")); e != nil {
			return identityProfile{}, e
		}
		result, e := client.SystemOauthToken(c, alipay.SystemOauthToken{GrantType: "authorization_code", Code: code})
		if e != nil || result == nil || result.AccessToken == "" {
			return identityProfile{}, rejected
		}
		if result.OpenId != "" {
			return identityProfile{Subject: result.OpenId}, nil
		}
		if result.UserId != "" {
			return identityProfile{Subject: result.UserId}, nil
		}
		return identityProfile{}, rejected
	}
	q := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {p.config("APP_ID")}, "client_secret": {p.config("APP_SECRET")}, "redirect_uri": {os.Getenv("LOGIN_CALLBACK_URL")}}
	var req *http.Request
	var e error
	if p.key == "huawei" {
		q.Set("code_verifier", proof)
		req, e = http.NewRequestWithContext(c, "POST", p.tokenURL, strings.NewReader(q.Encode()))
		if e == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		if p.key == "wechat" {
			q = url.Values{"appid": {p.config("APP_ID")}, "secret": {p.config("APP_SECRET")}, "code": {code}, "grant_type": {"authorization_code"}}
		} else {
			q.Set("fmt", "json")
			q.Set("need_openid", "1")
		}
		req, e = http.NewRequestWithContext(c, "GET", p.tokenURL+"?"+q.Encode(), nil)
	}
	if e != nil {
		return identityProfile{}, rejected
	}
	resp, e := socialClient.Do(req)
	if e != nil {
		if p.key == "huawei" {
			return identityProfile{}, fault{502, "暂时无法连接华为令牌服务，请稍后重试"}
		}
		return identityProfile{}, rejected
	}
	defer resp.Body.Close()
	var result struct {
		OpenID  string `json:"openid"`
		Token   string `json:"access_token"`
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	if resp.StatusCode != 200 || e != nil || result.Token == "" {
		if p.key == "huawei" {
			switch result.Error {
			case "invalid_client":
				return identityProfile{}, fault{502, "华为拒绝了月映的应用凭证"}
			case "invalid_grant":
				return identityProfile{}, bad("华为未接受本次授权码，请重新登录")
			default:
				return identityProfile{}, fault{502, "华为令牌服务未返回有效的访问令牌"}
			}
		}
		return identityProfile{}, rejected
	}
	if p.key == "huawei" {
		verifier := oidc.NewVerifier("https://accounts.huawei.com", huaweiKeys, &oidc.Config{ClientID: p.config("APP_ID"), SupportedSigningAlgs: []string{oidc.RS256, oidc.PS256}})
		id, e := verifier.Verify(c, result.IDToken)
		if e != nil {
			return identityProfile{}, bad("华为身份令牌验证失败，请重新登录")
		}
		if id.Subject == "" {
			return identityProfile{}, bad("华为身份令牌缺少用户标识")
		}
		return huaweiProfile(c, id.Subject, result.Token), nil
	}
	if result.OpenID == "" {
		return identityProfile{}, rejected
	}
	return identityProfile{Subject: result.OpenID}, nil
}
func (a *app) socialFinish(w http.ResponseWriter, r *http.Request, _ identity) error {
	var in struct{ State, Code, Verifier string }
	if e := body(r, &in); e != nil {
		return e
	}
	if in.Code == "" || !hexProof.MatchString(in.Verifier) {
		return bad("登录校验无效")
	}
	c := r.Context()
	var key string
	var session *string
	e := a.db.QueryRow(c, "delete from oauth_flows where state=$1 and challenge=$2 and expires_at>now() returning provider,session_key", hash(in.State), hash(in.Verifier)).Scan(&key, &session)
	if e != nil {
		return bad("登录请求已失效，请重试")
	}
	var u account
	if session != nil {
		if *session != hash(bearer(r)) {
			return forbidden()
		}
		u, e = a.account(r)
		if e != nil {
			return e
		}
	} else if bearer(r) != "" {
		return bad("账号状态已改变，请重试")
	}
	p, e := getPlatform(key)
	if e != nil {
		return e
	}
	// Reject flows started before Huawei began binding each code to the browser's proof.
	if key == "huawei" && !strings.HasPrefix(in.State, "pkce1.") {
		return bad("华为登录已更新，请重新登录")
	}
	profile, e := p.identify(c, in.Code, in.Verifier)
	if e != nil {
		return e
	}
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	subject := profile.Subject
	appID := p.config("APP_ID")
	if _, e = tx.Exec(c, "select pg_advisory_xact_lock(hashtextextended($1,0))", key+":"+appID+":"+subject); e != nil {
		return e
	}
	outcome := "signed_in"
	var previous account
	e = tx.QueryRow(c, "select a.id::text,a.display_name,a.avatar_url from social_identities i join accounts a on a.id=i.account_id where provider=$1 and app_id=$2 and subject=$3", key, appID, subject).Scan(&previous.ID, &previous.Name, &previous.Avatar)
	if e == nil {
		if u.ID != "" && u.ID != previous.ID {
			return fault{409, "请选择当前月映账号原先使用的平台账号"}
		}
		u = previous
		if session != nil {
			outcome = "profile_updated"
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		if session != nil {
			return fault{409, "此平台账号与当前月映账号不匹配"}
		}
		if e = tx.QueryRow(c, "insert into accounts(display_name) values($1) returning id::text,display_name", p.label+"用户").Scan(&u.ID, &u.Name); e != nil {
			return e
		}
		res, e := tx.Exec(c, "insert into social_identities values($1,$2,$3,$4) on conflict do nothing", key, appID, subject, u.ID)
		if e != nil {
			return e
		}
		if res.RowsAffected() != 1 {
			return fault{409, "此账号已有该平台的绑定"}
		}
	} else {
		return e
	}
	u.Provider = key
	if profile.Name != "" || profile.Avatar != "" {
		e = tx.QueryRow(c, "update accounts set display_name=coalesce(nullif($2,''),display_name),avatar_url=coalesce(nullif($3,''),avatar_url) where id=$1 returning display_name,avatar_url", u.ID, profile.Name, profile.Avatar).Scan(&u.Name, &u.Avatar)
		if e != nil {
			return e
		}
	}
	result := login{u, bearer(r)}
	if session == nil {
		result, e = newSession(c, tx, u)
		if e != nil {
			return e
		}
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	return respond(w, struct {
		login
		ProfileAvailable bool   `json:"profileAvailable"`
		Outcome          string `json:"outcome"`
	}{result, profile.Name != "" || profile.Avatar != "", outcome})
}
