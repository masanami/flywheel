package core

import (
	"context"
	"reflect"
	"testing"
)

// docs/features/ui-relation-graph.md §待っている理由。ボードの詳細は、まだ終わっていない前提の課題を示す。

func waitingOf(t *testing.T, s *Store, id string) WaitingReasons {
	t.Helper()
	d, err := s.GetChallengeWithoutReap(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallengeWithoutReap(%s): %v", id, err)
	}
	return d.Waiting
}

func TestWaiting_ListsOnlyUnmetPrerequisites(t *testing.T) {
	t.Parallel()
	s, _ := graphStore(t, StatusInProgress, StatusDone, StatusClassified, StatusClassified)
	// C-4 は C-2（完了）と C-3（未完了）に依存する。
	insertDepEdge(t, s, 4, 2)
	insertDepEdge(t, s, 4, 3)
	got := waitingOf(t, s, "C-4")
	wantPre := []WaitingChallenge{{ChallengeID: "C-3", Title: "t", Status: StatusClassified}}
	if !reflect.DeepEqual(got.Prerequisites, wantPre) {
		t.Errorf("prerequisites = %+v, want %+v", got.Prerequisites, wantPre)
	}
}

// 自分が終わった課題には、待っている理由が無い。空は nil でなく空スライス。
func TestWaiting_FinishedChallengeAndEmptyListAreNotNil(t *testing.T) {
	t.Parallel()
	s, _ := graphStore(t, StatusClassified, StatusDone, StatusWithdrawn)
	insertDepEdge(t, s, 2, 1)
	insertDepEdge(t, s, 3, 1)
	for _, id := range []string{"C-1", "C-2", "C-3"} {
		got := waitingOf(t, s, id)
		if got.Prerequisites == nil || len(got.Prerequisites) != 0 {
			t.Errorf("%s waiting = %+v, want an empty non-nil list", id, got)
		}
	}
}
