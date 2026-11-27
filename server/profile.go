package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const avatarPrefix = "data:image/jpeg;base64,"
const avatarSide = 192

// Store a small, metadata-free JPEG with the account so a provider URL can expire.
func avatarThumbnail(data []byte, maxSide int) (string, error) {
	invalid := bad("头像图片格式或尺寸无效")
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxSide || cfg.Height > maxSide {
		return "", invalid
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", invalid
	}
	side := min(cfg.Width, cfg.Height)
	size := min(side, avatarSide)
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a := src.At((cfg.Width-side)/2+x*side/size, (cfg.Height-side)/2+y*side/size).RGBA()
			dst.SetRGBA(x, y, color.RGBA{uint8((r + 65535 - a) >> 8), uint8((g + 65535 - a) >> 8), uint8((b + 65535 - a) >> 8), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 86}); err != nil {
		return "", err
	}
	return avatarPrefix + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func importHuaweiAvatar(ctx context.Context, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".hicloud.com") && !strings.HasSuffix(host, ".huawei.com") && !strings.HasSuffix(host, ".dbankcdn.com") && !strings.HasSuffix(host, ".dbankcdn.cn") {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ""
	}
	resp, err := socialClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	const limit = 3 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit {
		return ""
	}
	thumb, _ := avatarThumbnail(data, 4096)
	return thumb
}

func (a *app) editProfile(w http.ResponseWriter, r *http.Request, _ identity) error {
	u, err := a.account(r)
	if err != nil {
		return err
	}
	var patch struct{ Name, Avatar *string }
	if err := body(r, &patch); err != nil {
		return err
	}
	if patch.Name == nil && patch.Avatar == nil {
		return bad("请选择要修改的资料")
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return bad("昵称需为 1–40 个字符，且不能包含控制字符")
		}
		patch.Name = &name
	}
	if patch.Avatar != nil && *patch.Avatar != "" {
		payload, ok := strings.CutPrefix(*patch.Avatar, avatarPrefix)
		if !ok {
			return bad("请选择头像图片")
		}
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return bad("头像数据无效")
		}
		thumbnail, err := avatarThumbnail(data, avatarSide)
		if err != nil {
			return err
		}
		patch.Avatar = &thumbnail
	}
	err = a.db.QueryRow(r.Context(), "update accounts set display_name=coalesce($2,display_name), avatar_url=coalesce($3,avatar_url) where id=$1 returning display_name,avatar_url", u.ID, patch.Name, patch.Avatar).Scan(&u.Name, &u.Avatar)
	if err != nil {
		return err
	}
	return respond(w, map[string]string{"id": u.ID, "name": u.Name, "avatarUrl": u.Avatar})
}
