package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	mps "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/mps/v20190612"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (a *app) queueTranscode(w http.ResponseWriter, r *http.Request, u identity) error {
	if !u.allows("upload") {
		return forbidden()
	}
	if os.Getenv("MPS_ENABLED") != "1" {
		return bad("此环境尚未启用云转码")
	}
	res, e := a.db.Exec(r.Context(), "insert into transcodes(asset) select id from assets where id=$1 and room=$2 and ready and original_id is null on conflict do nothing", chi.URLParam(r, "id"), u.room)
	if e != nil {
		return e
	}
	return respond(w, map[string]bool{"queued": res.RowsAffected() == 1})
}

type converter struct {
	client         *mps.Client
	output         storage
	bucket, region string
	definition     uint64
	limit          int
}

func (a *app) worker(c context.Context) error {
	if os.Getenv("MPS_ENABLED") != "1" {
		return errors.New("set MPS_ENABLED=1 to enable paid processing")
	}
	limit, e := strconv.Atoi(os.Getenv("MPS_MONTHLY_MINUTES"))
	if e != nil || limit <= 0 {
		return errors.New("set a positive MPS_MONTHLY_MINUTES")
	}
	definition, e := strconv.ParseUint(os.Getenv("MPS_TEMPLATE"), 10, 64)
	if e != nil {
		return e
	}
	out := a.stores[os.Getenv("MPS_OUTPUT_STORE")]
	region, bucket := os.Getenv("MPS_REGION"), os.Getenv("MPS_BUCKET")
	if out == nil || bucket == "" || region == "" || os.Getenv("TENCENT_SECRET_ID") == "" || os.Getenv("TENCENT_SECRET_KEY") == "" {
		return errors.New("incomplete MPS configuration")
	}
	if _, e = exec.LookPath("ffprobe"); e != nil {
		return e
	}
	client, e := mps.NewClient(common.NewCredential(os.Getenv("TENCENT_SECRET_ID"), os.Getenv("TENCENT_SECRET_KEY")), region, profile.NewClientProfile())
	if e != nil {
		return e
	}
	cv := converter{client, out, bucket, region, definition, limit}
	// Keep the advisory lock on the same PostgreSQL session throughout the worker lifetime.
	conn, e := a.db.Acquire(c)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(c, "select pg_try_advisory_lock(817541)").Scan(&locked); e != nil || !locked {
		return errors.New("worker lock unavailable")
	}
	defer conn.Exec(c, "select pg_advisory_unlock(817541)")
	for c.Err() == nil {
		rows, e := a.db.Query(c, "select asset from transcodes where status in ('queued','reserved','processing') order by created_at")
		if e != nil {
			return e
		}
		ids, e := pgx.CollectRows(rows, pgx.RowTo[string])
		if e != nil {
			return e
		}
		for _, id := range ids {
			if e = a.process(c, cv, id); e != nil {
				log.Printf("transcode %s retry (%T)", id, e)
			}
		}
		select {
		case <-c.Done():
			return c.Err()
		case <-time.After(15 * time.Second):
		}
	}
	return c.Err()
}
func (a *app) process(c context.Context, v converter, id string) error {
	var store, key, status, task string
	var created time.Time
	e := a.db.QueryRow(c, "select a.store,a.object_key,t.status,coalesce(t.task_id,''),t.created_at from assets a join transcodes t on t.asset=a.id where a.id=$1", id).Scan(&store, &key, &status, &task, &created)
	if e != nil {
		return e
	}
	fail := func(reason string) error {
		_, e := a.db.Exec(c, "update transcodes set status='failed',detail=$2 where asset=$1", id, reason)
		return e
	}
	source := a.stores[store]
	if source == nil {
		return errors.New("missing source store")
	}
	u, e := source.readURL(c, key)
	if e != nil {
		return e
	}
	if status == "queued" {
		probeCtx, cancel := context.WithTimeout(c, 45*time.Second)
		defer cancel()
		b, e := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-protocol_whitelist", "https,tls,tcp,http", "-format_whitelist", "mov,matroska,webm,avi,mpegts", "-show_entries", "format=duration", "-of", "json", u).Output()
		if e != nil {
			return fail("读取片源时长失败")
		}
		var probe struct{ Format struct{ Duration string } }
		if json.Unmarshal(b, &probe) != nil {
			return fail("片源时长无效")
		}
		d, e := strconv.ParseFloat(probe.Format.Duration, 64)
		if e != nil || d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
			return fail("片源时长无效")
		}
		minutes := int(math.Ceil(d / 60))
		res, e := a.db.Exec(c, "update transcodes set status='reserved',reserved_minutes=$2,created_at=now() where asset=$1 and $2::int+(select coalesce(sum(reserved_minutes),0) from transcodes where created_at>=date_trunc('month',now()))<=$3", id, minutes, v.limit)
		if e != nil {
			return e
		}
		if res.RowsAffected() == 0 {
			return fail("本月转码额度不足")
		}
		status = "reserved"
		created = time.Now()
	}
	outkey := "yueying/" + id + ".mp4"
	if status == "reserved" {
		if time.Since(created) > 24*time.Hour {
			return fail("提交状态需人工核实")
		}
		req := mps.NewProcessMediaRequest()
		req.SessionId = &id
		req.InputInfo = &mps.MediaInputInfo{Type: common.StringPtr("URL"), UrlInputInfo: &mps.UrlInputInfo{Url: &u}}
		req.OutputStorage = &mps.TaskOutputStorage{Type: common.StringPtr("COS"), CosOutputStorage: &mps.CosOutputStorage{Bucket: &v.bucket, Region: &v.region}}
		req.MediaProcessTask = &mps.MediaProcessTaskInput{TranscodeTaskSet: []*mps.TranscodeTaskInput{{Definition: &v.definition, OutputObjectPath: common.StringPtr("/" + outkey)}}}
		response, e := v.client.ProcessMediaWithContext(c, req)
		if e != nil {
			return e
		}
		if response.Response.TaskId == nil {
			return errors.New("missing MPS task id")
		}
		_, e = a.db.Exec(c, "update transcodes set status='processing',task_id=$2 where asset=$1", id, *response.Response.TaskId)
		return e
	}
	req := mps.NewDescribeTaskDetailRequest()
	req.TaskId = &task
	result, e := v.client.DescribeTaskDetailWithContext(c, req)
	if e != nil {
		return e
	}
	wf := result.Response.WorkflowTask
	if wf == nil || wf.Status == nil || *wf.Status != "FINISH" {
		return nil
	}
	if wf.ErrCode != nil && *wf.ErrCode != 0 {
		return fail("云转码失败")
	}
	if len(wf.MediaProcessResultSet) != 1 {
		return fail("未生成播放版本")
	}
	t := wf.MediaProcessResultSet[0].TranscodeTask
	if t == nil || t.Output == nil || t.Output.Path == nil || t.ErrCode != nil && *t.ErrCode != 0 {
		return fail("转码产物不可用")
	}
	outkey = strings.TrimPrefix(*t.Output.Path, "/")
	n, e := v.output.size(c, outkey)
	if e != nil {
		return e
	}
	r, e := v.output.open(c, outkey)
	if e != nil {
		return e
	}
	defer r.Close()
	dest := a.stores[a.primary]
	variantKey := key + "/play.mp4"
	sum := sha256.New()
	if e = dest.put(c, variantKey, io.TeeReader(r, sum), n); e != nil {
		return e
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	if e = verifyFile(c, dest, variantKey, n, digest); e != nil {
		return e
	}
	tx, e := a.db.Begin(c)
	if e != nil {
		return e
	}
	defer tx.Rollback(c)
	file := token()
	_, e = tx.Exec(c, "insert into assets(id,room,owner_token,name,bytes,ready,store,object_key,upload_id,sha256,original_id) select $2,room,owner_token,name,$3,true,$4,$5,'',$6,id from assets where id=$1", id, file, n, a.primary, variantKey, digest)
	if e != nil {
		return e
	}
	_, e = tx.Exec(c, "insert into locations values($1,$2,$3,$4,$5)", file, a.primary, variantKey, n, digest)
	if e != nil {
		return e
	}
	_, e = tx.Exec(c, "update assets set playable_id=$2 where id=$1", id, file)
	if e != nil {
		return e
	}
	_, e = tx.Exec(c, "update transcodes set status='ready',detail='' where asset=$1", id)
	if e != nil {
		return e
	}
	return tx.Commit(c)
}
func verifyFile(c context.Context, s storage, key string, n int64, digest string) error {
	r, e := s.open(c, key)
	if e != nil {
		return e
	}
	defer r.Close()
	h := sha256.New()
	size, e := io.Copy(h, r)
	if e != nil {
		return e
	}
	if size != n || hex.EncodeToString(h.Sum(nil)) != digest {
		return fmt.Errorf("file verification failed")
	}
	return nil
}
