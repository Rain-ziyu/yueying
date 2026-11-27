package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// Queue every recorded copy in the same transaction that retires the room.
// A failed bucket request survives a deployment and is retried independently.
func (a *app) expireRoom(c context.Context) (bool, error) {
	tx, e := a.db.Begin(c)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(c)
	var room string
	e = tx.QueryRow(c, `select r.id from rooms r where last_used < now()-interval '30 days 2 minutes'
 and not exists(select 1 from members m where m.token=r.owner_token and m.account_id is not null)
 order by last_used limit 1 for update of r skip locked`).Scan(&room)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	a.mu.Lock()
	online := a.rooms[room] != nil
	a.mu.Unlock()
	if online {
		_, e = tx.Exec(c, "update rooms set last_used=now() where id=$1", room)
		if e != nil {
			return false, e
		}
		return true, tx.Commit(c)
	}
	statements := []string{
		`insert into media_deletions(store,object_key,upload_id)
 select store,object_key,case when ready then '' else upload_id end from assets where room=$1
 union select l.store,l.object_key,'' from locations l join assets a on a.id=l.asset where a.room=$1 on conflict do nothing`,
		"delete from transcodes where asset in(select id from assets where room=$1)",
		"delete from locations where asset in(select id from assets where room=$1)",
		"delete from assets where room=$1", "delete from members where room=$1", "delete from rooms where id=$1"}
	for _, q := range statements {
		if _, e = tx.Exec(c, q, room); e != nil {
			return false, e
		}
	}
	return true, tx.Commit(c)
}
func (a *app) deleteMedia(c context.Context) (bool, error) {
	tx, e := a.db.Begin(c)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(c)
	var store, key, upload string
	e = tx.QueryRow(c, "select store,object_key,upload_id from media_deletions where next_try<=now() order by next_try limit 1 for update skip locked").Scan(&store, &key, &upload)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	s := a.stores[store]
	ctx, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	if s == nil {
		e = errors.New("storage configuration unavailable")
	} else {
		if upload != "" {
			e = s.abort(ctx, key, upload)
		}
		if e == nil {
			e = s.remove(ctx, key)
		}
	}
	query := "delete from media_deletions where store=$1 and object_key=$2 and upload_id=$3"
	if e != nil {
		log.Printf("media cleanup deferred for store %s (%T)", store, e)
		query = "update media_deletions set next_try=now()+interval '1 hour' where store=$1 and object_key=$2 and upload_id=$3"
	}
	if _, e = tx.Exec(c, query, store, key, upload); e != nil {
		return false, e
	}
	return true, tx.Commit(c)
}
func (a *app) maintain(c context.Context) {
	for c.Err() == nil {
		for _, step := range []func(context.Context) (bool, error){a.expireRoom, a.deleteMedia} {
			for i := 0; i < 100; i++ {
				more, e := step(c)
				if e != nil {
					log.Printf("room housekeeping (%T)", e)
				}
				if e != nil || !more {
					break
				}
			}
		}
		for _, q := range []string{"delete from oauth_flows where expires_at<now()", "delete from account_sessions where expires_at<now()", "delete from auth_limits where until_at<now()"} {
			if _, e := a.db.Exec(c, q); e != nil {
				log.Printf("session housekeeping (%T)", e)
			}
		}
		select {
		case <-c.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
