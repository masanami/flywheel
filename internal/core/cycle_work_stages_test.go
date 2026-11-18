package core

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、サイクルを回すと進むものの数え（CountCycleWork）を、Issue #777 の受入基準の 1 つずつに照らして確かめる。
// 「サイクルが始まる／始まらない」（AC-204〜221）は、数えの合計が 1 以上か 0 かで決まる（0 なら始めず、1 以上なら始めることは
// internal/cli・internal/server のテストが確かめる）ので、ここでは「その状態だけがあるワークスペース」の段ごとの件数と合計を確かめる。

// cycleWorkCase は 1 つの受入基準の状態を作り、数える入力（ストア・宣言）と、数えの後で確かめること（after。起動しなかった理由が
// 受入基準の言う理由であること等。無ければ nil）を返す。
type cycleWorkCase struct {
	name  string
	want  CycleWorkStages
	build func(t *testing.T) (s *Store, agent *AgentDeclaration, conn *ConnectorsDeclaration, after func(t *testing.T))
}

// doneChallengeForCycleWork は、どの段の対象にもならない課題（完了。分類前の発生を持たない）を 1 件作る。
func doneChallengeForCycleWork(t *testing.T, s *Store) int64 {
	t.Helper()
	return mChallenge(t, s, "done")
}

// openContractRunForCycleWork は、課題 cid に終わっていない契約の実行（contract_run。ended_at が NULL）を 1 本置く。
func openContractRunForCycleWork(t *testing.T, s *Store, cid int64) {
	t.Helper()
	now := formatTimestamp(time.Now())
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (?, 'contract', 1, 's', 1, 'h', ?, ?, ?, 'succeeded', 1000000, 'impl')`, cid, now, now, now)
		if err != nil {
			return err
		}
		rid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
			VALUES (?, 'tool', 'ext-1', ?, 'running', 's', 1000000, 'x', 'x')`, cid, rid)
		return err
	})
}

// staleRunForCycleWork は、サイクルの始めの片付けの対象になる実行（結果が無く、心拍が古く、プロセスが生きていない run）を 1 つ置く。
func staleRunForCycleWork(t *testing.T, s *Store, cid int64) {
	t.Helper()
	old := time.Now().Add(-time.Hour).UTC()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111", PID: deadPID(t), Host: host,
		HeartbeatAt: old, StartedAt: old, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatal(err)
	}
}

// classifiedChallengeForCycleWork は分類済（classified）の課題を 1 件作る。
func classifiedChallengeForCycleWork(t *testing.T, s *Store) string {
	t.Helper()
	ch := mustCreateChallenge(t, s, "classified one")
	if _, err := s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	return ch.ID
}

// notStartedReasonAfter は、数えの後に委譲の段を実際に実行し、課題 id の起動しなかった理由が want であることを確かめる
// （受入基準の「起動しなかった理由が〜になる」を、作った状態が満たしていることの確認）。
func notStartedReasonAfter(f *delegateFixture, id string, want NotStartedReason) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		res := mustRunResult(t, f, nil)
		for _, ns := range res.NotStarted {
			if ns.ChallengeID == id && ns.Reason == want {
				return
			}
		}
		t.Fatalf("not_started = %+v, want %s with %s", res.NotStarted, id, want)
	}
}

