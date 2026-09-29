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

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画）の Store.PlanAutoJ2 を、実ストア（t.TempDir() の SQLite）・偽の判断の IF・
// 偽の上流の取得（UpstreamThreadSource）で検証する（AC-91〜115）。

// --- 偽の上流の取得 ---

type fakeUpstreamThreads struct {
	mu          sync.Mutex
	threads     map[string]UpstreamIssueThread // key: "<repo>#<番号>"
	refs        map[string]UpstreamIssue
	threadErr   error
	threadCalls []string
	refCalls    []string
}

func newFakeUpstreamThreads() *fakeUpstreamThreads {
	return &fakeUpstreamThreads{threads: map[string]UpstreamIssueThread{}, refs: map[string]UpstreamIssue{}}
}

func (f *fakeUpstreamThreads) GetIssueThread(_ context.Context, repo string, number int) (UpstreamIssueThread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%s#%d", repo, number)
	f.threadCalls = append(f.threadCalls, key)
	if f.threadErr != nil {
		return UpstreamIssueThread{}, f.threadErr
	}
	th, ok := f.threads[key]
	if !ok {
		return UpstreamIssueThread{}, ErrUpstreamIssueNotFound
	}
	return th, nil
}

func (f *fakeUpstreamThreads) GetReferencedIssue(_ context.Context, repo string, number int) (UpstreamIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%s#%d", repo, number)
	f.refCalls = append(f.refCalls, key)
	is, ok := f.refs[key]
	if !ok {
		return UpstreamIssue{}, ErrUpstreamIssueNotFound
	}
	return is, nil
}

func (f *fakeUpstreamThreads) calls() (threads, refs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.threadCalls...), append([]string(nil), f.refCalls...)
}

// --- フィクスチャ ---

type j2Fixture struct {
	s        *Store
	agent    *AgentDeclaration
	conn     *ConnectorsDeclaration
	upstream *fakeUpstreamThreads
}

func newJ2Fixture(t *testing.T) *j2Fixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return &j2Fixture{
		s:        s,
		agent:    newAgentDeclForJ1Test(t, s, "UNIQUE-POSITION-MARKER 担当範囲"),
		conn:     j2TestConnectors(t),
		upstream: newFakeUpstreamThreads(),
	}
}

func (f *j2Fixture) beginCycle(t *testing.T, budget float64) string {
	t.Helper()
	c, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "plan --auto", BudgetUSD: budget, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	return c.ID
}

// newClassified は分類済の課題を作る（priority が空なら未設定のまま分類できないので
// P2 にする）。
func (f *j2Fixture) newClassified(t *testing.T, title, priority string) *Challenge {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Description: "DESC-" + title})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if priority == "" {
		priority = "P2"
	}
	if _, err := f.s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: priority}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	return ch
}

// bind は課題に取り込み元の対応（external_key "o/r#<number>"）を付け、上流の偽に
// そのスレッドを置く。観測値は ObservedComments・ObservedUpdatedAt（ストアの値）、
// 上流の偽が返す値は thread.Issue.Comments・UpdatedAt。
func (f *j2Fixture) bind(t *testing.T, ch *Challenge, number int, thread UpstreamIssueThread) {
	t.Helper()
	key := fmt.Sprintf("o/r#%d", number)
	in := validBindingInput(key)
	in.CommentsCount = thread.Issue.Comments
	in.UpstreamUpdatedAt = thread.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest: %v", err)
	}
	thread.Issue.ExternalKey = key
	thread.Issue.Repo = "o/r"
	thread.Issue.Number = number
	if thread.Issue.State == "" {
		thread.Issue.State = "open"
	}
	f.upstream.threads[key] = thread
}

func simpleThread(body string, comments ...string) UpstreamIssueThread {
	th := UpstreamIssueThread{Issue: UpstreamIssue{Title: "UP-TITLE", Body: body, Comments: len(comments), UpdatedAt: "2026-09-25T09:00:00Z"}}
	for i, c := range comments {
		th.Comments = append(th.Comments, UpstreamComment{Author: "commenter", Body: c, CreatedAt: fmt.Sprintf("2026-09-25T0%d:00:00Z", i+1)})
	}
	return th
}

// out は plan の出力（hasSource なら flywheel/impl、なければ harness-repo/brief）。
func j2OutputFor(hasSource bool, mut func(m map[string]any)) []byte {
	return j2PlanOutput(func(m map[string]any) {
		if !hasSource {
			m["repo"] = "harness-repo"
			m["operation"] = "brief"
		}
		if mut != nil {
			mut(m)
		}
	})
}

func (f *j2Fixture) input(inv JudgmentInvoker, cycleID string, id *string) J2AutoInput {
	return J2AutoInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream, CycleID: cycleID}
}

