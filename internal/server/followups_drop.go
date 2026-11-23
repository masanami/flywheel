package server

import (
	"net/http"
)

// followupsDropHandler は POST …/followups/drop（フォローアップを捨てる）。本文は view.FollowupDropRequest、成功の本文は view.FollowupListResponse。
// 経路 ui・本人確認なし。実装は後続のタスクが埋める。
func followupsDropHandler(fleet *Fleet) http.HandlerFunc {
	_ = fleet
	return notImplementedPost
}
