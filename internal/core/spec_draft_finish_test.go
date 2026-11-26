package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// このファイルは、下書きづくりの実行が終わった後の扱いと費用を確かめる。テストの名前と注記の AC の番号は、
// Issue #1353 の受け持ち（親 #1339 の受入基準の並び順）。始めるのは委譲の段で、終わりは次の委譲の段の状態の確認が読む。

const sfDraftDoc = "# 下書き\n\n## 受入基準\n- [ ] 下書きの基準\n"

type sfFixture struct {
	*sdsFixture
	id      string
	cid     int64
	draft   string
	repoDir string
}

// sfStart は下書きづくりを 1 回始め、下書きのファイルを置いた足場を返す（記録は running）。
func sfStart(t *testing.T, d SpecDraft, before ...string) *sfFixture {
	t.Helper()
	f := &sfFixture{sdsFixture: newSDSFixture(t)}
	f.repoDir = filepath.Join(f.s.workspace, f.conn.Repos[len(f.conn.Repos)-1].Slots.Base)
	f.git.changedPaths = map[string][]string{f.repoDir: before}
	f.id = f.record(t, "finish", d)
	f.cid = mustChallengeID(t, f.id)
	f.draft = specDraftPathFor(f.cid)
	f.delegate(t, 100)
	f.requireRecord(t, f.id, SpecDraftStateRunning, d.Attempts+1)
	writeWorkspaceFile(t, f.s.workspace, f.draft, sfDraftDoc)
	return f
}

func sfOut(state ContractState, questions string, mut ...func(*ContractStatus)) ContractLaunchOutput {
	return contractOut(state, nil, func(st *ContractStatus) {
		if questions != "" {
			st.Facts = map[string]string{"spec_questions": questions}
		}
		for _, m := range mut {
			m(st)
		}
	})
}

func sfCost(usd float64) func(*ContractStatus) {
	return func(st *ContractStatus) { st.CostUSD = &usd }
}

func sfSteps(outcomes ...string) func(*ContractStatus) {
	return func(st *ContractStatus) {
		st.Steps = []ContractStep{}
		for i, o := range outcomes {
			st.Steps = append(st.Steps, ContractStep{Seq: i + 1, Step: "write", Kind: "llm", Attempt: 1, State: "finished", Outcome: &o, StartedAt: "2026-01-01T00:00:00Z"})
		}
	}
}

// finish は状態の確認が out を返すようにして、委譲の段を 1 回走らせる。
func (f *sfFixture) finish(t *testing.T, out ContractLaunchOutput) *ChallengeDetail {
	t.Helper()
	f.client.last = out
	f.delegate(t, 100)
	return f.detail(t, f.id)
}

