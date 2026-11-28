package main

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func (a *app) requestLow(w http.ResponseWriter, r *http.Request, u identity) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fault{503, "当前服务未安装低画质编码器"}
	}
	id := chi.URLParam(r, "id")
	var status string
	// Reads during encoding should not keep rewriting the original asset.
	err := a.db.QueryRow(r.Context(), "select low_status from assets where room=$1 and id=$2 and ready and original_id is null", u.room, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault{404, "片源不可用"}
	}
	if err != nil {
		return err
	}
	if status == "" || (status == "failed" && r.URL.Query().Get("retry") == "1") {
		_, err = a.db.Exec(r.Context(), "update assets set low_status='queued' where id=$1 and low_status=$2", id, status)
		if err != nil {
			return err
		}
		status = "queued"
	}
	if status == "queued" || status == "processing" {
		select {
		case a.qualityWake <- struct{}{}:
		default:
		}
	}
	return respond(w, map[string]string{"status": status})
}

func (a *app) qualityWorker(c context.Context) {
	for {
		select {
		case <-c.Done():
			return
		case <-a.qualityWake:
		}
		if err := a.drainQuality(c); err != nil {
			log.Printf("quality worker: %T", err)
		}
	}
}

func (a *app) drainQuality(c context.Context) error {
	conn, err := a.db.Acquire(c)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(c, "select pg_try_advisory_lock(817542)").Scan(&locked); err != nil || !locked {
		return err
	}
	defer conn.Exec(c, "select pg_advisory_unlock(817542)")
	for {
		var id, store, key string
		var size int64
		err = conn.QueryRow(c, `select a.id,f.store,f.object_key,f.bytes from assets a join assets f on f.id=coalesce(a.playable_id,a.id)
   where a.low_status in ('queued','processing') order by a.low_status,a.id limit 1`).Scan(&id, &store, &key, &size)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = conn.Exec(c, "update assets set low_status='processing' where id=$1", id); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c, 4*time.Hour)
		err = a.makeLow(ctx, id, store, key, size)
		cancel()
		if err != nil {
			log.Printf("quality encoding %s: %T", id, err)
			if _, err = conn.Exec(c, "update assets set low_status='failed' where id=$1", id); err != nil {
				return err
			}
		}
	}
}

func (a *app) makeLow(c context.Context, id, store, key string, size int64) error {
	src := a.stores[store]
	dst := a.stores[a.primary]
	if src == nil || dst == nil {
		return errors.New("missing media storage")
	}
	url, err := src.readURL(c, key)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "yueying-low-*.mp4")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	// A single encoder and bounded dimensions keep the room API responsive.
	cmd := exec.CommandContext(c, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "http,https,tcp,tls", "-format_whitelist", "mov,matroska,webm,avi,mpegts", "-i", url, "-map", "0:v:0", "-map", "0:a:0?", "-vf", "scale=w='min(640,iw)':h='min(360,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", "-c:v", "libx264", "-preset", "veryfast", "-threads", "1", "-pix_fmt", "yuv420p", "-crf", "29", "-maxrate", "420k", "-bufsize", "840k", "-c:a", "aac", "-ac", "2", "-b:a", "48k", "-movflags", "+faststart", path)
	if err = cmd.Run(); err != nil {
		return err
	}
	f, err = os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	n := info.Size()
	if n == 0 {
		return errors.New("empty media")
	}
	if n >= size {
		_, err = a.db.Exec(c, "update assets set low_status='already_small' where id=$1", id)
		return err
	}
	tx, err := a.db.Begin(c)
	if err != nil {
		return err
	}
	defer tx.Rollback(c)
	out := "rooms/low/" + id + ".mp4"
	file := token()
	// Keep room cleanup ordered with publishing the derived file.
	_, err = tx.Exec(c, `insert into assets(id,room,owner_token,name,bytes,ready,store,object_key,upload_id,original_id)
 select $2,room,owner_token,name,$3,true,$4,$5,'',id from assets where id=$1`, id, file, n, a.primary, out)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(c, "insert into locations(asset,store,object_key,bytes) values($1,$2,$3,$4)", file, a.primary, out, n); err != nil {
		return err
	}
	if _, err = tx.Exec(c, "update assets set low_status='ready',low_id=$2 where id=$1", id, file); err != nil {
		return err
	}
	if err = dst.put(c, out, f, n); err != nil {
		return err
	}
	return tx.Commit(c)
}
