package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは #107（親要件チケット #98 §J5 検証・§失敗・差し戻しの上限。AC-219・265〜277）の
// core 側を、実ストア・偽の判断／委譲の IF・偽の上流とチェックの取得で検証する。

// fakeChecks は UpstreamCheckSource の偽の実装。"<repo>#<番号>" ごとの結果を持つ。
type fakeChecks struct {
	mu    sync.Mutex
	prs   map[string]UpstreamPullRequestChecks
	err   error
	calls []string
}

func newFakeChecks() *fakeChecks { return &fakeChecks{prs: map[string]UpstreamPullRequestChecks{}} }

func (f *fakeChecks) GetPullRequestChecks(_ context.Context, repo string, number int) (UpstreamPullRequestChecks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%s#%d", repo, number)
	f.calls = append(f.calls, key)
	if f.err != nil {
		return UpstreamPullRequestChecks{}, f.err
	}
	pr, ok := f.prs[key]
	if !ok {
		return UpstreamPullRequestChecks{}, errors.New("no such pull request: " + key)
	}
	return pr, nil
}

func (f *fakeChecks) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

const j5PRURL = "https://github.com/o/r/pull/1"

func prChecks(state string, checks ...UpstreamCheck) UpstreamPullRequestChecks {
	return UpstreamPullRequestChecks{URL: j5PRURL, Title: "PR title 1", State: state, Base: "develop", HeadSHA: "abc123", Checks: checks}
}

var (
	checkDone    = UpstreamCheck{Name: "build", Status: "completed", Conclusion: "success"}
	checkFailed  = UpstreamCheck{Name: "test", Status: "completed", Conclusion: "failure"}
	checkRunning = UpstreamCheck{Name: "lint", Status: "in_progress"}
)

func j5Out(verdict string, feedback, question any) JudgmentLaunchOutput {
	b, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": "REASON-" + verdict, "feedback": feedback, "question": question})
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
}

type j5Fixture struct {
	*delegateFixture
	checks   *fakeChecks
	j5       *fakeJudgmentInvoker
	j5Inputs []JudgmentLaunchInput
}

// newJ5Fixture は C-1（operation の宣言の課題。着手中で承認済みの計画付き）を持つフィクスチャ。
func newJ5Fixture(t *testing.T, operation string) *j5Fixture {
	t.Helper()
	f := &j5Fixture{delegateFixture: newDelegateFixture(t), checks: newFakeChecks()}
	f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) { m["operation"] = operation }))
	f.j5 = &fakeJudgmentInvoker{result: j5Out("met", nil, nil)}
	f.j5.onInvoke = func(in JudgmentLaunchInput) { f.j5Inputs = append(f.j5Inputs, in) }
	return f
}

// delegateToVerifying は C-1 を委譲して検証中にする（PR のチェックは fakeChecks に別に置く）。
func (f *j5Fixture) delegateToVerifying(t *testing.T, id string) {
	t.Helper()
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
	f.checks.prs["o/r#1"] = prChecks("open", checkDone)
	f.runDelegation(t, id)
	if got := f.detail(t, id).Status; got != StatusVerifying {
		t.Fatalf("status after the delegation = %s, want verifying", got)
	}
}

func (f *j5Fixture) input(cycleID string, id *string) J5AutoInput {
	return J5AutoInput{ChallengeID: id, AgentDecl: f.agent, Invoker: f.j5, Upstream: f.upstream, Checks: f.checks, CycleID: cycleID}
}

func (f *j5Fixture) verify(t *testing.T, id *string) (*JudgmentAutoResult, error) {
	t.Helper()
	return f.s.VerifyAutoJ5(context.Background(), f.input(f.cycle(t, 300), id))
}