func (f *sfFixture) openRuns(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT COUNT(*) FROM contract_run WHERE kind = 'spec' AND ended_at IS NULL`)
}

// failed は理由 spec_failed の保留になったことを確かめ、問いの文を返す。
func (f *sfFixture) failed(t *testing.T) string {
	t.Helper()
	h := f.requireSpecFailed(t, f.id, 1)
	if n := f.openRuns(t); n != 0 {
		t.Fatalf("open spec runs = %d", n)
	}
	return h.Question
}

func sfQuestion(question string, labels []string, recommended int, reason string) string {
	opts := make([]string, len(labels))
	for i, l := range labels {
		opts[i] = fmt.Sprintf(`{"label":%q}`, l)
	}
	return fmt.Sprintf(`{"question":%q,"options":[%s],"recommended":%d,"reason":%q}`, question, strings.Join(opts, ","), recommended, reason)
}

func sfList(qs ...string) string { return "[" + strings.Join(qs, ",") + "]" }

func sfValid(n int) string {
	qs := make([]string, n)
	for i := range qs {
		qs[i] = sfQuestion(fmt.Sprintf("問い%d", i+1), []string{"a", "b"}, 1, "理由")
	}
	return sfList(qs...)
}

// AC-187・188・189・191。状態の確認の実行が枠 spec で記録されることも確かめる。
func TestFinishSpecDraft_PollsAClassifiedChallengeInTheRecordedWorkDir(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	if d := f.detail(t, f.id); d.Status != StatusClassified {
		t.Fatalf("status = %s", d.Status)
	}
	// 呼び出し元が作業ディレクトリを渡しても、記録の値を使う。
	f.client.last = sfOut(ContractStateRunning, "")
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		Git: f.git, CycleID: f.cycle(t, 100), WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(f.s.workspace, ".flywheel", "specs")
	if len(f.client.statuses) != 1 || !slices.Equal(f.client.workDirs["status"], []string{want}) {
		t.Fatalf("statuses = %v workDirs = %v", f.client.statuses, f.client.workDirs["status"])
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND budget_bucket <> 'spec' AND kind = 'contract'`, f.cid); n != 0 {
		t.Fatalf("non-spec contract runs = %d", n)
	}
	if d := f.detail(t, f.id); d.SpecPath != nil { // AC-191
		t.Fatalf("spec_path = %v while running", *d.SpecPath)
	}
	f.requireRecord(t, f.id, SpecDraftStateRunning, 1)
	f.client.cancelOut = sfOut(ContractStateCancelled, "")
	if _, err := f.s.CancelOpenSpecDraftRuns(context.Background(), ContractCancelInput{ChallengeID: f.id, AgentDecl: f.agent,
		ConnDecl: f.conn, Client: f.client, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.client.workDirs["cancel"], []string{want}) {
		t.Fatalf("cancel workDirs = %v", f.client.workDirs["cancel"])
	}
	if d := f.detail(t, f.id); len(d.Holds) != 0 {
		t.Fatalf("an owner cancel raised a hold: %+v", d.Holds)
	}
}

// AC-190・193・195・196・197・199・200
func TestFinishSpecDraft_DoneWithoutQuestions(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	d := f.finish(t, sfOut(ContractStateSucceeded, "[]", func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/1"}}
	}))
	f.requireRecord(t, f.id, SpecDraftStateDone, 1)
	if d.Status != StatusClassified || d.SpecPath == nil || *d.SpecPath != f.draft || d.SpecBasePath != nil || !d.SpecIsDraft() || len(d.Holds) != 0 {
		t.Fatalf("detail = status %s spec %v base %v holds %v", d.Status, d.SpecPath, d.SpecBasePath, d.Holds)
	}
	acts := seActivities(t, f.s, f.id, string(ActivityActionSpecDraftDone))
	if len(acts) != 1 || acts[0]["detail"] != "flow" {
		t.Fatalf("spec_draft_done = %v", acts)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 0 {
		t.Fatalf("artifacts = %d", n)
	}
	if n := f.openRuns(t); n != 0 {
		t.Fatalf("open spec runs = %d", n)
	}
}

// AC-194
func TestFinishSpecDraft_DoneWritesTheBasePath(t *testing.T) {
	t.Parallel()
	base := "docs/features/old.md"
	f := sfStart(t, SpecDraft{BasePath: &base})
	d := f.finish(t, sfOut(ContractStateSucceeded, "[]"))
	if d.SpecBasePath == nil || *d.SpecBasePath != base {
		t.Fatalf("spec_base_path = %v", d.SpecBasePath)
	}
}

// AC-198
func TestFinishSpecDraft_DoneFeedsTheDraftToPlanning(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.finish(t, sfOut(ContractStateSucceeded, "[]"))
	j := newJ2Fixture(t)
	j.s, j.conn = f.s, f.conn
	j.agent = newAgentDeclForJ1Test(t, f.s, "担当範囲")
	content, ok := j2SectionText(planInput(t, j, f.id), AcceptanceCriteriaSectionLabel)
	if !ok || !strings.Contains(content, "下書き（リポジトリにまだ入っていない）") {
		t.Fatalf("section = %q %v", content, ok)
	}
}

