package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// このファイルは、片付けの段（要求の解決。docs/features/request-to-plan-s3.md「証跡を消す（要求の解決）」）を、
// 実ストアで Store.RunCycle を通して確かめる。

// settleCase は、要求の記録 1 件と、それを指す statuses の状態の課題を持つサイクルのフィクスチャ。
type settleCase struct {
	f        *cycleFixture
	req      *Requirement
	evidence string // 証跡のファイルの絶対パス
	ids      []string
}

func newSettleCase(t *testing.T, statuses ...Status) *settleCase {
	t.Helper()
	f := newCycleFixture(t)
	c := &settleCase{f: f, req: createRequirement(t, f.s, "通知")}
	c.evidence = filepath.Join(f.s.workspace, c.req.EvidencePath)
	for i, st := range statuses {
		ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI,
			CreateInput{Title: fmt.Sprintf("slice-%d", i), Requirement: &c.req.ID})
		if err != nil {
			t.Fatalf("CreateChallenge: %v", err)
		}
		c.ids = append(c.ids, ch.ID)
		setStatusForSettle(t, f.s, ch.ID, st)
	}
	return c
}

func setStatusForSettle(t *testing.T, s *Store, id string, st Status) {
	t.Helper()
	cid, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("invalid id %q", id)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(st), cid)
		return err
	})
}

func writeEvidenceFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cycle は接続ツールの宣言の無いサイクルを 1 回回す（片付けの段は宣言を使わない）。
func (c *settleCase) cycle(t *testing.T) *CycleRunResult {
	t.Helper()
	in := c.f.input(mineThenPlanInvoker(t))
	in.ConnDecl = nil
	in.Upstream = nil
	return c.f.run(t, in)
}

func resolvedAt(t *testing.T, s *Store) bool {
	t.Helper()
	return countRows(t, s, `SELECT COUNT(*) FROM requirement WHERE resolved_at IS NOT NULL`) == 1
}

// requirementActivities は作業ログのうち action のものを「entity ID after」の形で返す。
func requirementActivities(t *testing.T, s *Store, action string) []string {
	t.Helper()
	all, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range all {
		if a.Action == action {
			out = append(out, a.Entity+" "+a.EntityID+" "+string(a.After))
		}
	}
	return out
}

func TestRunCycle_AC176_SettleStageRunsBetweenReleaseAndIngest(t *testing.T) {
	t.Parallel()
	f := newCycleFixture(t)
	res := f.run(t, f.input(mineThenPlanInvoker(t)))
	want := []CyclePhase{"release", "settle", "ingest", "classify", "plan", "run", "verify", "supervise", "learn", "improve", "backup"}
	if got := phaseNames(res); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
}

func TestRunCycle_AC177_AC178_ResolvedRequirementLosesEvidenceAndIsMarked(t *testing.T) {
	t.Parallel()
	c := newSettleCase(t, StatusDone, StatusWithdrawn)
	writeEvidenceFile(t, c.evidence)

	res := c.cycle(t)

	if _, err := os.Lstat(c.evidence); !os.IsNotExist(err) {
		t.Errorf("the evidence remains (err = %v)", err) // AC-177
	}
	if !resolvedAt(t, c.f.s) {
		t.Error("resolved_at is not written") // AC-178
	}
	got := requirementActivities(t, c.f.s, ActivityActionRequirementResolved)
	if len(got) != 1 || !strings.HasPrefix(got[0], "requirement RQ-1 ") {
		t.Errorf("requirement_resolved = %v, want one for RQ-1", got)
	}
	if ids := phaseOfResult(t, res, CyclePhaseSettle).ResolvedRequirements; fmt.Sprint(ids) != "[RQ-1]" {
		t.Errorf("settle phase resolved = %v, want [RQ-1]", ids)
	}

	// 解決の日時を持つ記録は、次のサイクルで扱わない。
	c.cycle(t)
	if got := requirementActivities(t, c.f.s, ActivityActionRequirementResolved); len(got) != 1 {
		t.Errorf("after the second cycle: requirement_resolved = %v, want still one", got)
	}
}

func TestRunCycle_AC179_AC180_UnresolvedRequirementIsLeftAlone(t *testing.T) {
	t.Parallel()
	cases := map[string][]Status{
		"AC179_a_classified_challenge_remains":  {StatusDone, StatusClassified},
		"AC179_an_awaiting_human_one_remains":   {StatusWithdrawn, StatusAwaitingHuman},
		"AC179_awaiting_completion_is_not_done": {StatusAwaitingCompletionApproval},
		"AC180_no_challenge_points_at_it":       nil,
	}
	for name, statuses := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newSettleCase(t, statuses...)
			writeEvidenceFile(t, c.evidence)

			c.cycle(t)

			if _, err := os.Lstat(c.evidence); err != nil {
				t.Errorf("the evidence was removed: %v", err)
			}
			if resolvedAt(t, c.f.s) {
				t.Error("resolved_at is written")
			}
			if got := requirementActivities(t, c.f.s, ActivityActionRequirementResolved); len(got) != 0 {
				t.Errorf("requirement_resolved = %v, want none", got)
			}
		})
	}
}

