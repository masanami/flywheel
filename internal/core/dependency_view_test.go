package core

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func insertBindingForTest(t *testing.T, s *Store, cid int64, upstream, policy string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
			VALUES (?, 's', ?, 'https://example.invalid/x', '1:a', ?, ?, '2026-10-24T00:00:00.000Z', '2026-10-24T00:00:00.000Z')`,
			cid, "o/r#"+formatChallengeID(cid), upstream, policy)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGetChallenge_DependenciesAndDependents(t *testing.T) {
	s := depFixture(t, 5)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2", "C-3", "C-4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-5", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	setChallengeStatus(t, s, 3, StatusDone)
	setChallengeStatus(t, s, 4, StatusDone)
	setReleaseOp(t, s, 4, "pending")
	insertBindingForTest(t, s, 2, "closed", "out_of_policy")

	d, err := s.GetChallenge(ctx, "C-1")
	if err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	want := []DependencyView{
		{ChallengeID: "C-2", Status: StatusUnclassified, Satisfied: false, UpstreamState: str("closed"), PolicyState: str("out_of_policy")},
		{ChallengeID: "C-3", Status: StatusDone, Satisfied: true},
		{ChallengeID: "C-4", Status: StatusDone, Satisfied: false},
	}
	if !reflect.DeepEqual(d.Dependencies, want) {
		t.Errorf("Dependencies = %+v, want %+v", d.Dependencies, want)
	}
	if len(d.Dependents) != 0 || d.Dependents == nil {
		t.Errorf("Dependents of C-1 = %#v, want empty non-nil", d.Dependents)
	}
	d2, err := s.GetChallenge(ctx, "C-2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d2.Dependents, []string{"C-1", "C-5"}) {
		t.Errorf("Dependents of C-2 = %v", d2.Dependents)
	}
	if d2.Dependencies == nil || len(d2.Dependencies) != 0 {
		t.Errorf("Dependencies of C-2 = %#v, want empty non-nil", d2.Dependencies)
	}
}

func TestGetChallenge_CyclicDependencyIsNotSatisfiedEvenWhenDone(t *testing.T) {
	s := depFixture(t, 2)
	insertDepEdge(t, s, 1, 2)
	insertDepEdge(t, s, 2, 1)
	setChallengeStatus(t, s, 2, StatusDone)
	d, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Dependencies) != 1 || d.Dependencies[0].Satisfied {
		t.Errorf("Dependencies = %+v, want one unsatisfied", d.Dependencies)
	}
}

func TestGetOverview_UnmetDependenciesAndCycleDiscrepancies(t *testing.T) {
	s := depFixture(t, 7)
	ctx := context.Background()
	// C-1 は C-2（未完了）と C-3（完了・release 済み）に依存。C-4 は依存なし。
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2", "C-3"}); err != nil {
		t.Fatal(err)
	}
	setChallengeStatus(t, s, 3, StatusDone)
	setReleaseOp(t, s, 3, "executed")
	// 輪: C-5→C-6→C-7→C-5。C-4 は輪に入る C-5 へ依存するだけで、輪には含まない。
	insertDepEdge(t, s, 5, 6)
	insertDepEdge(t, s, 6, 7)
	insertDepEdge(t, s, 7, 5)
	insertDepEdge(t, s, 4, 5)

	ov, err := s.GetOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := ov.UnmetDependencies["C-1"]; len(got) != 1 || got[0].ChallengeID != "C-2" {
		t.Errorf("unmet C-1 = %+v, want [C-2]", got)
	}
	if got := ov.UnmetDependencies["C-5"]; len(got) != 1 || got[0].ChallengeID != "C-6" {
		t.Errorf("unmet C-5 = %+v, want [C-6]", got)
	}
	if got := ov.UnmetDependencies["C-4"]; len(got) != 1 || got[0].ChallengeID != "C-5" {
		t.Errorf("unmet C-4 = %+v, want [C-5]", got)
	}
	if _, ok := ov.UnmetDependencies["C-2"]; ok {
		t.Errorf("C-2 has no dependencies but has an entry")
	}

	ring := []string{"C-5", "C-6", "C-7"}
	cycles := ov.Discrepancies
	if len(cycles) != 3 {
		t.Fatalf("discrepancies = %+v, want 3 (one per ring member)", cycles)
	}
	for i, d := range cycles {
		if d.ChallengeID != ring[i] || !reflect.DeepEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindDependencyCycle}) || !reflect.DeepEqual(d.ChallengeIDs, ring) {
			t.Errorf("discrepancy[%d] = %+v, want %s with ring %v", i, d, ring[i], ring)
		}
	}
}

func TestGetOverview_NoCycleNoDiscrepancy(t *testing.T) {
	s := depFixture(t, 3)
	if _, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Discrepancies) != 0 {
		t.Errorf("Discrepancies = %+v, want none", ov.Discrepancies)
	}
}

// 輪に含まれる課題が上流の食い違いも持つとき、要素は 1 つだけで kinds に両方が入る（輪の課題ごとに要素を 1 つ）。
// 食い違いは課題 ID の昇順（上流の食い違いの C-9 が、あとから足す輪の C-1・C-2 より後ろ）。
func TestGetOverview_CycleMergesIntoExistingDiscrepancyAndOrdersByChallengeID(t *testing.T) {
	s := depFixture(t, 9)
	insertDepEdge(t, s, 1, 2)
	insertDepEdge(t, s, 2, 1)
	insertBindingForTest(t, s, 2, "closed", "in_policy")
	insertBindingForTest(t, s, 9, "closed", "in_policy")

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ring := []string{"C-1", "C-2"}
	want := []Discrepancy{
		{ChallengeID: "C-1", Kinds: []DiscrepancyKind{DiscrepancyKindDependencyCycle}, ChallengeIDs: ring},
		{ChallengeID: "C-2", Kinds: []DiscrepancyKind{DiscrepancyKindUpstreamClosed, DiscrepancyKindDependencyCycle}, ChallengeIDs: ring},
		{ChallengeID: "C-9", Kinds: []DiscrepancyKind{DiscrepancyKindUpstreamClosed}},
	}
	if !reflect.DeepEqual(ov.Discrepancies, want) {
		t.Errorf("Discrepancies = %+v, want %+v", ov.Discrepancies, want)
	}
}