func (f *j2Fixture) run(t *testing.T, inv JudgmentInvoker, id *string) (*J2AutoResult, error) {
	t.Helper()
	return f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), id))
}

func (f *j2Fixture) detail(t *testing.T, id string) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge(%s): %v", id, err)
	}
	return d
}

func (f *j2Fixture) runResults(t *testing.T, id string) []RunResult {
	t.Helper()
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	var out []RunResult
	for _, r := range runs {
		out = append(out, r.Result)
	}
	return out
}

func idPtr(id string) *string { return &id }

func allSectionsText(secs []JudgmentDataSection) string { return sectionsText(secs) }

// --- 計画の登録（AC-95〜103） ---

func TestPlanAutoJ2_Plan_RegistersPlanBodyAndSpec(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "plan me", "P1")
	out := j2OutputFor(false, func(m map[string]any) { m["budget_impl_usd"] = 41; m["budget_review_usd"] = 21 })
	var launched JudgmentLaunchInput
	inv := succeededInvoker(out)
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }

	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.Items) != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("result = %+v, want 1 item", res)
	}
	item := res.Items[0]
	if item.Result != RunResultSucceeded || item.Outcome != "plan" || item.Status == nil || *item.Status != string(StatusAwaitingPlanApproval) {
		t.Errorf("item = %+v", item)
	}

	d := f.detail(t, ch.ID)
	// AC-95: 計画承認待ちになり、計画が 1 版登録される。
	if d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", d.Status)
	}
	if len(d.Plans) != 1 || d.Plans[0].Version != 1 {
		t.Fatalf("plans = %+v, want exactly version 1", d.Plans)
	}
	for _, want := range []string{"harness-repo", "brief", "サイズ: M", "DONE-MARKER", "実装枠: 41 USD", "レビュー対応枠: 21 USD"} {
		if !strings.Contains(d.Plans[0].Body, want) {
			t.Errorf("plan body does not contain %q:\n%s", want, d.Plans[0].Body)
		}
	}
	// AC-102: spec は構造化した出力と一致する。
	if d.Plans[0].Spec == nil {
		t.Fatal("plan spec = nil, want the structured output")
	}
	var gotSpec, wantSpec any
	if err := json.Unmarshal([]byte(*d.Plans[0].Spec), &gotSpec); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	if err := json.Unmarshal(out, &wantSpec); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(gotSpec, wantSpec) {
		t.Errorf("spec = %s, want %s", *d.Plans[0].Spec, out)
	}
	// 予算は J2 の判断点の上限額（既定 5）、時間の上限は判断の時間の上限。
	if launched.MaxBudgetUSD != 5 || launched.Judgment != JudgmentJ2 {
		t.Errorf("launch = (judgment %s, budget %v), want (J2, 5)", launched.Judgment, launched.MaxBudgetUSD)
	}
	if len(launched.OutputSchema) == 0 {
		t.Error("the J2 output schema must be passed to the invoker")
	}
}

func TestPlanAutoJ2_NullBudgetsUseSizeDefaultsInThePlan(t *testing.T) {
	// AC-103: null の額はサイズの既定（S 30/25・M 50/30・L 100/40）。
	for _, c := range []struct{ size, impl, review string }{{"S", "30", "25"}, {"M", "50", "30"}, {"L", "100", "40"}} {
		t.Run(c.size, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "sized", "P1")
			inv := succeededInvoker(j2OutputFor(false, func(m map[string]any) { m["size"] = c.size }))
			if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
				t.Fatalf("PlanAutoJ2: %v", err)
			}
			body := f.detail(t, ch.ID).Plans[0].Body
			if !strings.Contains(body, "実装枠: "+c.impl+" USD") || !strings.Contains(body, "レビュー対応枠: "+c.review+" USD") {
				t.Errorf("plan body does not carry the %s defaults (%s/%s):\n%s", c.size, c.impl, c.review, body)
			}
		})
	}
}

