package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backup の段（M5 S1・M5P3）を、実ストアと偽の WorkspaceGit で検証する。git の実物は
// internal/adapters/git のテストと internal/cli のテストが持つ。

type fakeWorkspaceGit struct {
	notTree bool
	ignored bool
	changed bool
	// changedPaths が nil でなければ、HasChanges はワークスペースからの相対パスのどれかが true のときだけ差ありを返す。
	changedPaths map[string]bool
	onCommit     func()
	treeErr      error
	commitErr    error
	sha          string
	calls        []string
	changePath   []string
	commitMsg    string
	commitPath   []string
	// interrupted が空でなければ、InterruptedState はその理由を返す。
	interrupted string
	// blockCommit が真なら、CommitPaths は ctx が終わるまで止まり、ctx のエラーを返す。
	blockCommit bool
}

func (g *fakeWorkspaceGit) InterruptedState(context.Context, string) (string, error) {
	g.calls = append(g.calls, "InterruptedState")
	return g.interrupted, nil
}

func (g *fakeWorkspaceGit) IsWorkTree(context.Context, string) (bool, error) {
	g.calls = append(g.calls, "IsWorkTree")
	return !g.notTree, g.treeErr
}
func (g *fakeWorkspaceGit) IsIgnored(context.Context, string, string) (bool, error) {
	g.calls = append(g.calls, "IsIgnored")
	return g.ignored, nil
}
func (g *fakeWorkspaceGit) HasChanges(_ context.Context, ws string, paths []string) (bool, error) {
	g.calls = append(g.calls, "HasChanges")
	g.changePath = paths
	if g.changedPaths == nil {
		return g.changed, nil
	}
	for _, p := range paths {
		if r, err := filepath.Rel(ws, p); err == nil && g.changedPaths[filepath.ToSlash(r)] {
			return true, nil
		}
	}
	return false, nil
}
func (g *fakeWorkspaceGit) CommitPaths(ctx context.Context, _, msg string, paths []string) (string, error) {
	g.calls = append(g.calls, "CommitPaths")
	if g.blockCommit {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if g.onCommit != nil {
		g.onCommit()
	}
	g.commitMsg, g.commitPath = msg, paths
	if g.commitErr != nil {
		return "", g.commitErr
	}
	if g.sha == "" {
		return "abc123", nil
	}
	return g.sha, nil
}

func relPaths(t *testing.T, ws string, abs []string) []string {
	t.Helper()
	var out []string
	for _, p := range abs {
		r, err := filepath.Rel(ws, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(r))
	}
	return out
}

func backupOnce(t *testing.T, g WorkspaceGit, declareMemory bool) (*Store, BackupResult) {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	agent := defaultAgentDeclaration()
	if declareMemory {
		agent.MemoryDir, agent.MemoryDirDeclared = "mem", true
	}
	return s, s.BackupWorkspace(context.Background(), g, agent, "Y-1")
}

func TestBackup_CommitsOnlyTheExportWhenMemoryDirIsNotDeclared(t *testing.T) {
	g := &fakeWorkspaceGit{changed: true}
	s, res := backupOnce(t, g, false)
	if res.Outcome != BackupCommitted || res.Commit != "abc123" || res.Detail != "" {
		t.Fatalf("result = %+v, want committed abc123", res)
	}
	if got := relPaths(t, s.Workspace(), g.commitPath); len(got) != 1 || got[0] != ".flywheel/export.json" {
		t.Errorf("committed paths = %v, want only .flywheel/export.json", got)
	}
	if g.commitMsg != "chore(flywheel): backup Y-1" {
		t.Errorf("message = %q", g.commitMsg)
	}
	if _, err := os.Stat(filepath.Join(s.Workspace(), ".flywheel", "export.json")); err != nil {
		t.Errorf("export file: %v", err)
	}
}

func TestBackup_CommitsExportAndDeclaredMemoryDir(t *testing.T) {
	g := &fakeWorkspaceGit{changed: true}
	s, res := backupOnce(t, g, true)
	if res.Outcome != BackupCommitted {
		t.Fatalf("result = %+v", res)
	}
	got := relPaths(t, s.Workspace(), g.commitPath)
	if len(got) != 2 || got[0] != ".flywheel/export.json" || got[1] != "mem" {
		t.Errorf("committed paths = %v, want export and mem", got)
	}
}

func TestBackup_UnchangedMakesNoCommit(t *testing.T) {
	g := &fakeWorkspaceGit{}
	_, res := backupOnce(t, g, false)
	if res.Outcome != BackupUnchanged || res.Commit != "" {
		t.Errorf("result = %+v, want unchanged without commit", res)
	}
	for _, c := range g.calls {
		if c == "CommitPaths" {
			t.Error("CommitPaths was called with no changes")
		}
	}
}

func TestBackup_NotGitWritesTheExportOnly(t *testing.T) {
	g := &fakeWorkspaceGit{notTree: true}
	s, res := backupOnce(t, g, true)
	if res.Outcome != BackupNotGit {
		t.Errorf("result = %+v, want not_git", res)
	}
	if _, err := os.Stat(filepath.Join(s.Workspace(), ".flywheel", "export.json")); err != nil {
		t.Errorf("export file: %v", err)
	}
}

func TestBackup_IgnoredExportWithoutMemoryChangeMakesNoCommit(t *testing.T) {
	g := &fakeWorkspaceGit{ignored: true}
	_, res := backupOnce(t, g, false)
	if res.Outcome != BackupIgnored || res.Commit != "" || res.Detail == "" {
		t.Errorf("result = %+v, want ignored, no commit, with detail", res)
	}
	if len(g.changePath) != 0 || len(g.commitPath) != 0 {
		t.Errorf("paths probed/committed = %v / %v, want none", g.changePath, g.commitPath)
	}
}

func TestBackup_IgnoredExportCommitsOnlyMemoryDir(t *testing.T) {
	g := &fakeWorkspaceGit{ignored: true, changed: true, sha: "mem1"}
	s, res := backupOnce(t, g, true)
	if res.Outcome != BackupIgnored || res.Commit != "mem1" {
		t.Fatalf("result = %+v, want ignored with commit mem1", res)
	}
	if got := relPaths(t, s.Workspace(), g.commitPath); len(got) != 1 || got[0] != "mem" {
		t.Errorf("committed paths = %v, want only mem", got)
	}
}

func TestBackup_CommitFailureIsFailedWithDetail(t *testing.T) {
	g := &fakeWorkspaceGit{changed: true, commitErr: errors.New("hook rejected")}
	_, res := backupOnce(t, g, false)
	if res.Outcome != BackupFailed || !strings.Contains(res.Detail, "hook rejected") {
		t.Errorf("result = %+v, want failed with the hook message", res)
	}
}

func TestBackup_GitProbeFailureIsFailedWithDetail(t *testing.T) {
	g := &fakeWorkspaceGit{treeErr: errors.New("git not found")}
	_, res := backupOnce(t, g, false)
	if res.Outcome != BackupFailed || res.Detail == "" {
		t.Errorf("result = %+v, want failed with detail", res)
	}
}

func TestBackup_ExportWriteFailureIsFailedWithDetail(t *testing.T) {
	s := newStoreForTest(t)
	if err := os.MkdirAll(filepath.Join(s.Workspace(), ".flywheel", "export.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &fakeWorkspaceGit{changed: true}
	res := s.BackupWorkspace(context.Background(), g, defaultAgentDeclaration(), "Y-1")
	if res.Outcome != BackupFailed || res.Detail == "" {
		t.Errorf("result = %+v, want failed with detail", res)
	}
	if len(g.calls) != 0 {
		t.Errorf("git calls = %v, want none after the export failed", g.calls)
	}
}

func TestBackup_DoesNotWriteTheStore(t *testing.T) {
	s := newStoreForTest(t)
	before, _, err := s.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.BackupWorkspace(context.Background(), &fakeWorkspaceGit{}, defaultAgentDeclaration(), "Y-1")
	after, _, err := s.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("BackupWorkspace changed the store")
	}
}

func backupActivities(t *testing.T, s *Store) []Activity {
	t.Helper()
	all, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []Activity
	for _, a := range all {
		if a.Entity == "cycle" {
			out = append(out, a)
		}
	}
	return out
}

func TestRunCycle_BackupPhaseIsLastAndRecordsAnActivityRow(t *testing.T) {
	f := newCycleFixture(t)
	g := &fakeWorkspaceGit{changed: true}
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = g
	res := f.run(t, in)

	last := res.Phases[len(res.Phases)-1]
	if last.Phase != CyclePhaseBackup || last.Skipped || last.Backup == nil || last.Backup.Outcome != BackupCommitted {
		t.Fatalf("last phase = %+v, want an executed backup that committed", last)
	}
	if g.commitMsg != "chore(flywheel): backup "+res.Cycle.ID {
		t.Errorf("message = %q", g.commitMsg)
	}
	rows := backupActivities(t, f.s)
	if len(rows) != 1 || rows[0].Action != ActivityActionBackup || rows[0].EntityID != res.Cycle.ID || rows[0].Channel != string(ChannelCLI) {
		t.Fatalf("cycle activities = %+v, want one backup row for %s", rows, res.Cycle.ID)
	}
	var after map[string]any
	if err := json.Unmarshal(rows[0].After, &after); err != nil {
		t.Fatal(err)
	}
	if _, hasVersion := after["version"]; hasVersion || after["outcome"] != "committed" || after["commit"] != "abc123" {
		t.Errorf("after = %v, want outcome/commit/detail and no version", after)
	}
	if v, ok := after["detail"]; !ok || v != nil {
		t.Errorf("after.detail = %v (present=%v), want an explicit null", v, ok)
	}
	if rows[0].Before != nil {
		t.Errorf("before = %s, want null", rows[0].Before)
	}
}

// assertCommittedBackupRow は周の backup の作業ログが 1 行で、after が committed・abc123・detail のキーを持つことを確かめる。
func assertCommittedBackupRow(t *testing.T, s *Store, cycleID string) {
	t.Helper()
	rows := backupActivities(t, s)
	if len(rows) != 1 || rows[0].EntityID != cycleID {
		t.Fatalf("cycle activities = %+v, want one backup row for %s", rows, cycleID)
	}
	var after map[string]any
	if err := json.Unmarshal(rows[0].After, &after); err != nil {
		t.Fatal(err)
	}
	if _, hasDetail := after["detail"]; !hasDetail || after["outcome"] != "committed" || after["commit"] != "abc123" {
		t.Errorf("after = %v, want outcome committed, commit abc123 and a detail key", after)
	}
}

func TestRunCycle_BackupRunsWithACancelledContext(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	ctx, cancel := context.WithCancel(context.Background())
	inv := &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput {
		cancel()
		return JudgmentLaunchOutput{Result: RunResultErrored}
	}}
	g := &fakeWorkspaceGit{changed: true}
	in := f.input(inv)
	in.ConnDecl = nil
	in.WorkspaceGit = g

	if _, err := f.s.RunCycle(ctx, in); err == nil {
		t.Fatal("RunCycle err = nil, want the phase error")
	}
	if _, err := os.Stat(filepath.Join(f.s.Workspace(), ".flywheel", "export.json")); err != nil {
		t.Errorf("export file after an errored cycle: %v", err)
	}
	assertCommittedBackupRow(t, f.s, "Y-1")
	if g.commitMsg != "chore(flywheel): backup Y-1" {
		t.Errorf("commit message = %q", g.commitMsg)
	}
}

// AC-20: ctx を取り消さず、段だけがエラーになった周でも backup を実行する。
func TestRunCycle_BackupRunsAfterAPhaseError(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	// J1 の起動中に actor を解決できなくし、分類の結果の書き込み（ストア）をエラーにする。
	// backup のコミットの時点で戻す（backup の作業ログを書けるようにする）。
	inv := &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput {
		withActorSource(t, actorSourceFuncs{
			userCurrent: func() (*user.User, error) { return nil, errors.New("no user") },
			getenv:      fakeGetenv(map[string]string{}),
		})
		return succeeded(j1MineP(t, "P1"))
	}}
	g := &fakeWorkspaceGit{changed: true, onCommit: func() { fixedActor(t, "alice") }}
	in := f.input(inv)
	in.ConnDecl = nil
	in.WorkspaceGit = g

	if _, err := f.s.RunCycle(context.Background(), in); !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("RunCycle err = %v, want the phase error (ErrActorUnavailable)", err)
	}
	assertCommittedBackupRow(t, f.s, "Y-1")
	if g.commitMsg != "chore(flywheel): backup Y-1" {
		t.Errorf("commit message = %q", g.commitMsg)
	}
}

