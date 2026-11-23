package server

import (
	"net/http"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// followupsDropHandler は POST …/followups/drop（フォローアップを捨てる）。本文は view.FollowupDropRequest、成功の本文は view.FollowupListResponse。
// 決まりは `flywheel followup drop` と同じ core の口（DropFollowups。全部か無しか）で、経路 ui・本人確認なし（方式 none）。
func followupsDropHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		var req view.FollowupDropRequest
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
		// 理由・存在・未であることの検査は、書き込みの前に core が行う（本人確認の前に終える口と同じ）。
		if _, err := st.PrepareFollowupDrop(r.Context(), req.IDs, req.Reason); err != nil {
			writeCoreError(w, err)
			return
		}
		target, ok := core.FollowupAttestationTarget(req.IDs)
		if !ok {
			writeError(w, http.StatusNotFound, CodeNotFound, "フォローアップが見つからない")
			return
		}
		att, err := core.Verify(core.ChannelUI, noneVerifier{}, "", target)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		fs, err := st.DropFollowups(r.Context(), req.IDs, req.Reason, att)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromFollowupList(fs))
	}
}
