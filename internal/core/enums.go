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

// ApprovalKind の4値（budget は `flywheel budget` の承認。M3 S2 が足した）。
const (
	ApprovalKindPlan       ApprovalKind = "plan"
	ApprovalKindCompletion ApprovalKind = "completion"
	ApprovalKindRelease    ApprovalKind = "release"
	ApprovalKindBudget     ApprovalKind = "budget"
)

var approvalKindValues = []ApprovalKind{ApprovalKindPlan, ApprovalKindCompletion, ApprovalKindRelease, ApprovalKindBudget}

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
// （ExecuteOperationApproval）、完了の承認による一括承認（D12）、および release の
// 実行（executed）・取り消し（approved から pending へ戻す）で変わる。他の閉集合（Status・OperationKind・ApprovalKind・ApprovalDecision）
// と同じく、値の正本をここ 1 箇所に置く。
type OperationState string

// OperationState の4値。executed は承認済みの release を core が実行した後の
// 状態で、未承認にも承認済み（未実行）にも数えない。
const (
	OperationStatePending  OperationState = "pending"
	OperationStateApproved OperationState = "approved"
	OperationStateRejected OperationState = "rejected"
	OperationStateExecuted OperationState = "executed"
)

var operationStateValues = []OperationState{
	OperationStatePending, OperationStateApproved, OperationStateRejected, OperationStateExecuted,
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

// RejectedField は課題の差し戻しで「ずれていた」と指す段階の欄
// （`reject --field` の閉集合。approval.rejected_fields の要素）。
type RejectedField string

// RejectedField の4値。
const (
	RejectedFieldIssues   RejectedField = "issues"
	RejectedFieldExpected RejectedField = "expected"
	RejectedFieldNonGoals RejectedField = "non_goals"
	RejectedFieldFigure   RejectedField = "figure"
)

// rejectedFieldValues は閉集合の順（記録する順もこの順）。
var rejectedFieldValues = []RejectedField{
	RejectedFieldIssues, RejectedFieldExpected, RejectedFieldNonGoals, RejectedFieldFigure,
}

// ParseRejectedField は s を差し戻しのずれた欄として解釈する。
// 前後の空白を除いた完全一致のみ受理する。
func ParseRejectedField(s string) (RejectedField, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range rejectedFieldValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	return "", false
}

// NormalizeRejectedFields は fields を閉集合で検査し、重複を 1 つにして閉集合の順に
// 並べて返す。閉集合の外の値があれば ok=false。空（nil を含む）は nil（NULL として書く）。
func NormalizeRejectedFields(fields []string) (out []RejectedField, ok bool) {
	seen := map[RejectedField]bool{}
	for _, f := range fields {
		v, valid := ParseRejectedField(f)
		if !valid {
			return nil, false
		}
		seen[v] = true
	}
	for _, v := range rejectedFieldValues {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out, true
}

// ActivityActionOpExecute は作業ログ（entity=operation・entity=challenge）の action で、
// release の実行の試行の結果が実行済みか取り消しのときに残す。待つ結果の試行は
// 作業ログに載せず operation_execution にだけ残す。
const ActivityActionOpExecute = "op_execute"

// ExecutionResult は release の実行の試行の結果（operation_execution.result）の閉集合。
type ExecutionResult string

// ExecutionResult の16値。
const (
	ExecutionResultMerged           ExecutionResult = "merged"
	ExecutionResultAlreadyMerged    ExecutionResult = "already_merged"
	ExecutionResultWaitingChecks    ExecutionResult = "waiting_checks"
	ExecutionResultUpstreamFailed   ExecutionResult = "upstream_failed"
	ExecutionResultChecksFailed     ExecutionResult = "checks_failed"
	ExecutionResultNotMergeable     ExecutionResult = "not_mergeable"
	ExecutionResultMergeFailed      ExecutionResult = "merge_failed"
	ExecutionResultMergeUnconfirmed ExecutionResult = "merge_unconfirmed"
	ExecutionResultHeadChanged      ExecutionResult = "head_changed"
	ExecutionResultClosed           ExecutionResult = "closed"
	ExecutionResultBaseChanged      ExecutionResult = "base_changed"
	ExecutionResultNoApprovedHead   ExecutionResult = "no_approved_head"
	ExecutionResultUnsupported      ExecutionResult = "unsupported"
	ExecutionResultMergedUnapproved ExecutionResult = "merged_unapproved"
	ExecutionResultLimitReached     ExecutionResult = "limit_reached"
	ExecutionResultInterrupted      ExecutionResult = "interrupted"
)

// ExecutionClass は試行の結果の分類。
type ExecutionClass string

// ExecutionClass の値。待つ結果は連続の回数に数えるか（ExecutionResult.Counts）で分かれる。
const (
	// ExecutionClassExecuted は実行済み（merged・already_merged）。
	ExecutionClassExecuted ExecutionClass = "executed"
	// ExecutionClassWait は待つ（承認を保ち、次の実行の口で試し直す）。
	ExecutionClassWait ExecutionClass = "wait"
	// ExecutionClassCancel は取り消す（承認を取り消し、release を未承認へ戻す）。
	ExecutionClassCancel ExecutionClass = "cancel"
	// ExecutionClassInterrupted は実行の口の都合（回収で閉じた試行）。
	ExecutionClassInterrupted ExecutionClass = "interrupted"
)

// executionResultSpec は結果ごとの分類と、連続の回数に数えるか。
var executionResultSpecs = []struct {
	result ExecutionResult
	class  ExecutionClass
	counts bool
}{
	{ExecutionResultMerged, ExecutionClassExecuted, false},
	{ExecutionResultAlreadyMerged, ExecutionClassExecuted, false},
	{ExecutionResultWaitingChecks, ExecutionClassWait, false},
	{ExecutionResultUpstreamFailed, ExecutionClassWait, false},
	{ExecutionResultChecksFailed, ExecutionClassWait, true},
	{ExecutionResultNotMergeable, ExecutionClassWait, true},
	{ExecutionResultMergeFailed, ExecutionClassWait, true},
	{ExecutionResultMergeUnconfirmed, ExecutionClassWait, false},
	{ExecutionResultHeadChanged, ExecutionClassCancel, false},
	{ExecutionResultClosed, ExecutionClassCancel, false},
	{ExecutionResultBaseChanged, ExecutionClassCancel, false},
	{ExecutionResultNoApprovedHead, ExecutionClassCancel, false},
	{ExecutionResultUnsupported, ExecutionClassCancel, false},
	{ExecutionResultMergedUnapproved, ExecutionClassCancel, false},
	{ExecutionResultLimitReached, ExecutionClassCancel, false},
	{ExecutionResultInterrupted, ExecutionClassInterrupted, false},
}

// ExecutionResultValues は閉集合の全値を表の順に返す。
func ExecutionResultValues() []ExecutionResult {
	out := make([]ExecutionResult, len(executionResultSpecs))
	for i, sp := range executionResultSpecs {
		out[i] = sp.result
	}
	return out
}

// ParseExecutionResult は s を試行の結果として解釈する（前後の空白を除いた完全一致）。
func ParseExecutionResult(s string) (ExecutionResult, bool) {
	trimmed := strings.TrimSpace(s)
	for _, sp := range executionResultSpecs {
		if string(sp.result) == trimmed {
			return sp.result, true
		}
	}
	return "", false
}

// Class は r の分類を返す。閉集合の外には ok=false。
func (r ExecutionResult) Class() (ExecutionClass, bool) {
	for _, sp := range executionResultSpecs {
		if sp.result == r {
			return sp.class, true
		}
	}
	return "", false
}

// Counts は r が連続の回数に数えられる結果（checks_failed・not_mergeable・merge_failed）か。
func (r ExecutionResult) Counts() bool {
	for _, sp := range executionResultSpecs {
		if sp.result == r {
			return sp.counts
		}
	}
	return false
}
