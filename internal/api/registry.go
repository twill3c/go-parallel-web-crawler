package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// registry は進行中・完了済みクロールの台帳(メモリのみ・SPEC F-23/F-24・D-03)。
// サーバレスでは呼び出しごとにインスタンスが違いうるので、同じインスタンスに当たったときだけ効く。
// 完了済みは keep の間だけ残し、最大 maxEntries 件で古い順に捨てる。
type registry struct {
	mu         sync.Mutex
	entries    map[string]*entry
	keep       time.Duration
	maxEntries int
}

type entry struct {
	cancel   context.CancelFunc
	result   *model.Result // 完了前は nil
	finished time.Time
}

func newRegistry() *registry {
	return &registry{entries: map[string]*entry{}, keep: 10 * time.Minute, maxEntries: 50}
}

// newID は 12 バイトの乱数を hex にした crawlId。
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))[:24]
	}
	return hex.EncodeToString(b)
}

// start は新しいクロールを台帳に登録し、id と cancel を返す。
func (r *registry) start(cancel context.CancelFunc) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictLocked()
	id := newID()
	r.entries[id] = &entry{cancel: cancel}
	return id
}

// finish は結果を保存する。
func (r *registry) finish(id string, res model.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok {
		e.result = &res
		e.finished = time.Now()
	}
}

// stop は進行中クロールの context を cancel する。未知の id は false。
func (r *registry) stop(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return false
	}
	e.cancel()
	return true
}

// result は id の結果を返す。進行中なら Status "running" の空結果。未知なら ok=false。
func (r *registry) result(id string) (model.Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return model.Result{}, false
	}
	if e.result == nil {
		return model.Result{Status: "running", Pages: []model.Page{}, Links: []model.Link{}}, true
	}
	return *e.result, true
}

// evictLocked は保持期限を過ぎた完了済みと、件数超過分(古い順)を捨てる。mu を持って呼ぶ。
func (r *registry) evictLocked() {
	now := time.Now()
	for id, e := range r.entries {
		if e.result != nil && now.Sub(e.finished) > r.keep {
			delete(r.entries, id)
		}
	}
	for len(r.entries) >= r.maxEntries {
		var oldest string
		var oldestAt time.Time
		for id, e := range r.entries {
			if e.result == nil {
				continue
			}
			if oldest == "" || e.finished.Before(oldestAt) {
				oldest, oldestAt = id, e.finished
			}
		}
		if oldest == "" {
			return // 全部進行中なら捨てない
		}
		delete(r.entries, oldest)
	}
}
