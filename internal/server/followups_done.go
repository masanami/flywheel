package server

import (
	"net/http"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// followupsDoneHandler は POST …/followups/done（フォローアップを済みにする）。本文は view.FollowupDoneRequest、成功の本文は view.FollowupListResponse。
// 決まりは `flywheel followup done` と同じ core の口（CompleteFollowups。全部か無しか）で、作業ログの経路は ui。登録簿は変えない。
func followupsDoneHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		var req view.FollowupDoneRequest
		if !decodeScreenBody(w, r, &req) {
			return
		}
		if !distinctNonEmpty(req.IDs) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "ids は 1 件以上で、同じ ID を 2 度指定できない")
			return
		}
		st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		fs, err := st.CompleteFollowups(r.Context(), core.ChannelUI, req.IDs, req.Note)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromFollowupList(fs))
	}
}
