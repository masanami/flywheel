package server

import (
	"net/http"
)

// cycleStopHandler は POST …/cycle/stop（サイクルを止める指示）。成功の本文は view.CycleStopResponse。実装は後続のタスクが埋める。
func cycleStopHandler(fleet *Fleet) http.HandlerFunc {
	_ = fleet
	return notImplementedPost
}
