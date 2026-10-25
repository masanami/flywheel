package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// このファイルは M4.5 S3（#363）の cycle の learn の段を、実ストア・偽の判断の IF で検証する
// （AC-222。docs/features/m4.5-learning-and-self-improvement.md）。J6 の写像そのものは judgment_j6_test.go が持つ。

func (f *j6Fixture) cycleInput() CycleRunInput {
	return CycleRunInput{Trigger: "manual", AgentDecl: f.agent, Invoker: f.inv}
}

// learn の段は最後に実行され、接続ツールの宣言（ConnDecl）が無くても skipped にならず、未分類の発生を持つ
// 完了の課題ごとに J6 を 1 回呼ぶ。
func TestRunCycle_LearnPhaseRunsLastWithoutConnectors(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusDone)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, nil)

	res, err := f.s.RunCycle(context.Background(), f.cycleInput())
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	names := phaseNames(res)
	if names[len(names)-1] != CyclePhaseLearn || fmt.Sprint(names) != "[release ingest classify plan run verify learn]" {
		t.Fatalf("phases = %v, want learn last", names)
	}
	learn := phaseOfResult(t, res, CyclePhaseLearn)
	if learn.Skipped || len(learn.Items) != 1 || learn.Items[0].ChallengeID != c || learn.Items[0].Outcome != "learned" {
		t.Fatalf("learn phase = %+v, want one learned item for %s", learn, c)
	}
	if f.unclassified(t) != 0 || f.learnActivities(t) != 1 {
		t.Errorf("unclassified = %d, learn activities = %d, want 0 and 1", f.unclassified(t), f.learnActivities(t))
	}
	// 対象外（着手中）の課題は黙って除かれ、J6 を呼ばない。
	other := f.challenge(t, StatusInProgress)
	f.occ(t, other, "plan_rejected", "bad", nil)
	before := len(f.inputs)
	res, err = f.s.RunCycle(context.Background(), f.cycleInput())
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if learn := phaseOfResult(t, res, CyclePhaseLearn); len(learn.Items) != 0 || len(learn.NotStarted) != 0 || len(f.inputs) != before {
		t.Errorf("learn phase = %+v, J6 inputs %d→%d, want an empty phase and no call", learn, before, len(f.inputs))
	}
}

// learn の段が足す発生（J6 の added）の経路で、出来事（作業ログの learn）と発生は同じトランザクションで書かれる
// （§クリティカル設計決定 4）。どちらかの書き込みが失敗すれば、もう一方も残らない。周は続行し、run は invalid_output になる。
func TestRunCycle_LearnPhase_OccurrenceAndWorkLogAreAtomic(t *testing.T) {
	setup := func(t *testing.T) *j6Fixture {
		f := newJ6Fixture(t)
		c := f.challenge(t, StatusDone)
		o := f.occ(t, c, "plan_rejected", "bad", nil)
		f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, []any{j6Added("good", "k", "L-1", "none")}, nil, nil)
		return f
	}
	check := func(t *testing.T, f *j6Fixture, res *CycleRunResult) {
		t.Helper()
		learn := phaseOfResult(t, res, CyclePhaseLearn)
		if len(learn.Items) != 1 || learn.Items[0].Result != RunResultInvalidOutput {
			t.Errorf("learn phase = %+v, want one invalid_output item (the cycle continues)", learn)
		}
		if f.count(t, `SELECT COUNT(*) FROM occurrence WHERE source = 'j6'`) != 0 || f.unclassified(t) != 1 || f.learnActivities(t) != 0 {
			t.Error("a partial write survived")
		}
	}
	t.Run("work log fails → no occurrence", func(t *testing.T) {
		f := setup(t)
		boom := errors.New("boom")
		f.s.insertActivity = func(tx *sql.Tx, row activityRow) error {
			if row.Action == ActivityActionLearn {
				return boom
			}
			return defaultInsertActivity(tx, row)
		}
		res, err := f.s.RunCycle(context.Background(), f.cycleInput())
		if err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		check(t, f, res)
	})
	t.Run("occurrence fails → no work log", func(t *testing.T) {
		f := setup(t)
		f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return errors.New("boom") }
		res, err := f.s.RunCycle(context.Background(), f.cycleInput())
		if err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		check(t, f, res)
	})
}