func TestRunCycle_AC181_AC182_AC183_EvidenceOutsideTheDirectoryIsKeptAndLogged(t *testing.T) {
	t.Parallel()
	c := newSettleCase(t, StatusDone)
	outside := filepath.Join(c.f.s.workspace, "docs", "keep.md")
	writeEvidenceFile(t, outside)
	if err := os.MkdirAll(filepath.Dir(c.evidence), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, c.evidence); err != nil {
		t.Fatal(err)
	}

	c.cycle(t)
	c.cycle(t) // 次の回りで消し直さない

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the linked file was removed: %v", err) // AC-181
	}
	got := requirementActivities(t, c.f.s, ActivityActionRequirementEvidenceCleanupFailed)
	if len(got) != 1 || !strings.HasPrefix(got[0], "requirement RQ-1 ") || !strings.Contains(got[0], `"path":"`+c.req.EvidencePath+`"`) {
		t.Errorf("cleanup_failed = %v, want exactly one for RQ-1 with the evidence path", got) // AC-182
	}
	if !resolvedAt(t, c.f.s) {
		t.Error("resolved_at is not written") // AC-183
	}
}

func TestRunCycle_AC184_AC185_AC186_MissingEvidenceIsNotAFailure(t *testing.T) {
	t.Parallel()
	c := newSettleCase(t, StatusDone)

	res := c.cycle(t) // AC-184: f.run はエラーで失敗する

	if res.Cycle.Result != CycleResultCompleted {
		t.Errorf("cycle result = %q, want completed", res.Cycle.Result)
	}
	if !resolvedAt(t, c.f.s) {
		t.Error("resolved_at is not written") // AC-185
	}
	if got := requirementActivities(t, c.f.s, ActivityActionRequirementEvidenceCleanupFailed); len(got) != 0 {
		t.Errorf("cleanup_failed = %v, want none", got) // AC-186
	}
}

func TestRunCycle_AC187_ChallengeDetailKeepsTheRequirementAfterResolution(t *testing.T) {
	t.Parallel()
	c := newSettleCase(t, StatusDone, StatusWithdrawn)
	c.cycle(t)

	for _, id := range c.ids {
		d := showDetail(t, c.f.s, id)
		if d.Requirement == nil || d.Requirement.ID != c.req.ID || d.Requirement.ResolvedAt == nil || len(d.Requirement.Challenges) != 2 {
			t.Errorf("%s: requirement = %+v, want %s resolved with 2 challenges", id, d.Requirement, c.req.ID)
		}
	}
}

func TestCountCycleWork_AC188_CountsResolvedRequirementsWithoutResolvedAt(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	agent := &AgentDeclaration{}
	count := func() CycleWork {
		t.Helper()
		w, err := s.CountCycleWork(context.Background(), agent, nil)
		if err != nil {
			t.Fatalf("CountCycleWork: %v", err)
		}
		return *w
	}
	// 指す課題が 0 件の記録は数えない。
	createRequirement(t, s, "empty")
	if w := count(); w.Stages.Settle != 0 || w.Total != 0 {
		t.Fatalf("no challenge: work = %+v, want zero", w)
	}
	for range 2 {
		r := createRequirement(t, s, "req")
		ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Requirement: &r.ID})
		if err != nil {
			t.Fatal(err)
		}
		setStatusForSettle(t, s, ch.ID, StatusDone)
	}
	if w := count(); w.Stages.Settle != 2 || w.Total != 2 {
		t.Fatalf("two resolved: work = %+v, want settle=2 total=2", w)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE requirement SET resolved_at = ? WHERE id = 2`, formatTimestamp(time.Now()))
		return err
	})
	if w := count(); w.Stages.Settle != 1 || w.Total != 1 {
		t.Fatalf("one marked: work = %+v, want settle=1 total=1", w)
	}
	// 数えは状態を変えない。
	if n := countRows(t, s, `SELECT COUNT(*) FROM requirement WHERE resolved_at IS NOT NULL`); n != 1 {
		t.Errorf("resolved = %d, want 1", n)
	}
}

// 対象を選んだ後に、課題が終端でなくなった記録は、証跡を消さず、日時を書かない。
func TestSettleRequirement_LeftAloneWhenNoLongerResolved(t *testing.T) {
	t.Parallel()
	c := newSettleCase(t, StatusDone)
	writeEvidenceFile(t, c.evidence)
	setStatusForSettle(t, c.f.s, c.ids[0], StatusVerifying)

	marked, err := c.f.s.settleRequirement(context.Background(), "tester", requirementSettleTarget{id: 1, evidencePath: c.req.EvidencePath})
	if err != nil {
		t.Fatalf("settleRequirement: %v", err)
	}
	if _, err := os.Lstat(c.evidence); err != nil {
		t.Errorf("the evidence was removed: %v", err)
	}
	if marked || resolvedAt(t, c.f.s) {
		t.Errorf("marked = %v, resolved_at written = %v, want neither", marked, resolvedAt(t, c.f.s))
	}
	if got := requirementActivities(t, c.f.s, ActivityActionRequirementResolved); len(got) != 0 {
		t.Errorf("requirement_resolved = %v, want none", got)
	}
}