func cycleWorkCases() []cycleWorkCase {
	return []cycleWorkCase{
		{
			// AC-209・AC-261: 実行できる承認済みの反映が 1 件あるだけ → release 1。
			name: "AC-209_AC-261_承認済みの反映が1件_release=1", want: CycleWorkStages{Release: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				cid := doneChallengeForCycleWork(t, f.s)
				coretest.InsertApprovedRelease(t, f.s.workspace, int(cid), "https://github.com/o/r/pull/1", "a1b2c3", execTS(0))
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-254: 未分類の課題が 2 件 → classify 2（AC-201 の「未分類が 1 件で始まる」の件数の側）。
			name: "AC-254_未分類の課題が2件_classify=2", want: CycleWorkStages{Classify: 2},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				mustCreateChallenge(t, f.s, "one")
				mustCreateChallenge(t, f.s, "two")
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-204: 分類済の課題が 1 件あるだけ → plan 1。
			name: "AC-204_分類済の課題が1件_plan=1", want: CycleWorkStages{Plan: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				classifiedChallengeForCycleWork(t, f.s)
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-213・AC-259: connectors.json が無ければ、分類済の課題があっても plan 0（合計 0）。
			name: "AC-213_AC-259_connectors無しの分類済の課題_plan=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				classifiedChallengeForCycleWork(t, f.s)
				return f.s, f.agent, nil, nil
			},
		},
		{
			// AC-205: 着手中で、起動できるタスクを持つ課題が 1 件あるだけ → run 1。
			name: "AC-205_起動できるタスクを持つ着手中の課題_run=1", want: CycleWorkStages{Run: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				f.newInProgress(t, "t", "P1", planSpec(nil))
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-206: 終わっていない契約の実行が 1 本あるだけ → run 1。
			name: "AC-206_終わっていない契約の実行が1本_run=1", want: CycleWorkStages{Run: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				openContractRunForCycleWork(t, f.s, doneChallengeForCycleWork(t, f.s))
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-255: 委譲の対象の課題が 1 件と、終わっていない契約の実行が 1 本 → run 2。
			name: "AC-255_委譲の対象1件と契約の実行1本_run=2", want: CycleWorkStages{Run: 2},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				f.newInProgress(t, "t", "P1", planSpec(nil))
				openContractRunForCycleWork(t, f.s, doneChallengeForCycleWork(t, f.s))
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-207・AC-262: 検証中の課題が 1 件あるだけ → verify 1。
			name: "AC-207_AC-262_検証中の課題が1件_verify=1", want: CycleWorkStages{Verify: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newJ5Fixture(t, "impl")
				f.delegateToVerifying(t, "C-1")
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-208・AC-263: 学びの対象の課題（完了で、原因が未分類の発生を持つ）が 1 件あるだけ → learn 1。
			name: "AC-208_AC-263_学びの対象の課題が1件_learn=1", want: CycleWorkStages{Learn: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				j6 := newJ6FixtureOn(t, f.s)
				j6.occ(t, j6.challenge(t, StatusDone), "completion_rejected", "bad", nil)
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-215・AC-257: 改修提案の起動の件数がしきい値に達している（失敗の上限には達していない）→ improve 1。
			name: "AC-215_AC-257_改修提案の起動の件数がしきい値_improve=1", want: CycleWorkStages{Improve: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				f.agent.ImproveThreshold = 10
				cid := doneChallengeForCycleWork(t, f.s)
				for i := 0; i < 10; i++ {
					j8Occ(t, f.s, cid, j8Row{})
				}
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-216・AC-258: 改修提案の起動の件数がしきい値に満たない → improve 0（合計 0）。
			name: "AC-216_AC-258_改修提案の起動の件数がしきい値未満_improve=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				f.agent.ImproveThreshold = 10
				cid := doneChallengeForCycleWork(t, f.s)
				for i := 0; i < 9; i++ {
					j8Occ(t, f.s, cid, j8Row{})
				}
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-214・AC-260: 片付けの対象になる実行が 1 つあるだけ → cleanup 1。
			name: "AC-214_AC-260_片付けの対象の実行が1つ_cleanup=1", want: CycleWorkStages{Cleanup: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				staleRunForCycleWork(t, f.s, doneChallengeForCycleWork(t, f.s))
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-210: 計画の承認待ちの課題があるだけ → 全段 0。
			name: "AC-210_計画の承認待ちの課題だけ_合計0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				ch := mustCreateChallenge(t, f.s, "waiting")
				cid, _ := parseChallengeID(ch.ID)
				writeForTest(t, f.s, func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE challenge SET status = ?, version = version + 1 WHERE id = ?`, string(StatusAwaitingPlanApproval), cid)
					return err
				})
				return f.s, f.agent, f.conn, nil
			},
		},
		{
			// AC-217: 予算の枠が尽きた（run_budget）着手中の課題があるだけ → run 0。
			name: "AC-217_run_budgetの着手中の課題だけ_run=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				id := f.newInProgress(t, "lowbudget", "P1", planSpec(func(m map[string]any) { m["budget_impl_usd"] = 0.5 }))
				return f.s, f.agent, f.conn, notStartedReasonAfter(f, id, NotStartedRunBudget)
			},
		},
		{
			// AC-218・AC-256: 統合先を使う（integration_unavailable）着手中の課題があるだけ → run 0。
			name: "AC-218_AC-256_integration_unavailableの着手中の課題だけ_run=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				id := f.newInProgress(t, "one", "P1", planSpec(nil))
				cid, _ := parseChallengeID(id)
				yes := true
				writeForTest(t, f.s, func(tx *sql.Tx) error { return setChallengeUsesIntegration(context.Background(), tx, cid, &yes) })
				return f.s, f.agent, f.conn, notStartedReasonAfter(f, id, NotStartedIntegrationUnavailable)
			},
		},
		{
			// AC-219: 記録した前提が終わっていない（dependency）着手中の課題があるだけ → run 0。
			// 前提の側の課題は、どの段の対象にもならない状態（計画の承認待ち）にして、「だけ」を保つ。
			name: "AC-219_dependencyの着手中の課題だけ_run=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				id := f.newInProgress(t, "t", "P1", planSpec(nil))
				dep := f.newDepTarget(t)
				writeForTest(t, f.s, func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE challenge SET status = ?, version = version + 1 WHERE id = ?`, string(StatusAwaitingPlanApproval), dep)
					return err
				})
				f.dependOn(t, id, dep)
				return f.s, f.agent, f.conn, func(t *testing.T) {
					t.Helper()
					assertDependencyNotStarted(t, mustRunResult(t, f, nil), id, formatChallengeID(dep))
				}
			},
		},
		{
			// AC-220: 起動できるタスクが無い（no_tasks）着手中の課題があるだけ → run 0。
			name: "AC-220_no_tasksの着手中の課題だけ_run=0", want: CycleWorkStages{},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newDelegateFixture(t)
				id := f.newInProgress(t, "old", "P1", oldShapePlanSpec)
				return f.s, f.agent, f.conn, notStartedReasonAfter(f, id, NotStartedNoTasks)
			},
		},
		{
			// AC-221: スロットに空きが無い（slot_unavailable）着手中の課題があるだけ → run 1（時間がたてば解ける理由は外さない）。
			name: "AC-221_slot_unavailableの着手中の課題だけ_run=1", want: CycleWorkStages{Run: 1},
			build: func(t *testing.T) (*Store, *AgentDeclaration, *ConnectorsDeclaration, func(*testing.T)) {
				f := newPlanFixture(t)
				f.ensure(t, "solo")
				f.setSlotState(t, "s1", slotStateNeedsAttention)
				id := f.candidate(t, "solo", "P1", 11)
				return f.s, f.agent, f.conn, func(t *testing.T) {
					t.Helper()
					res := f.mustRunPlan(t)
					if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
						t.Fatalf("not_started = %+v, want %s with slot_unavailable", res.NotStarted, id)
					}
				}
			},
		},
	}
}

// 受入基準の状態ごとに、段ごとの件数と合計を確かめる（AC-204〜210・213〜221・254〜263）。合計が段の件数の合計であること（AC-264）と、
// 数えがストアを書かないこと（数えの前後で全テーブルの内容が同じ）も、全ての状態で確かめる。
func TestCountCycleWork_StagesByAcceptanceCriterion(t *testing.T) {
	t.Parallel()
	for _, tc := range cycleWorkCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, agent, conn, after := tc.build(t)
			before := snapshotTables(t, s)
			w, err := s.CountCycleWork(context.Background(), agent, conn)
			if err != nil {
				t.Fatalf("CountCycleWork: %v", err)
			}
			if w.Stages != tc.want {
				t.Errorf("stages = %+v, want %+v", w.Stages, tc.want)
			}
			st := tc.want
			if sum := st.Release + st.Classify + st.Plan + st.Run + st.Verify + st.Learn + st.Improve + st.Cleanup; w.Total != sum {
				t.Errorf("total = %d, want %d (the sum of the stages, AC-264)", w.Total, sum)
			}
			if got := snapshotTables(t, s); !reflect.DeepEqual(got, before) {
				t.Errorf("counting changed the store:\nbefore: %s\nafter:  %s", before, got)
			}
			if after != nil {
				after(t)
			}
		})
	}
}