func TestPlanAutoJ2_ActivityIsInvokerNoneWithRunID(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "activity", "P1")
	res, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	runInt, _ := parseRunID(res.Items[0].RunID)

	var channel, verification string
	var runID sql.NullInt64
	err = f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT channel, verification, run_id FROM activity WHERE action = 'plan' ORDER BY id DESC LIMIT 1`).Scan(&channel, &verification, &runID)
	})
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if channel != string(ChannelInvoker) || verification != string(VerificationNone) || !runID.Valid || runID.Int64 != runInt {
		t.Errorf("activity = (%q, %q, %v), want (invoker, none, %d)", channel, verification, runID, runInt)
	}
}

// --- J2 の入力（AC-91〜94） ---

func TestPlanAutoJ2_InputContainsUpstreamReferencesConnectorsHoldsAndDefaults(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "input", "P1")
	th := simpleThread("UP-BODY-MARKER 参照 #9 と o/r#10", "UP-COMMENT-1", "UP-COMMENT-2")
	f.bind(t, ch, 7, th)
	f.upstream.refs["o/r#9"] = UpstreamIssue{ExternalKey: "o/r#9", Title: "R9", Body: "REF9-BODY-MARKER", State: "open"}
	f.upstream.refs["o/r#10"] = UpstreamIssue{ExternalKey: "o/r#10", Title: "R10", Body: "REF10-BODY-MARKER", State: "open"}
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, ch.ID, HoldInput{Question: "PAST-QUESTION"}); err != nil {
		t.Fatalf("HoldChallenge: %v", err)
	}
	answerHold(t, f.s, ch.ID, "PAST-ANSWER")

	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	all := allSectionsText(launched.Sections)
	for _, want := range []string{
		"input", "DESC-input", "UNIQUE-POSITION-MARKER", // 課題・ポジション定義
		"PAST-QUESTION", "PAST-ANSWER", // 保留の記録
		"UP-BODY-MARKER", "UP-COMMENT-1", "UP-COMMENT-2", // AC-91: 取得した本文とコメント
		"REF9-BODY-MARKER", "REF10-BODY-MARKER", // 参照先の Issue
		"flywheel", "harness-repo", "cli-repo", "impl", "define", "brief", "cli-op", // AC-94: リポジトリと操作の id
		"サイズごとの既定の額", "実装 30・レビュー対応 25", "実装 100・レビュー対応 40", // 予算の既定
	} {
		if !strings.Contains(all, want) {
			t.Errorf("J2 input does not contain %q:\n%s", want, all)
		}
	}
	threads, refs := f.upstream.calls()
	if len(threads) != 1 || threads[0] != "o/r#7" {
		t.Errorf("thread calls = %v, want [o/r#7]", threads)
	}
	if len(refs) != 2 {
		t.Errorf("ref calls = %v, want 2", refs)
	}
}

func answerHold(t *testing.T, s *Store, id, answer string) {
	t.Helper()
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}, "s", id)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	preview, err := s.PrepareAnswer(context.Background(), id, answer)
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, Answer: answer, ExpectedVersion: preview.Version}, att); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
}

func TestPlanAutoJ2_OnlyTheFirstFiveReferencesAreFetched(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "many refs", "P1")
	f.bind(t, ch, 7, simpleThread("#11 #12 #13 #14 #15 #16 #17"))
	for i := 11; i <= 17; i++ {
		f.upstream.refs[fmt.Sprintf("o/r#%d", i)] = UpstreamIssue{ExternalKey: fmt.Sprintf("o/r#%d", i), Body: "b", State: "open"}
	}
	if _, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	_, refs := f.upstream.calls()
	if len(refs) != 5 || refs[0] != "o/r#11" || refs[4] != "o/r#15" {
		t.Errorf("ref calls = %v, want the first 5 in order of appearance", refs)
	}
}

func TestPlanAutoJ2_UpstreamFetchFailure_DoesNotStartJ2AndChangesNothing(t *testing.T) {
	// AC-91。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "fetch fails", "P1")
	f.bind(t, ch, 7, simpleThread("b"))
	f.upstream.threadErr = errors.New("gh: boom")
	before := f.detail(t, ch.ID)
	inv := succeededInvoker(j2OutputFor(true, nil))
	called := false
	inv.onInvoke = func(JudgmentLaunchInput) { called = true }

	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if called {
		t.Error("J2 must not be started when the upstream fetch fails")
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 ||
		res.NotStarted[0].ChallengeID != ch.ID || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed {
		t.Errorf("result = %+v, want one NotStarted{%s, upstream_fetch_failed}", res, ch.ID)
	}
	if res.NotStarted[0].Detail == "" {
		t.Error("NotStarted.Detail should carry the failure reason for the text output")
	}
	after := f.detail(t, ch.ID)
	if after.Status != StatusClassified || after.Version != before.Version || len(after.Plans) != 0 {
		t.Errorf("challenge changed: before=%+v after=%+v", before.Challenge, after.Challenge)
	}
	if got := f.runResults(t, ch.ID); len(got) != 0 {
		t.Errorf("runs = %v, want none (no run may be recorded when J2 is not started)", got)
	}
	// 上流の取得の失敗は、自動の対象でも同じ扱い。
	resAuto, err := f.run(t, inv, nil)
	if err != nil {
		t.Fatalf("PlanAutoJ2 (auto): %v", err)
	}
	if len(resAuto.NotStarted) != 1 || resAuto.NotStarted[0].Reason != NotStartedUpstreamFetchFailed {
		t.Errorf("auto result = %+v", resAuto)
	}
}

func TestPlanAutoJ2_ReferenceThatCannotBeFetchedDoesNotBlockJ2(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "dangling ref", "P1")
	f.bind(t, ch, 7, simpleThread("see #404"))
	res, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Errorf("result = %+v, want the plan registered", res)
	}
}

func TestPlanAutoJ2_ChallengeWithoutSourceNeverCallsUpstream(t *testing.T) {
	// AC-93。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no source", "P1")
	res, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	threads, refs := f.upstream.calls()
	if len(threads) != 0 || len(refs) != 0 {
		t.Errorf("upstream was called for a challenge without a source binding: threads=%v refs=%v", threads, refs)
	}
	if len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Errorf("result = %+v", res)
	}
}

func TestPlanAutoJ2_OverCapDropsOldestCommentsAndStatesIt(t *testing.T) {
	// AC-92。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "big", "P1")
	big := strings.Repeat("あ", 30000)
	f.bind(t, ch, 7, simpleThread("BODY", "OLDEST-"+big, "MIDDLE-"+big, "NEWEST-"+big))
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	all := allSectionsText(launched.Sections)
	if strings.Contains(all, "OLDEST-") || !strings.Contains(all, "NEWEST-") {
		t.Error("the oldest comment must be dropped and the newest kept")
	}
	if !strings.Contains(all, "省略した") {
		t.Error("the input must state that comments were dropped")
	}
}

// --- 不正な出力（AC-104〜108） ---

func TestPlanAutoJ2_InvalidOutput_LeavesChallengeClassified(t *testing.T) {
	cases := []struct {
		name      string
		hasSource bool
		mut       func(m map[string]any)
	}{
		{"impl budget above max_run_budget_usd (AC-104)", false, func(m map[string]any) { m["budget_impl_usd"] = 999 }},
		{"cross_repo missing", false, func(m map[string]any) { delete(m, "cross_repo") }},
		{"repo not declared (AC-105)", false, func(m map[string]any) { m["repo"] = "ghost" }},
		{"operation not in the connector (AC-106)", false, func(m map[string]any) { m["operation"] = "impl" }},
		{"cli connector operation (AC-107)", false, func(m map[string]any) { m["repo"] = "cli-repo"; m["operation"] = "cli-op" }},
		{"{issue_number} operation without source (AC-108)", false, func(m map[string]any) { m["repo"] = "flywheel"; m["operation"] = "impl" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "invalid", "P1")
			before := f.detail(t, ch.ID)
			res, err := f.run(t, succeededInvoker(j2OutputFor(c.hasSource, c.mut)), idPtr(ch.ID))
			if err != nil {
				t.Fatalf("PlanAutoJ2: %v", err)
			}
			if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput || res.Items[0].Outcome != "" || res.Items[0].Status != nil {
				t.Errorf("item = %+v, want invalid_output with no outcome and no status", res.Items)
			}
			if got := f.runResults(t, ch.ID); len(got) != 1 || got[0] != RunResultInvalidOutput {
				t.Errorf("run results = %v, want [invalid_output]", got)
			}
			after := f.detail(t, ch.ID)
			if after.Status != StatusClassified || after.Version != before.Version || len(after.Plans) != 0 {
				t.Errorf("challenge changed: before=%+v after=%+v plans=%v", before.Challenge, after.Challenge, after.Plans)
			}
		})
	}
}

// --- uncertain（AC-109） ---

func TestPlanAutoJ2_Uncertain_PutsChallengeOnHoldWithQuestionAndRunID(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "unsure", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	res, err := f.run(t, succeededInvoker([]byte(`{"verdict":"uncertain","question":"UNIQUE-J2-QUESTION"}`)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	item := res.Items[0]
	if item.Outcome != "uncertain" || item.Status == nil || *item.Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", item)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || d.Holds[0].Question != "UNIQUE-J2-QUESTION" || d.Holds[0].FromStatus != StatusClassified {
		t.Errorf("detail = status %s holds %+v", d.Status, d.Holds)
	}
	if len(d.Plans) != 0 {
		t.Errorf("plans = %v, want none", d.Plans)
	}
	runInt, _ := parseRunID(item.RunID)
	var holdRunID sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT run_id FROM hold WHERE challenge_id = ?`, mustChallengeInternalID(t, ch.ID)).Scan(&holdRunID)
	}); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	if !holdRunID.Valid || holdRunID.Int64 != runInt {
		t.Errorf("hold.run_id = %v, want %d", holdRunID, runInt)
	}
	// AC-113: uncertain の J2 は読んだ記録を付けない。
	if sb := d.SourceBinding; sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "" {
		t.Errorf("read values = (%d, %q), want unchanged (0, \"\")", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
}

// --- 読んだ記録（AC-110〜115） ---

func TestPlanAutoJ2_ReadRecord_UsesValuesFromTheFetchBeforeJ2(t *testing.T) {
	// AC-110・115。ストアの観測値 (5, 2026-09-26) は取得の値 (2, 2026-09-25T09) と違う。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "read", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1", "c2"))
	if _, _, err := updateSourceBindingForTest(f.s, ch.ID, time.Now(), updateSourceBindingInput{
		CommentsCount: intPtr(5), UpstreamUpdatedAt: strPtr("2026-09-26T00:00:00Z"),
	}); err != nil {
		t.Fatalf("updateSourceBindingForTest: %v", err)
	}
	res, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	sb := f.detail(t, ch.ID).SourceBinding
	if sb.ReadCommentsCount != 2 || sb.ReadUpstreamUpdatedAt != "2026-09-25T09:00:00Z" {
		t.Errorf("read values = (%d, %q), want the fetched values (2, 2026-09-25T09:00:00Z)", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
	if sb.CommentsCount != 5 || sb.UpstreamUpdatedAt != "2026-09-26T00:00:00Z" {
		t.Errorf("observation = (%d, %q), want it untouched (5, 2026-09-26T00:00:00Z)", sb.CommentsCount, sb.UpstreamUpdatedAt)
	}

	// AC-115: 作業ログは upstream_read・経路 invoker・J2 の run の ID。
	runInt, _ := parseRunID(res.Items[0].RunID)
	var action, channel, verification string
	var runID sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT action, channel, verification, run_id FROM activity WHERE action = 'upstream_read' ORDER BY id DESC LIMIT 1`).
			Scan(&action, &channel, &verification, &runID)
	}); err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if channel != string(ChannelInvoker) || verification != string(VerificationNone) || !runID.Valid || runID.Int64 != runInt {
		t.Errorf("upstream_read activity = (%q, %q, %v), want (invoker, none, %d)", channel, verification, runID, runInt)
	}
}

func TestPlanAutoJ2_ReadRecord_IsNotAdvancedByAnIngestDuringJ2(t *testing.T) {
	// AC-111: 取得の後 J2 の終了までに観測値が進んでも、read は取得の値のまま。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "race", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		// J2 が走っている間に取り込みが進んだ（コメントが 1 件増え、更新日時が進んだ）。
		if _, _, err := updateSourceBindingForTest(f.s, ch.ID, time.Now(), updateSourceBindingInput{
			CommentsCount: intPtr(2), UpstreamUpdatedAt: strPtr("2026-09-27T00:00:00Z"),
		}); err != nil {
			t.Errorf("updateSourceBindingForTest: %v", err)
		}
	}
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	sb := f.detail(t, ch.ID).SourceBinding
	if sb.ReadCommentsCount != 1 || sb.ReadUpstreamUpdatedAt != "2026-09-25T09:00:00Z" {
		t.Errorf("read values = (%d, %q), want the fetched values (1, 2026-09-25T09:00:00Z)", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
	if sb.CommentsCount != 2 || sb.UpstreamUpdatedAt != "2026-09-27T00:00:00Z" {
		t.Errorf("observation = (%d, %q), want the advanced values", sb.CommentsCount, sb.UpstreamUpdatedAt)
	}
	ov, err := f.s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if d := findDiscrepancy(ov.Discrepancies, ch.ID); d == nil {
		t.Errorf("an unread update must remain in the overview after J2 (the comment posted during J2 was not read)")
	}
}

// 読んだ記録の書き込みに失敗しても、登録済みの計画の結果は失わない（読んだ記録が
// 付かないだけ）。失敗の理由は item の Note に残る。
func TestPlanAutoJ2_ReadRecordFailure_KeepsThePlanAndNotesIt(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "read record fails", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	cid := mustChallengeInternalID(t, ch.ID)
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		// J2 の実行中に対応の記録が無くなった（読んだ記録を付けられない状況）。
		err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM source_binding WHERE challenge_id = ?`, cid)
			return err
		})
		if err != nil {
			t.Errorf("delete source_binding: %v", err)
		}
	}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2 must not fail when only the read record cannot be written: %v", err)
	}
	item := res.Items[0]
	if item.Outcome != "plan" || item.Status == nil || *item.Status != string(StatusAwaitingPlanApproval) {
		t.Errorf("item = %+v, want the registered plan reported", item)
	}
	if !strings.Contains(item.Note, "読んだ記録を付けられなかった") {
		t.Errorf("Note = %q, want the reason the read record was not written", item.Note)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 {
		t.Errorf("status = %s plans = %v, want the plan kept", d.Status, d.Plans)
	}
}