// AC-201・211・212。レビュー待ち以外の待ちも同じに扱う。
func TestFinishSpecDraft_WaitingIsCancelledAndHeld(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{contractRequestedKindReview, contractRequestedKindAnswer, contractRequestedKindObserve} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			f.client.cancelOut = sfOut(ContractStateCancelled, "")
			d := f.finish(t, sfOut(ContractStateWaiting, "[]", func(st *ContractStatus) {
				st.RequestedAction = &ContractRequestedAction{Kind: kind, Decider: "human", Text: "見て", Options: []string{"land"}}
				st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/1"}, {Kind: "commit", Ref: "abc"}}
			}))
			if len(f.client.cancels) != 1 || len(f.client.resumes) != 0 {
				t.Fatalf("cancels = %v resumes = %v", f.client.cancels, f.client.resumes)
			}
			f.failed(t)
			if d.Status == StatusVerifying || d.SpecPath != nil || f.count(t, `SELECT COUNT(*) FROM run_artifact`) != 0 {
				t.Fatalf("status = %s", d.Status)
			}
		})
	}
}

// 取り消せなかった待ちは、保留にしたうえで実行を開けたまま残し、次の状態の確認が取り消し直す。
func TestFinishSpecDraft_WaitingThatCannotBeCancelledStaysOpen(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "cannot"}
	waiting := sfOut(ContractStateWaiting, "", func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: contractRequestedKindReview, Decider: "human", Text: "見て"}
	})
	f.finish(t, waiting)
	h := f.requireSpecFailed(t, f.id, 1)
	if !strings.Contains(h.Question, "取り消せません") || f.openRuns(t) != 1 {
		t.Fatalf("question = %q open = %d", h.Question, f.openRuns(t))
	}
	f.client.cancelOut = sfOut(ContractStateCancelled, "")
	d := f.finish(t, waiting)
	if len(f.client.cancels) != 2 || f.openRuns(t) != 0 || len(d.Holds) != 1 {
		t.Fatalf("cancels = %v open = %d holds = %d", f.client.cancels, f.openRuns(t), len(d.Holds))
	}
}

// AC-202: 課題にほかの保留が開いている間の成功は、held_success を付けない。記録と保留も書かず、実行を開けたまま残して、
// 課題が分類済へ戻った後の状態の確認が同じ終わりを扱う。
func TestFinishSpecDraft_AnotherOpenHoldDefersTheEnd(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, f.cid)
		return err
	})
	d := f.finish(t, sfOut(ContractStateSucceeded, "[]", sfCost(1)))
	f.requireRecord(t, f.id, SpecDraftStateRunning, 1)
	if len(f.client.statuses) != 1 || d.SpecPath != nil || f.openRuns(t) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM contract_run WHERE held_success = 1`) != 0 {
		t.Fatalf("statuses = %d spec = %v open = %d", len(f.client.statuses), d.SpecPath, f.openRuns(t))
	}
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE challenge SET status = 'classified' WHERE id = ?`, f.cid)
		return err
	})
	d = f.finish(t, ContractLaunchOutput{Result: RunResultErrored})
	f.requireRecord(t, f.id, SpecDraftStateDone, 1)
	if len(f.client.statuses) != 1 || f.openRuns(t) != 0 || d.SpecCostUSD != 1 {
		t.Fatalf("statuses = %d open = %d cost = %v", len(f.client.statuses), f.openRuns(t), d.SpecCostUSD)
	}
}

