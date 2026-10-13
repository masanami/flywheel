package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// defaultCheckTTL は PR ごとのチェックの取得結果（成功・失敗とも）を保持する時間。
const defaultCheckTTL = 60 * time.Second

// checkCache は PR ごとのチェックの取得結果を、取得した時刻から ttl だけ保持する。
// 保持するのはこれだけで、waiting_external の候補は要求ごとに core が導き直す。
type checkCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[checkKey]checkEntry
}

type checkKey struct {
	repo   string
	number int
}

type checkEntry struct {
	at     time.Time
	checks core.UpstreamPullRequestChecks
	err    error
}

func newCheckCache(ttl time.Duration) *checkCache {
	return &checkCache{ttl: ttl, now: time.Now, entries: map[checkKey]checkEntry{}}
}

// source は src の取得結果をこのキャッシュ越しに返す取得の口。
func (c *checkCache) source(src core.UpstreamCheckSource) core.UpstreamCheckSource {
	return cachedChecks{cache: c, src: src}
}

type cachedChecks struct {
	cache *checkCache
	src   core.UpstreamCheckSource
}

func (cc cachedChecks) GetPullRequestChecks(ctx context.Context, repo string, number int) (core.UpstreamPullRequestChecks, error) {
	c := cc.cache
	key := checkKey{repo, number}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		if c.now().Sub(e.at) < c.ttl {
			c.mu.Unlock()
			return e.checks, e.err
		}
		delete(c.entries, key)
	}
	c.mu.Unlock()

	checks, err := cc.src.GetPullRequestChecks(ctx, repo, number)
	if ctx.Err() != nil {
		return checks, err // 要求の取り消しによる失敗は保持しない
	}
	c.mu.Lock()
	now := c.now()
	for k, e := range c.entries { // 期限切れの項目を掃除する（再び要求されない PR が残り続けないように）
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
		}
	}
	c.entries[key] = checkEntry{at: now, checks: checks, err: err}
	c.mu.Unlock()
	return checks, err
}

// unavailableCheckSource は gh を組み立てられないときの、取得を常に失敗させる口
// （CLI の newCheckSource と同じ）。
type unavailableCheckSource struct{ err error }

func (u unavailableCheckSource) GetPullRequestChecks(context.Context, string, int) (core.UpstreamPullRequestChecks, error) {
	return core.UpstreamPullRequestChecks{}, fmt.Errorf("上流を取得できません: %w", u.err)
}

// newGitHubSource は要求ごとに gh を探して取得の口を作る。組み立てに失敗（ErrGHNotFound を含む）
// してもエラーにはせず、取得を常に失敗させる口を返す。
func newGitHubSource() core.UpstreamCheckSource {
	client, err := github.New(github.Options{})
	if err != nil {
		return unavailableCheckSource{err: err}
	}
	return client
}

// checkSource は要求ごとに gh を探して取得の口を作り、PR ごとの保持を通す。
func (c *checkCache) checkSource() core.UpstreamCheckSource {
	src := newGitHubSource()
	if _, failed := src.(unavailableCheckSource); failed {
		return src
	}
	return c.source(src)
}

// statusHandler は GET /api/v1/workspaces/{name}/status。本文は
// `flywheel status --json` と同じ形（internal/view の型）。
func statusHandler(fleet *Fleet, checks *checkCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		ov, err := statusOverview(r.Context(), fleet, r.PathValue("name"), checks)
		if err != nil {
			var le *LookupError
			if errors.As(err, &le) {
				writeError(w, le.Status, le.Code, le.Message)
				return
			}
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromOverview(ov))
	}
}

// statusOverview は status の Overview を返す。ストアの読み取りの借りを持つのは、ストアを読む間
// （基本の Overview と waiting_external の候補の導出）だけで、gh によるチェックの取得は借りを
// 返した後に行う（遅い gh が、ストアの状態更新の Lock を待たせないように）。
func statusOverview(ctx context.Context, fleet *Fleet, name string, checks *checkCache) (*core.Overview, error) {
	st, release, le := fleet.Acquire(ctx, name)
	if le != nil {
		return nil, le
	}
	ov, cands, err := func() (*core.Overview, []core.WaitingExternalCandidate, error) {
		defer release()
		ov, err := baseOverview(ctx, st)
		if err != nil {
			return nil, nil, err
		}
		cands, err := st.WaitingExternalCandidates(ctx)
		return ov, cands, err
	}()
	if err != nil {
		return nil, err
	}
	ov.WaitingExternal = core.ResolveWaitingExternal(ctx, cands, checks.checkSource())
	return ov, nil
}
