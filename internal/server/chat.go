package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の口（docs/features/core-redesign-s4d.md S4d-1「画面・API設計」）。
// 返事の進みはストアへ書き、サーバがストアの外に持つのは、進行中の返事の実行エンジンを止める手段
// （chatRun.cancel）だけ。実行エンジンの起動は core.ChatInvoker（internal/invoker が実装する）に任せる。

// CodeEngineUnavailable は実行エンジンのコマンドが見つからないときのエラーコード（HTTP 503）。
const CodeEngineUnavailable = "engine_unavailable"

const (
	// chatBodyLimit は POST の本文の上限（バイト）。
	chatBodyLimit = 1 << 20
	// chatStopWait は stop が、取り消した返事が終わるのを待つ上限。
	chatStopWait = 10 * time.Second
)

// chatRun は進行中の返事 1 つ。
type chatRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// chatRunner は欄の実行エンジンの起動と、進行中の返事の管理を持つ。ワークスペースごとに高々 1 つ。
type chatRunner struct {
	fleet *Fleet

	mu      sync.Mutex
	invoker core.ChatInvoker
	// executable は flywheel の名前で PATH に置くサーバの実行ファイルを返す（テストが差し替える）。
	executable func() (string, error)
	runs       map[string]*chatRun
	closing    bool
	wg         sync.WaitGroup
}

func newChatRunner(fleet *Fleet) *chatRunner {
	return &chatRunner{fleet: fleet, executable: os.Executable, runs: map[string]*chatRun{}}
}

func (c *chatRunner) engine() core.ChatInvoker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invoker
}

// recoverInterrupted は起動のとき、すべてのワークスペースのストアを 1 回ずつ見て、進行中のまま残った返事を
// stopped にする。ストアの無い（開けない）ワークスペースは、作らずに飛ばす。ストアは Fleet を通さず、見るだけ
// 開いてすぐ閉じる（進行中の返事が無ければ書かず、-wal・-shm も残さない）。
func (c *chatRunner) recoverInterrupted(ctx context.Context) {
	for _, e := range c.fleet.entries {
		st, err := core.OpenWorkspace(e.path)
		if err != nil {
			continue
		}
		if chat, err := st.ReadChat(ctx); err == nil && chat.Replying {
			_, _ = st.StopInterruptedChatReplies(ctx)
		}
		_ = st.Close()
	}
}

// stopAll は進行中の返事をすべて取り消し、終わりが記録されるまで待つ。以後は新しい返事を始めない。
func (c *chatRunner) stopAll() {
	c.mu.Lock()
	c.closing = true
	for _, r := range c.runs {
		r.cancel()
	}
	c.mu.Unlock()
	// 実行エンジンが取り消しに応じなくても、Shutdown を縛らない（待ちの上限は Shutdown と同じ）。
	waited := make(chan struct{})
	go func() { c.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(shutdownTimeout):
	}
}

// register は name の返事を登録する。停止中なら false。同じ name の前の返事は、ストアの上ではもう終わっていて
// 後始末の最中なので、置き換える（unregister は自分の登録だけを消す）。
func (c *chatRunner) register(name string, r *chatRun) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	c.runs[name] = r
	c.wg.Add(1)
	return true
}

func (c *chatRunner) unregister(name string, r *chatRun) {
	c.mu.Lock()
	if c.runs[name] == r {
		delete(c.runs, name)
	}
	c.mu.Unlock()
}

// withStore はバックグラウンドの書き込みのためにストアを借りる（借りられなければ何もしない）。
func (c *chatRunner) withStore(name string, fn func(st *core.Store)) {
	st, release, le := c.fleet.Acquire(context.Background(), name)
	if le != nil {
		return
	}
	defer release()
	fn(st)
}

// chatReply は返事 1 回の実行（goroutine で動く）。
func (c *chatRunner) chatReply(ctx context.Context, name string, run *chatRun, inv core.ChatInvoker, in core.ChatLaunchInput, turn *core.ChatTurn, cleanup func()) {
	defer c.wg.Done()
	defer close(run.done)
	defer c.unregister(name, run)
	defer cleanup()

	bg := context.Background()
	ev := core.ChatEvents{
		Started: func() {
			c.withStore(name, func(st *core.Store) { _ = st.MarkChatEngineStarted(bg, turn.SessionID) })
		},
		Speech: func(text string) {
			c.withStore(name, func(st *core.Store) { _ = st.AppendChatSpeech(bg, turn.ReplyMessageID, text) })
		},
		Command: func(cmd core.ChatCommand) {
			c.withStore(name, func(st *core.Store) { _ = st.AppendChatCommand(bg, turn.ReplyMessageID, cmd) })
		},
	}
	out, err := inv.InvokeChat(ctx, in, ev)
	state, cost := out.State, out.CostUSD
	switch {
	case err != nil && ctx.Err() != nil:
		state, cost = core.ChatStateStopped, 0
	case err != nil:
		state, cost = core.ChatStateFailed, 0
	case state == core.ChatStateReplying || !validEndState(state):
		state = core.ChatStateFailed
	}
	if cost < 0 {
		cost = 0
	}
	// 終わりの記録に失敗すると、返事の行が replying のまま残り、次の文も「新しい会話」も 409 になる。
	// 短く間を置いて数回やり直す（それでも書けなければ、stop が行の側から止められる）。
	for attempt := 0; attempt < 5; attempt++ {
		done := false
		c.withStore(name, func(st *core.Store) {
			err := st.FinishChatReply(bg, turn.ReplyMessageID, state, cost)
			done = err == nil || errors.Is(err, core.ErrValidation) || errors.Is(err, core.ErrNotFound)
		})
		if done {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func validEndState(s core.ChatMessageState) bool {
	for _, v := range core.ChatEndStates() {
		if v == s {
			return true
		}
	}
	return false
}

// prepareChatBin は一時のディレクトリを作り、flywheel の名前でサーバの実行ファイルへのシンボリックリンクを置く。
// 戻り値は、そのディレクトリと、消す関数。
func (c *chatRunner) prepareChatBin() (string, func(), error) {
	exe, err := c.executable()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "flywheel-chat-")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	if err := os.Symlink(exe, filepath.Join(dir, "flywheel")); err != nil {
		remove()
		return "", nil, err
	}
	return dir, remove, nil
}

// chatGetHandler は `GET …/chat`。
func chatGetHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		tools := core.ChatToolsRestricted
		if decl := agentDeclOrNil(st); decl != nil && decl.ChatTools != "" {
			tools = decl.ChatTools
		}
		chat, err := st.ReadChat(r.Context())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromChat(chat, tools))
	}
}