// AC-203
func TestFinishSpecDraft_RequiredFactsAreNotCounted(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.flowConnector().RequiredFacts = map[string][]string{"quality_gate": {"pass"}}
	// 数える処理は、タスクを持たない実行では課題が着手中のときだけ数えるので、その状態にして読む（分類済では守りが無くても数えない）。
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE challenge SET status = 'in_progress' WHERE id = ?`, f.cid)
		return err
	})
	f.client.cancelOut = sfOut(ContractStateCancelled, "")
	f.client.last = sfOut(ContractStateWaiting, "", func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: contractRequestedKindReview, Decider: "human", Text: "見て", Options: []string{"land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/1"}, {Kind: "commit", Ref: "abc"}}
	})
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		Git: f.git, CycleID: f.cycle(t, 100)}); err != nil {
		t.Fatal(err)
	}
	if len(f.client.statuses) != 1 {
		t.Fatalf("statuses = %v", f.client.statuses)
	}
	if n := f.count(t, `SELECT SUM(facts_unmet_count) FROM contract_run`); n != 0 {
		t.Fatalf("facts_unmet_count = %d", n)
	}
}

// AC-204・205・206: 種類 spec の実行は、完了の検証の直し先・反映の段・統合先のタスクの終わりの扱いに入らない。
func TestFinishSpecDraft_SpecRunStaysOutOfTaskPaths(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		// 統合先を使う課題で、直し先の確かめ待ちの中身が置かれていても、読まない。
		if _, err := tx.ExecContext(ctx, `UPDATE challenge SET uses_integration = 1 WHERE id = ?`, f.cid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_pending_ref = 'abc' WHERE challenge_id = ?`, f.cid); err != nil {
			return err
		}
		cands, err := scanJ5FixCandidates(ctx, tx, ` AND k.challenge_id = ?`, f.cid)
		if err != nil || len(cands) != 1 {
			return fmt.Errorf("fix candidates = %+v %w", cands, errors.Join(errors.New("unexpected"), err))
		}
		for _, status := range []Status{StatusInProgress, StatusDone} {
			c := cands[0]
			c.ChallengeStatus = string(status)
			if tg, err := resolveJ5FixTarget(ctx, tx, c); err != nil || tg != nil {
				return fmt.Errorf("fix target (%s) = %+v %w", status, tg, errors.Join(errors.New("unexpected"), err))
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_pending_ref = NULL WHERE challenge_id = ?`, f.cid); err != nil {
			return err
		}
		run, err := loadOpenRunInTx(ctx, tx, f.cid) // 反映の段が読む実行
		if err != nil || run != nil {
			return fmt.Errorf("release run = %+v %w", run, errors.Join(errors.New("unexpected"), err))
		}
		return nil
	})
	f.finish(t, sfOut(ContractStateSucceeded, "[]"))
	f.requireRecord(t, f.id, SpecDraftStateDone, 1)
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE upstream_unreadable_rounds > 0 OR held_success = 1 OR verified_ref IS NOT NULL OR land_ref IS NOT NULL`); n != 0 {
		t.Fatalf("integration columns were touched")
	}
	if d := f.detail(t, f.id); d.Status != StatusClassified || len(d.Holds) != 0 {
		t.Fatalf("status = %s holds = %v", d.Status, d.Holds)
	}
}