func TestPlanAutoJ2_ReadRecord_NotSetWhenACommentWasDropped(t *testing.T) {
	// AC-112。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "dropped", "P1")
	big := strings.Repeat("あ", 40000)
	f.bind(t, ch, 7, simpleThread("b", big, big, big))
	if _, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s, want the plan registered even though comments were dropped", d.Status)
	}
	if sb := d.SourceBinding; sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "" {
		t.Errorf("read values = (%d, %q), want unchanged (0, \"\") when a comment was dropped", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
}

func TestPlanAutoJ2_ReadRecord_NotSetWhenRunIsNotSucceeded(t *testing.T) {
	// AC-114。
	for _, result := range []RunResult{RunResultErrored, RunResultTimedOut, RunResultMalformed, RunResultBudgetExhausted, RunResultInvalidOutput} {
		t.Run(string(result), func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "failed run", "P1")
			f.bind(t, ch, 7, simpleThread("b", "c1"))
			inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: result, StructuredOutput: j2OutputFor(true, nil)}}
			res, err := f.run(t, inv, idPtr(ch.ID))
			if err != nil {
				t.Fatalf("PlanAutoJ2: %v", err)
			}
			if res.Items[0].Result != result || res.Items[0].Outcome != "" {
				t.Errorf("item = %+v, want result %s with no outcome", res.Items[0], result)
			}
			d := f.detail(t, ch.ID)
			if d.Status != StatusClassified || len(d.Plans) != 0 {
				t.Errorf("challenge changed by a %s run: status=%s plans=%v", result, d.Status, d.Plans)
			}
			if sb := d.SourceBinding; sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "" {
				t.Errorf("read values = (%d, %q), want unchanged", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
			}
		})
	}
}