func decodeChatPost(r *http.Request) (view.ChatPostRequest, bool) {
	var req view.ChatPostRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, chatBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, false
	}
	return req, true
}

// chatMessagesHandler は `POST …/chat/messages`。
func chatMessagesHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		st, release, le := c.fleet.Acquire(r.Context(), name)
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()

		req, ok := decodeChatPost(r)
		if !ok || strings.TrimSpace(req.Text) == "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, `本文は {"text": <空でない文字列>, "viewing": {"challenge_id", "task_id"}（省略可）} だけを受け付ける`)
			return
		}
		inv := c.engine()
		if inv == nil {
			writeError(w, http.StatusServiceUnavailable, CodeEngineUnavailable, "対話の実行エンジンが組み込まれていない")
			return
		}
		if err := inv.Available(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, CodeEngineUnavailable, err.Error())
			return
		}
		decl, err := core.LoadAgentDeclaration(st.Workspace())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		binDir, cleanup, err := c.prepareChatBin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternalError, "実行エンジンの PATH を用意できない: "+err.Error())
			return
		}
		var viewing *core.ChatViewing
		if req.Viewing != nil {
			viewing = &core.ChatViewing{ChallengeID: req.Viewing.ChallengeID, TaskID: req.Viewing.TaskID}
		}
		turn, err := st.PostChatMessage(r.Context(), req.Text, viewing)
		if err != nil {
			cleanup()
			if errors.Is(err, core.ErrChatReplying) {
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
				return
			}
			writeCoreError(w, err)
			return
		}

		in := core.ChatLaunchInput{
			Text:       req.Text,
			Viewing:    turn.Viewing,
			SessionID:  turn.EngineSession,
			Resume:     turn.Resume,
			WorkDir:    st.Workspace(),
			Env:        append(core.ChatMarkEnv(turn.EngineSession, turn.OwnerMessageID, decl.ChatTools), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH")),
			Tools:      decl.ChatTools,
			MaxTurns:   decl.ChatMaxTurns,
			TimeoutSec: decl.ChatTimeoutSec,
		}
		ctx, cancel := context.WithCancel(context.Background())
		run := &chatRun{cancel: cancel, done: make(chan struct{})}
		if !c.register(name, run) {
			// 停止中（サーバを止めている最中）。返事の行は止めた状態にして、断る。
			cancel()
			cleanup()
			_ = st.FinishChatReply(r.Context(), turn.ReplyMessageID, core.ChatStateStopped, 0)
			writeError(w, http.StatusServiceUnavailable, CodeEngineUnavailable, "サーバを停止している")
			return
		}
		go c.chatReply(ctx, name, run, inv, in, turn, func() { cancel(); cleanup() })
		writeJSON(w, http.StatusOK, view.ChatPostResponse{ID: turn.OwnerMessageID})
	}
}

// chatStopHandler は `POST …/chat/stop`。進行中の返事が無ければ何も変えずに成功を返す。
func chatStopHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		st, release, le := c.fleet.Acquire(r.Context(), name)
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		c.mu.Lock()
		run := c.runs[name]
		c.mu.Unlock()
		if run == nil {
			// このサーバが持つ返事が無いのに、ストアが進行中のままなら（終わりを書けなかった等）、行の側を止める。
			if chat, err := st.ReadChat(r.Context()); err == nil && chat.Replying {
				_, _ = st.StopInterruptedChatReplies(r.Context())
			}
		} else {
			run.cancel()
			select {
			case <-run.done:
			case <-time.After(chatStopWait):
			case <-r.Context().Done():
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chatNewHandler は `POST …/chat/new`。返事が進行中なら 409。
func chatNewHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		if err := st.CloseChat(r.Context()); err != nil {
			if errors.Is(err, core.ErrChatReplying) {
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
				return
			}
			writeCoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
