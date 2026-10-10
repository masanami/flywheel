package server

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/masanami/flywheel/internal/core"
)

// ワークスペースのストアの状態（`GET /api/v1/workspaces` の state）。閉集合。
const (
	StateOK            = "ok"
	StateStoreNotFound = "store_not_found"
	StateStoreTooNew   = "store_too_new"
	StateStoreError    = "store_error"
)

// CodeStoreNotFound などは、ok でないワークスペースへの要求に返すエラーコード。
// state と同じ値で、internal/cli のエラーコードの表とも一致する。
const (
	CodeStoreNotFound = StateStoreNotFound
	CodeStoreTooNew   = StateStoreTooNew
	CodeStoreError    = StateStoreError
)

// LookupError は Fleet.Acquire が返す、要求を処理できない理由。
// Status は HTTP の状態コード、Code は応答の error.code。
type LookupError struct {
	Status  int
	Code    string
	Message string
}

func (e *LookupError) Error() string { return e.Message }

// Fleet は server が束ねるワークスペースの集合。状態は要求ごとに判定し直し、
// ストアには独自の行・ロックを作らない（開くのは core だけ）。
type Fleet struct {
	entries []*fleetEntry
	byName  map[string]*fleetEntry
	latest  int
	latestE error
}

type fleetEntry struct {
	name string
	path string

	mu     sync.RWMutex
	latest int         // バイナリの知る最新のスキーマ版（NewFleet が設定）
	store  *core.Store // state が ok の間だけ保持する
	state  string
	err    error
}

// NewFleet は宣言の順にワークスペースを並べた Fleet を作る。ストアはここでは
// 開かない（開けなくても失敗しない）。
func NewFleet(workspaces []core.FleetWorkspace) *Fleet {
	f := &Fleet{byName: map[string]*fleetEntry{}}
	f.latest, f.latestE = core.LatestSchemaVersion()
	for _, w := range workspaces {
		e := &fleetEntry{name: w.Name, path: w.Path, state: StateStoreError, latest: f.latest}
		f.entries = append(f.entries, e)
		f.byName[w.Name] = e
	}
	return f
}

// Close は保持しているストアをすべて閉じる。
func (f *Fleet) Close() {
	for _, e := range f.entries {
		// 借りが残っている（終了を待たずに閉じた）ストアは閉じない。プロセスが終わる。
		if e.mu.TryLock() {
			e.closeStore()
			e.mu.Unlock()
		}
	}
}

// WorkspaceStatus は 1 つのワークスペースの、その時点で判定し直した状態。
type WorkspaceStatus struct {
	Name  string
	Path  string
	State string
	Err   error // State が ok のとき nil
}

// Statuses は宣言の順に、全ワークスペースの状態を判定し直して返す。
func (f *Fleet) Statuses(ctx context.Context) []WorkspaceStatus {
	out := make([]WorkspaceStatus, 0, len(f.entries))
	for _, e := range f.entries {
		_, release, st := e.lease(ctx, f)
		release()
		out = append(out, WorkspaceStatus{Name: e.name, Path: e.path, State: st.State, Err: st.Err})
	}
	return out
}

// Acquire は名前から、状態を判定し直したうえでそのワークスペースのストアを借りる。
// 戻り値の release は借りた側が必ず呼ぶ（何度呼んでもよい）。借りている間はストアは
// 閉じられない（同じワークスペースの要求どうしは並行に進める）。fleet に無い名前は
// 404 not_found、ok でないものはその state のエラーコード（store_not_found は 404、
// store_too_new・store_error は 503）。
func (f *Fleet) Acquire(ctx context.Context, name string) (*core.Store, func(), *LookupError) {
	e, ok := f.byName[name]
	if !ok {
		return nil, nil, &LookupError{Status: http.StatusNotFound, Code: CodeNotFound, Message: "workspace " + name + " is not in the fleet"}
	}
	s, release, st := e.lease(ctx, f)
	if st.State != StateOK {
		release()
		return nil, nil, &LookupError{Status: statusForState(st.State), Code: st.State, Message: st.Err.Error()}
	}
	return s, release, nil
}

func statusForState(state string) int {
	if state == StateStoreNotFound {
		return http.StatusNotFound
	}
	return http.StatusServiceUnavailable
}