func TestPlanAutoJ2_ReadRecord_NotSetWhenTheOutputIsInvalid(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "invalid with source", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	if _, err := f.run(t, succeededInvoker(j2OutputFor(true, func(m map[string]any) { m["repo"] = "ghost" })), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if sb := f.detail(t, ch.ID).SourceBinding; sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "" {
		t.Errorf("read values = (%d, %q), want unchanged", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
}

// --- 読み直した状態が遷移元でなくなっていたとき ---

func TestPlanAutoJ2_ChallengeLeftClassifiedDuringJ2_OnlyTheRunOutputIsKept(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "moved", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		// J2 の実行中に、人間が先に人間対応待ちへ進めた。
		if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, ch.ID, HoldInput{Question: "human moved it"}); err != nil {
			t.Errorf("HoldChallenge: %v", err)
		}
	}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if res.Items[0].Status != nil || res.Items[0].Outcome != "plan" || res.Items[0].Result != RunResultSucceeded {
		t.Errorf("item = %+v, want plan outcome, succeeded, and no status (not mapped)", res.Items[0])
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Plans) != 0 {
		t.Errorf("status = %s plans = %v, want awaiting_human with no plan", d.Status, d.Plans)
	}
	if sb := d.SourceBinding; sb.ReadCommentsCount != 0 {
		t.Errorf("read record was attached although no plan was registered: %+v", sb)
	}
	// run の出力は残る。
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &ch.ID})
	if err != nil || len(runs) != 1 || runs[0].Result != RunResultSucceeded {
		t.Errorf("runs = %+v err = %v, want the succeeded run kept", runs, err)
	}
}

