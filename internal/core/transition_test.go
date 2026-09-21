package core

import "testing"

// TestLookup_T1_CreateHasNoSource は、T1（create）が遷移元を持たず、
// 常に未分類を返すことを検証する。
func TestLookup_T1_CreateHasNoSource(t *testing.T) {
	tr, ok := Lookup(NoStatus, OpCreate)
	if !ok {
		t.Fatalf("Lookup(NoStatus, OpCreate) ok = false, want true")
	}
	got, err := tr.Target.Resolve(Table, NoStatus)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != StatusUnclassified {
		t.Errorf("T1 target = %q, want %q", got, StatusUnclassified)
	}
	if tr.RequiresVerification {
		t.Errorf("T1 RequiresVerification = true, want false")
	}
}

// TestLookup_AllRows は T1〜T15（T11は4遷移元に展開）の全行が
// docs/features/m1-core.md §状態機械 の遷移表と一致することを検証する。
func TestLookup_AllRows(t *testing.T) {
	cases := []struct {
		id      string
		from    Status
		op      Operation
		wantTo  Status // 固定遷移先。T12（保留に入る直前の状態）は別テストで検証するためここでは使わない
		wantVfy bool
		toHold  bool // true なら「保留に入る直前の状態」への遷移（T12専用）
	}{
		{id: "T2", from: StatusUnclassified, op: OpClassify, wantTo: StatusClassified, wantVfy: false},
		{id: "T3", from: StatusClassified, op: OpPlan, wantTo: StatusAwaitingPlanApproval, wantVfy: false},
		{id: "T4", from: StatusAwaitingPlanApproval, op: OpPlan, wantTo: StatusAwaitingPlanApproval, wantVfy: false},
		{id: "T5", from: StatusAwaitingPlanApproval, op: OpApprove, wantTo: StatusInProgress, wantVfy: true},
		{id: "T6", from: StatusAwaitingPlanApproval, op: OpReject, wantTo: StatusClassified, wantVfy: true},
		{id: "T7", from: StatusInProgress, op: OpSubmit, wantTo: StatusVerifying, wantVfy: false},
		{id: "T8", from: StatusVerifying, op: OpVerifyMet, wantTo: StatusAwaitingCompletionApproval, wantVfy: false},
		{id: "T9", from: StatusVerifying, op: OpVerifyNotMet, wantTo: StatusInProgress, wantVfy: false},
		{id: "T10", from: StatusVerifying, op: OpVerifyUncertain, wantTo: StatusAwaitingHuman, wantVfy: false},
		{id: "T11-unclassified", from: StatusUnclassified, op: OpHold, wantTo: StatusAwaitingHuman, wantVfy: false},
		{id: "T11-classified", from: StatusClassified, op: OpHold, wantTo: StatusAwaitingHuman, wantVfy: false},
		{id: "T11-in_progress", from: StatusInProgress, op: OpHold, wantTo: StatusAwaitingHuman, wantVfy: false},
		{id: "T11-verifying", from: StatusVerifying, op: OpHold, wantTo: StatusAwaitingHuman, wantVfy: false},
		{id: "T12", from: StatusAwaitingHuman, op: OpAnswer, wantVfy: true, toHold: true},
		{id: "T13", from: StatusAwaitingCompletionApproval, op: OpApprove, wantTo: StatusDone, wantVfy: true},
		{id: "T14", from: StatusAwaitingCompletionApproval, op: OpApproveHoldRelease, wantTo: StatusDone, wantVfy: true},
		{id: "T15", from: StatusAwaitingCompletionApproval, op: OpReject, wantTo: StatusInProgress, wantVfy: true},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			tr, ok := Lookup(c.from, c.op)
			if !ok {
				t.Fatalf("Lookup(%q, %q) ok = false, want true", c.from, c.op)
			}
			if tr.RequiresVerification != c.wantVfy {
				t.Errorf("RequiresVerification = %v, want %v", tr.RequiresVerification, c.wantVfy)
			}
			if c.toHold {
				if !tr.Target.ReturnToPrecedingHoldStatus {
					t.Errorf("Target.ReturnToPrecedingHoldStatus = false, want true")
				}
				return
			}
			if tr.Target.ReturnToPrecedingHoldStatus {
				t.Fatalf("Target.ReturnToPrecedingHoldStatus = true, want false")
			}
			got, err := tr.Target.Resolve(Table, NoStatus)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != c.wantTo {
				t.Errorf("target = %q, want %q", got, c.wantTo)
			}
		})
	}
}

// TestLookup_T13AndT14AreDistinctRows は、同じ遷移元（完了確認待ち）から
// approve と approve --hold-release が別の行として引けることを検証する。
func TestLookup_T13AndT14AreDistinctRows(t *testing.T) {
	t13, ok := Lookup(StatusAwaitingCompletionApproval, OpApprove)
	if !ok {
		t.Fatalf("T13 not found")
	}
	t14, ok := Lookup(StatusAwaitingCompletionApproval, OpApproveHoldRelease)
	if !ok {
		t.Fatalf("T14 not found")
	}
	got13, _ := t13.Target.Resolve(Table, NoStatus)
	got14, _ := t14.Target.Resolve(Table, NoStatus)
	if got13 != StatusDone || got14 != StatusDone {
		t.Fatalf("both T13 and T14 should target done, got T13=%q T14=%q", got13, got14)
	}
	// 行として区別できることの確認（Operation が異なる）。
	if t13.Op == t14.Op {
		t.Errorf("T13.Op == T14.Op, want distinct operations")
	}
}

