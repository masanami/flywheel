package core

import "testing"

// T18（#485）: 完了 → 着手中。本番反映でマージできなくなった場合に限る専用の書き込みだけが使う行で、
// 通常の操作の引き（Lookup）・出る遷移（OutgoingFrom）・終端の判定（IsTerminal）には現れない。
func TestT18_ReopenForReleaseIsARestrictedRow(t *testing.T) {
	tr, ok := LookupRestricted(StatusDone, OpReopenForRelease)
	if !ok {
		t.Fatalf("LookupRestricted(done, reopen_for_release) not found")
	}
	if tr.ID != "T18" || tr.Target.Fixed != StatusInProgress || tr.RequiresVerification || !tr.Restricted {
		t.Errorf("T18 = %+v, want restricted done -> in_progress without verification", tr)
	}
	if _, ok := Lookup(StatusDone, OpReopenForRelease); ok {
		t.Errorf("Lookup(done, reopen_for_release) found the restricted row, want only LookupRestricted to return it")
	}
	for _, e := range StatusVocabulary {
		if e.Code == StatusDone {
			continue
		}
		if _, ok := LookupRestricted(e.Code, OpReopenForRelease); ok {
			t.Errorf("LookupRestricted(%q, reopen_for_release) found, want only done", e.Code)
		}
	}
	if got := OutgoingFrom(Table, StatusDone); len(got) != 0 {
		t.Errorf("OutgoingFrom(Table, done) = %v, want empty (the restricted row is not an ordinary outgoing transition)", got)
	}
	if !IsTerminal(Table, StatusVocabulary, StatusDone) {
		t.Errorf("IsTerminal(done) = false, want true: every ordinary operation still sees done as terminal")
	}
	// 通常の操作のトークンは、制限つきの行を引けない。
	for _, op := range []Operation{OpClassify, OpPlan, OpSubmit, OpVerifyMet, OpHold, OpAnswer, OpApprove, OpReject, OpWithdraw} {
		if _, ok := Lookup(StatusDone, op); ok {
			t.Errorf("Lookup(done, %q) found, want none", op)
		}
	}
}
