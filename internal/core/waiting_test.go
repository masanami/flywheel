package core

import (
	"context"
	"reflect"
	"testing"
)

// docs/features/ui-relation-graph.md §待っている理由。ボードの詳細は、まだ終わっていない前提の課題と、
// いま効いている「衝突のため後回し」を別々に示す。

func waitingOf(t *testing.T, s *Store, id string) WaitingReasons {
	t.Helper()
	d, err := s.GetChallengeWithoutReap(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallengeWithoutReap(%s): %v", id, err)
	}
	return d.Waiting
}

func TestWaiting_ListsUnmetPrerequisitesAndTheConflictThisChallengeYieldsTo(t *testing.T) {
	s, _ := graphStore(t, StatusInProgress, StatusDone, StatusClassified, StatusClassified)
	// C-4 は C-2（完了）と C-3（未完了）に依存し、C-1 を先に委譲させて後回しにした。
	insertDepEdge(t, s, 4, 2)
	insertDepEdge(t, s, 4, 3)
	insertConflictForTest(t, s, 4, 1, true)
	got := waitingOf(t, s, "C-4")
	wantPre := []WaitingChallenge{{ChallengeID: "C-3", Title: "t", Status: StatusClassified}}
	if !reflect.DeepEqual(got.Prerequisites, wantPre) {
		t.Errorf("prerequisites = %+v, want %+v", got.Prerequisites, wantPre)
	}
	wantConf := []WaitingChallenge{{ChallengeID: "C-1", Title: "t", Status: StatusInProgress}}
	if !reflect.DeepEqual(got.Conflicts, wantConf) {
		t.Errorf("conflicts = %+v, want %+v", got.Conflicts, wantConf)
	}
}

// 待っている側だけが理由を持つ: 先に委譲した課題・向きの無い組の課題には「後回し」が付かない。
func TestWaiting_OnlyTheDeferredSideOfADirectedConflictWaits(t *testing.T) {
	s, _ := graphStore(t, StatusInProgress, StatusClassified, StatusClassified)
	insertConflictForTest(t, s, 2, 1, true)  // C-2 が C-1 に譲った
	insertConflictForTest(t, s, 2, 3, false) // C-2 と C-3 は向きが決まっていない
	for id, want := range map[string]int{"C-1": 0, "C-2": 1, "C-3": 0} {
		if got := waitingOf(t, s, id); len(got.Conflicts) != want || len(got.Prerequisites) != 0 {
			t.Errorf("%s waiting = %+v, want %d conflicts", id, got, want)
		}
	}
}

// 相手が完了・取り下げになった衝突、自分が終わった課題には、待っている理由が無い。空は nil でなく空スライス。
func TestWaiting_FinishedSidesAndEmptyListsAreNotNil(t *testing.T) {
	s, _ := graphStore(t, StatusDone, StatusClassified, StatusWithdrawn)
	insertConflictForTest(t, s, 2, 1, true)
	insertConflictForTest(t, s, 3, 2, true)
	for _, id := range []string{"C-1", "C-2", "C-3"} {
		got := waitingOf(t, s, id)
		if got.Prerequisites == nil || got.Conflicts == nil || len(got.Prerequisites) != 0 || len(got.Conflicts) != 0 {
			t.Errorf("%s waiting = %+v, want empty non-nil lists", id, got)
		}
	}
}
