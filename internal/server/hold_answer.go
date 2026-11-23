package server

import (
	"net/http"
)

// holdAnswerHandler は POST …/challenges/{id}/holds/{hold}/answer（画面のボタンで選ぶだけの回答）。本文は view.HoldAnswerRequest、成功の本文は view.HoldAnswerResponse。
// 実装は後続のタスクが埋める（誤りの対応は apierror.go の表のとおり。経路 ui の回答の限りの強制は core）。
func holdAnswerHandler(fleet *Fleet, launch CycleStarter) http.HandlerFunc {
	_, _ = fleet, launch
	return notImplementedPost
}
