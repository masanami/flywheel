package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは区切り S4b-2 のスーパーバイザーの判断点の入力（データの区画）を、実ストアで確かめる
// （#1075 の AC-60・AC-62・AC-63・AC-64・AC-70・AC-71）。区画は buildSupervisorSections が組み立てる。

type svInputFixture struct {
	*offeredFixture
}

func newSVInputFixture(t *testing.T) *svInputFixture {
	t.Helper()
	f := &svInputFixture{offeredFixture: newOfferedFixture(t)}
	f.agent.MemoryDir = "memory/harness"
	dir := filepath.Join(f.s.Workspace(), "memory", "harness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte("# INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// sections は、タスクを指す保留（止まり方 kind）についての入力の区画を、ラベルから本文への写像で返す。
func (f *svInputFixture) sections(t *testing.T, kind StopKind) map[string]string {
	t.Helper()
	tg := f.hold(t, StatusVerifying, kind, true)
	offered, err := f.s.offeredSupervisorHands(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	secs, err := f.s.buildSupervisorSections(context.Background(), f.agent, tg, offered)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, s := range secs {
		m[s.Label] = s.Content
	}
	return m
}

// addContractRun は、契約の実行（終わった状態）を、開始の時刻 at・要約 summary・委譲先が返した値 facts で足す。
func (f *svInputFixture) addContractRun(t *testing.T, ext, at, summary, facts string) {
	t.Helper()
	if _, err := f.exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, facts, cost_usd, max_budget_usd,
			started_at, observed_at, ended_at, task_id)
		SELECT challenge_id, connector_id, ?, start_run_id, 'succeeded', ?, ?, 0, max_budget_usd, ?, observed_at, ?, task_id FROM contract_run ORDER BY id LIMIT 1`,
		ext, summary, facts, at, at); err != nil {
		t.Fatal(err)
	}
}

func (f *svInputFixture) endFirstContractRun(t *testing.T, at string) {
	t.Helper()
	f.setRun(t, fmt.Sprintf(`state = 'failed', summary = 'RUN-FIRST', started_at = '%s', ended_at = '%s', requested_kind = NULL`, at, at))
}

func TestSupervisorInput_TaskHold_HasNumberedCriteriaPolicyActionsAndMemoryIndex(t *testing.T) {
	t.Parallel()
	f := newSVInputFixture(t)
	f.endFirstContractRun(t, "2026-11-01T00:00:00.000Z")
	if _, err := f.exec(`UPDATE task SET done_criteria = ? WHERE id = ?`, "- 条件A\n- 条件B", f.taskID); err != nil {
		t.Fatal(err)
	}
	tg := f.hold(t, StatusVerifying, StopKindVerifyUncertain, true)
	f.record(t, tg, HandDefer, ActionExecuted)
	secs := f.sections(t, StopKindVerifyUncertain)

	// AC-60: タスクの完了条件が 1 から始まる番号つきで入る。
	if body := secs["課題とタスク"]; !strings.Contains(body, "タスクの完了条件（番号つき）:\n1. 条件A\n2. 条件B") {
		t.Errorf("the task criteria are not numbered from 1: %q", body)
	}
	// AC-62: 計画の方針が入る。
	if body := secs["計画の方針"]; !strings.Contains(body, "PLAN-BODY-T") {
		t.Errorf("the plan policy is missing: %q", body)
	}
	// AC-70: そのタスクへのこれまでの対処の記録が入る。
	if body := secs["これまでの対処の記録"]; !strings.Contains(body, "手 defer 結果 executed") {
		t.Errorf("the past actions are missing: %q", body)
	}
	// AC-71: 記憶の索引の場所が入る。
	if body := secs[memoryIndexSectionLabel]; !strings.Contains(body, "索引のパス: ") || !strings.Contains(body, "INDEX.md") {
		t.Errorf("the memory index location is missing: %q", body)
	}
	if secs["選べる手の一覧"] == "" {
		t.Error("the offered hands section is missing")
	}
}

// AC-63: 実行の記録が 6 件あるタスクについて、入るのは新しいほうの 5 件。委譲先が返した値と成果物の識別が付く。
func TestSupervisorInput_RunRecords_AreTheNewestFiveWithFactsAndArtifacts(t *testing.T) {
	t.Parallel()
	f := newSVInputFixture(t)
	f.endFirstContractRun(t, "2026-11-01T00:00:00.000Z") // いちばん古い
	for i := 2; i <= 6; i++ {
		f.addContractRun(t, fmt.Sprintf("ext-%d", i), fmt.Sprintf("2026-11-0%dT00:00:00.000Z", i), fmt.Sprintf("RUN-%d", i), fmt.Sprintf("FACTS-%d", i))
	}
	startRun := f.count(t, `SELECT start_run_id FROM contract_run ORDER BY id LIMIT 1`)
	if _, err := f.exec(`INSERT INTO run_artifact (run_id, kind, ref, state) VALUES (?, 'pull_request', 'org/repo#42', 'open')`, startRun); err != nil {
		t.Fatal(err)
	}
	rec := f.sections(t, StopKindDelegateStopped)["実行の記録"]
	if got := strings.Count(rec, "- 実行 "); got != 5 {
		t.Errorf("run records = %d, want 5: %s", got, rec)
	}
	if strings.Contains(rec, "RUN-FIRST") {
		t.Errorf("the oldest run must not be in the record: %s", rec)
	}
	for _, w := range []string{"要約: RUN-6", "委譲先が返した値: FACTS-6", "成果物: pull_request org/repo#42 (open)", "状態 succeeded"} {
		if !strings.Contains(rec, w) {
			t.Errorf("the run record lacks %q: %s", w, rec)
		}
	}
}

// 契約でない委譲の実行（run の kind が delegate）も、そのタスクの実行の記録に入る（仕様は実行の種類を契約に限らない）。
func TestSupervisorInput_RunRecords_IncludeANonContractDelegateRun(t *testing.T) {
	t.Parallel()
	f := newSVInputFixture(t)
	f.endFirstContractRun(t, "2026-11-01T00:00:00.000Z")
	if _, err := f.exec(`INSERT INTO run (kind, challenge_id, task_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, output, error)
		VALUES ('delegate', ?, ?, 1, 'h', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z', '2026-11-02T00:01:00.000Z', 'errored', 1, 'impl', 'DELEGATE-OUT', 'DELEGATE-ERR')`,
		f.cidInt, f.taskID); err != nil {
		t.Fatal(err)
	}
	rec := f.sections(t, StopKindDelegateStopped)["実行の記録"]
	for _, w := range []string{"状態 errored", "要約: DELEGATE-ERR", "委譲先が返した値: DELEGATE-OUT", "RUN-FIRST"} {
		if !strings.Contains(rec, w) {
			t.Errorf("the run record lacks %q: %s", w, rec)
		}
	}
}

// 契約の実行と契約でない委譲の実行が混ざるとき、開始の時刻の新しい順に並び、新しいほうの 5 件だけが入る（id の順ではない）。
func TestSupervisorInput_RunRecords_MixedKindsAreNewestFirstAndCutAtFive(t *testing.T) {
	t.Parallel()
	f := newSVInputFixture(t)
	f.endFirstContractRun(t, "2026-11-01T00:00:00.000Z")
	f.addContractRun(t, "ext-4", "2026-11-04T00:00:00.000Z", "RUN-4", "")
	f.addContractRun(t, "ext-6", "2026-11-06T00:00:00.000Z", "RUN-6", "")
	for _, d := range []string{"02", "03", "05", "07"} {
		if _, err := f.exec(`INSERT INTO run (kind, challenge_id, task_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, output, error)
			VALUES ('delegate', ?, ?, 1, 'h', ?, ?, ?, 'errored', 1, 'impl', ?, '')`,
			f.cidInt, f.taskID, "2026-11-"+d+"T00:00:00.000Z", "2026-11-"+d+"T00:00:00.000Z", "2026-11-"+d+"T00:01:00.000Z", "OUT-"+d); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.sections(t, StopKindDelegateStopped)["実行の記録"]
	if got := strings.Count(rec, "- 実行 "); got != 5 {
		t.Fatalf("run records = %d, want 5: %s", got, rec)
	}
	last := -1
	for _, w := range []string{"OUT-07", "RUN-6", "OUT-05", "RUN-4", "OUT-03"} {
		i := strings.Index(rec, w)
		if i < 0 || i < last {
			t.Fatalf("%s is missing or out of order (newest first): %s", w, rec)
		}
		last = i
	}
	for _, w := range []string{"OUT-02", "RUN-FIRST"} {
		if strings.Contains(rec, w) {
			t.Errorf("%s is older than the newest five and must be cut: %s", w, rec)
		}
	}
	if !strings.Contains(rec, "状態 errored\n  要約: (なし)") {
		t.Errorf("an empty delegate error must read (なし): %s", rec)
	}
}