// AC-211: 対応する課題の無い、開いている Issue が上流にあるだけでは 0 件（数えは上流を読まない。CountCycleWork は上流を読む口を
// 引数に取らないので、上流に何があっても結果は変わらない。ここでは、偽の上流に開いている Issue を置いた取り込み元つきのストアで 0 を確かめる）。
func TestCountCycleWork_AC211_OpenUpstreamIssueWithoutAChallengeIsZero(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	th := simpleThread("body")
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = "o/r#77", "o/r", 77, "open"
	f.upstream.threads["o/r#77"] = th
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if *w != (CycleWork{}) {
		t.Fatalf("work = %+v, want zero", *w)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM challenge`); n != 0 {
		t.Fatalf("challenges = %d, want 0 (counting must not ingest)", n)
	}
}

// 連続の失敗の上限に達した着手中の課題は、サイクルを回すと進むものに数える（委譲の段が、その課題を人間対応待ちにして
// オーナーへ見せる。数えから外すと、ほかに進むものが無いときにサイクルが始まらず、課題が着手中のまま止まる）。
// 段が保留にした後は、数えない。
func TestCountCycleWork_FailureLimitHitIsCountedUntilTheStagePutsItOnHold(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultLaunchFailed), failOut(RunResultLaunchFailed)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	ctx := context.Background()

	w, err := f.s.CountCycleWork(ctx, f.agent, f.conn)
	if err != nil {
		t.Fatalf("CountCycleWork: %v", err)
	}
	if w.Stages.Run != 1 || w.Total != 1 {
		t.Fatalf("work = %+v, want run=1 (the stage will put the challenge on hold)", *w)
	}
	// 数えたとおり、段を回すと進む: 起動はせず、人間対応待ちになる。
	res, err := f.s.RunDelegation(ctx, f.input(f.cycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("notStarted = %+v, want failure_limit", res.NotStarted)
	}
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	// 保留になった後は、オーナーの回答待ちなので、数えない。
	w, err = f.s.CountCycleWork(ctx, f.agent, f.conn)
	if err != nil {
		t.Fatalf("CountCycleWork: %v", err)
	}
	if w.Stages.Run != 0 {
		t.Errorf("run = %d after the hold, want 0", w.Stages.Run)
	}
}

// 数えの判定（委譲の段がこの課題に着くと何かが進むか）を、上限と実装枠の組み合わせの全部で確かめる。
// 失敗の上限・やり直しの上限に達した課題は、実装枠が尽きていても進む（段が、枠より先に上限を見て、人間対応待ちにする）。
func TestDelegationContext_AdvancesWhenTheStageRuns(t *testing.T) {
	t.Parallel()
	enough, exhausted := minImplLaunchMicros, minImplLaunchMicros-1
	for _, tc := range []struct {
		name   string
		limit  *failureLimitHit
		rework *reworkLimitHit
		impl   int64
		want   bool
	}{
		{"上限なし・枠あり", nil, nil, enough, true},
		{"上限なし・枠が尽きた（run_budget）", nil, nil, exhausted, false},
		{"失敗の上限・枠あり", &failureLimitHit{}, nil, enough, true},
		{"失敗の上限・枠が尽きた", &failureLimitHit{}, nil, exhausted, true},
		{"やり直しの上限・枠あり", nil, &reworkLimitHit{}, enough, true},
		{"やり直しの上限・枠が尽きた", nil, &reworkLimitHit{}, exhausted, true},
	} {
		dc := &delegationContext{LimitHit: tc.limit, ReworkHit: tc.rework, ImplBudgetMicros: tc.impl}
		if got := dc.advancesWhenTheStageRuns(); got != tc.want {
			t.Errorf("%s: advancesWhenTheStageRuns() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
