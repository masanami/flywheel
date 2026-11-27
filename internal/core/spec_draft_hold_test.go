package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 下書きづくりの保留を作る口と、記録の下書きを消す共通の処理（docs/features/request-to-plan-s2b.md の S2b-1）を実ストアで確かめる。
// テストの名前の AC の番号は、Issue #1351 の受け持ち（親 #1339 の受入基準の並び順）。

func shQuestions() []SpecDraftQuestion {
	return []SpecDraftQuestion{
		{Question: "取り消しは実行中の処理にも効かせるか", Options: []SpecDraftOption{{Label: "実行中にも効かせる"}, {Label: "待ちの処理だけ"}},
			Recommended: 1, Reason: "すぐ止めたいと書いてあるため"},
		{Question: "記録は残すか", Options: []SpecDraftOption{{Label: "消す"}, {Label: "残す"}, {Label: "選べるようにする"}},
			Recommended: 2, Reason: "後で追えるようにするため"},
	}
}

// shDrafting は、計画を作る判断点が下書きづくりへ回した分類済の課題を作り、記録を state・attempts にする。返す run はその判断の実行。
func shDrafting(t *testing.T, state SpecDraftState, attempts int) (*j2Fixture, string, string) {
	t.Helper()
	f, id := newDraftFixture(t)
	item, reason := runDraftJ2(t, f, id, needsSpecOutput("flow-wt", nil))
	if f.detail(t, id).SpecDraft == nil {
		t.Fatalf("no spec draft record: %+v %s", item, reason)
	}
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: state, Attempts: attempts}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return f, id, item.RunID
}

// shRaise は保留を作り、作った保留と、その後の詳細を返す。
func shRaise(t *testing.T, f *j2Fixture, runID, id string, h specDraftHold) (Hold, *ChallengeDetail) {
	t.Helper()
	held, err := f.s.holdSpecDraft(context.Background(), runID, id, h)
	if err != nil || !held {
		t.Fatalf("holdSpecDraft = %v, %v", held, err)
	}
	d := f.detail(t, id)
	var open []Hold
	for _, x := range d.Holds {
		if x.Open() {
			open = append(open, x)
		}
	}
	if len(open) != 1 {
		t.Fatalf("open holds = %+v, want 1", open)
	}
	return open[0], d
}

func shOptionIDs(h Hold) []string {
	var ids []string
	for _, o := range h.Options {
		ids = append(ids, o.ID)
	}
	return ids
}