func (f *j5Fixture) j5Runs(t *testing.T) []runRow {
	t.Helper()
	var out []runRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM run WHERE judgment = 'J5' ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		for _, id := range ids {
			r, err := loadRunByID(context.Background(), tx, id)
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *j5Fixture) stdin(t *testing.T) string {
	t.Helper()
	if len(f.j5Inputs) == 0 {
		t.Fatal("J5 was not invoked")
	}
	var b strings.Builder
	for _, s := range f.j5Inputs[len(f.j5Inputs)-1].Sections {
		b.WriteString("## " + s.Label + "\n" + s.Content + "\n")
	}
	return b.String()
}

// --- 出力の検査 ---

func TestValidateJ5Output(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"met", `{"verdict":"met","reason":"r","feedback":null,"question":null}`, true},
		{"met without the nullable keys", `{"verdict":"met","reason":"r"}`, true},
		{"not_met with feedback", `{"verdict":"not_met","reason":"r","feedback":"fix it","question":null}`, true},
		{"not_met with null feedback", `{"verdict":"not_met","reason":"r","feedback":null,"question":null}`, false},
		{"not_met with blank feedback", `{"verdict":"not_met","reason":"r","feedback":"  ","question":null}`, false},
		{"uncertain with question", `{"verdict":"uncertain","reason":"r","feedback":null,"question":"?"}`, true},
		{"uncertain with null question", `{"verdict":"uncertain","reason":"r","feedback":null,"question":null}`, false},
		{"unknown verdict", `{"verdict":"maybe","reason":"r"}`, false},
		{"missing reason", `{"verdict":"met"}`, false},
		{"not json", `nope`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := validateJ5Output([]byte(tc.raw))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && tc.name == "not_met with feedback" && (v.Feedback == nil || *v.Feedback != *str("fix it")) {
				t.Errorf("feedback = %v", v.Feedback)
			}
		})
	}
}

func TestJ5OutputSchema_HasTheFourKeysAndTheClosedVerdicts(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(j5OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"verdict", "reason", "feedback", "question"} {
		if _, ok := schema.Properties[k]; !ok {
			t.Errorf("schema lacks %q", k)
		}
	}
	got := schema.Properties["verdict"].Enum
	if len(got) != len(J5VerdictValues) {
		t.Fatalf("verdict enum = %v", got)
	}
	for i, v := range J5VerdictValues {
		if got[i] != string(v) {
			t.Errorf("verdict enum[%d] = %q, want %q", i, got[i], v)
		}
	}
}

// --- 対象（AC-268） ---

func TestVerifyAutoJ5_OmittedID_OnlyVerifyingChallengesWithoutAnActiveRun(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1") // 検証中・終了していない run なし
	other := f.newInProgress(t, "other", "P1", planSpec(nil))
	_ = other // 着手中: 対象外
	busy := f.newInProgress(t, "busy", "P1", planSpec(nil))
	f.setStatus(t, busy, "verifying")
	cid, _ := parseChallengeID(busy)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: f.s.currentTime(), StartedAt: f.s.currentTime(), MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatalf("VerifyAutoJ5: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != "C-1" {
		t.Fatalf("items = %+v, want only C-1", res.Items)
	}
	if got := f.detail(t, other).Status; got != StatusInProgress {
		t.Errorf("the in-progress challenge changed: %s", got)
	}
}

func TestVerifyAutoJ5_ExplicitID_StatusAndActiveRunErrors(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	id := "C-1" // 着手中
	if _, err := f.verify(t, &id); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition for a challenge that is not verifying", err)
	}
	f.delegateToVerifying(t, id)
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: f.s.currentTime(), StartedAt: f.s.currentTime(), MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, &id); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("err = %v, want ErrRunInProgress", err)
	}
}

