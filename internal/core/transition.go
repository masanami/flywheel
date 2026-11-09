package core

import "fmt"

// NoStatus は「遷移元が無い」ことを表すセンチネル値（T1: create の遷移元）。
// ParseStatus は空文字列を語彙として解釈しないため、状態語彙9値とは衝突しない。
const NoStatus Status = ""

// Operation は状態機械への操作を識別するトークン。CLI のフラグ表記
// （例: `verify --result met`）とは独立した core 内部の語彙であり、
// 操作の解釈（フラグの解析・閉集合の検査）は CLI 層（後続チケット）が担う。
type Operation string

// 操作トークンの定数。T8〜T10（verify の結果違い）・T13/T14（approve の
// --hold-release 有無）はそれぞれ別のトークンにして、同じ遷移元からでも
// 別の行として引けるようにする。
const (
	OpCreate             Operation = "create"
	OpClassify           Operation = "classify"
	OpPlan               Operation = "plan"
	OpSubmit             Operation = "submit"
	OpVerifyMet          Operation = "verify_met"
	OpVerifyNotMet       Operation = "verify_not_met"
	OpVerifyUncertain    Operation = "verify_uncertain"
	OpHold               Operation = "hold"
	OpAnswer             Operation = "answer"
	OpApprove            Operation = "approve"
	OpApproveHoldRelease Operation = "approve_hold_release"
	OpReject             Operation = "reject"
	OpWithdraw           Operation = "withdraw"
	// OpCompleteDirect は、親が直接行った作業を完了として記録する操作（T17）。
	OpCompleteDirect Operation = "complete_direct"
	// OpReopenForRelease は、完了の課題を着手中へ戻す操作（T18）。本番反映でマージできなくなった場合に限る
	// 専用の書き込み（ReopenChallengeForRelease）だけが使う。CLI の操作には対応しない。
	OpReopenForRelease Operation = "reopen_for_release"
)

// TransitionTarget は遷移先の表現。T1〜T11・T13〜T15 は固定の状態を持つが、
// T12（answer）だけは「保留に入る直前の状態」という種別で表す
// （固定の状態ではないため、呼び出し側が直前の状態を渡して解決する）。
type TransitionTarget struct {
	// ReturnToPrecedingHoldStatus が true なら、Fixed は使わず Resolve に
	// 渡された直前状態を遷移先とする（T12 専用）。
	ReturnToPrecedingHoldStatus bool
	// Fixed は ReturnToPrecedingHoldStatus が false のときの固定遷移先。
	Fixed Status
	// IfCompletionDirect は、課題の completion_direct（いまの完了確認待ちが親が直接行った完了〔T17〕で入ったもの
	// であること）が真のときの遷移先。ゼロ値（NoStatus）なら無い（Fixed だけ）。T15（差し戻し）だけが持ち、
	// 親が直接した完了の差し戻しを着手中ではなく「親が直接やる」保留（人間対応待ち）へ戻す（#481）。
	// 値は遷移のたびに runTransition が書き（T17 で真・ほかで偽）、Resolve は読まない（既存の呼び出し元が見る遷移先は
	// Fixed のまま）。真偽を渡して引くのは ResolveForCompletionDirect。
	IfCompletionDirect Status
}

// Resolve は遷移先を確定する。固定遷移先の行では preceding を無視して Fixed を返す。
// T12（ReturnToPrecedingHoldStatus）の行では、preceding が table 上で保留に
// 入り得た状態（HoldEntrySources(table) の要素）でなければエラーを返す
// （fail-closed）。
//
// table を引数で受け取るのは、パッケージ変数 Table を暗黙に読むと、closure.go
// が前提とする「table・vocab を引数で受け取り、合成データに対しても検出ロジックを
// 単体で検査できる」設計と食い違うため。通常の呼び出しは Lookup(from, op)
// （パッケージ変数 Table を対象に引く）で得た Transition に対して
// `tr.Target.Resolve(Table, preceding)` のように同じ Table を渡す。
func (tt TransitionTarget) Resolve(table []Transition, preceding Status) (Status, error) {
	if !tt.ReturnToPrecedingHoldStatus {
		return tt.Fixed, nil
	}
	for _, s := range HoldEntrySources(table) {
		if s == preceding {
			return preceding, nil
		}
	}
	return "", fmt.Errorf("core: %q is not a valid preceding-hold status", preceding)
}