func TestPlanAutoJ2_ChallengeAlreadyHasAPlan_IsNotRevisedByJ2(t *testing.T) {
	// T4（計画の改訂）を J2 が行わない: 実行中に人が計画を登録した課題へ写さない。
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "human planned", "P1")
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		if _, _, err := f.s.PlanChallenge(context.Background(), ChannelCLI, ch.ID, PlanInput{Body: "HUMAN-PLAN"}); err != nil {
			t.Errorf("PlanChallenge: %v", err)
		}
	}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if res.Items[0].Status != nil {
		t.Errorf("item = %+v, want not mapped", res.Items[0])
	}
	d := f.detail(t, ch.ID)
	if len(d.Plans) != 1 || d.Plans[0].Body != "HUMAN-PLAN" || d.Plans[0].Spec != nil {
		t.Errorf("plans = %+v, want only the human's plan (spec NULL)", d.Plans)
	}
}

// --- 対象の選び方 ---

func TestPlanAutoJ2_AutoTargets_OnlyClassifiedInPriorityThenIDOrder(t *testing.T) {
	f := newJ2Fixture(t)
	p2a := f.newClassified(t, "p2-first", "P2")
	p0 := f.newClassified(t, "p0", "P0")
	p1 := f.newClassified(t, "p1", "P1")
	p2b := f.newClassified(t, "p2-second", "P2")
	// 対象外: 未分類・計画承認待ち・人間対応待ち。
	if _, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "unclassified"}); err != nil {
		t.Fatal(err)
	}
	planned := f.newClassified(t, "planned", "P0")
	if _, _, err := f.s.PlanChallenge(context.Background(), ChannelCLI, planned.ID, PlanInput{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	held := f.newClassified(t, "held", "P0")
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, held.ID, HoldInput{Question: "q"}); err != nil {
		t.Fatal(err)
	}

	var order []string
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) {
		for _, sec := range in.Sections {
			if sec.Label == "課題" {
				title := strings.TrimPrefix(strings.SplitN(sec.Content, "\n", 2)[0], "タイトル: ")
				order = append(order, title)
			}
		}
	}
	res, err := f.run(t, inv, nil)
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	want := []string{"p0", "p1", "p2-first", "p2-second"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("processed order = %v, want %v", order, want)
	}
	if len(res.Items) != 4 {
		t.Errorf("items = %d, want 4", len(res.Items))
	}
	for _, ch := range []*Challenge{p2a, p0, p1, p2b} {
		if st := f.detail(t, ch.ID).Status; st != StatusAwaitingPlanApproval {
			t.Errorf("%s status = %s, want awaiting_plan_approval", ch.ID, st)
		}
	}
	// 計画承認待ち・人間対応待ちだった課題は変わらない。
	if st := f.detail(t, held.ID).Status; st != StatusAwaitingHuman {
		t.Errorf("held status = %s", st)
	}
}

