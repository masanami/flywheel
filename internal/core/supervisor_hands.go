package core

// このファイルは、スーパーバイザーの判断点へ渡す「選べる手の一覧」を作る（区切り S4b-2）。
// いまは、止まり方ごとの表（supervisorHandsByStopKind。閉じた一覧は 1 か所）をそのまま返す。
// いまの状態で実行できない手を外す規則（事前に外す手）は、この関数を書き換えて足す（別のタスク）。

// offeredSupervisorHands は止まり方 k で、判断点へ渡す選べる手の一覧を返す（表の写し。状態では絞らない）。
func offeredSupervisorHands(k StopKind) []SupervisorHand {
	return SupervisorHandsFor(k)
}

// supervisorHandDescription は選べる手の一覧に添える、手ごとの 1 行の説明。
func supervisorHandDescription(h SupervisorHand) string {
	switch h {
	case HandReverify:
		return "調べた結果を添えて、検証（受け取り）をやり直す"
	case HandSendBack:
		return "添え書きを付けて、委譲先へ差し戻す"
	case HandRetry:
		return "同じ入力でやり直す"
	case HandSplitFollowup:
		return "残った条件をフォローアップへ回して、ほかの条件で判定する"
	case HandReplan:
		return "計画を作り直す（作り直した計画の承認はオーナー）"
	case HandDefer:
		return "このサイクルでは見送る"
	case HandEscalate:
		return "調べた内容と推奨を付けて、オーナーへ上げる"
	}
	return ""
}
