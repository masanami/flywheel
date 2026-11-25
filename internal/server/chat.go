package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の口のうち、読み取りと、発言・止める・新しい会話（docs/features/chat-session.md
// 「画面・API設計」）。確認への答えは chat_prompts.go、会話の一覧・続ける・全文は chat_sessions.go。会話の
// 実行エンジンのプロセスの寿命は chat_process.go が持つ。

// CodeEngineUnavailable は実行エンジンのコマンドが見つからないときのエラーコード（HTTP 503）。
const CodeEngineUnavailable = "engine_unavailable"

// chatBodyLimit は POST の本文の上限（バイト）。
const chatBodyLimit = 1 << 20

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
		chat, err := st.ReadChat(r.Context())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromChat(chat))
	}
}

// decodeChatBody は本文の JSON を 1 つだけ読む。知らないキーと、後に続く値は受けない。
func decodeChatBody(r *http.Request, into any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, chatBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// knownAgent は name のエージェントのストアを借りられるかを確かめ、借りられなければ誤りを返す。ストアはすぐ返す。
//
// 排他の順は chatAgent.op が先で、ストアの貸し出しが後。プロセスを止めて待つ間はストアを借りない（プロセスの
// 終わりの記録もストアを借りるので、間に貸し出しの書き込みの待ちが入ると進まなくなる）。
func (c *chatRunner) knownAgent(w http.ResponseWriter, r *http.Request, name string) bool {
	_, release, le := c.fleet.Acquire(r.Context(), name)
	if le != nil {
		writeError(w, le.Status, le.Code, le.Message)
		return false
	}
	release()
	return true
}

// chatMessagesHandler は `POST …/chat/messages`。
func chatMessagesHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		if !c.knownAgent(w, r, name) {
			return
		}

		var req view.ChatPostRequest
		if ok := decodeChatBody(r, &req); !ok || strings.TrimSpace(req.Text) == "" {
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
		a := c.agent(name)
		a.op.Lock()
		defer a.op.Unlock()
		if c.isClosing() {
			writeError(w, http.StatusServiceUnavailable, CodeEngineUnavailable, "サーバを停止している")
			return
		}
		st, release, le := c.fleet.Acquire(r.Context(), name)
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		release = sync.OnceFunc(release)
		defer release()
		workDir := st.Workspace()
		// 宣言が読めない（無くした項目が残っている場合を含む）まま欄を動かさない。起動したままのプロセスが
		// あっても、発言のたびに確かめる。
		if _, err := core.LoadAgentDeclaration(workDir); err != nil {
			writeCoreError(w, err)
			return
		}
		var viewing *core.ChatViewing
		if req.Viewing != nil {
			viewing = &core.ChatViewing{ChallengeID: req.Viewing.ChallengeID, TaskID: req.Viewing.TaskID}
		}
		turn, err := st.PostChatMessage(r.Context(), req.Text, viewing)
		if err != nil {
			if errors.Is(err, core.ErrChatReplying) {
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
				return
			}
			writeCoreError(w, err)
			return
		}
		// deliver は前のプロセスを止めて待つことがあるので、先にストアを返す。
		release()
		if err := c.deliver(a, name, workDir, inv, turn, core.ChatUtterance{Text: req.Text, Viewing: turn.Viewing}); err != nil {
			// 発言は記録した後なので、返事を失敗として終える。
			c.finishReply(a, name, turn.ReplyMessageID, core.ChatStateFailed, nil)
		}
		writeJSON(w, http.StatusOK, view.ChatPostResponse{ID: turn.OwnerMessageID})
	}
}

// chatStopHandler は `POST …/chat/stop`。進行中の返事があれば、プロセスとその子を止めて返事を stopped にする。
// 無ければ何も変えずに成功を返す（動いているプロセスも止めない）。
func chatStopHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		if !c.knownAgent(w, r, name) {
			return
		}
		a := c.agent(name)
		a.op.Lock()
		defer a.op.Unlock()
		if p, replyID := a.replying(); p != nil {
			ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
			defer cancel()
			if err := c.stopProcess(ctx, a, p); err != nil {
				// 止め切れなくても、行は止めた状態にする（終わっていれば何も変わらない）。
				c.finishReply(a, name, replyID, core.ChatStateStopped, nil)
				writeError(w, http.StatusInternalServerError, CodeInternalError, "実行エンジンを止められない: "+err.Error())
				return
			}
		} else {
			// このサーバが持つ返事が無いのに、ストアが進行中のままなら（終わりを書けなかった等）、行の側を止める。
			a.settle.Lock()
			defer a.settle.Unlock()
			c.withStore(name, func(st *core.Store) {
				if chat, err := st.ReadChat(r.Context()); err == nil && chat.Replying {
					_, _ = st.StopInterruptedChatReplies(r.Context())
				}
			})
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chatNewHandler は `POST …/chat/new`。いまの会話を閉じて、そのプロセスを止める。返事が進行中なら 409 で、
// 会話もプロセスも変えない。
func chatNewHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		if !c.knownAgent(w, r, name) {
			return
		}
		a := c.agent(name)
		a.op.Lock()
		defer a.op.Unlock()
		st, release, le := c.fleet.Acquire(r.Context(), name)
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		release = sync.OnceFunc(release)
		defer release()
		if err := st.CloseChat(r.Context()); err != nil {
			if errors.Is(err, core.ErrChatReplying) {
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
				return
			}
			writeCoreError(w, err)
			return
		}
		release() // プロセスの終わりを待つ間は、ストアを借りない
		if p := a.current(); p != nil {
			ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
			defer cancel()
			// 会話は閉じ終えたので、止め切れなくても成功にする（次の発言の前と、サーバの停止で止め直す）。
			_ = c.stopProcess(ctx, a, p)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