// AC-207・208: 完了の検証が読む「最後の委譲の実行」は、種類 spec の実行の前後どちらでも、タスクの実行になる。
func TestFinishSpecDraft_LastDelegateRunIsTheTaskRun(t *testing.T) {
	t.Parallel()
	for name, specIsLast := range map[string]bool{"AC-207 spec の後にタスク": false, "AC-208 タスクの後に spec": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cf := newContractFixture(t)
			cid := mustChallengeID(t, cf.cid)
			var taskRun string
			insert := func(bucket budgetBucket) {
				if err := cf.s.db.Write(context.Background(), func(tx *sql.Tx) error {
					now := time.Now()
					row, err := insertRun(context.Background(), tx, cid, insertRunInput{Kind: runKindContract, ChallengeVersion: 1,
						SessionID: "s-" + string(bucket), PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: bucket})
					if err != nil {
						return err
					}
					if bucket == budgetBucketImpl {
						taskRun = row.ID
					}
					n, _ := parseRunID(row.ID)
					_, err = updateRunEnd(context.Background(), tx, n, updateRunEndInput{EndedAt: now, Result: RunResultSucceeded})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if specIsLast {
				insert(budgetBucketImpl)
				insert(budgetBucketSpec)
			} else {
				insert(budgetBucketSpec)
				insert(budgetBucketImpl)
			}
			if _, err := cf.s.SubmitChallenge(context.Background(), ChannelCLI, cf.cid); err != nil {
				t.Fatal(err)
			}
			ch, err := cf.s.GetChallenge(context.Background(), cf.cid)
			if err != nil {
				t.Fatal(err)
			}
			vc, err := cf.s.loadJ5Context(context.Background(), ch.Challenge)
			if err != nil || vc.LastDelegate == nil || vc.LastDelegate.ID != taskRun {
				t.Fatalf("last delegate = %+v %v, want %s", vc, err, taskRun)
			}
		})
	}
}

// AC-209・210
func TestFinishSpecDraft_SkippedRecordIsLeftAlone(t *testing.T) {
	t.Parallel()
	for name, state := range map[string]ContractState{"AC-209 成功": ContractStateSucceeded, "AC-210 失敗": ContractStateFailed, "取り消し": ContractStateCancelled} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			f.write(t, func(ctx context.Context, tx *sql.Tx) error {
				return updateSpecDraft(ctx, tx, f.cid, specDraftUpdate{State: SpecDraftStateSkipped, Attempts: 1}, time.Now())
			})
			d := f.finish(t, sfOut(state, "[]", sfCost(2)))
			f.requireRecord(t, f.id, SpecDraftStateSkipped, 1)
			if d.Status != StatusClassified || d.SpecPath != nil || len(d.Holds) != 0 || f.openRuns(t) != 0 || d.SpecCostUSD != 2 {
				t.Fatalf("status = %s spec = %v holds = %v open = %d cost = %v", d.Status, d.SpecPath, d.Holds, f.openRuns(t), d.SpecCostUSD)
			}
		})
	}
}

// AC-213〜226
func TestFinishSpecDraft_BadResultsAreFailures(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("あ", 2001)
	one := func(q string, labels []string, rec int, reason string) string {
		return sfList(sfQuestion(q, labels, rec, reason))
	}
	ab := []string{"a", "b"}
	for name, tc := range map[string]struct {
		questions string
		draft     string // "-" はファイルを消す
	}{
		"AC-213 下書きが無い":             {"[]", "-"},
		"AC-214 問いがあっても下書きが無い":      {sfValid(1), "-"},
		"AC-215 受入基準が 0 件":          {"[]", "# 下書き\n"},
		"AC-216 spec_questions が無い": {"", sfDraftDoc},
		"AC-217 配列でない":              {`{"a":1}`, sfDraftDoc},
		"null":                      {"null", sfDraftDoc},
		"AC-218 推奨が範囲の外":            {one("q", ab, 3, "r"), sfDraftDoc},
		"推奨が 0":                     {one("q", ab, 0, "r"), sfDraftDoc},
		"AC-219 選択肢が 1 個":           {one("q", []string{"a"}, 1, "r"), sfDraftDoc},
		"AC-220 選択肢が 6 個":           {one("q", []string{"1", "2", "3", "4", "5", "6"}, 1, "r"), sfDraftDoc},
		"AC-221 問いの文が空":             {one("", ab, 1, "r"), sfDraftDoc},
		"AC-222 推奨の理由が空":            {one("q", ab, 1, " "), sfDraftDoc},
		"見出しが空":                     {one("q", []string{"a", ""}, 1, "r"), sfDraftDoc},
		"AC-223 問いが 11 件":           {sfValid(11), sfDraftDoc},
		"AC-224 問いの文が 2001 文字":      {one(long, ab, 1, "r"), sfDraftDoc},
		"AC-225 見出しが 2001 文字":       {one("q", []string{"a", long}, 1, "r"), sfDraftDoc},
		"AC-226 推奨の理由が 2001 文字":     {one("q", ab, 1, long), sfDraftDoc},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			if tc.draft == "-" {
				if err := os.Remove(filepath.Join(f.s.workspace, f.draft)); err != nil {
					t.Fatal(err)
				}
			} else {
				writeWorkspaceFile(t, f.s.workspace, f.draft, tc.draft)
			}
			d := f.finish(t, sfOut(ContractStateSucceeded, tc.questions))
			if q := f.failed(t); strings.Contains(q, "上限額") || strings.Contains(q, "リポジトリが書き換えられた") {
				t.Fatalf("question = %q", q)
			}
			if d.SpecPath != nil || len(d.SpecDraft.Questions) != 0 {
				t.Fatalf("spec = %v questions = %v", d.SpecPath, d.SpecDraft.Questions)
			}
		})
	}
}

