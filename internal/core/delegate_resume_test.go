package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは #104（親要件チケット #98 §保留と再開・§失敗・差し戻しの上限。
// AC-241〜262・282）の core 側を、実ストア・偽の委譲の IF・偽の SlotGit・偽の gh で検証する。

const resumeConnectorsPatch = `"paths": ["slot1"]}},
    {"name": "sibling"`

// newResumeFixture は main-repo が clone のスロットを 2 本（slot1・slot1b）、wt-repo が
// worktree のスロットを 2 本持つ宣言のフィクスチャ。
func newResumeFixture(t *testing.T) *delegateFixture {
	t.Helper()
	f := newDelegateFixture(t)
	raw := strings.Replace(delegateConnectorsJSON, resumeConnectorsPatch, `"paths": ["slot1", "slot1b"]}},
    {"name": "wt-repo", "remote": "o/w", "default_branch": "main", "connector": "harness", "slots": {"provider": "worktree", "base": "wt-base", "count": 2}},
    {"name": "sibling"`, 1)
	conn, err := parseConnectorsDeclaration([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.validate(); err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	f.git.states[pathOf(f, "slot1b")] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/r.git"}
	f.git.provisioned = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/w.git"}
	return f
}

func pathOf(f *delegateFixture, rel string) string { return f.s.Workspace() + "/" + rel }

func questionsOut(branch string) JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(2), StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["branch"] = branch
		m["questions"] = []any{
			map[string]any{"kind": "scope", "text": "QUESTION-ONE", "options": []string{"A", "B"}, "recommendation": "A"},
			map[string]any{"kind": "minor", "text": "QUESTION-TWO", "options": []string{}, "recommendation": nil},
		}
	})}
}

func failOut(r RunResult) JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: r, ErrorSummary: string(r)}
}

// answerLatestHold は最後の保留に回答し、課題を保留前の状態へ戻す（M1 T12 の結果の写し）。
func (f *delegateFixture) answerLatestHold(t *testing.T, id, answer string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE hold SET answer = ?, answered_at = ?, answered_by = 'alice'
			WHERE id = (SELECT MAX(id) FROM hold WHERE challenge_id = ?)`, answer, formatTimestamp(time.Now().Add(time.Millisecond)), cid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress', version = version + 1 WHERE id = ?`, cid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *delegateFixture) version(t *testing.T, id string) int { return f.detail(t, id).Version }

func (f *delegateFixture) lastLaunch(t *testing.T) DelegateLaunchInput {
	t.Helper()
	l := f.deleg.launched()
	if len(l) == 0 {
		t.Fatal("no delegation was launched")
	}
	return l[len(l)-1]
}

func (f *delegateFixture) runDelegation(t *testing.T, id string) *DelegateResult {
	t.Helper()
	f.refillImplBudget(t, id)
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

// --- 保留（AC-241・242・282） ---

func TestResume_QuestionsHoldsWithEachQuestionAndTheCauseRun(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = questionsOut("feat/x")
	res := f.runDelegation(t, id)

	d := f.detail(t, id)
	if d.Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", d.Status)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	for _, want := range []string{"QUESTION-ONE", "QUESTION-TWO", "[scope]", "A / B"} {
		if !strings.Contains(q, want) {
			t.Errorf("hold question lacks %q:\n%s", want, q)
		}
	}
	runs := f.delegateRuns(t)
	if got := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`); got != strings.TrimPrefix(runs[0].ID, "R-") {
		t.Errorf("hold.run_id = %q, want the delegation run %s", got, runs[0].ID)
	}
	if len(res.Items) != 1 || res.Items[0].Outcome != "questions" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Errorf("items = %+v", res.Items)
	}
}

func TestResume_BlockedHoldsWithTheSummary(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "blocked"
		m["summary"] = "BLOCKED-SUMMARY"
	})}
	f.runDelegation(t, id)
	if f.detail(t, id).Status != StatusAwaitingHuman {
		t.Fatalf("status = %s", f.detail(t, id).Status)
	}
	if q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`); !strings.Contains(q, "BLOCKED-SUMMARY") {
		t.Errorf("hold question = %q", q)
	}
}

