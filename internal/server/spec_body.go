package server

import (
	"net/http"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// challengeSpecHandler は GET …/challenges/{id}/spec（読み取りだけ）。受け取るのは課題の ID だけで、読む場所は core が決める
// （クエリは読まない）。GET 以外は 405（CSRF の値の無い POST は、ルートより手前の検査が 403 で断る）。
func challengeSpecHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, CodeUsageError, "method not allowed")
			return
		}
		var spec *core.ChallengeSpec
		err := readForCards(r.Context(), fleet, r.PathValue("name"), func(st *core.Store) (err error) {
			spec, err = st.ReadChallengeSpec(r.Context(), r.PathValue("id"))
			return err
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromChallengeSpec(spec))
	}
}
