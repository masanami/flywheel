package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の会話の一覧・閉じた会話の続き・全文の口（docs/features/chat-session.md「画面・API設計」）。
// 全文は、読むたびに実行エンジンの側（core.ChatConversationInvoker）が読み、ストアへ写さない。

// CodeTranscriptUnavailable は会話の全文のファイルが見つからない・読めないときのエラーコード
// （全文の口では HTTP 404、続ける口では 409）。
const CodeTranscriptUnavailable = "transcript_unavailable"

// transcriptAvailable は、workDir の会話の全文のファイルが見つかるかを返す関数を作る。実行エンジンが
// 組み込まれていなければ nil（core はどの会話も「見つからない」として扱う）。
func (c *chatRunner) transcriptAvailable(ctx context.Context, workDir string) func(string) bool {
	inv := c.engine()
	if inv == nil {
		return nil
	}
	return func(engineSession string) bool {
		return inv.HasTranscript(ctx, core.ChatTranscriptRef{SessionID: engineSession, WorkDir: workDir})
	}
}

// chatSessionsHandler は `GET …/chat/sessions`。
func chatSessionsHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		st, release, le := c.fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		sessions, err := st.ListChatSessions(r.Context(), c.transcriptAvailable(r.Context(), st.Workspace()))
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromChatSessions(sessions))
	}
}

// chatResumeHandler は `POST …/chat/sessions/{id}/resume`。判定の順は core が持つ。成功したら、選んだ会話の
// ものでないプロセスを止める（開いている会話を指したときは、そのプロセスはそのまま）。
func chatResumeHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name, engineSession := r.PathValue("name"), r.PathValue("id")
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
		if err := st.ResumeChatSession(r.Context(), engineSession, c.transcriptAvailable(r.Context(), st.Workspace())); err != nil {
			switch {
			case errors.Is(err, core.ErrChatReplying):
				writeError(w, http.StatusConflict, CodeConflict, err.Error())
			case errors.Is(err, core.ErrChatTranscriptUnavailable):
				writeError(w, http.StatusConflict, CodeTranscriptUnavailable, err.Error())
			default:
				writeCoreError(w, err)
			}
			return
		}
		release() // プロセスの終わりを待つ間は、ストアを借りない
		if p := a.current(); p != nil && p.engineSession != engineSession {
			ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
			defer cancel()
			// 会話は開き直し終えたので、止め切れなくても成功にする（次の発言の前と、サーバの停止で止め直す）。
			_ = c.stopProcess(ctx, a, p)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// chatTranscriptHandler は `GET …/chat/sessions/{id}/transcript`。読むのは、このエージェントのストアに
// ある会話だけ（実行エンジンは会話の識別でファイルを探すので、ほかの場所の会話を指させない）。
func chatTranscriptHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		engineSession := r.PathValue("id")
		st, release, le := c.fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		release = sync.OnceFunc(release)
		defer release()
		workDir := st.Workspace()
		sessions, err := st.ListChatSessions(r.Context(), nil)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		release() // ファイルを読む間は、ストアを借りない
		if !slices.ContainsFunc(sessions, func(s core.ChatSessionSummary) bool { return s.EngineSession == engineSession }) {
			writeError(w, http.StatusNotFound, CodeNotFound, "その会話は無い")
			return
		}
		inv := c.engine()
		if inv == nil {
			writeError(w, http.StatusNotFound, CodeTranscriptUnavailable, "対話の実行エンジンが組み込まれていない")
			return
		}
		entries, err := inv.ReadTranscript(r.Context(), core.ChatTranscriptRef{SessionID: engineSession, WorkDir: workDir})
		if err != nil {
			writeError(w, http.StatusNotFound, CodeTranscriptUnavailable, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view.FromChatTranscript(entries))
	}
}