// AC-21: 周の上限で起動を打ち切った周でも backup を実行する。
func TestRunCycle_BackupRunsInACycleCutByTheCycleBudget(t *testing.T) {
	// 委譲の対象（着手中の課題）がある周で、周の上限を J3 と実装枠には足りない額にする（フィクスチャに J1・J2 の
	// 対象は無く、判断の呼び出しは起きない。上限の額は J1・J2 の枠の和に 1 を足したもの）。
	// 委譲の段（run）が not_started: cycle_budget を出し、それでも backup は実行される。
	f := newJ5Fixture(t, "impl")
	f.agent.CycleBudgetUSD = f.agent.JudgmentBudgetUSD.J1 + f.agent.JudgmentBudgetUSD.J2 + 1
	var calls []JudgmentPoint
	g := &fakeWorkspaceGit{changed: true}
	in := f.cycleInput(cycleS2Invoker("met", &calls))
	in.WorkspaceGit = g
	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	run := phaseOfResult(t, res, CyclePhaseRun)
	if len(run.NotStarted) != 1 || run.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Fatalf("run phase = %+v, want one not_started cycle_budget", run)
	}
	if len(calls) != 0 {
		t.Errorf("judgment calls = %v, want none (J3 must not be paid)", calls)
	}
	last := res.Phases[len(res.Phases)-1]
	if last.Phase != CyclePhaseBackup || last.Backup == nil || last.Backup.Outcome != BackupCommitted {
		t.Fatalf("last phase = %+v, want a committed backup", last)
	}
	if g.commitMsg == "" {
		t.Error("CommitPaths was not called")
	}
	assertCommittedBackupRow(t, f.s, res.Cycle.ID)
}

