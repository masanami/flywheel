package core

// このファイルは、スーパーバイザーのその場の対処（区切り S4b-2）が使う閉じた一覧を 1 か所に置く:
// 止まり方・手・対処の結果・断りの理由・保留の処理の状態、と「止まり方ごとに選べる手」の表。
// 子の問い（child_question。止まり方）と手 answer は S4b-3 で足す（ここには入れない）。

// StopKind は保留を作る呼び出し元が渡す、スーパーバイザーを呼ぶきっかけの種類（止まり方）。保留の理由から導かない。
type StopKind string

// StopKind の 9 値（child_question は入れない）。
const (
	// StopKindVerifyUncertain は、完了の検証・タスクの検証が「判断できない」を返した。
	StopKindVerifyUncertain StopKind = "verify_uncertain"
	// StopKindArtifactMissing は、委譲の報告の照合で成果物が見つからない。
	StopKindArtifactMissing StopKind = "artifact_missing"
	// StopKindDelegateStopped は、委譲先の実行が失敗・中止で終わった、または委譲の連続の失敗が上限に達した。
	StopKindDelegateStopped StopKind = "delegate_stopped"
	// StopKindFactsUnmet は、委譲先が返す値が宣言の求める値を満たさないレビュー待ちが続いた。
	StopKindFactsUnmet StopKind = "facts_unmet"
	// StopKindReworkLimit は、差し戻し・タスクの検証の連続の「満たしていない」が上限に達した。
	StopKindReworkLimit StopKind = "rework_limit"
	// StopKindChecksFailed は、検証した中身の確かめに失敗がある。
	StopKindChecksFailed StopKind = "checks_failed"
	// StopKindJudgmentFailed は、判断点の失敗・不正な出力が連続の上限に達した。
	StopKindJudgmentFailed StopKind = "judgment_failed"
	// StopKindIntakeUnverified は、統合先を使う課題で、タスクの成果を受け取れない。
	StopKindIntakeUnverified StopKind = "intake_unverified"
	// StopKindUpstreamUnreadable は、「入れてよい」を返した後、上流を読めない回りが続いた。
	StopKindUpstreamUnreadable StopKind = "upstream_unreadable"
)

var stopKindValues = []StopKind{
	StopKindVerifyUncertain, StopKindArtifactMissing, StopKindDelegateStopped, StopKindFactsUnmet, StopKindReworkLimit,
	StopKindChecksFailed, StopKindJudgmentFailed, StopKindIntakeUnverified, StopKindUpstreamUnreadable,
}

// StopKindValues は止まり方の閉集合を定義の順に返す（写し）。
func StopKindValues() []StopKind { return append([]StopKind(nil), stopKindValues...) }

// ParseStopKind は s を止まり方として解釈する。完全一致のみ受理する。
func ParseStopKind(s string) (StopKind, bool) {
	for _, v := range stopKindValues {
		if string(v) == s {
			return v, true
		}
	}
	return "", false
}

// SupervisorHand はスーパーバイザーが選ぶ手。
type SupervisorHand string

// SupervisorHand の 7 値（answer は入れない。S4b-3）。
const (
	// HandReverify は、調べた結果を添えて検証（受け取り）をやり直す。
	HandReverify SupervisorHand = "reverify"
	// HandSendBack は、添え書きを付けて委譲先へ差し戻す。
	HandSendBack SupervisorHand = "send_back"
	// HandRetry は、同じ入力でやり直す。
	HandRetry SupervisorHand = "retry"
	// HandSplitFollowup は、残った条件をフォローアップへ回して、ほかの条件で判定する。
	HandSplitFollowup SupervisorHand = "split_followup"
	// HandReplan は、計画を作り直す。
	HandReplan SupervisorHand = "replan"
	// HandDefer は、このサイクルでは見送る。
	HandDefer SupervisorHand = "defer"
	// HandEscalate は、調べた内容と推奨を付けて、オーナーへ上げる。
	HandEscalate SupervisorHand = "escalate"
)

var supervisorHandValues = []SupervisorHand{
	HandReverify, HandSendBack, HandRetry, HandSplitFollowup, HandReplan, HandDefer, HandEscalate,
}

// SupervisorHandValues は手の閉集合を定義の順に返す（写し）。
func SupervisorHandValues() []SupervisorHand {
	return append([]SupervisorHand(nil), supervisorHandValues...)
}

// ParseSupervisorHand は s を手として解釈する。完全一致のみ受理する。
func ParseSupervisorHand(s string) (SupervisorHand, bool) {
	for _, v := range supervisorHandValues {
		if string(v) == s {
			return v, true
		}
	}
	return "", false
}

// SupervisorActionResult は対処（スーパーバイザーの判断 1 回と、core がその手を実行した結果）の結果。
type SupervisorActionResult string