// ResolveForCompletionDirect は Resolve に、課題の completion_direct（IfCompletionDirect の注記を参照）を
// 加えた版。completionDirect が真で IfCompletionDirect を持つ行なら IfCompletionDirect、それ以外は Resolve と同じ。
func (tt TransitionTarget) ResolveForCompletionDirect(table []Transition, preceding Status, completionDirect bool) (Status, error) {
	if completionDirect && tt.IfCompletionDirect != NoStatus {
		return tt.IfCompletionDirect, nil
	}
	return tt.Resolve(table, preceding)
}

// Transition は遷移表1行分。ID は docs/features/m1-core.md の表番号（T1〜T18）を指す。
// T11 は遷移元が4つあるため、物理的には ID="T11" の行が4つ存在する。
type Transition struct {
	ID                   string
	From                 Status
	Op                   Operation
	Target               TransitionTarget
	RequiresVerification bool
	// Restricted が true の行は、専用の core の書き込みだけが使う（T18）。Lookup・OutgoingFrom・IsTerminal には現れず、
	// LookupRestricted だけが引く。通常の操作から見た状態の終端（完了）は変わらない。
	Restricted bool
}

// Table は遷移表 T1〜T18 の正本。docs/features/m1-core.md §状態機械 の
// 遷移表と1対1に対応する（T11 は遷移元4つに展開して4行として持つ）。
// この表に無い（状態, 操作）の組はすべて不可（Lookup が ok=false を返す）。
var Table = []Transition{
	{ID: "T1", From: NoStatus, Op: OpCreate, Target: TransitionTarget{Fixed: StatusUnclassified}},
	{ID: "T2", From: StatusUnclassified, Op: OpClassify, Target: TransitionTarget{Fixed: StatusClassified}},
	{ID: "T3", From: StatusClassified, Op: OpPlan, Target: TransitionTarget{Fixed: StatusAwaitingPlanApproval}},
	{ID: "T4", From: StatusAwaitingPlanApproval, Op: OpPlan, Target: TransitionTarget{Fixed: StatusAwaitingPlanApproval}},
	{ID: "T5", From: StatusAwaitingPlanApproval, Op: OpApprove, Target: TransitionTarget{Fixed: StatusInProgress}, RequiresVerification: true},
	{ID: "T6", From: StatusAwaitingPlanApproval, Op: OpReject, Target: TransitionTarget{Fixed: StatusClassified}, RequiresVerification: true},
	{ID: "T7", From: StatusInProgress, Op: OpSubmit, Target: TransitionTarget{Fixed: StatusVerifying}},
	{ID: "T8", From: StatusVerifying, Op: OpVerifyMet, Target: TransitionTarget{Fixed: StatusAwaitingCompletionApproval}},
	{ID: "T9", From: StatusVerifying, Op: OpVerifyNotMet, Target: TransitionTarget{Fixed: StatusInProgress}},
	{ID: "T10", From: StatusVerifying, Op: OpVerifyUncertain, Target: TransitionTarget{Fixed: StatusAwaitingHuman}},
	{ID: "T11", From: StatusUnclassified, Op: OpHold, Target: TransitionTarget{Fixed: StatusAwaitingHuman}},
	{ID: "T11", From: StatusClassified, Op: OpHold, Target: TransitionTarget{Fixed: StatusAwaitingHuman}},
	{ID: "T11", From: StatusInProgress, Op: OpHold, Target: TransitionTarget{Fixed: StatusAwaitingHuman}},
	{ID: "T11", From: StatusVerifying, Op: OpHold, Target: TransitionTarget{Fixed: StatusAwaitingHuman}},
	{ID: "T12", From: StatusAwaitingHuman, Op: OpAnswer, Target: TransitionTarget{ReturnToPrecedingHoldStatus: true}, RequiresVerification: true},
	{ID: "T13", From: StatusAwaitingCompletionApproval, Op: OpApprove, Target: TransitionTarget{Fixed: StatusDone}, RequiresVerification: true},
	{ID: "T14", From: StatusAwaitingCompletionApproval, Op: OpApproveHoldRelease, Target: TransitionTarget{Fixed: StatusDone}, RequiresVerification: true},
	// T15: 完了の差し戻し。着手中へ戻す。ただし、いまの完了確認待ちが親が直接行った完了（T17）で入ったもの
	// （challenge.completion_direct が真）なら、着手中ではなく人間対応待ちへ戻し、同じトランザクションで種類が
	// 親が直接行うの保留を新しく立てる（ExecuteApproval。#481）。戻り先が 2 つあるが遷移表の行は 1 つで、
	// 後者は IfCompletionDirect で表す。
	{ID: "T15", From: StatusAwaitingCompletionApproval, Op: OpReject, Target: TransitionTarget{Fixed: StatusInProgress, IfCompletionDirect: StatusAwaitingHuman}, RequiresVerification: true},
	// T16: 取り下げ（docs/features/m4.5-learning-and-self-improvement.md S4。M45H13・M45H28）。
	// 完了以外の 7 状態から取り下げへ。取り下げから出る行は持たない（終端）。
	{ID: "T16", From: StatusUnclassified, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusClassified, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusAwaitingPlanApproval, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusInProgress, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusVerifying, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusAwaitingCompletionApproval, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	{ID: "T16", From: StatusAwaitingHuman, Op: OpWithdraw, Target: TransitionTarget{Fixed: StatusWithdrawn}, RequiresVerification: true},
	// T17: 親が直接行った作業の完了の記録（docs/features/m1-core.md §状態機械。課題 #452）。人間対応待ちから
	// 完了確認待ちへ進む。遷移表の行は状態だけで決まるため、「開いている保留の種類が親が直接行う」ことと
	// 「成果物の参照が 1 件以上」は CompleteDirect が検査する（T12 の戻り先の検査と同じ位置づけ）。
	// 完了の承認は T13・T14 のとおり本人確認つきで、この行は本人確認を要さない（submit・hold と同じ、エージェントの記録）。
	{ID: "T17", From: StatusAwaitingHuman, Op: OpCompleteDirect, Target: TransitionTarget{Fixed: StatusAwaitingCompletionApproval}},
	// T18: 本番反映でマージできなくなった完了の課題を着手中へ戻す（#485）。本番反映のやり直し（ReopenChallengeForRelease）
	// だけが使う制限つきの行で、通常の操作の対象にならない。本人確認は要さない（エージェントの記録。戻す理由は
	// 本番反映の結果か、検証の後の中身の変更で、人の承認はやり直しの完了の承認で受ける）。
	{ID: "T18", From: StatusDone, Op: OpReopenForRelease, Target: TransitionTarget{Fixed: StatusInProgress}, Restricted: true},
}

// Lookup は (from, op) から遷移表の行を引く。表に無い組は ok=false を返す
// （表に無い遷移はすべて拒否する＝docs/features/m1-core.md §状態機械）。
func Lookup(from Status, op Operation) (Transition, bool) {
	for _, tr := range Table {
		if tr.From == from && tr.Op == op && !tr.Restricted {
			return tr, true
		}
	}
	return Transition{}, false
}

// LookupRestricted は (from, op) から制限つきの行（T18）を引く。通常の行は返さない。
func LookupRestricted(from Status, op Operation) (Transition, bool) {
	for _, tr := range Table {
		if tr.From == from && tr.Op == op && tr.Restricted {
			return tr, true
		}
	}
	return Transition{}, false
}

// OutgoingFrom は table の中で from を遷移元に持つ行（制限つきの行を除く）をすべて返す。
// AC-27（完了から出る遷移が無いこと）の検査や、将来の invalid_transition /
// terminal_state の判定（後続チケット）に使う。
func OutgoingFrom(table []Transition, from Status) []Transition {
	var out []Transition
	for _, tr := range table {
		if tr.From == from && !tr.Restricted {
			out = append(out, tr)
		}
	}
	return out
}

// HoldEntrySources は table の中で人間対応待ちへ遷移する行（T10・T11 相当）の
// 遷移元の集合を返す。T12（answer）の Resolve が妥当性検査に使う。
// From が NoStatus（T1 のような「遷移元が無い」行）の場合は対象から除く
// （fail-closed。T1 が誤って人間対応待ちを Target にする壊れたデータでも、
// 「遷移元なし」を保留の戻り先として受理しないようにする）。
func HoldEntrySources(table []Transition) []Status {
	seen := map[Status]bool{}
	var out []Status
	for _, tr := range table {
		if tr.From == NoStatus {
			continue
		}
		if tr.Target.ReturnToPrecedingHoldStatus {
			continue
		}
		if tr.Target.Fixed != StatusAwaitingHuman {
			continue
		}
		if seen[tr.From] {
			continue
		}
		seen[tr.From] = true
		out = append(out, tr.From)
	}
	return out
}

// IsTerminal は、s が vocab に含まれる既知の状態であり、かつ table に s から
// 出る遷移が1つも無いことを返す（AC-27 相当。「完了」のような終端状態の判定を、
// 呼び出し側〈#10 の CLI 層〉に invalid_transition / terminal_state の
// 振り分けとして書かせず core 側に置く）。vocab に無い状態（破損したストアの値など）は fail-closed で
// false を返す（true にすると、未知状態がすべて「完了と同じ終端」として
// 扱われてしまう）。
func IsTerminal(table []Transition, vocab []StatusVocabEntry, s Status) bool {
	known := false
	for _, e := range vocab {
		if e.Code == s {
			known = true
			break
		}
	}
	if !known {
		return false
	}
	return len(OutgoingFrom(table, s)) == 0
}

// OperationForVerify は VerifyResult に対応する Operation（T8〜T10 のいずれか）を返す
// （設計書 §8.2 の写像）。CLI 層（#10）が `verify --result` の値から遷移表を
// 引く際、判断値と操作トークンの対応を core の外へ書かせないための変換点。
func OperationForVerify(r VerifyResult) (Operation, bool) {
	switch r {
	case VerifyResultMet:
		return OpVerifyMet, true
	case VerifyResultNotMet:
		return OpVerifyNotMet, true
	case VerifyResultUncertain:
		return OpVerifyUncertain, true
	default:
		return "", false
	}
}

// OperationForApprove は `approve`（holdRelease=false）/ `approve --hold-release`
// （holdRelease=true）に対応する Operation を返す。holdRelease=false は
// OpApprove（計画承認待ちからは T5、完了確認待ちからは T13 として Lookup で
// 引ける）、holdRelease=true は OpApproveHoldRelease（完了確認待ちからは T14
// として引けるが、計画承認待ちや不可逆操作の ID にはそもそも表に行が無い）を返す。
//
// --hold-release が計画承認待ちの課題・不可逆操作の ID に指定された場合の
// usage_error（docs/features/m1-core.md §承認・§受入基準「不可逆操作と D12」）は、
// 遷移表の検査（Lookup の ok=false）では表現できない CLI 入力規則であり、core
// はその判定を担わない。CLI 層（#10）が、対象の種別・現在の状態を見て
// usage_error を判定してから core を呼ぶこと。
func OperationForApprove(holdRelease bool) Operation {
	if holdRelease {
		return OpApproveHoldRelease
	}
	return OpApprove
}