func TestRunCycle_BackupIsNotRunWhenTheLockIsHeld(t *testing.T) {
	f := newCycleFixture(t)
	g := &fakeWorkspaceGit{changed: true}
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = g
	hold, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "other", BudgetUSD: 1, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.s.ReleaseCycleLock(context.Background(), hold.ID) }()
	if _, err := f.s.RunCycle(context.Background(), in); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if len(g.calls) != 0 || len(backupActivities(t, f.s)) != 0 {
		t.Errorf("git calls = %v / backup rows = %d, want none", g.calls, len(backupActivities(t, f.s)))
	}
	if _, err := os.Stat(filepath.Join(f.s.Workspace(), ".flywheel", "export.json")); err == nil {
		t.Error("export.json was written by a cycle that never started")
	}
}

func TestRunCycle_BackupFailureDoesNotFailTheCycle(t *testing.T) {
	f := newCycleFixture(t)
	g := &fakeWorkspaceGit{changed: true, commitErr: errors.New("boom")}
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = g
	res := f.run(t, in)
	if res.Cycle.Result != CycleResultCompleted {
		t.Errorf("cycle result = %q, want completed", res.Cycle.Result)
	}
	if b := phaseOfResult(t, res, CyclePhaseBackup); b.Backup.Outcome != BackupFailed || b.Backup.Detail == "" {
		t.Errorf("backup = %+v, want failed with detail", b.Backup)
	}
}