// AC-282: questions の後にスロットが idle に戻り、同じ周の別の課題に割り当てられる。
func TestResume_SlotIsIdleAfterQuestionsAndReusedInTheSameCycle(t *testing.T) {
	f := newDelegateFixture(t) // main-repo のスロットは slot1 の 1 本
	a := f.newInProgress(t, "a", "P1", planSpec(nil))
	b := f.newInProgress(t, "b", "P2", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{questionsOut("feat/a")}
	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || len(res.NotStarted) != 0 {
		t.Fatalf("items=%+v notStarted=%+v, want both launched", res.Items, res.NotStarted)
	}
	if f.detail(t, a).Status != StatusAwaitingHuman {
		t.Errorf("a = %s", f.detail(t, a).Status)
	}
	if got := f.deleg.launched(); len(got) != 2 {
		t.Errorf("launches = %d, want 2 (b took the slot a released)", len(got))
	}
	_ = b
	if st := f.slotStates(t)["slot1"]; st != "idle" {
		t.Errorf("slot1 = %s, want idle", st)
	}
}

// --- 回答の後の再開（AC-243〜245） ---

func TestResume_AnswerResumesTheCauseRunSessionWithoutJ3(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{questionsOut("feat/x")}
	f.runDelegation(t, id)
	first := f.lastLaunch(t)
	f.answerLatestHold(t, id, "ANSWER-FROM-HUMAN")
	j3Before := len(f.j3Inputs)

	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindAnswer ||
		got.ResumeAnswer != "ANSWER-FROM-HUMAN" || got.ResumeBranch != "feat/x" {
		t.Errorf("launch = %+v, first session %s", got, first.SessionID)
	}
	if len(f.j3Inputs) != j3Before {
		t.Error("a resume must not call J3")
	}
	runs := f.delegateRuns(t)
	if len(runs) != 2 || runs[1].SessionID != first.SessionID || runs[1].ResumedFromRunID == nil || *runs[1].ResumedFromRunID != runs[0].ID {
		t.Errorf("runs = %+v", runs)
	}
}

func TestResume_HumanHoldAndJudgmentHoldStartANewSession(t *testing.T) {
	for _, name := range []string{"human_hold", "j5_uncertain_hold"} {
		t.Run(name, func(t *testing.T) {
			f := newResumeFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
			f.runDelegation(t, id)
			first := f.lastLaunch(t)
			if name == "human_hold" {
				if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "why?"}); err != nil {
					t.Fatal(err)
				}
			} else {
				// J5 が検証中で uncertain を返した保留（原因の run は後から作られた判断の run）。
				cid, _ := parseChallengeID(id)
				err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
					now := time.Now()
					r, err := insertRun(context.Background(), tx, cid, insertRunInput{Kind: runKindJudgment, Judgment: "J5", ChallengeVersion: 1,
						SessionID: "33333333-3333-4333-8333-333333333333", PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketJudgment})
					if err != nil {
						return err
					}
					n, _ := parseRunID(r.ID)
					if _, err := tx.Exec(`UPDATE run SET result = 'succeeded', ended_at = ? WHERE id = ?`, formatTimestamp(now), n); err != nil {
						return err
					}
					if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, run_id) VALUES (?, 'q', 'in_progress', ?, ?)`, cid, formatTimestamp(now), n); err != nil {
						return err
					}
					_, err = tx.Exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, cid)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.answerLatestHold(t, id, "ans")
			f.runDelegation(t, id)
			got := f.lastLaunch(t)
			if got.IsResume || got.SessionID == first.SessionID {
				t.Errorf("launch = resume %v session %s (first %s), want a new session", got.IsResume, got.SessionID, first.SessionID)
			}
		})
	}
}

// --- 再開のスロット（AC-246〜251） ---

func (f *delegateFixture) repo(t *testing.T, name string) ConnectorRepo {
	t.Helper()
	r, _ := f.conn.findConnectorRepo(name)
	if r == nil {
		t.Fatalf("no repo %s", name)
	}
	return *r
}

// occupy は repo の path のスロットを predict の run で使用中にする。
func (f *delegateFixture) occupy(t *testing.T, repo, slotRel string) *SlotAssignment {
	t.Helper()
	ctx := context.Background()
	r := f.repo(t, repo)
	if err := f.s.EnsureSlots(ctx, r); err != nil {
		t.Fatal(err)
	}
	slots, _ := f.s.ListSlots(ctx)
	for _, sl := range slots {
		if strings.HasSuffix(sl.Path, "/"+slotRel) {
			var row *slotRow
			_ = f.s.db.Read(ctx, func(tx *sql.Tx) error {
				id, _ := parseSlotID(sl.ID)
				row, _ = loadSlotByID(ctx, tx, id)
				return nil
			})
			a, err := f.s.assignSlot(ctx, *row, bindRunForTest(ctx))
			if err != nil || a == nil {
				t.Fatalf("assignSlot(%s) = %v, %v", sl.ID, a, err)
			}
			return a
		}
	}
	t.Fatalf("no slot %s", slotRel)
	return nil
}

func (f *delegateFixture) slotOfLastRun(t *testing.T) string {
	t.Helper()
	runs := f.delegateRuns(t)
	return *runs[len(runs)-1].SlotID
}

func (f *delegateFixture) questionAndAnswer(t *testing.T, id, branch string) {
	t.Helper()
	f.deleg.queue = []JudgmentLaunchOutput{questionsOut(branch)}
	f.runDelegation(t, id)
	f.answerLatestHold(t, id, "ANS")
}

func TestResume_Clone_PushedBranchUsesAnotherSlotWhenTheOriginalIsBusy(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.branches.branches["feat/x"] = true
	f.questionAndAnswer(t, id, "feat/x")
	original := f.slotOfLastRun(t)
	other := f.occupy(t, "main-repo", "slot1")
	if other.SlotID != original {
		t.Fatalf("setup: occupied %s, original %s", other.SlotID, original)
	}
	f.runDelegation(t, id)
	if got := f.slotOfLastRun(t); got == original {
		t.Errorf("resume used the busy original slot %s", got)
	}
	if got := f.lastLaunch(t); !got.IsResume || got.ResumeBranch != "feat/x" || !strings.HasSuffix(got.WorkDir, "slot1b") {
		t.Errorf("launch = %+v", got)
	}
}

func TestResume_Clone_UnpushedBranchUsesTheOriginalSlotWhenIdle(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.questionAndAnswer(t, id, "feat/x") // リモートに無い
	original := f.slotOfLastRun(t)
	f.runDelegation(t, id)
	if got := f.slotOfLastRun(t); got != original {
		t.Errorf("slot = %s, want the original %s", got, original)
	}
}

func TestResume_Clone_UnpushedBranchWithBusyOriginalIsSlotUnavailable(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.questionAndAnswer(t, id, "feat/x")
	f.occupy(t, "main-repo", "slot1")
	before, runsBefore := f.version(t, id), len(f.delegateRuns(t))
	launchesBefore := len(f.deleg.launched())

	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable || res.NotStarted[0].ChallengeID != id {
		t.Fatalf("notStarted = %+v", res.NotStarted)
	}
	if f.version(t, id) != before || len(f.delegateRuns(t)) != runsBefore || len(f.deleg.launched()) != launchesBefore {
		t.Error("state, runs or launches changed")
	}
	if f.detail(t, id).Status != StatusInProgress {
		t.Errorf("status = %s", f.detail(t, id).Status)
	}
}

func (f *delegateFixture) worktreeChallenge(t *testing.T) string {
	t.Helper()
	return f.newInProgress(t, "w", "P1", planSpec(func(m map[string]any) { m["repo"] = "wt-repo" }))
}

func TestResume_Worktree_IdleOriginalIsUsedEvenWhenPushedAndOthersAreIdle(t *testing.T) {
	f := newResumeFixture(t)
	id := f.worktreeChallenge(t)
	f.branches.branches["feat/w"] = true
	f.questionAndAnswer(t, id, "feat/w")
	original := f.slotOfLastRun(t)
	f.runDelegation(t, id)
	if got := f.slotOfLastRun(t); got != original {
		t.Errorf("slot = %s, want the original %s", got, original)
	}
	if got := f.lastLaunch(t); !got.IsResume {
		t.Error("not a resume")
	}
}

func TestResume_Worktree_NeedsAttentionOriginalIsSlotUnavailable(t *testing.T) {
	f := newResumeFixture(t)
	id := f.worktreeChallenge(t)
	f.branches.branches["feat/w"] = true
	f.questionAndAnswer(t, id, "feat/w")
	original := f.slotOfLastRun(t)
	sid, _ := parseSlotID(original)
	if err := f.s.mutateSlots(context.Background(), func(tx *sql.Tx) error {
		_, err := updateSlotState(context.Background(), tx, sid, slotStateNeedsAttention, nil, "dirty")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	launches := len(f.deleg.launched())
	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable || len(f.deleg.launched()) != launches {
		t.Errorf("notStarted = %+v, launches %d→%d", res.NotStarted, launches, len(f.deleg.launched()))
	}
}

func TestResume_Worktree_BusyOriginalWaitsInTheSameCycle(t *testing.T) {
	old := slotWaitInterval
	slotWaitInterval = 10 * time.Millisecond
	t.Cleanup(func() { slotWaitInterval = old })

	f := newResumeFixture(t)
	id := f.worktreeChallenge(t)
	f.questionAndAnswer(t, id, "feat/w")
	original := f.slotOfLastRun(t)
	busy := f.occupy(t, "wt-repo", original)
	if busy.SlotID != original {
		t.Fatalf("setup: %s vs %s", busy.SlotID, original)
	}

	var wg sync.WaitGroup
	var res *DelegateResult
	var err error
	in := f.input(f.cycle(t, 300), nil)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err = f.s.RunDelegation(context.Background(), in)
	}()
	time.Sleep(150 * time.Millisecond)
	if n := len(f.deleg.launched()); n != 1 {
		t.Fatalf("launches while waiting = %d, want 1 (the first run only)", n)
	}
	releaseForTest(t, f.s, busy)
	wg.Wait()
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if got := f.slotOfLastRun(t); got != original {
		t.Errorf("slot = %s, want %s", got, original)
	}
}

// --- 失敗の結果と再開（AC-252〜255） ---

func TestResume_FailedResultsKeepStateAndResumeTheSameSessionNextCycle(t *testing.T) {
	for _, r := range []RunResult{RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut, RunResultInterrupted} {
		t.Run(string(r), func(t *testing.T) {
			f := newResumeFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			v := f.version(t, id)
			f.deleg.queue = []JudgmentLaunchOutput{failOut(r)}
			f.runDelegation(t, id)
			first := f.lastLaunch(t)
			if f.detail(t, id).Status != StatusInProgress || f.version(t, id) != v {
				t.Fatalf("status/version changed: %s v%d→v%d", f.detail(t, id).Status, v, f.version(t, id))
			}
			f.runDelegation(t, id)
			got := f.lastLaunch(t)
			if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindInterrupted {
				t.Errorf("launch = %+v, want resume of %s (interrupted)", got, first.SessionID)
			}
		})
	}
}

func TestResume_LaunchFailedKeepsStateAndRepeatsTheSameForm(t *testing.T) {
	t.Run("new session", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		v := f.version(t, id)
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultLaunchFailed)}
		f.runDelegation(t, id)
		first := f.lastLaunch(t)
		if f.detail(t, id).Status != StatusInProgress || f.version(t, id) != v {
			t.Fatal("state changed")
		}
		f.runDelegation(t, id)
		got := f.lastLaunch(t)
		if got.IsResume || got.SessionID == first.SessionID || got.Brief == "" {
			t.Errorf("launch = %+v, want a fresh session with a brief", got)
		}
	})
	t.Run("answer resume", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.questionAndAnswer(t, id, "feat/x")
		first := f.lastLaunch(t)
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultLaunchFailed)}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		got := f.lastLaunch(t)
		if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindAnswer || got.ResumeAnswer != "ANS" {
			t.Errorf("launch = %+v, want the same answer resume", got)
		}
	})
	t.Run("interrupted resume", func(t *testing.T) {
		f := newResumeFixture(t)
		f.agent.FailureLimit = 3
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultLaunchFailed)}
		f.runDelegation(t, id)
		first := f.lastLaunch(t)
		f.runDelegation(t, id) // resume → launch_failed
		f.runDelegation(t, id)
		got := f.lastLaunch(t)
		if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindInterrupted {
			t.Errorf("launch = %+v", got)
		}
	})
}

// --- 連続失敗の上限（AC-256〜262） ---

func TestFailureLimit_ReachedHoldsWithKindAndCountAndReportsNotStarted(t *testing.T) {
	for _, results := range [][]RunResult{
		{RunResultLaunchFailed, RunResultLaunchFailed},
		{RunResultErrored, RunResultMalformed},
		{RunResultInvalidOutput, RunResultTimedOut},
		{RunResultInterrupted, RunResultLaunchFailed},
	} {
		t.Run(string(results[0])+"+"+string(results[1]), func(t *testing.T) {
			f := newResumeFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.queue = []JudgmentLaunchOutput{failOut(results[0]), failOut(results[1])}
			f.runDelegation(t, id)
			f.runDelegation(t, id)
			launches := len(f.deleg.launched())

			res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(f.deleg.launched()) != launches {
				t.Error("a delegation was launched at the limit")
			}
			if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit || res.NotStarted[0].ChallengeID != id {
				t.Fatalf("notStarted = %+v", res.NotStarted)
			}
			if f.detail(t, id).Status != StatusAwaitingHuman {
				t.Errorf("status = %s", f.detail(t, id).Status)
			}
			q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
			if !strings.Contains(q, "連続失敗") || !strings.Contains(q, "回数: 2") || !strings.Contains(q, "上限: 2") {
				t.Errorf("hold question = %q", q)
			}
		})
	}
}

func TestFailureLimit_NotReachedLaunchesAgain(t *testing.T) {
	t.Run("limit 3 with two failures", func(t *testing.T) {
		f := newResumeFixture(t)
		f.agent.FailureLimit = 3
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		n := len(f.deleg.launched())
		f.runDelegation(t, id)
		if len(f.deleg.launched()) != n+1 {
			t.Error("the third launch did not happen")
		}
	})
	t.Run("failure, success(questions), failure is not two in a row", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), questionsOut("feat/x"), failOut(RunResultErrored)}
		f.runDelegation(t, id) // 失敗
		f.runDelegation(t, id) // questions（resume）
		f.answerLatestHold(t, id, "ANS")
		f.runDelegation(t, id) // 失敗
		n := len(f.deleg.launched())
		res := f.runDelegation(t, id)
		if len(f.deleg.launched()) != n+1 || len(res.NotStarted) != 0 {
			t.Errorf("launches %d→%d, notStarted %+v", n, len(f.deleg.launched()), res.NotStarted)
		}
	})
}

func TestFailureLimit_PreviousPlanVersionAndRateLimitedRunsAreNotCounted(t *testing.T) {
	t.Run("previous plan version", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		// 計画の版を 2 に進める（旧版の run は数えない）。
		cid, _ := parseChallengeID(id)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) SELECT challenge_id, 2, body, created_at, spec FROM task_plan WHERE challenge_id = ? AND version = 1`, cid); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
				VALUES (?, 'plan', 'approved', 2, 'alice', 'cli', 'tty_confirm', '2026-09-26T00:00:00.000Z')`, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		n := len(f.deleg.launched())
		res := f.runDelegation(t, id)
		if len(f.deleg.launched()) != n+1 || len(res.NotStarted) != 0 {
			t.Errorf("launches %d→%d, notStarted %+v", n, len(f.deleg.launched()), res.NotStarted)
		}
		if last := f.lastLaunch(t); last.IsResume {
			t.Error("a new plan version starts a new session")
		}
	})
	t.Run("rate limited run", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		limited := failOut(RunResultErrored)
		limited.RateLimited = true
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), limited}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		n := len(f.deleg.launched())
		f.refillImplBudget(t, id)
		res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), &id))
		if err != nil {
			t.Fatal(err)
		}
		if len(f.deleg.launched()) != n+1 || len(res.NotStarted) != 0 {
			t.Errorf("launches %d→%d, notStarted %+v", n, len(f.deleg.launched()), res.NotStarted)
		}
	})
}

func TestFailureLimit_AnswerRestartsTheCount(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	f.runDelegation(t, id) // 上限を検出して人間対応待ち
	if f.detail(t, id).Status != StatusAwaitingHuman {
		t.Fatalf("status = %s", f.detail(t, id).Status)
	}
	f.answerLatestHold(t, id, "keep going")
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)} // 回答の後の 1 回目の失敗
	n := len(f.deleg.launched())
	res := f.runDelegation(t, id)
	if len(f.deleg.launched()) != n+1 || len(res.NotStarted) != 0 {
		t.Fatalf("launches %d→%d, notStarted %+v", n, len(f.deleg.launched()), res.NotStarted)
	}
	got := f.lastLaunch(t)
	if !got.IsResume || got.ResumeKind != ResumeKindAnswer || got.ResumeAnswer != "keep going" {
		t.Errorf("launch = %+v, want the answer resume of the last failed session", got)
	}
	// 回答の後に 1 回失敗しただけでは、再び上限にならない。
	n = len(f.deleg.launched())
	f.runDelegation(t, id)
	if len(f.deleg.launched()) != n+1 {
		t.Error("one failure after the answer must not hit the limit")
	}
}

func TestResume_ErrorsFromBranchLookupDoNotBlockResume(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.questionAndAnswer(t, id, "feat/x")
	f.branches.err = errors.New("gh down")
	f.runDelegation(t, id) // 未 push 扱い。元のスロットが idle なので起動する
	if got := f.lastLaunch(t); !got.IsResume {
		t.Error("not a resume")
	}
}

// 連続失敗の数え方の単体検査: 失敗 → 成功 → 失敗 は連続して 2 回と数えない（成功が連続を切る）。
func TestDecideLaunch_ASuccessBreaksTheRunOfFailures(t *testing.T) {
	pv := int64(1)
	mk := func(id string, r RunResult) runRow {
		return runRow{ID: id, Kind: runKindDelegate, SessionID: "s-" + id, Result: r, PlanVersion: &pv, StartedAt: time.Now()}
	}
	h := &launchHistory{Runs: []runRow{mk("R-1", RunResultErrored), mk("R-2", RunResultSucceeded), mk("R-3", RunResultErrored)}}
	if _, limit := h.decideLaunch(1, 2); limit != nil {
		t.Errorf("limit = %+v, want none", limit)
	}
	h = &launchHistory{Runs: []runRow{mk("R-1", RunResultSucceeded), mk("R-2", RunResultErrored), mk("R-3", RunResultErrored)}}
	if _, limit := h.decideLaunch(1, 2); limit == nil || limit.Count != 2 {
		t.Errorf("limit = %+v, want count 2", limit)
	}
}

// 既定ブランチは子が作ったブランチではない: 再開のブランチ名にしない。
func TestResume_DefaultBranchIsNotPassedAsTheBranchToContinue(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.setSlotBranch("main")
	f.branches.branches["main"] = true
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); !got.IsResume || got.ResumeBranch != "" {
		t.Errorf("launch = %+v, want a resume with no branch", got)
	}
}

// 回答を渡す再開は、保留の問いも渡す（子が出した問いでない保留への回答でも意味が通る）。
func TestResume_AnswerResumeCarriesTheHoldQuestion(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{questionsOut("feat/x")}
	f.runDelegation(t, id)
	f.answerLatestHold(t, id, "ANS")
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); !strings.Contains(got.ResumeQuestion, "QUESTION-ONE") {
		t.Errorf("ResumeQuestion = %q", got.ResumeQuestion)
	}
}

// 中断の後の再開は、クローンでもブランチが push 済みでも元のスロットだけを使う。
func TestResume_InterruptedUsesOnlyTheOriginalSlot(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.branches.branches["feat/x"] = true
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
	f.setSlotBranch("feat/x")
	f.runDelegation(t, id)
	f.occupy(t, "main-repo", "slot1")
	f.refillImplBudget(t, id)
	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
		t.Errorf("notStarted = %+v", res.NotStarted)
	}
}

// refillImplBudget は、失敗の run の費用（上限額が費用になる）で実装枠が尽きても再開・再起動の
// 規則を検証できるよう、実装枠の残りを 50 USD に置き直す（flywheel budget と同じく、
// 計画の版の枠の上書きで行う。枠の残りの規則そのものは budget_test.go が検証する）。
func (f *delegateFixture) refillImplBudget(t *testing.T, id string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		plan, ok, err := loadApprovedPlan(context.Background(), tx, cid)
		if err != nil || !ok {
			return err
		}
		sp, err := loadBucketSpend(context.Background(), tx, cid, int64(plan.Version))
		if err != nil {
			return err
		}
		impl := sp.Impl + 50_000_000
		prev, err := loadPlanBudgetOverride(context.Background(), tx, cid, int64(plan.Version))
		if err != nil {
			return err
		}
		o := planBudgetOverride{ImplBudgetUSD: &impl}
		if prev != nil {
			o.ReviewBudgetUSD = prev.ReviewBudgetUSD
		}
		_, err = setPlanBudgetOverride(context.Background(), tx, cid, int64(plan.Version), o)
		return err
	})
	if err != nil {
		t.Fatalf("refill: %v", err)
	}
}
