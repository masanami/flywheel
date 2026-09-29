package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestListRuns_NewestFirst(t *testing.T) {
	s := newStoreForTest(t)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	// 課題ごとに終了していない run は高々1つ（一意制約）なので、3件を同時に
	// 開いたままにするには別々の課題を使う。順序の検証（新しい順＝run.id降順）
	// には支障が無い。
	for i := 0; i < 3; i++ {
		id := createChallengeForJudgmentTest(t, s)
		cid, _ := parseChallengeID(id)
		if _, err := insertRunForTest(t, s, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
			SessionID: "11111111-1111-1111-1111-11111111111" + string(rune('1'+i)),
			PID:       1000 + int64(i), Host: "h",
			HeartbeatAt: base, StartedAt: base.Add(time.Duration(i) * time.Minute),
			MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
		}); err != nil {
			t.Fatalf("insertRunForTest[%d]: %v", i, err)
		}
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("ListRuns = %d runs, want 3", len(runs))
	}
	// id 降順＝新しい順（最後に挿入したものが先頭）。
	if runs[0].ID != "R-3" || runs[1].ID != "R-2" || runs[2].ID != "R-1" {
		t.Errorf("order = %v, want R-3,R-2,R-1", []string{runs[0].ID, runs[1].ID, runs[2].ID})
	}
}

func TestListRuns_FiltersByChallengeID(t *testing.T) {
	s := newStoreForTest(t)
	id1 := createChallengeForJudgmentTest(t, s)
	id2 := createChallengeForJudgmentTest(t, s)
	cid1, _ := parseChallengeID(id1)
	cid2, _ := parseChallengeID(id2)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

	if _, err := insertRunForTest(t, s, cid1, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111", PID: 1, Host: "h",
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatalf("insert run for id1: %v", err)
	}
	if _, err := insertRunForTest(t, s, cid2, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "22222222-2222-2222-2222-222222222222", PID: 2, Host: "h",
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatalf("insert run for id2: %v", err)
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id1})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ChallengeID != id1 {
		t.Fatalf("ListRuns(%s) = %+v, want exactly the run for %s", id1, runs, id1)
	}
}

func TestListRuns_UnknownChallengeIDIsNotFound(t *testing.T) {
	s := newStoreForTest(t)
	missing := "C-999"
	_, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &missing})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListRuns_OpenOnlyExcludesEndedRuns(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	cid, _ := parseChallengeID(id)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

	// 課題ごとに終了していない run は高々1つのため、先に1件作って終了させ
	// てから、開いたままの2件目を作る。
	closed, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ2, ChallengeVersion: 1,
		SessionID: "22222222-2222-2222-2222-222222222222", PID: 2, Host: "h",
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	})
	if err != nil {
		t.Fatalf("insert to-be-closed run: %v", err)
	}
	closedID, _ := parseRunID(closed.ID)
	cost := int64(50_000)
	if _, err := updateRunEndForTest(t, s, closedID, updateRunEndInput{
		EndedAt: base.Add(time.Minute), Result: runResultSucceeded, CostUSD: &cost, CostSource: costSourceReported,
	}); err != nil {
		t.Fatalf("updateRunEndForTest: %v", err)
	}

	if _, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111", PID: 1, Host: "h",
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatalf("insert open run: %v", err)
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id, OpenOnly: true})
	if err != nil {
		t.Fatalf("ListRuns(open): %v", err)
	}
	if len(runs) != 1 || runs[0].Result != "" {
		t.Fatalf("ListRuns(open) = %+v, want exactly 1 open run", runs)
	}
}

// AC-154（core 側）: GetChallenge（show の元）の Runs は、その課題の run を新しい順に
// 最大 20 件返す。他の課題の run は含めない。
func TestGetChallenge_RunsAreNewestFirstAndCappedAt20(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	other := createChallengeForJudgmentTest(t, s)
	cid, _ := parseChallengeID(id)
	ocid, _ := parseChallengeID(other)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

	endRun := func(runID int64, at time.Time) {
		t.Helper()
		cost := int64(1)
		if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := updateRunEnd(context.Background(), tx, runID, updateRunEndInput{
				EndedAt: at, Result: runResultSucceeded, CostUSD: &cost, CostSource: costSourceReported,
			})
			return err
		}); err != nil {
			t.Fatalf("updateRunEnd: %v", err)
		}
	}
	insert := func(c int64, i int) int64 {
		t.Helper()
		r, err := insertRunForTest(t, s, c, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
			SessionID: fmt.Sprintf("11111111-1111-1111-1111-%012d", i), PID: 1, Host: "h",
			HeartbeatAt: base, StartedAt: base.Add(time.Duration(i) * time.Minute),
			MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
		})
		if err != nil {
			t.Fatalf("insertRunForTest[%d]: %v", i, err)
		}
		n, _ := parseRunID(r.ID)
		return n
	}
	const total = 23
	var wantNewest []string
	for i := 0; i < total; i++ {
		rid := insert(cid, i)
		endRun(rid, base.Add(time.Hour))
		wantNewest = append([]string{formatRunID(rid)}, wantNewest...)
		if i == 5 { // 他の課題の run が混ざっても、この課題の一覧には出ない
			endRun(insert(ocid, 100+i), base.Add(time.Hour))
		}
	}

	detail, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if len(detail.Runs) != 20 {
		t.Fatalf("len(Runs) = %d, want 20 (capped)", len(detail.Runs))
	}
	for i, r := range detail.Runs {
		if r.ID != wantNewest[i] {
			t.Fatalf("Runs[%d].ID = %s, want %s (newest first)", i, r.ID, wantNewest[i])
		}
		if r.ChallengeID != id {
			t.Errorf("Runs[%d].ChallengeID = %s, want %s", i, r.ChallengeID, id)
		}
	}
}

// AC-154（core 側）: run の無い課題の Runs は空スライス（nil でない）。
func TestGetChallenge_NoRunsIsEmptySlice(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	detail, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if detail.Runs == nil || len(detail.Runs) != 0 {
		t.Errorf("Runs = %#v, want an empty non-nil slice", detail.Runs)
	}
}