// TestLookup_T8ToT10AreDistinctRows は、検証中からの verify の3つの結果が
// 別の行として引けることを検証する。
func TestLookup_T8ToT10AreDistinctRows(t *testing.T) {
	met, ok := Lookup(StatusVerifying, OpVerifyMet)
	if !ok {
		t.Fatalf("T8 not found")
	}
	notMet, ok := Lookup(StatusVerifying, OpVerifyNotMet)
	if !ok {
		t.Fatalf("T9 not found")
	}
	uncertain, ok := Lookup(StatusVerifying, OpVerifyUncertain)
	if !ok {
		t.Fatalf("T10 not found")
	}
	gotMet, _ := met.Target.Resolve(Table, NoStatus)
	gotNotMet, _ := notMet.Target.Resolve(Table, NoStatus)
	gotUncertain, _ := uncertain.Target.Resolve(Table, NoStatus)
	if gotMet != StatusAwaitingCompletionApproval {
		t.Errorf("T8 target = %q, want %q", gotMet, StatusAwaitingCompletionApproval)
	}
	if gotNotMet != StatusInProgress {
		t.Errorf("T9 target = %q, want %q", gotNotMet, StatusInProgress)
	}
	if gotUncertain != StatusAwaitingHuman {
		t.Errorf("T10 target = %q, want %q", gotUncertain, StatusAwaitingHuman)
	}
}

// TestLookup_AllPairsNotInTableAreRejected は、8状態×既知の操作の直積のうち
// 表に無い組がすべて不可（ok=false）であることを検証する（T1 は遷移元を持たないため対象外）。
func TestLookup_AllPairsNotInTableAreRejected(t *testing.T) {
	valid := map[[2]string]bool{}
	for _, tr := range Table {
		if tr.From == NoStatus {
			continue
		}
		valid[[2]string{string(tr.From), string(tr.Op)}] = true
	}

	statuses := make([]Status, len(StatusVocabulary))
	for i, e := range StatusVocabulary {
		statuses[i] = e.Code
	}
	ops := []Operation{
		OpClassify, OpPlan, OpSubmit, OpVerifyMet, OpVerifyNotMet, OpVerifyUncertain,
		OpHold, OpAnswer, OpApprove, OpApproveHoldRelease, OpReject,
	}

	checked := 0
	for _, s := range statuses {
		for _, op := range ops {
			checked++
			_, ok := Lookup(s, op)
			want := valid[[2]string{string(s), string(op)}]
			if ok != want {
				t.Errorf("Lookup(%q, %q) ok = %v, want %v", s, op, ok, want)
			}
		}
	}
	if checked != len(statuses)*len(ops) {
		t.Fatalf("checked %d pairs, want %d", checked, len(statuses)*len(ops))
	}
}

// TestLookup_CreateNotAvailableFromExistingStatus は、既存の状態から create は
// 引けないことを検証する（T1 の遷移元は「なし」のみ）。
func TestLookup_CreateNotAvailableFromExistingStatus(t *testing.T) {
	if _, ok := Lookup(StatusUnclassified, OpCreate); ok {
		t.Errorf("Lookup(unclassified, create) ok = true, want false")
	}
}

// TestTable_RowCount は、Table の物理行数が18（T1〜T15、うちT11が4遷移元に展開）であることを検証する。
func TestTable_RowCount(t *testing.T) {
	if len(Table) != 18 {
		t.Fatalf("len(Table) = %d, want 18", len(Table))
	}
}

// TestTransitionTargetResolve_PrecedingHold_AcceptsValidPreceding は、
// T12 の Resolve が、保留の直前状態として妥当な状態（T10・T11 の遷移元）を
// そのまま返すことを検証する。
func TestTransitionTargetResolve_PrecedingHold_AcceptsValidPreceding(t *testing.T) {
	tr, ok := Lookup(StatusAwaitingHuman, OpAnswer)
	if !ok {
		t.Fatalf("T12 not found")
	}
	for _, preceding := range []Status{StatusUnclassified, StatusClassified, StatusInProgress, StatusVerifying} {
		got, err := tr.Target.Resolve(Table, preceding)
		if err != nil {
			t.Fatalf("Resolve(%q): unexpected error %v", preceding, err)
		}
		if got != preceding {
			t.Errorf("Resolve(%q) = %q, want %q", preceding, got, preceding)
		}
	}
}

