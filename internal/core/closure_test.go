package core

import "testing"

// --- AC-25: 遷移表が参照する状態はすべて語彙にある ---

// TestUnknownStatusesReferencedByTable_RealTableIsClean は、実際の Table・
// StatusVocabulary の組で参照違反が無いことを検証する（AC-25 本体）。
func TestUnknownStatusesReferencedByTable_RealTableIsClean(t *testing.T) {
	got := UnknownStatusesReferencedByTable(Table, StatusVocabulary)
	if len(got) != 0 {
		t.Errorf("UnknownStatusesReferencedByTable(real) = %v, want empty", got)
	}
}

// TestUnknownStatusesReferencedByTable_DetectsUnknownFrom は、遷移元に語彙に無い
// 状態を参照する行を混入させた破損データを検出できることを検証する
// （検出ロジック自体を壊れた入力に対して単体で検査する）。
func TestUnknownStatusesReferencedByTable_DetectsUnknownFrom(t *testing.T) {
	corrupted := append([]Transition{}, Table...)
	corrupted = append(corrupted, Transition{
		ID: "T-broken", From: Status("no_such_status"), Op: OpClassify,
		Target: TransitionTarget{Fixed: StatusClassified},
	})
	got := UnknownStatusesReferencedByTable(corrupted, StatusVocabulary)
	if len(got) != 1 || got[0] != Status("no_such_status") {
		t.Fatalf("UnknownStatusesReferencedByTable(corrupted from) = %v, want [no_such_status]", got)
	}
}

// TestUnknownStatusesReferencedByTable_DetectsUnknownTarget は、固定遷移先に
// 語彙に無い状態を参照する行を混入させた破損データを検出できることを検証する。
func TestUnknownStatusesReferencedByTable_DetectsUnknownTarget(t *testing.T) {
	corrupted := append([]Transition{}, Table...)
	corrupted = append(corrupted, Transition{
		ID: "T-broken", From: StatusUnclassified, Op: OpAnswer,
		Target: TransitionTarget{Fixed: Status("no_such_status")},
	})
	got := UnknownStatusesReferencedByTable(corrupted, StatusVocabulary)
	if len(got) != 1 || got[0] != Status("no_such_status") {
		t.Fatalf("UnknownStatusesReferencedByTable(corrupted target) = %v, want [no_such_status]", got)
	}
}

// TestUnknownStatusesReferencedByTable_DetectsMissingFixedTarget は、
// ReturnToPrecedingHoldStatus でない行の固定遷移先が未設定（ゼロ値）である
// 破損データを検出できることを検証する（From の NoStatus は T1 の正当な表現だが、
// 固定遷移先の NoStatus は設定漏れであり、AC-25 の検査をすり抜けさせない）。
func TestUnknownStatusesReferencedByTable_DetectsMissingFixedTarget(t *testing.T) {
	corrupted := append([]Transition{}, Table...)
	corrupted = append(corrupted, Transition{
		ID: "T-broken", From: StatusUnclassified, Op: Operation("bogus_op"),
		// Target を意図的に未設定（ゼロ値 = Fixed: NoStatus）のままにする。
	})
	got := UnknownStatusesReferencedByTable(corrupted, StatusVocabulary)
	if len(got) != 1 || got[0] != NoStatus {
		t.Fatalf("UnknownStatusesReferencedByTable(corrupted, missing target) = %v, want [NoStatus]", got)
	}
}

// --- AC-26: 語彙の状態はすべて T1 から遷移表をたどって到達できる ---

// TestUnreachableFromCreate_RealTableIsClean は、実際の Table・StatusVocabulary の
// 組で、T1 から到達できない状態が無いことを検証する（AC-26 本体）。
// T12（保留に入る直前の状態への遷移）は閉包計算では no-op として扱う
// （UnreachableFromCreate のコメント参照。T12 を無条件に到達済みとして辿ると
// 偽陰性を生むため）。
// 実 Table では T12 を経由せずとも、T1→T2→T3→T5→T7→T8→T13 の主経路と
// T10 だけで8状態すべてに到達できる。
func TestUnreachableFromCreate_RealTableIsClean(t *testing.T) {
	got := UnreachableFromCreate(Table, StatusVocabulary)
	if len(got) != 0 {
		t.Errorf("UnreachableFromCreate(real) = %v, want empty", got)
	}
}

// TestUnreachableFromCreate_DetectsUnreachableStatus は、ある状態への
// 到達経路を欠いた破損データ（遷移表から特定の状態への遷移を全て除去したもの）を
// 検出できることを検証する（T13・T14 除去で完了が到達不能になるケース）。
func TestUnreachableFromCreate_DetectsUnreachableStatus(t *testing.T) {
	var corrupted []Transition
	for _, tr := range Table {
		if tr.ID == "T13" || tr.ID == "T14" {
			continue
		}
		corrupted = append(corrupted, tr)
	}
	got := UnreachableFromCreate(corrupted, StatusVocabulary)
	found := false
	for _, s := range got {
		if s == StatusDone {
			found = true
		}
	}
	if !found {
		t.Fatalf("UnreachableFromCreate(corrupted, T13/T14 removed) = %v, want to include %q", got, StatusDone)
	}
}

