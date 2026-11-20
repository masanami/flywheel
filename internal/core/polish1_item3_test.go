package core

import (
	"context"
	"testing"
)

// 項目 3: 全部のタスクが済み、開いた契約の実行が無い、統合先を使う着手中の課題（上げる依頼を始められる課題）は、
// サイクルを回すと進むものに数える。
func TestPolish1_CountCycleWorkCountsAChallengeThatCanStartTheUpRequest(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(id)
	if ids, err := f.s.reflectCandidates(context.Background(), &cid); err != nil || len(ids) != 1 {
		t.Fatalf("precondition: reflect candidates = %v, %v", ids, err)
	}
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Run < 1 || w.Total < 1 {
		t.Fatalf("run = %d, total = %d, want the up request counted", w.Stages.Run, w.Total)
	}
}
