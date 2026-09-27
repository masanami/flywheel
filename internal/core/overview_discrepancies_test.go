package core

import (
	"context"
	"testing"
	"time"
)

// このファイルは #58 の「食い違いの表示」（AC-80〜AC-86）のうち、core 側
// （GetOverview の Discrepancies）を検証する。CLI 側（status --json の
// needs_human.discrepancies）は internal/cli/overview_discrepancies_test.go が
// 担当する。

func bindDiscrepancyFixture(t *testing.T, s *Store, title, externalKey string, upstream upstreamState, policy policyState) *Challenge {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	in := validBindingInput(externalKey)
	in.UpstreamState = upstream
	in.PolicyState = policy
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest(%s) error = %v", externalKey, err)
	}
	return ch
}

func findDiscrepancy(discrepancies []Discrepancy, challengeID string) *Discrepancy {
	for i := range discrepancies {
		if discrepancies[i].ChallengeID == challengeID {
			return &discrepancies[i]
		}
	}
	return nil
}

func kindsEqual(a, b []DiscrepancyKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- AC-80: upstream_state=closed の未完了課題 ---

func TestGetOverview_Discrepancies_UpstreamClosed(t *testing.T) {
	s := newStoreForTest(t)
	ch := bindDiscrepancyFixture(t, s, "closed", "o/r#1", upstreamStateClosed, policyStateInPolicy)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindUpstreamClosed}) {
		t.Fatalf("discrepancy = %+v, want kinds=[upstream_closed] (AC-80)", d)
	}
}

// --- AC-81: upstream_state=missing の未完了課題 ---

func TestGetOverview_Discrepancies_UpstreamMissing(t *testing.T) {
	s := newStoreForTest(t)
	ch := bindDiscrepancyFixture(t, s, "missing", "o/r#1", upstreamStateMissing, policyStateInPolicy)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindUpstreamMissing}) {
		t.Fatalf("discrepancy = %+v, want kinds=[upstream_missing] (AC-81)", d)
	}
}

// --- AC-82: policy_state=out_of_policy の未完了課題 ---

func TestGetOverview_Discrepancies_OutOfPolicy(t *testing.T) {
	s := newStoreForTest(t)
	ch := bindDiscrepancyFixture(t, s, "oop", "o/r#1", upstreamStateOpen, policyStateOutOfPolicy)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindOutOfPolicy}) {
		t.Fatalf("discrepancy = %+v, want kinds=[out_of_policy] (AC-82)", d)
	}
}

// --- AC-83: closed かつ out_of_policy の課題は両方の kinds を含む ---

func TestGetOverview_Discrepancies_ClosedAndOutOfPolicyIncludesBothKinds(t *testing.T) {
	s := newStoreForTest(t)
	ch := bindDiscrepancyFixture(t, s, "both", "o/r#1", upstreamStateClosed, policyStateOutOfPolicy)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindUpstreamClosed, DiscrepancyKindOutOfPolicy}) {
		t.Fatalf("discrepancy = %+v, want kinds=[upstream_closed, out_of_policy] (AC-83)", d)
	}
}

// --- AC-84: 食い違いのある課題が完了すると discrepancies に出なくなる ---

func TestGetOverview_Discrepancies_DisappearsWhenChallengeCompletes(t *testing.T) {
	s := newStoreForTest(t)
	ch := bindDiscrepancyFixture(t, s, "closed", "o/r#1", upstreamStateClosed, policyStateInPolicy)
	cid := mustParseChallengeID(t, ch.ID)
	setChallengeStatus(t, s, cid, StatusDone)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if d := findDiscrepancy(ov.Discrepancies, ch.ID); d != nil {
		t.Fatalf("discrepancy = %+v, want none for a done challenge (AC-84)", d)
	}
}

// --- 対応の無い課題（create で作った課題）は候補にならない（AC-86 の補助） ---

func TestGetOverview_Discrepancies_ChallengeWithoutBindingIsNeverACandidate(t *testing.T) {
	s := newStoreForTest(t)
	mustCreateChallenge(t, s, "no binding")

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.Discrepancies) != 0 {
		t.Fatalf("Discrepancies = %+v, want empty", ov.Discrepancies)
	}
}

// --- AC-86: 食い違いの無いワークスペースは空配列 ---

func TestGetOverview_Discrepancies_EmptyWorkspaceReturnsEmptySlice(t *testing.T) {
	s := newStoreForTest(t)
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if ov.Discrepancies == nil || len(ov.Discrepancies) != 0 {
		t.Fatalf("Discrepancies = %#v, want a non-nil empty slice", ov.Discrepancies)
	}
}

// --- challenge_id 昇順 ---

func TestGetOverview_Discrepancies_OrderedByChallengeIDAscending(t *testing.T) {
	s := newStoreForTest(t)
	insertChallengeWithID(t, s, 5, "c5", StatusUnclassified)
	insertChallengeWithID(t, s, 2, "c2", StatusUnclassified)
	for id, key := range map[int64]string{5: "o/r#5", 2: "o/r#2"} {
		in := validBindingInput(key)
		in.UpstreamState = upstreamStateClosed
		if _, err := createSourceBindingForTest(s, formatChallengeID(id), time.Now(), in); err != nil {
			t.Fatalf("createSourceBindingForTest(%d) error = %v", id, err)
		}
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.Discrepancies) != 2 || ov.Discrepancies[0].ChallengeID != "C-2" || ov.Discrepancies[1].ChallengeID != "C-5" {
		t.Fatalf("Discrepancies = %+v, want [C-2, C-5] in order", ov.Discrepancies)
	}
}

// --- 閉集合の双方向照合（仕様の全 5 種類。upstream_commented・upstream_updated
// は #72〔上流の更新の観測と既読〕が足す） ---

func TestDiscrepancyKindValues_MatchSpecClosedSet(t *testing.T) {
	want := []string{"upstream_closed", "upstream_missing", "out_of_policy", "upstream_commented", "upstream_updated"}
	got := DiscrepancyKindValues()
	if len(got) != len(want) {
		t.Fatalf("DiscrepancyKindValues() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("DiscrepancyKindValues()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
