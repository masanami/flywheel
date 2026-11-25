package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の確認（許可の確認と問い）への答えの口（docs/features/chat-session.md「画面・API設計」）。
// 受けられる確認と答えの形は core が決める。実行エンジンへ送って記録する順は chat_process.go が持つ。

// chatAnswerHandler は `POST …/chat/prompts/{id}/answer`。
func chatAnswerHandler(c *chatRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		name := r.PathValue("name")
		if !c.knownAgent(w, r, name) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, CodeNotFound, "その確認は無い")
			return
		}
		var req view.ChatPromptAnswerRequest
		if !decodeChatBody(r, &req) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, `本文は、許可の確認なら {"decision": "allow"|"deny", "message"（断るときだけ。省略可）}、問いなら {"answers": {<問いの文>: <答えの文>}} だけを受け付ける`)
			return
		}
		err = c.answerPrompt(r.Context(), c.agent(name), name, id, req.ToCore())
		var le *LookupError
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.As(err, &le):
			writeError(w, le.Status, le.Code, le.Message)
		case errors.Is(err, core.ErrChatPromptNotPending), errors.Is(err, errChatAnswerNotSent):
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
		default:
			writeCoreError(w, err)
		}
	}
}
