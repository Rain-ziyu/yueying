package main

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
)

const protocol = "s3-multipart-v1"

func (a *app) begin(w http.ResponseWriter, r *http.Request, u identity) error {
	var p struct {
		Name      string
		Bytes     int64
		Protocols []string
	}
	if e := body(r, &p); e != nil {
		return e
	}
	if p.Bytes <= 0 || p.Bytes > 8<<30 || len(p.Name) > 512 || p.Name == "" {
		return bad("无效文件或超过 8 GiB")
	}
	store := a.primary
	if len(p.Protocols) == 0 {
		p.Protocols = []string{protocol}
	}
	if !slices.Contains(p.Protocols, a.stores[store].protocolName()) {
		fallback := a.stores[a.fallback]
		if fallback == nil || !slices.Contains(p.Protocols, fallback.protocolName()) {
			return bad("请更新客户端以支持当前存储")
		}
		store = a.fallback
	}
	s := a.stores[store]
	if s == nil {
		return bad("存储配置不可用")
	}
	id := token()
	key := "rooms/" + u.room + "/" + id
	c := r.Context()
	upload, e := s.begin(c, key)
	if e != nil {
		return e
	}
	_, e = a.db.Exec(c, "insert into assets(id,room,owner_token,name,bytes,store,object_key,upload_id) values($1,$2,$3,$4,$5,$6,$7,$8)", id, u.room, u.token, p.Name, p.Bytes, store, key, upload)
	if e != nil {
		s.abort(c, key, upload)
		return e
	}
	return respond(w, map[string]any{"id": id, "protocol": s.protocolName(), "partSize": partSize})
}
func (a *app) upload(w http.ResponseWriter, r *http.Request, u identity) error {
	op, id := chi.URLParam(r, "op"), chi.URLParam(r, "id")
	c := r.Context()
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	var store, key, uid string
	var size int64
	var ready bool
	e = tx.QueryRow(c, "select store,object_key,upload_id,bytes,ready from assets where id=$1 and owner_token=$2 for update", id, u.token).Scan(&store, &key, &uid, &size, &ready)
	if errors.Is(e, pgx.ErrNoRows) {
		return fault{404, "上传不存在"}
	}
	if e != nil {
		return e
	}
	if ready {
		if op == "complete" {
			return respond(w, true)
		}
		return bad("上传已经完成")
	}
	s := a.stores[store]
	if s == nil {
		return errors.New("missing store")
	}
	switch op {
	case "part":
		var p struct{ Number int }
		if e = body(r, &p); e != nil {
			return e
		}
		if p.Number < 1 || int64(p.Number) > (size+partSize-1)/partSize {
			return bad("分片越界")
		}
		url, e := s.part(c, key, uid, p.Number)
		if e != nil {
			return e
		}
		return respond(w, url)
	case "parts":
		p, e := s.parts(c, key, uid)
		if e != nil {
			return e
		}
		return respond(w, p)
	case "cancel":
		if e = s.abort(c, key, uid); e != nil {
			return e
		}
		_, e = tx.Exec(c, "delete from assets where id=$1", id)
	case "complete":
		// A completed object can outlive a rolled-back database transaction.
		if n, err := s.size(c, key); err != nil || n != size {
			parts, err := s.parts(c, key, uid)
			if err != nil {
				return err
			}
			if int64(len(parts)) != (size+partSize-1)/partSize {
				return bad("分片不完整")
			}
			if err = s.finish(c, key, uid, parts); err != nil {
				return err
			}
			n, err = s.size(c, key)
			if err != nil || n != size {
				return bad("文件长度校验失败")
			}
		}
		_, e = tx.Exec(c, "insert into locations(asset,store,object_key,bytes) values($1,$2,$3,$4)", id, store, key, size)
		if e == nil {
			_, e = tx.Exec(c, "update assets set ready=true where id=$1", id)
		}
	default:
		return bad("未知上传操作")
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(c); e != nil {
		return e
	}
	return respond(w, true)
}
func (a *app) list(w http.ResponseWriter, r *http.Request, u identity) error {
	rows, e := a.db.Query(r.Context(), "select a.id,a.name,coalesce(t.status,''),coalesce(t.detail,'') from assets a left join transcodes t on t.asset=a.id where a.room=$1 and ready and a.original_id is null order by a.name,a.id", u.room)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, n, status, detail string
		if e = rows.Scan(&id, &n, &status, &detail); e != nil {
			return e
		}
		out = append(out, map[string]string{"id": id, "name": n, "transcode": status, "detail": detail})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return respond(w, out)
}
func (a *app) play(w http.ResponseWriter, r *http.Request, u identity) error {
	var store, key string
	e := a.db.QueryRow(r.Context(), "select f.store,f.object_key from assets a join assets f on f.id=coalesce(a.playable_id,a.id) where a.room=$1 and a.id=$2 and a.ready and f.ready", u.room, chi.URLParam(r, "id")).Scan(&store, &key)
	if e != nil {
		return fault{404, "片源不可用"}
	}
	s := a.stores[store]
	if s == nil {
		return errors.New("missing store")
	}
	url, e := s.readURL(r.Context(), key)
	if e != nil {
		return e
	}
	w.Header().Set("Cache-Control", "no-store")
	return respond(w, map[string]any{"url": url, "ttl": 3600})
}