// AC-192・227・228・247。上限の値（問い 10 件・2000 文字・選択肢 5 個）は通す。
func TestFinishSpecDraft_QuestionsAreHeld(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("あ", 2000)
	boundary := sfList(sfQuestion(long, []string{long, "2", "3", "4", "5"}, 5, long))
	for name, tc := range map[string]struct {
		attempts  int
		questions string
		draft     string
		want      SpecDraftHoldReason
		n         int
	}{
		"AC-227・228 2 件で 1 回目": {0, sfValid(2), sfDraftDoc, SpecDraftHoldQuestion, 2},
		"2 回目":                 {1, sfValid(10), sfDraftDoc, SpecDraftHoldQuestion, 10},
		"AC-247 3 回目":          {2, sfValid(1), sfDraftDoc, SpecDraftHoldAttemptLimit, 1},
		"境界の値":                 {0, boundary, sfDraftDoc, SpecDraftHoldQuestion, 1},
		"問いがあれば受入基準が 0 件でも保留に": {0, sfValid(1), "# 下書き\n", SpecDraftHoldQuestion, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{Attempts: tc.attempts})
			writeWorkspaceFile(t, f.s.workspace, f.draft, tc.draft)
			d := f.finish(t, sfOut(ContractStateSucceeded, tc.questions))
			sd := d.SpecDraft
			if d.Status != StatusAwaitingHuman || sd.State != SpecDraftStateAwaitingOwner || sd.HoldReason == nil || *sd.HoldReason != tc.want ||
				len(sd.Questions) != tc.n || sd.Attempts != tc.attempts+1 || d.SpecPath != nil || f.openRuns(t) != 0 { // AC-192
				t.Fatalf("status = %s spec_draft = %+v", d.Status, sd)
			}
		})
	}
}

// AC-229〜233
func TestFinishSpecDraft_RewrittenRepository(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{}, "dirty.go")
	added := filepath.Join(f.repoDir, "new.txt")
	writeWorkspaceFile(t, f.repoDir, "new.txt", "x")
	f.git.changedPaths[f.repoDir] = []string{"dirty.go", "new.txt"}
	d := f.finish(t, sfOut(ContractStateSucceeded, "[]"))
	q := f.failed(t)
	if !strings.Contains(q, "リポジトリが書き換えられた") || !strings.Contains(q, "new.txt") || strings.Contains(q, "dirty.go") {
		t.Fatalf("question = %q", q)
	}
	if d.SpecDraft.State == SpecDraftStateDone || d.SpecPath != nil {
		t.Fatalf("spec_draft = %+v", d.SpecDraft)
	}
	if _, err := os.Stat(added); err != nil {
		t.Fatalf("the added file was removed: %v", err)
	}
}

// AC-234・235
func TestFinishSpecDraft_ComparesTheSetOfChangedPaths(t *testing.T) {
	t.Parallel()
	t.Run("AC-234 同じ一覧", func(t *testing.T) {
		t.Parallel()
		f := sfStart(t, SpecDraft{}, "tracked.go", "line\nbreak", `"quoted`)
		f.finish(t, sfOut(ContractStateSucceeded, "[]"))
		f.requireRecord(t, f.id, SpecDraftStateDone, 1)
	})
	t.Run("AC-235 追跡しているファイルが新しく変わった", func(t *testing.T) {
		t.Parallel()
		f := sfStart(t, SpecDraft{}, "a.go")
		f.git.changedPaths[f.repoDir] = []string{"a.go", "b.go"}
		f.finish(t, sfOut(ContractStateSucceeded, "[]"))
		if q := f.failed(t); !strings.Contains(q, "b.go") {
			t.Fatalf("question = %q", q)
		}
	})
}

