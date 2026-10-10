package core

import (
	"context"
	"os"
	"sync"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// WorkspaceChange は WorkspaceWatcher.Poll の 1 回の結果。
type WorkspaceChange struct {
	// Available はストアを観測できているか。ストアが無い・開けないワークスペースでは
	// false（エラーでは返さず、状態として返す）。
	Available bool
	// Changed は前回の Poll から変化があったか。最初の Poll は基準を取るだけで false。
	// 観測できる状態から観測できない状態へ（またはその逆へ）変わったときも true。
	Changed bool
}

// WorkspaceWatcher は 1 つのワークスペースのストアの変化を観測する。
// 書き込み・閲覧の接続（Store）とは別の、固定した 1 本の専用の接続で
// PRAGMA data_version を読む。CLI・cycle・別の server の書き込みを、作業ログに
// 載らない run・slot の変化（heartbeat の更新だけを含む）を含めて検出する。
// 呼ぶ間隔は呼び出し側（server）が決める。並行に呼んでも安全だが、変化は
// 1 回の Poll にだけ返る（前回値はウォッチャーに 1 つ）。複数の購読者へは呼び出し側が
// 1 か所の Poll の結果を配る。
type WorkspaceWatcher struct {
	storePath string

	mu      sync.Mutex
	obs     *store.Observer
	info    os.FileInfo // obs を開いたときのストアのファイル。差し替えを検出する
	version int64
	started bool // 基準を取ったか
	avail   bool // 前回の Poll の Available
}

// NewWorkspaceWatcher は workspaceDir（ワークスペースの絶対パス）のストアを観測する
// WorkspaceWatcher を返す。この時点ではストアを開かない（開けなくても失敗しない）。
func NewWorkspaceWatcher(workspaceDir string) *WorkspaceWatcher {
	return &WorkspaceWatcher{storePath: storeDBPath(workspaceDir)}
}

// Poll はストアの現在の値を読み、前回から変わったかを返す。
func (w *WorkspaceWatcher) Poll(ctx context.Context) WorkspaceChange {
	w.mu.Lock()
	defer w.mu.Unlock()

	available, changed := w.observe(ctx)
	if ctx.Err() != nil {
		// 呼び出し側の取り消し・期限切れはストアの状態ではない。状態を変えずに返す。
		return WorkspaceChange{Available: w.avail}
	}
	first := !w.started
	w.started = true
	prevAvail := w.avail
	w.avail = available
	if first {
		return WorkspaceChange{Available: available}
	}
	return WorkspaceChange{Available: available, Changed: changed || available != prevAvail}
}

// observe は接続を（必要なら開き直して）読み、(観測できたか, 値が変わったか) を返す。
// 開き直したときの値は前の接続の値と比べられないので、変わったものとして扱う。
func (w *WorkspaceWatcher) observe(ctx context.Context) (available, changed bool) {
	info, err := os.Stat(w.storePath)
	if err != nil || info.IsDir() {
		w.closeObserver()
		return false, false
	}
	if w.obs != nil && !os.SameFile(w.info, info) {
		w.closeObserver() // ストアのファイルが差し替えられた
	}
	reopened := false
	if w.obs == nil {
		obs, err := store.OpenObserver(ctx, w.storePath)
		if err != nil {
			return false, false
		}
		w.obs, w.info, reopened = obs, info, true
	}
	v, err := w.obs.DataVersion(ctx)
	if err != nil {
		w.closeObserver()
		return false, false
	}
	prev := w.version
	w.version = v
	return true, reopened || v != prev
}

func (w *WorkspaceWatcher) closeObserver() {
	if w.obs != nil {
		_ = w.obs.Close()
	}
	w.obs, w.info = nil, nil
}

// Close は専用の接続を閉じる。Close 後に Poll すると接続を開き直す。
func (w *WorkspaceWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeObserver()
	w.started = false // 閉じたあとの最初の Poll は基準を取り直す
	return nil
}