// TestTransitionTargetResolve_PrecedingHold_RejectsInvalidPreceding は、
// T10・T11 の遷移元になり得ない状態（例: 完了）を直前状態として渡すとエラーになることを検証する。
func TestTransitionTargetResolve_PrecedingHold_RejectsInvalidPreceding(t *testing.T) {
	tr, ok := Lookup(StatusAwaitingHuman, OpAnswer)
	if !ok {
		t.Fatalf("T12 not found")
	}
	if _, err := tr.Target.Resolve(Table, StatusDone); err == nil {
		t.Errorf("Resolve(done) err = nil, want error")
	}
}

// TestHoldEntrySources は、人間対応待ちへ遷移できる状態の集合
// （T10・T11 の遷移元の和集合）が、未分類・分類済・着手中・検証中であることを検証する。
func TestHoldEntrySources(t *testing.T) {
	got := HoldEntrySources(Table)
	want := map[Status]bool{
		StatusUnclassified: true,
		StatusClassified:   true,
		StatusInProgress:   true,
		StatusVerifying:    true,
	}
	if len(got) != len(want) {
		t.Fatalf("HoldEntrySources = %v, want %v", got, want)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected status in HoldEntrySources: %q", s)
		}
	}
}

// TestHoldEntrySources_ExcludesNoStatusFrom は、From が NoStatus（T1 相当）の行は
// 人間対応待ちを固定遷移先に持っていても対象に含めないことを検証する
// （fail-closed）。
func TestHoldEntrySources_ExcludesNoStatusFrom(t *testing.T) {
	corrupted := append([]Transition{}, Table...)
	corrupted = append(corrupted, Transition{
		ID: "T-broken", From: NoStatus, Op: OpCreate,
		Target: TransitionTarget{Fixed: StatusAwaitingHuman},
	})
	got := HoldEntrySources(corrupted)
	for _, s := range got {
		if s == NoStatus {
			t.Fatalf("HoldEntrySources(corrupted) = %v, must not include NoStatus", got)
		}
	}
}

// TestIsTerminal_Done は、完了が終端状態（出る遷移が無い）であることを検証する（AC-27相当）。
func TestIsTerminal_Done(t *testing.T) {
	if !IsTerminal(Table, StatusVocabulary, StatusDone) {
		t.Errorf("IsTerminal(Table, vocab, done) = false, want true")
	}
}

// TestIsTerminal_NonTerminalStatuses は、完了以外の状態が終端でないことを検証する。
func TestIsTerminal_NonTerminalStatuses(t *testing.T) {
	for _, e := range StatusVocabulary {
		if e.Code == StatusDone {
			continue
		}
		if IsTerminal(Table, StatusVocabulary, e.Code) {
			t.Errorf("IsTerminal(Table, vocab, %q) = true, want false", e.Code)
		}
	}
}

// TestIsTerminal_RejectsUnknownStatus は、語彙に無い状態を fail-closed で
// 終端扱いしない（false を返す）ことを検証する（未知状態を終端＝完了と同列に扱う fail-open を避ける）。
func TestIsTerminal_RejectsUnknownStatus(t *testing.T) {
	if IsTerminal(Table, StatusVocabulary, Status("no_such_status")) {
		t.Errorf("IsTerminal(Table, vocab, unknown) = true, want false (fail-closed)")
	}
}

// TestOperationForVerify_MapsAllKnownResults は、VerifyResult の3値すべてが
// 対応する Operation（T8〜T10）へ写像され、Lookup(検証中, op) で引けることを検証する。
func TestOperationForVerify_MapsAllKnownResults(t *testing.T) {
	cases := []struct {
		result VerifyResult
		wantOp Operation
	}{
		{VerifyResultMet, OpVerifyMet},
		{VerifyResultNotMet, OpVerifyNotMet},
		{VerifyResultUncertain, OpVerifyUncertain},
	}
	for _, c := range cases {
		op, ok := OperationForVerify(c.result)
		if !ok || op != c.wantOp {
			t.Errorf("OperationForVerify(%q) = (%q, %v), want (%q, true)", c.result, op, ok, c.wantOp)
		}
		if _, found := Lookup(StatusVerifying, op); !found {
			t.Errorf("Lookup(verifying, %q) not found", op)
		}
	}
}

// TestOperationForVerify_RejectsUnknownResult は、閉集合外の VerifyResult では
// ok=false を返すことを検証する。
func TestOperationForVerify_RejectsUnknownResult(t *testing.T) {
	if _, ok := OperationForVerify(VerifyResult("bogus")); ok {
		t.Errorf("OperationForVerify(bogus) ok = true, want false")
	}
}

// TestOperationForApprove_MapsHoldReleaseFlag は、--hold-release の有無に応じて
// approve と approve_hold_release が正しく区別されることを検証する。
func TestOperationForApprove_MapsHoldReleaseFlag(t *testing.T) {
	if got := OperationForApprove(false); got != OpApprove {
		t.Errorf("OperationForApprove(false) = %q, want %q", got, OpApprove)
	}
	if got := OperationForApprove(true); got != OpApproveHoldRelease {
		t.Errorf("OperationForApprove(true) = %q, want %q", got, OpApproveHoldRelease)
	}
}
