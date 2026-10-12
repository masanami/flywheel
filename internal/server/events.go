package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// SSE の時間の既定値。テストでは短くする（Server の内部の設定）。
const (
	defaultWatchInterval     = 1 * time.Second
	defaultKeepAliveInterval = 30 * time.Second
)

// eventsConfig は server の内部設定（SSE の時間と、カードの導出に使う現在時刻）。テストが差し替える。
type eventsConfig struct {
	watchInterval     time.Duration
	keepAliveInterval time.Duration
	// now はカードの導出に使う現在時刻（既定は time.Now。テストが固定する）。
	now func() time.Time
}

func (c eventsConfig) withDefaults() eventsConfig {
	if c.watchInterval <= 0 {
		c.watchInterval = defaultWatchInterval
	}
	if c.keepAliveInterval <= 0 {
		c.keepAliveInterval = defaultKeepAliveInterval
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// eventHub はワークスペースごとの core.WorkspaceWatcher を 1 か所で Poll し、
// 変化したワークスペースの名前を購読者全員へ配る。Poll の結果は 1 回の Poll にだけ
// 返るため、購読者ごとに Poll しない。gh は起動せず、ストアの観測だけを行う。
type eventHub struct {
	names    []string
	watchers []*core.WorkspaceWatcher

	mu      sync.Mutex // started と起動
	subsMu  sync.Mutex
	subs    map[chan string]struct{}
	started bool
	wg      sync.WaitGroup
}

func newEventHub(workspaces []core.FleetWorkspace) *eventHub {
	h := &eventHub{subs: map[chan string]struct{}{}}
	for _, w := range workspaces {
		h.names = append(h.names, w.Name)
		h.watchers = append(h.watchers, core.NewWorkspaceWatcher(w.Path))
	}
	return h
}

// ensureStarted は最初の購読で観測を始める。購読者がいない間はストアを開かない
// （購読の無い server は、要求の拒否などでストアのディレクトリを変えない）。
// 基準の Poll はここで同期して取るので、購読の開始後の変化は取りこぼさない。
// 観測は done が閉じるまで interval ごとに全ワークスペースを読む。
func (h *eventHub) ensureStarted(done <-chan struct{}, interval time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	h.started = true
	ctx, cancel := context.WithCancel(context.Background())
	h.poll(ctx) // 基準。最初の Poll は変化を返さない
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer cancel()
		defer func() {
			for _, w := range h.watchers {
				_ = w.Close()
			}
		}()
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				h.poll(ctx)
			}
		}
	}()
}

// wait は観測の goroutine が終わる（done が閉じた後）まで待つ。
func (h *eventHub) wait() {
	h.mu.Lock() // ensureStarted の Add と Wait が競合しないようにする
	defer h.mu.Unlock()
	h.wg.Wait()
}

func (h *eventHub) poll(ctx context.Context) {
	for i, w := range h.watchers {
		if ctx.Err() != nil {
			return
		}
		if w.Poll(ctx).Changed {
			h.publish(h.names[i])
		}
	}
}

func (h *eventHub) publish(name string) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- name:
		default: // 遅い購読者は待たせない（UI は通知を受けて取り直すだけ）
		}
	}
}

func (h *eventHub) subscribe() (<-chan string, func()) {
	ch := make(chan string, 64)
	h.subsMu.Lock()
	h.subs[ch] = struct{}{}
	h.subsMu.Unlock()
	return ch, func() {
		h.subsMu.Lock()
		delete(h.subs, ch)
		h.subsMu.Unlock()
	}
}

// eventsHandler は `GET /api/v1/events`（Server-Sent Events）。
func eventsHandler(h *eventHub, done <-chan struct{}, cfg eventsConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal_error", "streaming is not supported")
			return
		}
		ch, unsubscribe := h.subscribe()
		defer unsubscribe()
		h.ensureStarted(done, cfg.watchInterval)

		hd := w.Header()
		hd.Set("Content-Type", "text/event-stream")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		t := time.NewTicker(cfg.keepAliveInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case name := <-ch:
				b, err := json.Marshal(view.WorkspaceChanged{Type: "workspace_changed", Workspace: name})
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
					return
				}
				flusher.Flush()
			case <-t.C:
				if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
