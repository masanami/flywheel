package server

import (
	"net/http"

	"github.com/masanami/flywheel/internal/view"
)

// cycleStopHandler は POST …/cycle/stop（サイクルを止める指示）。成功の本文は view.CycleStopResponse。
// ターミナルの `cycle stop` と同じ core の口を呼ぶ（走っていなければ何も変えない）。本人確認は要らず、登録簿・誰が出したかの記録は足さない。
func cycleStopHandler(fleet *Fleet) http.HandlerFunc {
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
		res, err := st.RequestCycleStop(r.Context())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		s := view.FromCycleStopResult(res)
		// 走っていたか: 指示を出したなら走っていた。出さなかったのは、走っていなかったか、すでに指示が出ていたとき。
		writeJSON(w, http.StatusOK, view.CycleStopResponse{CycleID: s.CycleID, Requested: s.Requested, Running: s.CycleID != nil})
	}
}