func TestPlanAutoJ2_AutoTargets_ExcludeClosedMissingOutOfPolicyAndActiveRun(t *testing.T) {
	f := newJ2Fixture(t)
	target := f.newClassified(t, "eligible", "P1")
	excluded := map[string]struct {
		upstream upstreamState
		policy   policyState
	}{
		"closed":        {upstreamStateClosed, policyStateInPolicy},
		"missing":       {upstreamStateMissing, policyStateInPolicy},
		"out_of_policy": {upstreamStateOpen, policyStateOutOfPolicy},
	}
	var excludedIDs []string
	i := 100
	for name, e := range excluded {
		ch := f.newClassified(t, "excluded-"+name, "P0")
		in := validBindingInput(fmt.Sprintf("o/r#%d", i))
		in.UpstreamState, in.PolicyState = e.upstream, e.policy
		if _, err := createSourceBindingForTest(f.s, ch.ID, time.Now(), in); err != nil {
			t.Fatal(err)
		}
		i++
		excludedIDs = append(excludedIDs, ch.ID)
	}
	busy := f.newClassified(t, "busy", "P0")
	insertOpenRunForTest(t, f.s, busy.ID, JudgmentJ2)

	res, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), nil)
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != target.ID {
		t.Errorf("items = %+v, want only %s", res.Items, target.ID)
	}
	for _, id := range append(excludedIDs, busy.ID) {
		if st := f.detail(t, id).Status; st != StatusClassified {
			t.Errorf("%s status = %s, want it untouched (classified)", id, st)
		}
	}
	threads, _ := f.upstream.calls()
	if len(threads) != 0 {
		t.Errorf("upstream was fetched for excluded targets: %v", threads)
	}
}

func TestPlanAutoJ2_ByID_DoesNotApplyPolicyExclusion(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "closed but explicit", "P1")
	in := validBindingInput("o/r#7")
	in.UpstreamState = upstreamStateClosed
	if _, err := createSourceBindingForTest(f.s, ch.ID, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	f.upstream.threads["o/r#7"] = UpstreamIssueThread{Issue: UpstreamIssue{ExternalKey: "o/r#7", Repo: "o/r", Number: 7, State: "closed"}}
	res, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Errorf("result = %+v, want the closed challenge planned when named explicitly", res)
	}
}

func TestPlanAutoJ2_ByID_RejectsNonClassifiedAndActiveRun(t *testing.T) {
	f := newJ2Fixture(t)
	unclassified, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), idPtr(unclassified.ID)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("unclassified: error = %v, want ErrInvalidTransition", err)
	}
	planned := f.newClassified(t, "planned", "P1")
	if _, _, err := f.s.PlanChallenge(context.Background(), ChannelCLI, planned.ID, PlanInput{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), idPtr(planned.ID)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("awaiting_plan_approval: error = %v, want ErrInvalidTransition (J2 must not revise a plan)", err)
	}
	if _, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), idPtr("C-999")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: error = %v, want ErrNotFound", err)
	}

	busy := f.newClassified(t, "busy", "P1")
	f.bind(t, busy, 7, simpleThread("b"))
	insertOpenRunForTest(t, f.s, busy.ID, JudgmentJ2)
	if _, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(busy.ID)); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("active run: error = %v, want ErrRunInProgress", err)
	}
	threads, _ := f.upstream.calls()
	if len(threads) != 0 {
		t.Errorf("upstream was fetched although a run was in progress: %v", threads)
	}
}

// --- 周の枠超過・上限 ---

