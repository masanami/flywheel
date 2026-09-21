package core

import "fmt"

// NoStatus は「遷移元が無い」ことを表すセンチネル値（T1: create の遷移元）。
// ParseStatus は空文字列を語彙として解釈しないため、状態語彙8値とは衝突しない。
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

// Transition は遷移表1行分。ID は docs/features/m1-core.md の表番号（T1〜T15）を指す。
// T11 は遷移元が4つあるため、物理的には ID="T11" の行が4つ存在する。
type Transition struct {
	ID                   string
	From                 Status
	Op                   Operation
	Target               TransitionTarget
	RequiresVerification bool
}

// Table は遷移表 T1〜T15 の正本。docs/features/m1-core.md §状態機械 の
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
	{ID: "T15", From: StatusAwaitingCompletionApproval, Op: OpReject, Target: TransitionTarget{Fixed: StatusInProgress}, RequiresVerification: true},
}

// Lookup は (from, op) から遷移表の行を引く。表に無い組は ok=false を返す
// （表に無い遷移はすべて拒否する＝docs/features/m1-core.md §状態機械）。
func Lookup(from Status, op Operation) (Transition, bool) {
	for _, tr := range Table {
		if tr.From == from && tr.Op == op {
			return tr, true
		}
	}
	return Transition{}, false
}

// OutgoingFrom は table の中で from を遷移元に持つ行をすべて返す。
// AC-27（完了から出る遷移が無いこと）の検査や、将来の invalid_transition /
// terminal_state の判定（後続チケット）に使う。
func OutgoingFrom(table []Transition, from Status) []Transition {
	var out []Transition
	for _, tr := range table {
		if tr.From == from {
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