func TestRunCycle_WorkspaceGitIsRequired(t *testing.T) {
	f := newCycleFixture(t)
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = nil
	if _, err := f.s.RunCycle(context.Background(), in); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

// AC-25: memory_dir だけに差があるときも、記憶をコミットする（差の検査に memory_dir を含める）。
func TestBackup_CommitsWhenOnlyTheMemoryDirChanged(t *testing.T) {
	g := &fakeWorkspaceGit{changedPaths: map[string]bool{"mem": true}}
	s, res := backupOnce(t, g, true)
	if res.Outcome != BackupCommitted || res.Commit != "abc123" {
		t.Fatalf("result = %+v, want committed", res)
	}
	if got := relPaths(t, s.Workspace(), g.changePath); len(got) != 2 || got[1] != "mem" {
		t.Errorf("HasChanges paths = %v, want export and mem", got)
	}
}

// AC-33: 書き出しが除外され memory_dir に差が無いときは ignored で、コミットしない。
func TestBackup_IgnoredExportWithDeclaredMemoryDirWithoutChangeMakesNoCommit(t *testing.T) {
	g := &fakeWorkspaceGit{ignored: true}
	_, res := backupOnce(t, g, true)
	if res.Outcome != BackupIgnored || res.Commit != "" || res.Detail == "" {
		t.Errorf("result = %+v, want ignored without commit and with detail", res)
	}
	for _, c := range g.calls {
		if c == "CommitPaths" {
			t.Error("CommitPaths was called")
		}
	}
}

// memory_dir が .flywheel を含む場所（"."）を指すときは記憶を外し、そのことを detail に出す。
func TestBackup_MemoryDirContainingFlywheelIsExcludedWithDetail(t *testing.T) {
	g := &fakeWorkspaceGit{changed: true}
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	agent := defaultAgentDeclaration()
	agent.MemoryDir, agent.MemoryDirDeclared = ".", true
	res := s.BackupWorkspace(context.Background(), g, agent, "Y-1")
	if res.Outcome != BackupCommitted || res.Detail == "" {
		t.Errorf("result = %+v, want committed with a detail about the exclusion", res)
	}
	if got := relPaths(t, s.Workspace(), g.commitPath); len(got) != 1 || got[0] != ".flywheel/export.json" {
		t.Errorf("commit paths = %v, want only .flywheel/export.json", got)
	}
}

// backup の作業ログの記録だけに失敗しても RunCycle はエラーを返さず、phases に backup の結果を
// 残す（detail に記録の失敗を足す）。周は completed で閉じ、排他を解放する。
func TestRunCycle_BackupRecordFailureIsAWarningInDetailAndTheCycleStillCloses(t *testing.T) {
	f := newCycleFixture(t)
	g := &fakeWorkspaceGit{changed: true}
	// backup のコミットの後で actor を解決できなくし、作業ログの書き込み（mutate）を失敗させる。
	g.onCommit = func() {
		withActorSource(t, actorSourceFuncs{
			userCurrent: func() (*user.User, error) { return nil, errors.New("no user") },
			getenv:      fakeGetenv(map[string]string{}),
		})
	}
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = g
	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	last := res.Phases[len(res.Phases)-1]
	if last.Phase != CyclePhaseBackup || last.Backup == nil || last.Backup.Outcome != BackupCommitted {
		t.Fatalf("last phase = %+v, want the committed backup result", last)
	}
	if !strings.Contains(last.Backup.Detail, "activity log") {
		t.Errorf("detail = %q, want the record failure", last.Backup.Detail)
	}
	if len(backupActivities(t, f.s)) != 0 {
		t.Error("a backup row was recorded")
	}
	st, err := f.s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LockHeld || st.LastResult == nil || *st.LastResult != CycleResultCompleted {
		t.Errorf("status = %+v (last result %v), want the lock released and the cycle completed", st, st.LastResult)
	}
}

// git が止まったら期限（backupTimeout）で failed（detail に timeout）にし、周は閉じる。
func TestRunCycle_BackupTimesOutAsFailedAndTheCycleStillCloses(t *testing.T) {
	old := backupTimeout
	backupTimeout = 100 * time.Millisecond
	t.Cleanup(func() { backupTimeout = old })
	f := newCycleFixture(t)
	g := &fakeWorkspaceGit{changed: true, blockCommit: true}
	in := f.input(mineThenPlanInvoker(t))
	in.WorkspaceGit = g
	res := f.run(t, in)
	b := res.Phases[len(res.Phases)-1].Backup
	if b == nil || b.Outcome != BackupFailed || !strings.Contains(b.Detail, "timeout") {
		t.Fatalf("backup = %+v, want failed with timeout", b)
	}
	if res.Cycle.Result != CycleResultCompleted {
		t.Errorf("cycle result = %s", res.Cycle.Result)
	}
	st, err := f.s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LockHeld {
		t.Errorf("status = %+v, want the lock released", st)
	}
}

// 期限の既定値は 120 秒（M5 仕様の決定）。
func TestBackupTimeoutDefaultIs120Seconds(t *testing.T) {
	if backupTimeout != 120*time.Second {
		t.Errorf("backupTimeout = %s, want 120s", backupTimeout)
	}
}

// detached HEAD・rebase・merge・cherry-pick・revert の途中ではコミットせず failed（detail に理由）にする。
func TestBackupWorkspace_InterruptedStateIsFailedWithoutCommit(t *testing.T) {
	for _, reason := range []string{"detached HEAD", "rebase in progress", "merge in progress", "cherry-pick in progress", "revert in progress"} {
		t.Run(reason, func(t *testing.T) {
			s := newStoreForTest(t)
			g := &fakeWorkspaceGit{changed: true, interrupted: reason}
			res := s.BackupWorkspace(context.Background(), g, defaultAgentDeclaration(), "Y-1")
			if res.Outcome != BackupFailed || !strings.Contains(res.Detail, reason) || res.Commit != "" {
				t.Fatalf("result = %+v, want failed with %q", res, reason)
			}
			if g.commitMsg != "" {
				t.Error("CommitPaths was called")
			}
		})
	}
}

// 差が無ければ途中の状態でも unchanged のまま（検査しない）。
func TestBackupWorkspace_InterruptedStateIsNotCheckedWithoutChanges(t *testing.T) {
	s := newStoreForTest(t)
	g := &fakeWorkspaceGit{changed: false, interrupted: "detached HEAD"}
	res := s.BackupWorkspace(context.Background(), g, defaultAgentDeclaration(), "Y-1")
	if res.Outcome != BackupUnchanged {
		t.Fatalf("result = %+v, want unchanged", res)
	}
}
