package core

import "testing"

// --- Priority (P0|P1|P2) ---

func TestParsePriority_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"P0", "P1", "P2"} {
		if _, ok := ParsePriority(s); !ok {
			t.Errorf("ParsePriority(%q) ok = false, want true", s)
		}
	}
}

func TestParsePriority_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"P3", "p0", ""} {
		if _, ok := ParsePriority(s); ok {
			t.Errorf("ParsePriority(%q) ok = true, want false", s)
		}
	}
}

// TestParsePriority_TrimsSurroundingWhitespace は、Status と同じ規則で
// 前後の空白を除いて一致判定することを検証する。
func TestParsePriority_TrimsSurroundingWhitespace(t *testing.T) {
	got, ok := ParsePriority(" P0 ")
	if !ok || got != PriorityP0 {
		t.Errorf("ParsePriority(%q) = (%q, %v), want (%q, true)", " P0 ", got, ok, PriorityP0)
	}
}

// --- Urgency (高|中|低。現行台帳と同じ日本語の値が正でコードは無い) ---

func TestParseUrgency_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"高", "中", "低"} {
		if _, ok := ParseUrgency(s); !ok {
			t.Errorf("ParseUrgency(%q) ok = false, want true", s)
		}
	}
}

func TestParseUrgency_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"最高", "low", ""} {
		if _, ok := ParseUrgency(s); ok {
			t.Errorf("ParseUrgency(%q) ok = true, want false", s)
		}
	}
}

// TestParseUrgency_TrimsSurroundingWhitespace は、Status と同じ規則で
// 前後の空白を除いて一致判定することを検証する。
func TestParseUrgency_TrimsSurroundingWhitespace(t *testing.T) {
	got, ok := ParseUrgency(" 高 ")
	if !ok || got != UrgencyHigh {
		t.Errorf("ParseUrgency(%q) = (%q, %v), want (%q, true)", " 高 ", got, ok, UrgencyHigh)
	}
}

// --- VerifyResult (met|not_met|uncertain) ---

func TestParseVerifyResult_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"met", "not_met", "uncertain"} {
		if _, ok := ParseVerifyResult(s); !ok {
			t.Errorf("ParseVerifyResult(%q) ok = false, want true", s)
		}
	}
}

func TestParseVerifyResult_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"MET", "notmet", ""} {
		if _, ok := ParseVerifyResult(s); ok {
			t.Errorf("ParseVerifyResult(%q) ok = true, want false", s)
		}
	}
}

// --- OperationKind（不可逆操作の種類。release|delete|external_send|other） ---

func TestParseOperationKind_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"release", "delete", "external_send", "other"} {
		if _, ok := ParseOperationKind(s); !ok {
			t.Errorf("ParseOperationKind(%q) ok = false, want true", s)
		}
	}
}

func TestParseOperationKind_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"deploy", "", "RELEASE"} {
		if _, ok := ParseOperationKind(s); ok {
			t.Errorf("ParseOperationKind(%q) ok = true, want false", s)
		}
	}
}

// --- OperationState（不可逆操作の状態。pending|approved|rejected） ---

func TestParseOperationState_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"pending", "approved", "rejected"} {
		if _, ok := ParseOperationState(s); !ok {
			t.Errorf("ParseOperationState(%q) ok = false, want true", s)
		}
	}
}

func TestParseOperationState_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"executed", "", "PENDING", "approve"} {
		if _, ok := ParseOperationState(s); ok {
			t.Errorf("ParseOperationState(%q) ok = true, want false", s)
		}
	}
}

// --- ApprovalKind（承認の種類。plan|completion|release|budget） ---

func TestParseApprovalKind_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"plan", "completion", "release", "budget"} {
		if _, ok := ParseApprovalKind(s); !ok {
			t.Errorf("ParseApprovalKind(%q) ok = false, want true", s)
		}
	}
}

func TestParseApprovalKind_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"irreversible_op", "", "PLAN"} {
		if _, ok := ParseApprovalKind(s); ok {
			t.Errorf("ParseApprovalKind(%q) ok = true, want false", s)
		}
	}
}

// --- ApprovalDecision（決定。approved|rejected） ---

func TestParseApprovalDecision_AcceptsKnownValues(t *testing.T) {
	for _, s := range []string{"approved", "rejected"} {
		if _, ok := ParseApprovalDecision(s); !ok {
			t.Errorf("ParseApprovalDecision(%q) ok = false, want true", s)
		}
	}
}

func TestParseApprovalDecision_RejectsUnknownValue(t *testing.T) {
	for _, s := range []string{"accepted", "", "Approved"} {
		if _, ok := ParseApprovalDecision(s); ok {
			t.Errorf("ParseApprovalDecision(%q) ok = true, want false", s)
		}
	}
}

// RunResultValues（CLI の閉集合の照合が使う）は、run.result の検査（valid）が受理する値と
// 一致する（片方だけに値を足す取りこぼしを防ぐ）。
func TestRunResultValues_MatchWhatTheStoreAccepts(t *testing.T) {
	values := RunResultValues()
	if len(values) != 8 {
		t.Fatalf("len(RunResultValues()) = %d, want 8", len(values))
	}
	seen := map[RunResult]bool{}
	for _, v := range values {
		if !v.valid() {
			t.Errorf("RunResultValues has %q, which the store does not accept", v)
		}
		if seen[v] {
			t.Errorf("RunResultValues has %q twice", v)
		}
		seen[v] = true
	}
	if RunResult("no_such_result").valid() {
		t.Error("an unlisted result must not be accepted")
	}
}

// NotStartedReasonValues は 仕様の 10 値で、写しを返す（呼び出し側が書き換えても本体は変わらない）。
func TestNotStartedReasonValues_AreTheTenAndACopy(t *testing.T) {
	got := NotStartedReasonValues()
	want := []NotStartedReason{NotStartedCycleBudget, NotStartedRateLimited, NotStartedRunBudget, NotStartedSlotUnavailable,
		NotStartedFailureLimit, NotStartedReworkLimit, NotStartedUpstreamFetchFailed, NotStartedSerialized, NotStartedWaitingExternal, NotStartedPlanUnavailable}
	if len(got) != len(want) {
		t.Fatalf("NotStartedReasonValues() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NotStartedReasonValues()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got[0] = "tampered"
	if NotStartedReasonValues()[0] != NotStartedCycleBudget {
		t.Error("NotStartedReasonValues returned the internal slice, not a copy")
	}
}