// lease は e の状態を判定し直し、ok なら読み取りの借り（RLock）を持ったまま Store を
// 返す。release は借りを返す（冪等）。ok でなければ借りは持たず、release は何もしない。
// 状態が変わる（ストアを開く・閉じる）ときだけ排他の Lock を取る。
func (e *fleetEntry) lease(ctx context.Context, f *Fleet) (*core.Store, func(), WorkspaceStatus) {
	noop := func() {}
	for attempt := 0; ; attempt++ {
		e.mu.RLock()
		if s, st, settled := e.checkHeld(ctx, f); settled {
			if st.State == StateOK {
				var once sync.Once
				return s, func() { once.Do(e.mu.RUnlock) }, st
			}
			e.mu.RUnlock()
			return nil, noop, st
		}
		e.mu.RUnlock()

		e.mu.Lock()
		e.refresh(ctx)
		st := WorkspaceStatus{Name: e.name, Path: e.path, State: e.state, Err: e.err}
		e.mu.Unlock()
		if st.State != StateOK {
			return nil, noop, st
		}
		if attempt >= 3 {
			// 状態が落ち着かない（借りようとするたびに変わった）。借りは渡さない。
			return nil, noop, WorkspaceStatus{Name: e.name, Path: e.path, State: StateStoreError,
				Err: errors.New("workspace state kept changing; retry the request")}
		}
		// ok になったので、借り直す（その間に変わっていれば最初からやり直す）。
	}
}

// checkHeld は RLock の下で、保持している Store でこの要求を処理できるかを調べる。
// settled=false は、Lock を取って状態を更新する必要があることを表す。
func (e *fleetEntry) checkHeld(ctx context.Context, f *Fleet) (*core.Store, WorkspaceStatus, bool) {
	st := WorkspaceStatus{Name: e.name, Path: e.path, State: e.state, Err: e.err}
	if e.store == nil {
		return nil, st, false // ok でないものは要求のたびに開き直す
	}
	if !e.store.IsCurrent() {
		return nil, st, false // 削除・差し替えられた
	}
	v, err := e.store.SchemaVersion(ctx)
	switch {
	case err == nil && f.latestE == nil && v <= f.latest:
		return e.store, st, true
	case ctx.Err() != nil || errors.Is(err, core.ErrStoreBusy):
		// 要求の取り消し・一時的な競合はストアの状態ではない。状態を変えない。
		return e.store, st, true
	}
	return nil, st, false
}

// refresh は e の状態を判定し直す（e.mu の Lock を持って呼ぶ）。
func (e *fleetEntry) refresh(ctx context.Context) {
	if e.store != nil {
		v, err := e.store.SchemaVersion(ctx)
		switch {
		case !e.store.IsCurrent():
			e.closeStore() // 削除・差し替え。下で開き直して判定する
		case err == nil && v <= e.latest:
			e.set(e.store, StateOK, nil)
			return
		case err == nil, errors.Is(err, core.ErrStoreTooNew):
			e.set(nil, StateStoreTooNew, core.ErrStoreTooNew)
			return
		case ctx.Err() != nil || errors.Is(err, core.ErrStoreBusy):
			return
		default:
			e.set(nil, StateStoreError, err)
			return
		}
	}
	s, err := core.OpenWorkspace(e.path)
	switch {
	case err == nil:
		e.set(s, StateOK, nil)
	case errors.Is(err, core.ErrStoreNotFound):
		e.set(nil, StateStoreNotFound, err)
	case errors.Is(err, core.ErrStoreTooNew):
		e.set(nil, StateStoreTooNew, err)
	default:
		e.set(nil, StateStoreError, err)
	}
}

// set は状態を更新する。ok でなくなるときは保持していた Store を閉じる。
func (e *fleetEntry) set(s *core.Store, state string, err error) {
	if state != StateOK {
		e.closeStore()
	}
	if state != StateOK && err == nil {
		err = errors.New(state)
	}
	e.store, e.state, e.err = s, state, err
}

func (e *fleetEntry) closeStore() {
	if e.store != nil {
		_ = e.store.Close()
		e.store = nil
	}
}