// AC-236・237。取り消しでも同じ。
func TestFinishSpecDraft_RewrittenRepositoryOfAFailedRun(t *testing.T) {
	t.Parallel()
	for _, state := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			var paths []string
			for i := 1; i <= 25; i++ {
				paths = append(paths, fmt.Sprintf("added-%02d.txt", i))
			}
			f.git.changedPaths[f.repoDir] = paths
			f.finish(t, sfOut(state, ""))
			q := f.failed(t)
			if !strings.Contains(q, "リポジトリが書き換えられた") || strings.Count(q, "added-") != 20 || !strings.Contains(q, "残り 5 件") {
				t.Fatalf("question = %q", q)
			}
		})
	}
}

// AC-238・239
func TestFinishSpecDraft_UnverifiableRepositoryIsAFailure(t *testing.T) {
	t.Parallel()
	for name, breakIt := range map[string]func(t *testing.T, f *sfFixture){
		"AC-238 一覧を読めない": func(_ *testing.T, f *sfFixture) { f.git.changedErr = errors.New("git is broken") },
		"AC-239 始める前の一覧が無い": func(t *testing.T, f *sfFixture) {
			m, err := filepath.Glob(filepath.Join(f.s.workspace, ".flywheel", "runs", "*", repoStatusBeforeFileName))
			if err != nil || len(m) != 1 {
				t.Fatalf("%s = %v %v", repoStatusBeforeFileName, m, err)
			}
			if err := os.Remove(m[0]); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			breakIt(t, f)
			f.finish(t, sfOut(ContractStateSucceeded, "[]"))
			if q := f.failed(t); !strings.Contains(q, "確かめられません") {
				t.Fatalf("question = %q", q)
			}
		})
	}
}

// AC-240〜246: 失敗・取り消しの終わりと上限額。
func TestFinishSpecDraft_FailedAndCancelled(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		out        ContractLaunchOutput
		want, deny []string
	}{
		"AC-240 失敗":          {sfOut(ContractStateFailed, ""), nil, []string{"上限額"}},
		"AC-241 取り消し":        {sfOut(ContractStateCancelled, ""), nil, []string{"上限額"}},
		"AC-243 最後のステップが上限額": {sfOut(ContractStateFailed, "", sfCost(1), sfSteps("ok", "budget_exhausted")), []string{"上限額", "1 USD"}, nil},
		"AC-244 費用が上限額以上":    {sfOut(ContractStateFailed, "", sfCost(15)), []string{"上限額", "費用: 15 USD"}, nil},
		"AC-245 費用 3.2 USD":  {sfOut(ContractStateFailed, "", sfCost(3.2), sfSteps("budget_exhausted")), []string{"上限額", "費用: 3.2 USD"}, nil},
		"AC-246 どちらでもない":     {sfOut(ContractStateFailed, "", sfCost(14.99), sfSteps("budget_exhausted", "step_error")), nil, []string{"上限額"}},
		"取り消しで上限額":           {sfOut(ContractStateCancelled, "", sfCost(16)), []string{"上限額", "16 USD"}, nil},
		"成功で費用が上限額以上でも上限額とは書かない（問いの不正）": {sfOut(ContractStateSucceeded, "x", sfCost(16)), nil, []string{"上限額"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			f.finish(t, tc.out)
			q := f.failed(t)
			for _, w := range tc.want {
				if !strings.Contains(q, w) {
					t.Errorf("question lacks %q: %q", w, q)
				}
			}
			for _, w := range tc.deny {
				if strings.Contains(q, w) {
					t.Errorf("question has %q: %q", w, q)
				}
			}
			if _, err := os.Stat(filepath.Join(f.s.workspace, f.draft)); err != nil { // AC-242
				t.Fatalf("the draft was removed: %v", err)
			}
		})
	}
}

