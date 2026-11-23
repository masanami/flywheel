package server

import (
	"net/http"
)

// noticesHandler は GET …/notices（起きたことの知らせ。新しい順に直近の 20 件まで）。成功の本文は view.NoticesResponse。実装は後続のタスクが埋める。
func noticesHandler(fleet *Fleet) http.HandlerFunc {
	_ = fleet
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		writeError(w, http.StatusNotFound, CodeNotFound, "not found") // 中身を置くまでは、ルートが無いときと同じ
	}
}
