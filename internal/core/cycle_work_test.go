package core

import (
	"context"
	"testing"
)

// 何も無いストアでは、全段 0・合計 0 になる。
func TestCountCycleWork_EmptyStoreIsZero(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	s := newStoreForTest(t)
	w, err := s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if *w != (CycleWork{}) {
		t.Fatalf("work = %+v, want zero", *w)
	}
}

// 宣言（agent.json）が無ければ、全段 0。
func TestCountCycleWork_NoAgentDeclarationIsZero(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	w, err := f.s.CountCycleWork(context.Background(), nil, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if *w != (CycleWork{}) {
		t.Fatalf("work = %+v, want zero", *w)
	}
}

// 着手中で承認済みの計画を持つ課題は、委譲の段で 1 件と数える。合計は段の合計。
// 接続ツールの宣言が無ければ、委譲の段は 0 になる。数えは状態を書かない。
func TestCountCycleWork_InProgressChallengeCountsOneRun(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Run != 1 || w.Total != 1 {
		t.Fatalf("work = %+v, want run=1 total=1", *w)
	}
	if n := len(f.delegateRuns(t)); n != 0 {
		t.Fatalf("delegate runs = %d, want 0 (counting must not write)", n)
	}

	none, err := f.s.CountCycleWork(context.Background(), f.agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *none != (CycleWork{}) {
		t.Fatalf("work without connectors = %+v, want zero", *none)
	}
}

// 未分類の課題は分類の段に数える。
func TestCountCycleWork_UnclassifiedCountsClassify(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	mustCreateChallenge(t, f.s, "new one")
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Classify != 1 {
		t.Fatalf("classify = %d, want 1 (%+v)", w.Stages.Classify, *w)
	}
	if w.Total != w.Stages.Release+w.Stages.Classify+w.Stages.Plan+w.Stages.Run+w.Stages.Verify+w.Stages.Learn+w.Stages.Improve+w.Stages.Cleanup {
		t.Fatalf("total %d is not the sum of stages %+v", w.Total, w.Stages)
	}
}

// 承認済みの計画が今の宣言と合わない課題は、起動しないと決まるので委譲に数えない。
func TestCountCycleWork_ExcludesPlanUnavailable(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	other := *f.conn
	other.Repos = nil
	w, err := f.s.CountCycleWork(context.Background(), f.agent, &other)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Run != 0 {
		t.Fatalf("run = %d, want 0 (%+v)", w.Stages.Run, *w)
	}
}