func TestVerifyAutoJ5_RejectsIncompleteInput(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	base := f.input(f.cycle(t, 300), nil)
	for name, mut := range map[string]func(in *J5AutoInput){
		"agent":    func(in *J5AutoInput) { in.AgentDecl = nil },
		"invoker":  func(in *J5AutoInput) { in.Invoker = nil },
		"upstream": func(in *J5AutoInput) { in.Upstream = nil },
		"checks":   func(in *J5AutoInput) { in.Checks = nil },
		"cycle":    func(in *J5AutoInput) { in.CycleID = "" },
	} {
		in := base
		mut(&in)
		if _, err := f.s.VerifyAutoJ5(context.Background(), in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

// --- CI の待ち（AC-269・270・271・M3P42） ---

func TestVerifyAutoJ5_PendingChecks_NoJ5NoChangeAndWaitingExternal(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = prChecks("open", checkDone, checkRunning)
	before := f.detail(t, "C-1")

	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatalf("VerifyAutoJ5: %v", err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != "C-1" ||
		res.NotStarted[0].Reason != NotStartedWaitingExternal || res.NotStarted[0].Detail != j5PRURL {
		t.Fatalf("result = %+v, want C-1 not started with waiting_external", res)
	}
	if len(f.j5Inputs) != 0 || len(f.j5Runs(t)) != 0 {
		t.Error("J5 must not be invoked or recorded while a check is not completed")
	}
	if after := f.detail(t, "C-1"); after.Status != before.Status || after.Version != before.Version {
		t.Errorf("challenge changed: %s v%d -> %s v%d", before.Status, before.Version, after.Status, after.Version)
	}

	waiting, err := f.s.ListWaitingExternal(context.Background(), f.checks)
	if err != nil || len(waiting) != 1 || waiting[0].ChallengeID != "C-1" || waiting[0].PRURL != j5PRURL {
		t.Fatalf("ListWaitingExternal = %+v, %v", waiting, err)
	}

	// 1 つでも完了していなければ待つ（キューに入ったままのチェックも）。
	f.checks.prs["o/r#1"] = prChecks("open", checkDone, UpstreamCheck{Name: "queued", Status: "queued"})
	if res, _ := f.verify(t, nil); len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedWaitingExternal {
		t.Errorf("queued check must be waited for: %+v", res)
	}
}

func TestVerifyAutoJ5_AllChecksCompleted_J5RunsAndSeesTheResultsIncludingFailures(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = prChecks("open", checkDone, checkFailed)

	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("VerifyAutoJ5 = %+v, %v", res, err)
	}
	in := f.stdin(t)
	for _, want := range []string{j5PRURL, "build", "test", "success", "failure", "abc123"} {
		if !strings.Contains(in, want) {
			t.Errorf("J5 input lacks %q:\n%s", want, in)
		}
	}
	waiting, _ := f.s.ListWaitingExternal(context.Background(), f.checks)
	if len(waiting) != 0 {
		t.Errorf("waiting = %+v, want none once the checks completed", waiting)
	}
}

func TestVerifyAutoJ5_ClosedUnmergedPRChecksAreNotWaitedFor(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = prChecks("closed", checkRunning)
	res, _ := f.verify(t, nil)
	if len(res.Items) != 1 {
		t.Fatalf("result = %+v, want J5 to run", res)
	}
}

func TestVerifyAutoJ5_NoPR_BranchAndNone_ChecksNotQueriedAndInputSaysSo(t *testing.T) {
	for _, operation := range []string{"impl-branch", "impl-none"} {
		t.Run(operation, func(t *testing.T) {
			f := newJ5Fixture(t, operation)
			f.branches.branches["feat/x"] = true
			f.runDelegation(t, "C-1")
			if got := f.detail(t, "C-1").Status; got != StatusVerifying {
				t.Fatalf("status = %s, want verifying", got)
			}
			res, err := f.verify(t, nil)
			if err != nil || len(res.Items) != 1 {
				t.Fatalf("VerifyAutoJ5 = %+v, %v", res, err)
			}
			if calls := f.checks.callLog(); len(calls) != 0 {
				t.Errorf("checks were queried for a challenge without a PR: %v", calls)
			}
			if in := f.stdin(t); !strings.Contains(in, "PR は無い") || !strings.Contains(in, "CI は調べていない") {
				t.Errorf("J5 input must say there is no PR:\n%s", in)
			}
		})
	}
}

func TestVerifyAutoJ5_ChecksFetchFailure_NotStartedAsUpstreamFetchFailed(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.err = errors.New("gh down")
	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed || len(f.j5Inputs) != 0 {
		t.Fatalf("result = %+v, want upstream_fetch_failed without invoking J5", res)
	}
	waiting, err := f.s.ListWaitingExternal(context.Background(), f.checks)
	if err != nil || len(waiting) != 0 {
		t.Errorf("a failed lookup must not be shown as waiting: %+v, %v", waiting, err)
	}
}

// --- 入力（AC-272〜274） ---

func TestVerifyAutoJ5_InputHasAllSections(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	cid, _ := parseChallengeID("C-1")
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			VALUES (?, 'HOLD-Q', 'in_progress', '2026-09-25T00:00:00.000Z', 'HOLD-A', '2026-09-25T00:01:00.000Z', 'alice')`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	in := f.stdin(t)
	for _, want := range []string{
		"DESC-t",                // 課題
		"DC-t",                  // 達成条件（課題のもの）
		"PLAN-BODY-t",           // 承認済みの計画の本文
		`"repo"`,                // 承認済みの計画の構造化した出力
		"summary",               // 直前の委譲の報告
		"feat/x",                // 照合の結果（ブランチ）
		"PR title 1", "develop", // PR の状態
		"HOLD-Q", "HOLD-A", // 保留の記録
	} {
		if !strings.Contains(in, want) {
			t.Errorf("J5 input lacks %q:\n%s", want, in)
		}
	}
	last := f.j5Inputs[len(f.j5Inputs)-1]
	if last.Judgment != JudgmentJ5 {
		t.Errorf("judgment = %s, want J5", last.Judgment)
	}
}

// attachSource は id の課題に取り込み元の対応（key の Issue）を付け、偽の上流に本文とコメントを置く。
func (f *j5Fixture) attachSource(t *testing.T, id, key string, repo string, number int, bodyAndComments ...string) {
	t.Helper()
	in := validBindingInput(key)
	th := simpleThread(bodyAndComments[0], bodyAndComments[1:]...)
	in.CommentsCount = th.Issue.Comments
	in.UpstreamUpdatedAt = th.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = key, repo, number, "open"
	f.upstream.threads[key] = th
}

func TestVerifyAutoJ5_InputHasTheUpstreamLatestState(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.attachSource(t, "C-1", "o/r#42", "o/r", 42, "UP-BODY", "UP-COMMENT")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	in := f.stdin(t)
	for _, want := range []string{"UP-BODY", "UP-COMMENT", "o/r#42"} {
		if !strings.Contains(in, want) {
			t.Errorf("J5 input lacks %q:\n%s", want, in)
		}
	}
	// 読んだ記録は付けない（上流の取得は J5 の入力のためだけ）。
	if got := f.queryString(t, `SELECT read_comments_count FROM source_binding WHERE challenge_id = 1`); got != "0" {
		t.Errorf("read_comments_count = %q, want it unchanged", got)
	}
}

func TestVerifyAutoJ5_UpstreamFetchFailure_NotStartedAndUnchanged(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.attachSource(t, "C-1", "o/r#42", "o/r", 42, "UP-BODY")
	f.upstream.threadErr = errors.New("gh down")
	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed || len(f.j5Runs(t)) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Errorf("status = %s", got)
	}
}

func TestVerifyAutoJ5_EmptyChallengeDoneCriteria_UsesThePlansDoneCriteria(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET done_criteria = '' WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	in := f.stdin(t)
	if !strings.Contains(in, "DONE-MARKER") || strings.Contains(in, "DC-t") {
		t.Errorf("done criteria must come from the plan when the challenge has none:\n%s", in)
	}
	if !strings.Contains(in, "承認済みの計画の達成条件") {
		t.Errorf("the origin of the criteria must be stated:\n%s", in)
	}
}

func TestVerifyAutoJ5_PlanWithoutSpec_StillVerifiesWithTheChallengesCriteria(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	id := f.newInProgress(t, "manual", "P0", "") // 人が登録した計画（構造化した出力なし）
	f.setStatus(t, id, "verifying")
	if _, err := f.verify(t, &id); err != nil {
		t.Fatal(err)
	}
	in := f.stdin(t)
	if !strings.Contains(in, "DC-manual") || !strings.Contains(in, "PLAN-BODY-manual") || !strings.Contains(in, "直前の委譲の run は無い") {
		t.Errorf("unexpected input:\n%s", in)
	}
}

// --- 起動の引数と記録（AC-219） ---

func TestVerifyAutoJ5_LaunchUsesTheJ5BudgetTimeoutSchemaAndRecordsThePlanVersion(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.JudgmentBudgetUSD.J5 = 4.25
	f.agent.TimeoutSec.Judgment = 321
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	got := f.j5Inputs[0]
	if got.MaxBudgetUSD != 4.25 || got.TimeoutSec != 321 || string(got.OutputSchema) != string(j5OutputSchema()) {
		t.Errorf("launch = budget %v timeout %d", got.MaxBudgetUSD, got.TimeoutSec)
	}
	runs := f.j5Runs(t)
	if len(runs) != 1 || runs[0].PlanVersion == nil || *runs[0].PlanVersion != 1 || runs[0].BudgetBucket != budgetBucketJudgment {
		t.Fatalf("runs = %+v, want one J5 run with plan_version 1", runs)
	}
}

func TestVerifyAutoJ5_CycleBudget_ExplicitIDErrorsAndOmittedIDNotStarted(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.JudgmentBudgetUSD.J5 = 5
	small := f.cycle(t, 4)
	id := "C-1"
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.input(small, &id)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	res, err := f.s.VerifyAutoJ5(context.Background(), f.input(small, nil))
	if err != nil || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Fatalf("result = %+v, %v, want cycle_budget", res, err)
	}
	if len(f.j5Runs(t)) != 0 {
		t.Error("no run may be recorded when the cycle budget cannot hold J5")
	}
}

func TestVerifyAutoJ5_RateLimitedCycleStartsNothing(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	jc := NewJudgmentCycle(f.cycle(t, 300))
	jc.rateLimited.Store(true)
	in := f.input(jc.cycleID, nil)
	in.Cycle = jc
	res, err := f.s.VerifyAutoJ5(context.Background(), in)
	if err != nil || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedRateLimited {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// --- 出力の検査と写像（AC-275〜277） ---

func TestVerifyAutoJ5_InvalidOutputs_BecomeInvalidOutputAndLeaveTheChallenge(t *testing.T) {
	cases := map[string]JudgmentLaunchOutput{
		"not_met with null feedback":   j5Out("not_met", nil, nil),
		"uncertain with null question": j5Out("uncertain", nil, nil),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJ5Fixture(t, "impl")
			f.delegateToVerifying(t, "C-1")
			before := f.detail(t, "C-1")
			f.j5.result = out
			res, err := f.verify(t, nil)
			if err != nil || len(res.Items) != 1 {
				t.Fatalf("VerifyAutoJ5 = %+v, %v", res, err)
			}
			if res.Items[0].Result != RunResultInvalidOutput || res.Items[0].Outcome != "" || res.Items[0].Status != nil {
				t.Errorf("item = %+v", res.Items[0])
			}
			if runs := f.j5Runs(t); len(runs) != 1 || runs[0].Result != RunResultInvalidOutput {
				t.Errorf("runs = %+v", runs)
			}
			if after := f.detail(t, "C-1"); after.Status != StatusVerifying || after.Version != before.Version {
				t.Errorf("challenge changed: %s v%d", after.Status, after.Version)
			}
		})
	}
}

func TestVerifyAutoJ5_NotSucceeded_DoesNotMap(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	res, _ := f.verify(t, nil)
	if len(res.Items) != 1 || res.Items[0].Result != RunResultErrored || res.Items[0].Status != nil {
		t.Fatalf("items = %+v", res.Items)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Errorf("status = %s", got)
	}
}

func (f *j5Fixture) lastActivity(t *testing.T, id string) (action, channel string, runID sql.NullInt64) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT action, channel, run_id FROM activity WHERE entity = 'challenge' AND entity_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&action, &channel, &runID)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestVerifyAutoJ5_Met_AwaitsCompletionApproval(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	it := res.Items[0]
	if it.Outcome != "met" || it.Status == nil || *it.Status != string(StatusAwaitingCompletionApproval) || it.Result != RunResultSucceeded {
		t.Errorf("item = %+v", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s", got)
	}
	action, channel, runID := f.lastActivity(t, "C-1")
	runs := f.j5Runs(t)
	if action != "verify_met" || channel != "invoker" || !runID.Valid || formatRunID(runID.Int64) != runs[0].ID {
		t.Errorf("activity = %s/%s/%v, want verify_met/invoker/%s", action, channel, runID, runs[0].ID)
	}
}

func TestVerifyAutoJ5_NotMet_ReturnsToInProgress(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Outcome != "not_met" || it.Status == nil || *it.Status != string(StatusInProgress) {
		t.Errorf("item = %+v", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Errorf("status = %s", got)
	}
	if action, channel, _ := f.lastActivity(t, "C-1"); action != "verify_not_met" || channel != "invoker" {
		t.Errorf("activity = %s/%s", action, channel)
	}
}

func TestVerifyAutoJ5_Uncertain_HoldsWithTheQuestionAndTheCauseRun(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5Out("uncertain", nil, "QUESTION-FOR-HUMAN")
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Outcome != "uncertain" || it.Status == nil || *it.Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", it)
	}
	if got := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`); got != "QUESTION-FOR-HUMAN" {
		t.Errorf("hold question = %q", got)
	}
	runs := f.j5Runs(t)
	if got := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`); got != strings.TrimPrefix(runs[0].ID, "R-") {
		t.Errorf("hold.run_id = %q, want the J5 run %s", got, runs[0].ID)
	}
}

func TestVerifyAutoJ5_StatusChangedWhileJ5Ran_IsNotMapped(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.onInvoke = func(JudgmentLaunchInput) { f.setStatus(t, "C-1", "awaiting_human") }
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if res.Items[0].Outcome != "met" || res.Items[0].Status != nil {
		t.Errorf("item = %+v, want the verdict recorded but no state written", res.Items[0])
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Errorf("status = %s", got)
	}
}

// --- 差し戻しの上限と --resume（AC-265〜267） ---

// notMetRound は C-1 を検証中から J5 の not_met で着手中へ戻し、次の委譲を起動する。
func (f *j5Fixture) notMetRound(t *testing.T, id string) {
	t.Helper()
	f.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	if _, err := f.verify(t, &id); err != nil {
		t.Fatal(err)
	}
}

func TestRework_NotMetResumesThePreviousDelegationSessionWithTheFeedback(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	first := f.delegateRuns(t)[0]
	f.notMetRound(t, "C-1")

	res := f.runDelegation(t, "C-1")
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("result = %+v", res)
	}
	got := f.lastLaunch(t)
	if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindRework || got.ResumeFeedback != "FEEDBACK-TEXT" {
		t.Errorf("launch = resume %v session %q kind %q feedback %q; want a rework resume of %q", got.IsResume, got.SessionID, got.ResumeKind, got.ResumeFeedback, first.SessionID)
	}
	if len(f.j3Inputs) != 1 {
		t.Errorf("J3 must not be called again for a rework resume (calls = %d)", len(f.j3Inputs))
	}
	runs := f.delegateRuns(t)
	if len(runs) != 2 || runs[1].ResumedFromRunID == nil || *runs[1].ResumedFromRunID != runs[0].ID {
		t.Errorf("runs = %+v, want the second run resumed from the first", runs)
	}
}

func TestRework_LimitIsDetectedInTheNextCycle_TwoStillLaunch(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.ReworkLimit = 3

	// 2 回の not_met では、次の委譲が起動される。
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1")
	f.notMetRound(t, "C-1")
	res := f.runDelegation(t, "C-1")
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("after 2 not_met: result = %+v, want a launch", res)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}

	// 3 回目の not_met の後の周では、委譲を起動せず人間対応待ちにする。
	launches := len(f.deleg.launched())
	f.notMetRound(t, "C-1")
	res = f.runDelegation(t, "C-1")
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedReworkLimit {
		t.Fatalf("result = %+v, want rework_limit", res)
	}
	if len(f.deleg.launched()) != launches {
		t.Error("a delegation was launched at the rework limit")
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1 ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(q, "差し戻し") || !strings.Contains(q, "回数: 3") || !strings.Contains(q, "上限: 3") {
		t.Errorf("hold question lacks the kind and count of the limit:\n%s", q)
	}
	j5 := f.j5Runs(t)
	if got := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1 ORDER BY id DESC LIMIT 1`); got != strings.TrimPrefix(j5[len(j5)-1].ID, "R-") {
		t.Errorf("hold.run_id = %q, want the last J5 run", got)
	}
}

func TestRework_AnswerAfterTheLimitStartsCountingAgain(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.ReworkLimit = 2
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1")
	f.notMetRound(t, "C-1")
	if res := f.runDelegation(t, "C-1"); len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedReworkLimit {
		t.Fatalf("result = %+v, want rework_limit", res)
	}

	f.answerLatestHold(t, "C-1", "go on")
	launches := len(f.deleg.launched())
	res := f.runDelegation(t, "C-1")
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || len(f.deleg.launched()) != launches+1 {
		t.Fatalf("after the answer: result = %+v, want a launch (the earlier not_met must not count)", res)
	}
	// 回答の後は新しいセッション（原因の run が J5 の保留）。
	if got := f.lastLaunch(t); got.IsResume {
		t.Errorf("a hold caused by J5 must restart in a new session, got resume %q", got.SessionID)
	}

	// 回答の後の not_met から数え直す: 1 回では上限（2）に達しない。
	f.notMetRound(t, "C-1")
	if res := f.runDelegation(t, "C-1"); len(res.NotStarted) != 0 {
		t.Fatalf("one not_met after the answer must not hit the limit: %+v", res)
	}
}

func TestRework_LimitCountsOnlyTheSamePlanVersion(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.ReworkLimit = 2
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1")
	f.notMetRound(t, "C-1")
	// 計画の版を替える（旧い版の not_met は新しい版の回数に数えない）。
	cid, _ := parseChallengeID("C-1")
	spec := planSpec(nil)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'P2', '2026-09-26T00:00:00.000Z', ?)`, cid, spec); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			VALUES (?, 'plan', 'approved', 2, 'alice', 'cli', 'tty_confirm', '2026-09-26T00:00:00.000Z')`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res := f.runDelegation(t, "C-1")
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("result = %+v, want a launch for the new plan version", res)
	}
	if got := f.lastLaunch(t); got.IsResume {
		t.Errorf("a new plan version must start a new session, got resume %v", got.ResumeKind)
	}
}