// TestUnreachableFromCreate_DoesNotMaskUnreachabilityViaHold は、hold/answer
// （T10・T11・T12）を経由した迂回到達で、本来の到達経路が壊れた状態を
// 「到達可能」と誤判定しないことを検証する。
//
// 回帰の固定: T12 の行を「HoldEntrySources を無条件に reached へ追加する」と
// 扱う実装では、T2（未分類→分類済）を遷移表から除いても、classified が
// 「到達可能」と誤判定される（誤マスク経路: T1 で unclassified が
// reached になり、T11 の unclassified --hold--> 人間対応待ち で
// awaiting_human が reached になった時点で、hold の遷移元候補
// {unclassified, classified, in_progress, verifying} 全体が無条件に
// reached 扱いになる。T2 が無いため classified・awaiting_plan_approval
// 経由の正規到達は無く、T5 は発火しない）。hold は「今その状態にいる」ことが
// 前提の遷移であり、結果として新しい状態を生む遷移ではないため、これは偽陰性である。
func TestUnreachableFromCreate_DoesNotMaskUnreachabilityViaHold(t *testing.T) {
	var corrupted []Transition
	for _, tr := range Table {
		if tr.ID == "T2" {
			continue
		}
		corrupted = append(corrupted, tr)
	}
	got := UnreachableFromCreate(corrupted, StatusVocabulary)
	found := false
	for _, s := range got {
		if s == StatusClassified {
			found = true
		}
	}
	if !found {
		t.Fatalf("UnreachableFromCreate(corrupted, T2 removed) = %v, want to include %q (must not be masked by hold/answer detour)", got, StatusClassified)
	}
}

// TestUnreachableFromCreate_DetectsUnreachableStatusViaSubmit は、検証中への
// 唯一の到達経路（T7）を除去した場合に検証中が到達不能と検出されることを検証する
// （同じ hold 迂回到達の偽陰性が、T10 の遷移元である検証中についても起きないことの確認）。
func TestUnreachableFromCreate_DetectsUnreachableStatusViaSubmit(t *testing.T) {
	var corrupted []Transition
	for _, tr := range Table {
		if tr.ID == "T7" {
			continue
		}
		corrupted = append(corrupted, tr)
	}
	got := UnreachableFromCreate(corrupted, StatusVocabulary)
	found := false
	for _, s := range got {
		if s == StatusVerifying {
			found = true
		}
	}
	if !found {
		t.Fatalf("UnreachableFromCreate(corrupted, T7 removed) = %v, want to include %q", got, StatusVerifying)
	}
}

// TestUnreachableFromCreate_DetectsMissingT1 は、T1（create）自体が無い
// 破損データでは、未分類を含め全状態が到達不能になることを検証する。
func TestUnreachableFromCreate_DetectsMissingT1(t *testing.T) {
	var corrupted []Transition
	for _, tr := range Table {
		if tr.ID == "T1" {
			continue
		}
		corrupted = append(corrupted, tr)
	}
	got := UnreachableFromCreate(corrupted, StatusVocabulary)
	if len(got) != len(StatusVocabulary) {
		t.Fatalf("UnreachableFromCreate(corrupted, T1 removed) = %v, want all %d statuses unreachable", got, len(StatusVocabulary))
	}
}

// --- AC-27: 遷移表に「完了」から出る遷移は無い ---

// TestOutgoingFrom_Done_RealTableIsEmpty は、実際の Table で完了から出る遷移が
// 無いことを検証する（AC-27 本体）。
func TestOutgoingFrom_Done_RealTableIsEmpty(t *testing.T) {
	got := OutgoingFrom(Table, StatusDone)
	if len(got) != 0 {
		t.Errorf("OutgoingFrom(Table, done) = %v, want empty", got)
	}
}

// TestOutgoingFrom_Done_DetectsInjectedOutgoingTransition は、完了から出る遷移を
// 混入させた破損データを検出できることを検証する。
func TestOutgoingFrom_Done_DetectsInjectedOutgoingTransition(t *testing.T) {
	corrupted := append([]Transition{}, Table...)
	corrupted = append(corrupted, Transition{
		ID: "T-broken", From: StatusDone, Op: OpClassify,
		Target: TransitionTarget{Fixed: StatusClassified},
	})
	got := OutgoingFrom(corrupted, StatusDone)
	if len(got) != 1 {
		t.Fatalf("OutgoingFrom(corrupted, done) = %v, want 1 violation", got)
	}
}