// 状態の確認のコマンドが失敗した読みでは、何も決めない。
func TestFinishSpecDraft_UnreadStatusChangesNothing(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.finish(t, ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"})
	f.requireRecord(t, f.id, SpecDraftStateRunning, 1)
	if f.openRuns(t) != 1 {
		t.Fatalf("open = %d", f.openRuns(t))
	}
}

// AC-352〜355
func TestSpecDraftCost(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	cycle := f.cycle(t, 100)
	f.client.last = sfOut(ContractStateRunning, "", sfCost(2))
	f.delegateIn(t, cycle)
	ended, err := f.s.EndCycle(context.Background(), cycle, CycleResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if ended.SpentUSD != 2 {
		t.Fatalf("cycle spent = %v", ended.SpentUSD)
	}
	d := f.finish(t, sfOut(ContractStateSucceeded, sfValid(1), sfCost(2)))
	if d.SpecCostUSD != 2 {
		t.Fatalf("spec_cost_usd = %v", d.SpecCostUSD)
	}
	b, err := f.s.GetChallengeBudget(context.Background(), f.id, f.agent)
	if err != nil || b.ImplSpentUSD != 0 || b.ReviewSpentUSD != 0 {
		t.Fatalf("budget = %+v %v", b, err)
	}
	// 対話の欄の仕様づくりの費用 1 USD を足すと 3。
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_session (engine_session, started_at) VALUES ('e', ?)`, formatTimestamp(time.Now()))
		if err != nil {
			return err
		}
		sid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_message (session_id, role, body, state, cost_usd, charged_challenge_id, created_at) VALUES (?, ?, 'x', 'done', ?, ?, ?)`,
			sid, string(ChatRoleOrchestrator), usdToMicros(1), f.cid, formatTimestamp(time.Now()))
		return err
	})
	if got := f.detail(t, f.id).SpecCostUSD; got != 3 {
		t.Fatalf("spec_cost_usd with chat = %v", got)
	}
}

// AC-356 と、AC-113 の後半を含む通し（始める → 終わる → 保留 → やり直し → … → 済み）。
func TestSpecDraft_EndToEnd(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	retry := func() {
		t.Helper()
		// retry・answer の回答の後の形（保留を閉じ、分類済と実行待ちへ戻す）。
		f.write(t, func(ctx context.Context, tx *sql.Tx) error {
			d, err := loadSpecDraft(ctx, tx, f.cid)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE hold SET closed_at = ? WHERE challenge_id = ? AND closed_at IS NULL`, formatTimestamp(time.Now()), f.cid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE challenge SET status = 'classified' WHERE id = ?`, f.cid); err != nil {
				return err
			}
			return updateSpecDraft(ctx, tx, f.cid, specDraftUpdate{State: SpecDraftStatePending, Attempts: d.Attempts, Answers: d.Answers}, time.Now())
		})
		f.delegate(t, 100)
	}
	end := func(run int, state ContractState, questions string, cost float64) *ChallengeDetail {
		t.Helper()
		return f.finish(t, sfOut(state, questions, sfCost(cost), func(st *ContractStatus) { st.RunID = fmt.Sprintf("ext-%d", run) }))
	}
	end(1, ContractStateFailed, "", 1)
	f.requireSpecFailed(t, f.id, 1)
	retry()
	end(2, ContractStateSucceeded, sfValid(2), 2)
	f.requireRecord(t, f.id, SpecDraftStateAwaitingOwner, 2)
	retry()
	f.requireRecord(t, f.id, SpecDraftStateRunning, 3)
	d := end(3, ContractStateSucceeded, "[]", 4)
	f.requireRecord(t, f.id, SpecDraftStateDone, 3)
	if d.SpecPath == nil || *d.SpecPath != f.draft || d.Status != StatusClassified || d.SpecCostUSD != 7 || len(f.client.starts) != 3 {
		t.Fatalf("detail = %s %v cost %v starts %d", d.Status, d.SpecPath, d.SpecCostUSD, len(f.client.starts))
	}
}