// SupervisorActionResult の 3 値。
const (
	// ActionExecuted は、手を実行した。
	ActionExecuted SupervisorActionResult = "executed"
	// ActionFailed は、判断できなかった・出力に不備があった・手を実行できなかった。
	ActionFailed SupervisorActionResult = "failed"
	// ActionRejected は、実行の直前に状態が変わっていて、手を実行しなかった（理由は SupervisorRejectReason）。
	ActionRejected SupervisorActionResult = "rejected"
)

var supervisorActionResultValues = []SupervisorActionResult{ActionExecuted, ActionFailed, ActionRejected}

// SupervisorActionResultValues は対処の結果の閉集合を定義の順に返す（写し）。
func SupervisorActionResultValues() []SupervisorActionResult {
	return append([]SupervisorActionResult(nil), supervisorActionResultValues...)
}

// SupervisorRejectReason は、対処の結果が rejected のときの断りの理由。
type SupervisorRejectReason string

// SupervisorRejectReason の 2 値。
const (
	// RejectStateChanged は、保留の直前の状態・課題の状態・契約の実行など、手を実行できる状態でなくなっていた。
	RejectStateChanged SupervisorRejectReason = "state_changed"
	// RejectOwnerAnswered は、判断の間にオーナーが保留へ回答していた。
	RejectOwnerAnswered SupervisorRejectReason = "owner_answered"
)

var supervisorRejectReasonValues = []SupervisorRejectReason{RejectStateChanged, RejectOwnerAnswered}

// SupervisorRejectReasonValues は断りの理由の閉集合を定義の順に返す（写し）。
func SupervisorRejectReasonValues() []SupervisorRejectReason {
	return append([]SupervisorRejectReason(nil), supervisorRejectReasonValues...)
}

// SupervisorState は保留の supervisor_state（スーパーバイザーの処理の状態）。NULL（SupervisorStateNone）は
// 対象にしない保留。
type SupervisorState string

// SupervisorState の値。
const (
	// SupervisorStateNone は対象にしない保留（列は NULL）。
	SupervisorStateNone SupervisorState = ""
	// SupervisorStatePending は、スーパーバイザー待ち。
	SupervisorStatePending SupervisorState = "pending"
	// SupervisorStateHandled は、手を打って閉じた。
	SupervisorStateHandled SupervisorState = "handled"
	// SupervisorStateEscalated は、オーナーへ上げた。
	SupervisorStateEscalated SupervisorState = "escalated"
)

var supervisorStateValues = []SupervisorState{SupervisorStatePending, SupervisorStateHandled, SupervisorStateEscalated}

// SupervisorStateValues は supervisor_state の閉集合（NULL を除く 3 値）を定義の順に返す（写し）。
func SupervisorStateValues() []SupervisorState {
	return append([]SupervisorState(nil), supervisorStateValues...)
}

// supervisorHandsByStopKind は止まり方ごとに選べる手の表（設計の「止まり方ごとに選べる手」）。
// 各値の並びは手の一覧（supervisorHandValues）の順。ここから、いまの状態で実行できない手を core が外して判断点へ渡す。
var supervisorHandsByStopKind = map[StopKind][]SupervisorHand{
	StopKindVerifyUncertain:    {HandReverify, HandSendBack, HandSplitFollowup, HandReplan, HandDefer, HandEscalate},
	StopKindArtifactMissing:    {HandReverify, HandRetry, HandDefer, HandEscalate},
	StopKindDelegateStopped:    {HandRetry, HandReplan, HandDefer, HandEscalate},
	StopKindFactsUnmet:         {HandSendBack, HandEscalate},
	StopKindChecksFailed:       {HandSendBack, HandEscalate},
	StopKindReworkLimit:        {HandSendBack, HandReplan, HandEscalate},
	StopKindJudgmentFailed:     {HandRetry, HandEscalate},
	StopKindIntakeUnverified:   {HandReverify, HandDefer, HandEscalate},
	StopKindUpstreamUnreadable: {HandDefer, HandEscalate},
}

// SupervisorHandsFor は止まり方 k で選べる手を返す（写し）。k が閉集合の外なら nil。
func SupervisorHandsFor(k StopKind) []SupervisorHand {
	return append([]SupervisorHand(nil), supervisorHandsByStopKind[k]...)
}

// supervisorHandAllowed は手 h が止まり方 k で選べる手かを返す。
func supervisorHandAllowed(k StopKind, h SupervisorHand) bool {
	for _, v := range supervisorHandsByStopKind[k] {
		if v == h {
			return true
		}
	}
	return false
}

// supervisorActorName は、スーパーバイザーが保留を閉じたときの回答した者（hold.answered_by）。
// オーナーの回答（本人確認つき）と区別する。
const supervisorActorName = "supervisor"
