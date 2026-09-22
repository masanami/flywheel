package core

import "strings"

// このファイルは docs/features/m1-core.md §状態機械・§承認・§データモデル が
// 定める閉集合（優先度・緊急度・検証結果・不可逆操作の種類・承認の種類・決定）の
// 正本を持つ。コード値はすべて仕様・親要件チケット #4 のデータモデルに記載が
// あるものをそのまま採用している。
//
// 各 Parse 関数は ParseStatus と同じ規則（前後の空白を除いた完全一致のみ受理）で
// 閉集合を検査する（仕様が空白の扱いを明記しているのは状態だけで、他の閉集合へ
// 同じ規則を当てるのは本チケットの仮定）。大文字小文字の違い・内部の空白違いは受理しない
// （fail-closed。docs/features/m1-core.md 「現行から引き継ぐ振る舞い」）。

// Priority は課題の優先度（`classify --priority` の閉集合）。
type Priority string

// Priority の3値。
const (
	PriorityP0 Priority = "P0"
	PriorityP1 Priority = "P1"
	PriorityP2 Priority = "P2"
)

var priorityValues = []Priority{PriorityP0, PriorityP1, PriorityP2}

// ParsePriority は s を優先度として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParsePriority(s string) (Priority, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range priorityValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// Urgency は課題の緊急度。現行台帳と同じ日本語の値が正であり、
// 別のコードを設けない（docs/features/m1-core.md 「成功時のJSON出力の規約」）。
type Urgency string

// Urgency の3値。
const (
	UrgencyHigh   Urgency = "高"
	UrgencyMedium Urgency = "中"
	UrgencyLow    Urgency = "低"
)

var urgencyValues = []Urgency{UrgencyHigh, UrgencyMedium, UrgencyLow}

// ParseUrgency は s を緊急度として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseUrgency(s string) (Urgency, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range urgencyValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// VerifyResult は `verify --result` の閉集合（設計書 §8.2 の写像）。
type VerifyResult string

// VerifyResult の3値。
const (
	VerifyResultMet       VerifyResult = "met"
	VerifyResultNotMet    VerifyResult = "not_met"
	VerifyResultUncertain VerifyResult = "uncertain"
)

var verifyResultValues = []VerifyResult{VerifyResultMet, VerifyResultNotMet, VerifyResultUncertain}

// ParseVerifyResult は s を検証結果として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseVerifyResult(s string) (VerifyResult, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range verifyResultValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// OperationKind は不可逆操作の種類（`op add --kind` の閉集合）。
type OperationKind string

// OperationKind の4値。
const (
	OperationKindRelease      OperationKind = "release"
	OperationKindDelete       OperationKind = "delete"
	OperationKindExternalSend OperationKind = "external_send"
	OperationKindOther        OperationKind = "other"
)

var operationKindValues = []OperationKind{
	OperationKindRelease, OperationKindDelete, OperationKindExternalSend, OperationKindOther,
}

// ParseOperationKind は s を不可逆操作の種類として解釈する。
// 前後の空白を除いた完全一致のみ受理する。
func ParseOperationKind(s string) (OperationKind, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range operationKindValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// ApprovalKind は承認の種類（§データモデル `approval.kind`）。不可逆操作の
// 承認・差し戻しは種類にかかわらず ApprovalKindRelease（本番反映）として記録する
// （H2・H5。docs/features/m1-core.md §承認）。
type ApprovalKind string

// ApprovalKind の3値。
const (
	ApprovalKindPlan       ApprovalKind = "plan"
	ApprovalKindCompletion ApprovalKind = "completion"
	ApprovalKindRelease    ApprovalKind = "release"
)

var approvalKindValues = []ApprovalKind{ApprovalKindPlan, ApprovalKindCompletion, ApprovalKindRelease}

// ParseApprovalKind は s を承認の種類として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseApprovalKind(s string) (ApprovalKind, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range approvalKindValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// ApprovalDecision は承認・差し戻しの決定（§データモデル `approval.decision`）。
type ApprovalDecision string

// ApprovalDecision の2値。
const (
	ApprovalDecisionApproved ApprovalDecision = "approved"
	ApprovalDecisionRejected ApprovalDecision = "rejected"
)

var approvalDecisionValues = []ApprovalDecision{ApprovalDecisionApproved, ApprovalDecisionRejected}

// ParseApprovalDecision は s を承認の決定として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseApprovalDecision(s string) (ApprovalDecision, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range approvalDecisionValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// OperationState は不可逆操作の状態（§データモデル `operation.state`）。
// 登録直後は OperationStatePending で、単独の承認・差し戻し
// （ExecuteOperationApproval）または完了の承認による一括承認（D12）でだけ
// 変わる。他の閉集合（Status・OperationKind・ApprovalKind・ApprovalDecision）
// と同じく、値の正本をここ 1 箇所に置く。
type OperationState string

// OperationState の3値。
const (
	OperationStatePending  OperationState = "pending"
	OperationStateApproved OperationState = "approved"
	OperationStateRejected OperationState = "rejected"
)

var operationStateValues = []OperationState{
	OperationStatePending, OperationStateApproved, OperationStateRejected,
}

// ParseOperationState は s を不可逆操作の状態として解釈する。
// 前後の空白を除いた完全一致のみ受理する。
func ParseOperationState(s string) (OperationState, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range operationStateValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// operationStateForDecision は承認の決定（approved／rejected）に対応する
// 不可逆操作の状態を返す（単独の承認・差し戻しと D12 の一括承認が共有する）。
func operationStateForDecision(d ApprovalDecision) (OperationState, bool) {
	switch d {
	case ApprovalDecisionApproved:
		return OperationStateApproved, true
	case ApprovalDecisionRejected:
		return OperationStateRejected, true
	default:
		return "", false
	}
}