func TestHoldSpecDraft_Question(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
	f.agent.Supervisor = true
	questions := shQuestions()
	hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldQuestion, Questions: questions})

	t.Run("AC-270・AC-271・AC-272・AC-273 記録と保留", func(t *testing.T) {
		rec := d.SpecDraft
		if rec.HoldReason == nil || *rec.HoldReason != SpecDraftHoldQuestion {
			t.Errorf("hold_reason = %v", rec.HoldReason)
		}
		if rec.HoldID == nil || *rec.HoldID != hold.HoldID {
			t.Errorf("hold_id = %v, want %s", rec.HoldID, hold.HoldID)
		}
		if rec.State != SpecDraftStateAwaitingOwner || rec.Attempts != 1 {
			t.Errorf("state = %s, attempts = %d", rec.State, rec.Attempts)
		}
	})
	t.Run("AC-274〜AC-280 保留の形", func(t *testing.T) {
		if d.Status != StatusAwaitingHuman {
			t.Errorf("status = %s", d.Status)
		}
		if hold.Scope != "" || hold.TaskID != nil || hold.ScopeLabel() != "challenge" {
			t.Errorf("scope = %q task = %v", hold.Scope, hold.TaskID)
		}
		if hold.FromStatus != StatusClassified {
			t.Errorf("from_status = %s", hold.FromStatus)
		}
		if hold.StopKind != "" || hold.SupervisorState != "" {
			t.Errorf("stop_kind = %q, supervisor_state = %q", hold.StopKind, hold.SupervisorState)
		}
		var stopKind, supState sql.NullString
		rowID, _ := parseHoldID(hold.HoldID)
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT stop_kind, supervisor_state FROM hold WHERE id = ?`, rowID).Scan(&stopKind, &supState)
		}); err != nil {
			t.Fatal(err)
		}
		if stopKind.Valid || supState.Valid {
			t.Errorf("stop_kind = %v, supervisor_state = %v, want NULL", stopKind, supState)
		}
		if hold.Kind != HoldKindHuman {
			t.Errorf("kind = %s", hold.Kind)
		}
		if !hold.OptionRequired {
			t.Error("option_required = false")
		}
		if hold.RunID == nil || *hold.RunID != runID {
			t.Errorf("run_id = %v", hold.RunID)
		}
	})
	t.Run("AC-281〜AC-286 問いの文", func(t *testing.T) {
		want := "仕様の下書きに、決めないと進まない問いが 2 件残りました。下書き: " + d.SpecDraft.Path + "\n" +
			"\nQ1. 取り消しは実行中の処理にも効かせるか\n" +
			"  1. 実行中にも効かせる（推奨）\n" +
			"  2. 待ちの処理だけ\n" +
			"  推奨の理由: すぐ止めたいと書いてあるため\n" +
			"\nQ2. 記録は残すか\n" +
			"  1. 消す\n" +
			"  2. 残す（推奨）\n" +
			"  3. 選べるようにする\n" +
			"  推奨の理由: 後で追えるようにするため"
		if hold.Question != want {
			t.Errorf("question =\n%s\nwant\n%s", hold.Question, want)
		}
	})
	t.Run("AC-287 記録の label に推奨の印を入れない", func(t *testing.T) {
		if !reflect.DeepEqual(d.SpecDraft.Questions, shQuestions()) || !reflect.DeepEqual(questions, shQuestions()) {
			t.Errorf("questions = %+v", d.SpecDraft.Questions)
		}
	})
	t.Run("AC-288・AC-289 選択肢", func(t *testing.T) {
		if got := shOptionIDs(hold); !reflect.DeepEqual(got, []string{"answer", "proceed", "skip"}) {
			t.Fatalf("options = %v", got)
		}
		var labels []string
		for _, o := range hold.Options {
			labels = append(labels, o.Label)
		}
		want := []string{"推奨のまま進める", "いまの下書きで計画へ進む", "仕様の文書なしで計画する"}
		if !reflect.DeepEqual(labels, want) {
			t.Errorf("labels = %v", labels)
		}
	})
}

func TestHoldSpecDraft_OptionsByReason(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		attempts int
		hold     specDraftHold
		want     []string
	}{
		"AC-290 失敗・1 回": {1, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"}, []string{"retry", "proceed", "skip"}},
		"失敗・2 回":        {2, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"}, []string{"retry", "proceed", "skip"}},
		"AC-291 失敗・3 回": {3, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"}, []string{"proceed", "skip"}},
		"AC-292 回数の上限":  {3, specDraftHold{Reason: SpecDraftHoldAttemptLimit, Questions: shQuestions()}, []string{"proceed", "skip"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, SpecDraftStateRunning, c.attempts)
			hold, d := shRaise(t, f, runID, id, c.hold)
			if got := shOptionIDs(hold); !reflect.DeepEqual(got, c.want) {
				t.Errorf("options = %v, want %v", got, c.want)
			}
			if !hold.OptionRequired || d.SpecDraft.HoldReason == nil || *d.SpecDraft.HoldReason != c.hold.Reason {
				t.Errorf("hold = %+v, reason = %v", hold, d.SpecDraft.HoldReason)
			}
		})
	}
}

func TestHoldSpecDraft_AttemptLimitQuestionStartsAtQ1(t *testing.T) { // AC-293
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 3)
	hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldAttemptLimit, Questions: shQuestions()})
	for _, want := range []string{"上限（3 回）", "下書き: " + d.SpecDraft.Path, "\nQ1. 取り消しは", "\nQ2. 記録は残すか",
		"  1. 実行中にも効かせる（推奨）\n", "  推奨の理由: 後で追えるようにするため"} {
		if !strings.Contains(hold.Question, want) {
			t.Errorf("question does not contain %q:\n%s", want, hold.Question)
		}
	}
	if !reflect.DeepEqual(d.SpecDraft.Questions, shQuestions()) {
		t.Errorf("questions = %+v", d.SpecDraft.Questions)
	}
}

// 起動の失敗は、記録が pending のまま保留にする。失敗の内容は問いの文に出て、記録の答えは残る。
func TestHoldSpecDraft_FailedFromPendingKeepsAnswers(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStatePending, 1)
	cid, _ := parseChallengeID(id)
	answers := []SpecDraftAnswer{{Kind: SpecDraftAnswerKindAnswer, Attempt: 1, Question: "P?", Answer: "yes", By: "owner", Basis: []SpecDraftBasis{}}}
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: SpecDraftStatePending, Attempts: 1, Answers: answers}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "開始のコマンドを起動できない\n"})
	want := "仕様の下書きづくりが失敗しました（実行 1 回）。下書き: " + d.SpecDraft.Path + "\n\n開始のコマンドを起動できない"
	if hold.Question != want {
		t.Errorf("question = %q, want %q", hold.Question, want)
	}
	if d.SpecDraft.State != SpecDraftStateAwaitingOwner || !reflect.DeepEqual(d.SpecDraft.Answers, answers) || d.SpecDraft.Questions != nil {
		t.Errorf("record = %+v", d.SpecDraft)
	}
}

// 実行の終わりを読んだとき、課題か記録が対象でなくなっていれば、保留も記録も変えない。
func TestHoldSpecDraft_NotEligibleChangesNothing(t *testing.T) {
	t.Parallel()
	failed := specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"}
	for _, state := range []SpecDraftState{SpecDraftStateAwaitingOwner, SpecDraftStateDone, SpecDraftStateSkipped} {
		t.Run("記録 "+string(state), func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, state, 1)
			shRequireNotHeld(t, f, runID, id, failed)
		})
	}
	for _, status := range []Status{StatusAwaitingHuman, StatusAwaitingPlanApproval, StatusInProgress, StatusWithdrawn} {
		t.Run("課題 "+string(status), func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
			cid, _ := parseChallengeID(id)
			setChallengeStatus(t, f.s, cid, status)
			shRequireNotHeld(t, f, runID, id, failed)
		})
	}
	t.Run("記録が無い", func(t *testing.T) {
		t.Parallel()
		f, _, runID := shDrafting(t, SpecDraftStateRunning, 1)
		other := f.newClassified(t, "no record", "P1").ID
		shRequireNotHeld(t, f, runID, other, failed)
	})
}

func shRequireNotHeld(t *testing.T, f *j2Fixture, runID, id string, h specDraftHold) {
	t.Helper()
	before := f.detail(t, id)
	held, err := f.s.holdSpecDraft(context.Background(), runID, id, h)
	if err != nil || held {
		t.Fatalf("holdSpecDraft = %v, %v, want not held", held, err)
	}
	after := f.detail(t, id)
	if after.Status != before.Status || len(after.Holds) != len(before.Holds) || !reflect.DeepEqual(after.SpecDraft, before.SpecDraft) {
		t.Errorf("changed: %s %+v → %s %+v", before.Status, before.SpecDraft, after.Status, after.SpecDraft)
	}
}

func TestHoldSpecDraft_RejectsMalformedInput(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
	outOfRange := shQuestions()
	outOfRange[0].Recommended = 3
	for name, h := range map[string]specDraftHold{
		"理由が閉集合の外":    {Reason: "other", Detail: "x"},
		"問いの保留に問いが無い": {Reason: SpecDraftHoldQuestion},
		"上限の保留に問いが無い": {Reason: SpecDraftHoldAttemptLimit},
		"推奨が選択肢の範囲の外": {Reason: SpecDraftHoldQuestion, Questions: outOfRange},
		"失敗の保留に内容が無い": {Reason: SpecDraftHoldFailed, Detail: " \n"},
		"失敗の保留に問いがある": {Reason: SpecDraftHoldFailed, Detail: "x", Questions: shQuestions()},
	} {
		held, err := f.s.holdSpecDraft(context.Background(), runID, id, h)
		if held || !errors.Is(err, ErrValidation) {
			t.Errorf("%s: held = %v, err = %v, want ErrValidation", name, held, err)
		}
	}
	if d := f.detail(t, id); d.Status != StatusClassified || len(d.Holds) != 0 || d.SpecDraft.State != SpecDraftStateRunning {
		t.Errorf("changed: %+v", d.SpecDraft)
	}
}

func TestHoldSpecDraft_ClosedSetsAreUnchanged(t *testing.T) {
	t.Parallel()
	if n := len(StopKindValues()); n != 10 { // AC-294
		t.Errorf("stop kinds = %d, want 10", n)
	}
	if n := len(HoldKindValues()); n != 2 { // AC-295
		t.Errorf("hold kinds = %d, want 2", n)
	}
	sum := sha256.Sum256(SVOutputSchema()) // AC-296: この区切りの前のスキーマの要約値。
	if got := hex.EncodeToString(sum[:]); got != "f08c1b55ea117bbc65cb9b734e68d93c6aeb78f3272419e1166b8260a826b60e" {
		t.Errorf("supervisor output schema changed: sha256 = %s", got)
	}
}

// shDraftFile は記録を持つ課題と、記録の場所の下書きのファイルを作る。
func shDraftFile(t *testing.T) (*j2Fixture, string, int64, string) {
	t.Helper()
	f, id, _ := shDrafting(t, SpecDraftStateRunning, 1)
	cid, _ := parseChallengeID(id)
	p := specDraftPathFor(cid)
	writeWorkspaceFile(t, f.s.workspace, p, "# 下書き\n")
	return f, id, cid, p
}

func shDiscard(t *testing.T, f *j2Fixture, id string, wantFailures int) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	f.s.discardSpecDraft(context.Background(), cid, "alice")
	got := scFailures(t, f.s, id)
	if len(got) != wantFailures {
		t.Fatalf("cleanup failures = %v, want %d", got, wantFailures)
	}
	for _, m := range got {
		if m["path"] != specDraftPathFor(cid) || m["reason"] == "" {
			t.Errorf("failure = %v", m)
		}
	}
}

func TestDiscardSpecDraft_RemovesTheRecordedFile(t *testing.T) { // AC-297
	t.Parallel()
	f, id, _, p := shDraftFile(t)
	shDiscard(t, f, id, 0)
	scRequireExists(t, f.s, p, false)
	if d := f.detail(t, id); d.SpecDraft == nil || d.SpecDraft.State != SpecDraftStateRunning || d.SpecDraft.Path != p {
		t.Errorf("record = %+v", d.SpecDraft)
	}
}

func TestDiscardSpecDraft_MissingFileIsNotAnError(t *testing.T) { // AC-298
	t.Parallel()
	f, id, _, p := shDraftFile(t)
	if err := os.Remove(filepath.Join(f.s.workspace, p)); err != nil {
		t.Fatal(err)
	}
	shDiscard(t, f, id, 0)
}

func TestDiscardSpecDraft_WithoutARecordDoesNothing(t *testing.T) {
	t.Parallel()
	s := scStore(t)
	id := createAndAdvance(t, s, StatusClassified).ID
	cid, _ := parseChallengeID(id)
	writeWorkspaceFile(t, s.workspace, specDraftPathFor(cid), "# 下書き\n")
	s.discardSpecDraft(context.Background(), cid, "alice")
	scRequireExists(t, s, specDraftPathFor(cid), true)
	if got := scFailures(t, s, id); len(got) != 0 {
		t.Errorf("failures = %v", got)
	}
}

// 呼び出し元の操作は成立した後なので、ctx が取り消されていても消す。
func TestDiscardSpecDraft_IgnoresCancellation(t *testing.T) {
	t.Parallel()
	f, _, cid, p := shDraftFile(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.s.discardSpecDraft(ctx, cid, "alice")
	scRequireExists(t, f.s, p, false)
}

func TestDiscardSpecDraft_KeepsAFileAnotherChallengePointsAt(t *testing.T) { // AC-299・AC-301
	t.Parallel()
	f, id, _, p := shDraftFile(t)
	other := f.newClassified(t, "shares the draft", "P1").ID
	sdSetSpec(t, f.s, other, "./"+p, sdBase)
	shDiscard(t, f, id, 1)
	scRequireExists(t, f.s, p, true)
}

// 指している課題が終わっていれば消す。
func TestDiscardSpecDraft_RemovesWhenTheOtherChallengeIsFinished(t *testing.T) {
	t.Parallel()
	f, id, _, p := shDraftFile(t)
	other := f.newClassified(t, "finished", "P1").ID
	sdSetSpec(t, f.s, other, p, sdBase)
	oid, _ := parseChallengeID(other)
	setChallengeStatus(t, f.s, oid, StatusWithdrawn)
	shDiscard(t, f, id, 0)
	scRequireExists(t, f.s, p, false)
}

func TestDiscardSpecDraft_KeepsATargetOutsideTheDraftDirectory(t *testing.T) { // AC-300・AC-301
	t.Parallel()
	for name, target := range map[string]string{"ワークスペースの中": "docs/real.md", "ワークスペースの外": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, _, p := shDraftFile(t)
			var abs string
			if target == "" {
				abs = filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(abs, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeWorkspaceFile(t, f.s.workspace, target, "x")
				abs = filepath.Join(f.s.workspace, target)
			}
			link := filepath.Join(f.s.workspace, p)
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(abs, link); err != nil {
				t.Fatal(err)
			}
			shDiscard(t, f, id, 1)
			if !scExists(t, abs) || !scExists(t, link) {
				t.Error("removed the target or the link")
			}
		})
	}
}

func TestDiscardSpecDraft_NonEmptyDirectoryIsLogged(t *testing.T) { // AC-301・AC-302
	t.Parallel()
	f, id, _, p := shDraftFile(t)
	full := filepath.Join(f.s.workspace, p)
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, f.s.workspace, p+"/inner.md", "x")
	shDiscard(t, f, id, 1)
	scRequireExists(t, f.s, p+"/inner.md", true)
}

// 記録を running にする前に開始が失敗したときは、保留と同じ書き込みで実行を数える。選択肢と問いの文は数えた後の回数で作る。
func TestHoldSpecDraft_CountsTheFailedStartInTheSameWrite(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStatePending, 2)
	hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "起動できない", CountAttempt: true})
	if d.SpecDraft.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", d.SpecDraft.Attempts)
	}
	if got := shOptionIDs(hold); !reflect.DeepEqual(got, []string{"proceed", "skip"}) {
		t.Errorf("options = %v", got)
	}
	if !strings.Contains(hold.Question, "（実行 3 回）") {
		t.Errorf("question = %q", hold.Question)
	}
}

// running の記録は始めるときに数えてあるので、もう一度は数えない。
func TestHoldSpecDraft_RejectsCountingARunningAttemptTwice(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
	held, err := f.s.holdSpecDraft(context.Background(), runID, id,
		specDraftHold{Reason: SpecDraftHoldFailed, Detail: "x", CountAttempt: true})
	if held || !errors.Is(err, ErrValidation) {
		t.Fatalf("held = %v, err = %v, want ErrValidation", held, err)
	}
	if d := f.detail(t, id); d.Status != StatusClassified || len(d.Holds) != 0 || d.SpecDraft.Attempts != 1 {
		t.Errorf("changed: %+v", d.SpecDraft)
	}
}
