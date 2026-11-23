package server

import (
	"net/http"
)

// followupsDoneHandler は POST …/followups/done（フォローアップを済みにする）。本文は view.FollowupDoneRequest、成功の本文は view.FollowupListResponse。
// 実装は後続のタスクが埋める。
func followupsDoneHandler(fleet *Fleet) http.HandlerFunc {
	_ = fleet
	return notImplementedPost
}