func TestRework_WithoutNotMetTheNormalFlowIsUnchanged(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	// J5 の uncertain の後に人間が回答して着手中へ戻っても、差し戻しの再開にはならない。
	f.j5.result = j5Out("uncertain", nil, "Q?")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	f.answerLatestHold(t, "C-1", "A")
	launches := len(f.deleg.launched())
	f.runDelegation(t, "C-1")
	if len(f.deleg.launched()) != launches+1 {
		t.Fatalf("the delegation after the answer was not launched")
	}
	if got := f.lastLaunch(t); got.ResumeKind == ResumeKindRework || got.IsResume {
		t.Errorf("an uncertain verdict must restart in a new session, got resume=%v kind=%q", got.IsResume, got.ResumeKind)
	}
}

func TestRework_LaunchFailedRetryStillCarriesTheFeedback(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	first := f.delegateRuns(t)[0]
	f.notMetRound(t, "C-1")

	f.deleg.result = failOut(RunResultLaunchFailed)
	f.runDelegation(t, "C-1")
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}
	f.runDelegation(t, "C-1")

	got := f.lastLaunch(t)
	if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindRework || got.ResumeFeedback != "FEEDBACK-TEXT" {
		t.Errorf("retry launch = resume %v session %q kind %q feedback %q", got.IsResume, got.SessionID, got.ResumeKind, got.ResumeFeedback)
	}
}

func TestVerifyAutoJ5_ExplicitID_ActiveRunWinsOverPendingChecks(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = prChecks("open", checkRunning)
	cid, _ := parseChallengeID("C-1")
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: f.s.currentTime(), StartedAt: f.s.currentTime(), MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id := "C-1"
	if _, err := f.verify(t, &id); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("err = %v, want ErrRunInProgress", err)
	}
}

func TestCheckCompleted_LegacyStatusesAndCheckRuns(t *testing.T) {
	for _, tc := range []struct {
		c    UpstreamCheck
		want bool
	}{
		{UpstreamCheck{Status: "completed"}, true},
		{UpstreamCheck{Status: "queued"}, false},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "success"}, true},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "failure"}, true},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "error"}, true},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "pending"}, false},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "weird"}, false},
	} {
		if got := checkCompleted(tc.c); got != tc.want {
			t.Errorf("checkCompleted(%+v) = %v, want %v", tc.c, got, tc.want)
		}
	}
}