func TestPlanAutoJ2_RateLimit_StopsRemainingTargetsWithoutFetching(t *testing.T) {
	f := newJ2Fixture(t)
	first := f.newClassified(t, "first", "P0")
	second := f.newClassified(t, "second", "P1")
	f.bind(t, second, 7, simpleThread("b"))
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}}

	res, err := f.run(t, inv, nil)
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != first.ID {
		t.Errorf("items = %+v, want only the first", res.Items)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != second.ID || res.NotStarted[0].Reason != NotStartedRateLimited {
		t.Errorf("not started = %+v, want [%s rate_limited]", res.NotStarted, second.ID)
	}
	threads, _ := f.upstream.calls()
	if len(threads) != 0 {
		t.Errorf("upstream fetched after the rate limit: %v", threads)
	}
	if st := f.detail(t, second.ID).Status; st != StatusClassified {
		t.Errorf("second status = %s", st)
	}
}

// 周の上限を使い切っているときは、上流の取得（gh の GET）をせずに cycle_budget で
// 起動しない（取得の失敗より周の上限が先に判定される）。
func TestPlanAutoJ2_CycleBudgetExhausted_DoesNotFetchUpstream(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "budget mapped", "P1")
	f.bind(t, ch, 7, simpleThread("b"))
	f.upstream.threadErr = errors.New("gh: would fail")
	inv := succeededInvoker(j2OutputFor(true, nil))

	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 2), nil))
	if err != nil {
		t.Fatalf("PlanAutoJ2 (auto): %v", err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("not started = %+v, want cycle_budget (not upstream_fetch_failed)", res.NotStarted)
	}
	if threads, refs := f.upstream.calls(); len(threads) != 0 || len(refs) != 0 {
		t.Errorf("upstream was fetched although the cycle budget was exhausted: %v %v", threads, refs)
	}
	if _, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 2), idPtr(ch.ID))); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("by id: error = %v, want ErrBudgetExceeded", err)
	}
	if threads, _ := f.upstream.calls(); len(threads) != 0 {
		t.Errorf("upstream was fetched by id although the cycle budget was exhausted: %v", threads)
	}
}

func TestPlanAutoJ2_CycleBudget_AutoNotStartedByIDError(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "budget", "P1")
	inv := succeededInvoker(j2OutputFor(false, nil))
	// 周の上限 2・J2 の上限 5 → 起動できない。
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 2), nil))
	if err != nil {
		t.Fatalf("PlanAutoJ2 (auto): %v", err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("auto: not started = %+v, want cycle_budget", res.NotStarted)
	}
	_, err = f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 2), idPtr(ch.ID)))
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("by id: error = %v, want ErrBudgetExceeded", err)
	}
	if st := f.detail(t, ch.ID).Status; st != StatusClassified {
		t.Errorf("status = %s, want unchanged", st)
	}
}

// --- 入力の検査・宣言の不備 ---

func TestPlanAutoJ2_RejectsIncompleteInput(t *testing.T) {
	f := newJ2Fixture(t)
	inv := succeededInvoker(j2OutputFor(false, nil))
	cycle := f.beginCycle(t, 300)
	for name, in := range map[string]J2AutoInput{
		"no agent decl": {ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream, CycleID: cycle},
		"no conn decl":  {AgentDecl: f.agent, Invoker: inv, Upstream: f.upstream, CycleID: cycle},
		"no invoker":    {AgentDecl: f.agent, ConnDecl: f.conn, Upstream: f.upstream, CycleID: cycle},
		"no upstream":   {AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, CycleID: cycle},
		"no cycle":      {AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream},
	} {
		if _, err := f.s.PlanAutoJ2(context.Background(), in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", name, err)
		}
	}
}

func TestPlanAutoJ2_MissingPositionFile_IsConfigInvalidAndStartsNothing(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no position", "P1")
	f.agent.PositionFile = "no-such-position.md"
	called := false
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(JudgmentLaunchInput) { called = true }
	_, err := f.run(t, inv, idPtr(ch.ID))
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("error = %v, want ErrConfigInvalid", err)
	}
	if called {
		t.Error("J2 must not start without the position file")
	}
}

func TestPlanAutoJ2_InvokerUnavailable_ChangesNothing(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no claude", "P1")
	f.bind(t, ch, 7, simpleThread("b"))
	wantErr := errors.New("claude not found")
	inv := &fakeJudgmentInvoker{availableErr: wantErr}
	before := f.detail(t, ch.ID)
	_, err := f.run(t, inv, idPtr(ch.ID))
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want to wrap the Available error", err)
	}
	after := f.detail(t, ch.ID)
	if after.Version != before.Version || after.Status != before.Status {
		t.Errorf("challenge changed")
	}
}

// insertOpenRunForTest は challengeDisplayID の課題に、終了していない run を 1 件入れる。
func insertOpenRunForTest(t *testing.T, s *Store, challengeDisplayID string, judgment judgmentPoint) {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		_, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgment, ChallengeVersion: 1, SessionID: "22222222-2222-2222-2222-222222222222",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		return err
	})
	if err != nil {
		t.Fatalf("insertRun: %v", err)
	}
}

func intPtr(i int) *int { return &i }
