package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// このファイルは、途中で仕様の文書の場所を付けたときと、取り下げの後片付けを確かめる。テストの名前と注記の AC の番号は、
// Issue #1355 の受け持ち（親 #1339 の受入基準の並び順）。下書きづくりの実行は、種類 spec の contract_run の行を直接置く。

// seOwnerSpec は人が付ける仕様の文書の場所。
const seOwnerSpec = "docs/spec.md"

// seClient は取り消しのコマンドが呼ばれた時点を観測する偽の接続ツール。
type seClient struct {
	*fakeContractClient
	onCancel func(command []string)
}

func (c seClient) Cancel(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	if c.onCancel != nil {
		c.onCancel(in.Command)
	}
	return c.fakeContractClient.Cancel(ctx, in)
}

type seFixture struct {
	*contractFixture
	id    string
	cid   int64
	draft string
}

// newSEFixture は、分類済の課題に state の下書きづくりの記録と下書きのファイルを置く（state が空なら記録を置かない）。
func newSEFixture(t *testing.T, state SpecDraftState) *seFixture {
	t.Helper()
	cf := newContractFixture(t)
	c := createAndAdvance(t, cf.s, StatusClassified)
	f := &seFixture{contractFixture: cf, id: c.ID}
	f.cid, _ = parseChallengeID(c.ID)
	f.draft = specDraftPathFor(f.cid)
	writeWorkspaceFile(t, f.s.workspace, seOwnerSpec, "# 仕様\n")
	if state != "" {
		putSpecDraft(t, f.s, f.id, specDraftUpdate{State: state})
		writeWorkspaceFile(t, f.s.workspace, f.draft, "# 下書き\n")
	}
	f.client.uniqueRunIDs = true
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	return f
}

// seRun は終わった flywheel の実行を 1 件置き、その内部整数 ID を返す。
func (f *seFixture) seRun(t *testing.T) int64 {
	t.Helper()
	n, err := f.s.beginContractRun(context.Background(), f.cid, nil, nil, "00000000-0000-4000-8000-000000000000", 1_000_000, false)
	if err != nil {
		t.Fatal(err)
	}
	f.insertRow(t, `UPDATE run SET result = 'succeeded', ended_at = started_at, budget_bucket = 'spec' WHERE id = ?`, n)
	return n
}

// openSpecRun は終わっていない種類 spec の実行を置く。
func (f *seFixture) openSpecRun(t *testing.T, ext string) {
	t.Helper()
	start := f.seRun(t)
	f.insertRow(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd,
		started_at, observed_at, kind, work_dir) VALUES (?, 'tool', ?, ?, 'running', 's', 1000000, '2026-11-01T00:00:00.000Z',
		'2026-11-01T00:00:00.000Z', 'spec', ?)`, f.cid, ext, start, f.s.workspace)
}

// raiseHold は pending の記録を、失敗の保留で awaiting_owner にする。
func (f *seFixture) raiseHold(t *testing.T) {
	t.Helper()
	held, err := f.s.holdSpecDraft(context.Background(), formatRunID(f.seRun(t)), f.id,
		specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗", CountAttempt: true})
	if err != nil || !held {
		t.Fatalf("holdSpecDraft = %v, %v", held, err)
	}
}

func (f *seFixture) cancelInput(client ContractClient) func() (ContractCancelInput, error) {
	return func() (ContractCancelInput, error) {
		return ContractCancelInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: client}, nil
	}
}

func (f *seFixture) editSpec(t *testing.T, spec string) (*Challenge, error) {
	t.Helper()
	return f.s.EditChallenge(context.Background(), ChannelCLI, f.id, EditInput{Spec: &spec, SpecDraftCancel: f.cancelInput(f.client)})
}

func (f *seFixture) mustEditSpec(t *testing.T, spec string) {
	t.Helper()
	if _, err := f.editSpec(t, spec); err != nil {
		t.Fatalf("edit --spec %s: %v", spec, err)
	}
}

func (f *seFixture) show(t *testing.T) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *seFixture) requireState(t *testing.T, want SpecDraftState) {
	t.Helper()
	if d := f.show(t); d.SpecDraft == nil || d.SpecDraft.State != want {
		t.Errorf("spec draft = %+v, want %s", d.SpecDraft, want)
	}
}

// seActivities は課題の作業ログのうち action のものの after を返す。
func seActivities(t *testing.T, s *Store, id, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range activitiesFor(t, s, id) {
		if a.Action != action {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(a.After, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestSpecDraftEdit_PendingBecomesSkipped(t *testing.T) { // AC-248・AC-261・AC-264
	t.Parallel()
	f := newSEFixture(t, SpecDraftStatePending)
	c, err := f.editSpec(t, seOwnerSpec)
	if err != nil {
		t.Fatal(err)
	}
	d := f.show(t)
	f.requireState(t, SpecDraftStateSkipped)
	if d.SpecPath == nil || *d.SpecPath != seOwnerSpec {
		t.Errorf("spec_path = %v, want %s", d.SpecPath, seOwnerSpec)
	}
	if c.Version != d.Version || c.Status != d.Status {
		t.Errorf("returned = %s v%d, detail = %s v%d", c.Status, c.Version, d.Status, d.Version)
	}
	logs := seActivities(t, f.s, f.id, "spec_draft_skipped")
	if len(logs) != 1 || logs[0]["detail"] != "edit" {
		t.Errorf("spec_draft_skipped = %v, want one with detail edit", logs)
	}
}

func TestSpecDraftEdit_RunningCancelsBeforeWriting(t *testing.T) { // AC-249・AC-250・AC-251・AC-257
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	var seen []SpecDraftState
	client := seClient{fakeContractClient: f.client, onCancel: func([]string) { seen = append(seen, f.show(t).SpecDraft.State) }}
	spec := seOwnerSpec
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id, EditInput{Spec: &spec, SpecDraftCancel: f.cancelInput(client)}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"tool", "cancel", "spec-1"}}; !reflect.DeepEqual(f.client.cancels, want) {
		t.Errorf("cancels = %v, want %v", f.client.cancels, want)
	}
	if !reflect.DeepEqual(seen, []SpecDraftState{SpecDraftStateRunning}) {
		t.Errorf("state at cancel = %v, want running", seen)
	}
	if got := f.client.workDirs["cancel"]; !reflect.DeepEqual(got, []string{f.s.workspace}) {
		t.Errorf("cancel work dir = %v, want the recorded one", got)
	}
	f.requireState(t, SpecDraftStateSkipped)
	// 取り消した実行の終わりを後から読む道（状態の確認と、終わりが呼ぶ保留の口）を通しても、記録は変わらない。
	f.poll(t)
	held, err := f.s.holdSpecDraft(context.Background(), f.runIDDisplay(t), f.id, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "取り消し"})
	if err != nil || held {
		t.Errorf("holdSpecDraft after skip = %v, %v, want not held", held, err)
	}
	f.requireState(t, SpecDraftStateSkipped)
	if d := f.show(t); d.Status != StatusClassified {
		t.Errorf("status = %s, want classified", d.Status)
	}
}

func (f *seFixture) runIDDisplay(t *testing.T) string {
	t.Helper()
	return formatRunID(f.count(t, `SELECT MAX(id) FROM run`))
}

func TestSpecDraftEdit_CancelFailureWritesNothing(t *testing.T) { // AC-252・AC-253
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	before := f.show(t)
	if _, err := f.editSpec(t, seOwnerSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	after := f.show(t)
	if after.SpecPath != nil || after.Version != before.Version {
		t.Errorf("spec_path = %v, version %d -> %d, want nothing written", after.SpecPath, before.Version, after.Version)
	}
	f.requireState(t, SpecDraftStateRunning)
	scRequireExists(t, f.s, f.draft, true)
}

func TestSpecDraftEdit_WithoutAWayToCancelIsAConflict(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	spec := seOwnerSpec
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id, EditInput{Spec: &spec}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	f.requireState(t, SpecDraftStateRunning)
}

func TestSpecDraftEdit_StartingRunIsAConflict(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	// 開始のコマンドの最中: flywheel の実行（枠 spec）が開いていて、contract_run の行はまだ無い。
	n := f.seRun(t)
	f.insertRow(t, `UPDATE run SET result = NULL, ended_at = NULL, heartbeat_at = ? WHERE id = ?`, formatTimestamp(f.s.currentTime()), n)
	if _, err := f.editSpec(t, seOwnerSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	f.requireState(t, SpecDraftStateRunning)
}

func TestSpecDraftEdit_InvalidInputDoesNotCancel(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	spec, urgency := seOwnerSpec, "no-such"
	_, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id,
		EditInput{Spec: &spec, Urgency: &urgency, SpecDraftCancel: f.cancelInput(f.client)})
	if !errors.Is(err, ErrValidation) || len(f.client.cancels) != 0 {
		t.Errorf("err = %v, cancels = %v", err, f.client.cancels)
	}
}

func TestSpecDraftEdit_OtherFieldsAndClearingLeaveTheDraftingAlone(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	title, empty := "新しい題名", ""
	for _, in := range []EditInput{{Title: &title}, {Spec: &empty}} {
		in.SpecDraftCancel = f.cancelInput(f.client)
		if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id, in); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.client.cancels) != 0 {
		t.Errorf("cancels = %v", f.client.cancels)
	}
	f.requireState(t, SpecDraftStateRunning)
	scRequireExists(t, f.s, f.draft, true)
}

func TestSpecDraftEdit_CancelsOnlyTheSpecRun(t *testing.T) { // AC-254
	t.Parallel()
	f := newSEFixture(t, "")
	// 着手中でタスクの実行を持つ課題に、実行中の記録と種類 spec の実行を置く（作り得る状況）。
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.id = f.contractFixture.cid
	f.cid, _ = parseChallengeID(f.id)
	f.draft = specDraftPathFor(f.cid)
	putSpecDraft(t, f.s, f.id, specDraftUpdate{State: SpecDraftStateRunning})
	f.openSpecRun(t, "spec-1")
	f.mustEditSpec(t, seOwnerSpec)
	if want := [][]string{{"tool", "cancel", "spec-1"}}; !reflect.DeepEqual(f.client.cancels, want) {
		t.Errorf("cancels = %v, want only the spec run", f.client.cancels)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE kind = 'task' AND ended_at IS NULL AND state = 'running'`); n != 1 {
		t.Errorf("open task runs = %d, want 1", n)
	}
	if d := f.show(t); d.Status != StatusInProgress {
		t.Errorf("status = %s, want in_progress", d.Status)
	}
}

func TestSpecDraftEdit_CancelledCostIsSpecCost(t *testing.T) { // AC-256
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	cost := 1.0
	f.client.cancelOut = contractOut(ContractStateCancelled, &cost, nil)
	before := f.show(t).SpecCostUSD
	f.mustEditSpec(t, seOwnerSpec)
	if got := f.show(t).SpecCostUSD; got != before+1 {
		t.Errorf("spec_cost_usd = %v, want %v", got, before+1)
	}
	// 取り消しの実行は課題の予算の枠に数えない。
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE cost_usd = 1000000 AND budget_bucket = 'spec'`); n != 1 {
		t.Errorf("runs of the spec bucket with the cost = %d, want 1", n)
	}
}

func TestSpecDraftEdit_AwaitingOwnerClosesTheHold(t *testing.T) { // AC-258・AC-259・AC-260
	t.Parallel()
	f := newSEFixture(t, SpecDraftStatePending)
	f.raiseHold(t)
	if d := f.show(t); d.Status != StatusAwaitingHuman || d.SpecDraft.HoldID == nil {
		t.Fatalf("setup: %s %+v", d.Status, d.SpecDraft)
	}
	f.mustEditSpec(t, seOwnerSpec)
	d := f.show(t)
	f.requireState(t, SpecDraftStateSkipped)
	if d.Status != StatusClassified {
		t.Errorf("status = %s, want classified", d.Status)
	}
	if len(d.Holds) != 1 || d.Holds[0].Open() || d.Holds[0].Answer != nil {
		t.Errorf("holds = %+v, want closed without an answer", d.Holds)
	}
	if d.SpecDraft.HoldID != nil || d.SpecPath == nil || *d.SpecPath != seOwnerSpec {
		t.Errorf("hold_id = %v, spec_path = %v", d.SpecDraft.HoldID, d.SpecPath)
	}
	scRequireExists(t, f.s, f.draft, false)
}

// 作れないはずの状況（下書きづくりの保留に、別の開いた保留が重なる）では、開いた保留を持つ分類済の課題を作らずに断る。
func TestSpecDraftEdit_AnotherOpenHoldIsAConflict(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStatePending)
	f.raiseHold(t)
	f.insertRow(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, task_id, scope)
		VALUES (?, 'q', 'in_progress', '2026-11-26T00:00:00.000Z', (SELECT MIN(id) FROM task), 'task')`, f.cid)
	if _, err := f.editSpec(t, seOwnerSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	f.requireState(t, SpecDraftStateAwaitingOwner)
	if d := f.show(t); d.Status != StatusAwaitingHuman || d.SpecPath != nil {
		t.Errorf("status = %s, spec_path = %v, want nothing written", d.Status, d.SpecPath)
	}
	scRequireExists(t, f.s, f.draft, true)
}

func TestSpecDraftEdit_DraftFile(t *testing.T) { // AC-262・AC-263・AC-266・AC-267・AC-265
	t.Parallel()
	for _, state := range []SpecDraftState{SpecDraftStatePending, SpecDraftStateDone} {
		after := state
		if state == SpecDraftStatePending {
			after = SpecDraftStateSkipped
		}
		for _, tc := range []struct {
			name string
			spec func(draft string) string
			kept bool
		}{
			{"another", func(string) string { return seOwnerSpec }, false},
			{"same", func(d string) string { return d }, true},
			{"same with dot slash", func(d string) string { return "./" + d }, true},
		} {
			t.Run(string(state)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				f := newSEFixture(t, state)
				f.mustEditSpec(t, tc.spec(f.draft))
				f.requireState(t, after)
				scRequireExists(t, f.s, f.draft, tc.kept)
				if n := len(scFailures(t, f.s, f.id)); n != 0 {
					t.Errorf("cleanup failures = %d", n)
				}
				if state == SpecDraftStateDone && len(seActivities(t, f.s, f.id, "spec_draft_skipped")) != 0 {
					t.Error("a done record must not log spec_draft_skipped")
				}
			})
		}
	}
}

func TestSpecDraftEdit_DoneRecordReplacingItsOwnSpecPath(t *testing.T) { // AC-265・AC-266
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateDone)
	sdSetSpec(t, f.s, f.id, f.draft, "")
	f.mustEditSpec(t, seOwnerSpec)
	f.requireState(t, SpecDraftStateDone)
	scRequireExists(t, f.s, f.draft, false)
}

func TestSpecDraftEdit_CleanupFailureStillEdits(t *testing.T) { // AC-268・AC-269
	t.Parallel()
	for _, state := range []SpecDraftState{SpecDraftStatePending, SpecDraftStateDone} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newSEFixture(t, state)
			draft := filepath.Join(f.s.workspace, f.draft)
			if err := os.Remove(draft); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, f.s.workspace, f.draft+"/inner", "x")
			f.mustEditSpec(t, seOwnerSpec)
			if d := f.show(t); d.SpecPath == nil || *d.SpecPath != seOwnerSpec {
				t.Errorf("spec_path = %v", d.SpecPath)
			}
			if n := len(scFailures(t, f.s, f.id)); n != 1 {
				t.Errorf("cleanup failures = %d, want 1", n)
			}
		})
	}
}

func TestSpecDraftEdit_WithoutARecordNeedsNoDeclaration(t *testing.T) { // AC-255
	t.Parallel()
	f := newSEFixture(t, "")
	called := false
	spec := seOwnerSpec
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id, EditInput{Spec: &spec,
		SpecDraftCancel: func() (ContractCancelInput, error) {
			called = true
			return ContractCancelInput{}, errors.New("no declaration")
		}}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("the cancel input must not be built without a record")
	}
}

func TestCancelOpenSpecDraftRuns_LeavesOtherKinds(t *testing.T) {
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	n, err := f.s.CancelOpenSpecDraftRuns(context.Background(), ContractCancelInput{ChallengeID: f.contractFixture.cid})
	if err != nil || n != 0 || len(f.client.cancels) != 0 {
		t.Errorf("CancelOpenSpecDraftRuns = %d, %v, cancels = %v; want nothing", n, err, f.client.cancels)
	}
}

func TestSpecDraftWithdraw_RemovesTheDraftAndKeepsTheState(t *testing.T) { // AC-390・AC-391・AC-392・AC-393・AC-397
	t.Parallel()
	for _, state := range SpecDraftStateValues {
		if state == SpecDraftStateSkipped {
			continue
		}
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newSEFixture(t, state)
			if state == SpecDraftStateDone {
				sdSetSpec(t, f.s, f.id, f.draft, "")
			}
			scWithdraw(t, f.s, f.id)
			f.requireState(t, state)
			scRequireExists(t, f.s, f.draft, false)
			if n := len(scFailures(t, f.s, f.id)); n != 0 {
				t.Errorf("cleanup failures = %d", n)
			}
		})
	}
}

func TestSpecDraftWithdraw_AwaitingOwnerWithTheHold(t *testing.T) { // AC-390・AC-391
	t.Parallel()
	f := newSEFixture(t, SpecDraftStatePending)
	f.raiseHold(t)
	scWithdraw(t, f.s, f.id)
	f.requireState(t, SpecDraftStateAwaitingOwner)
	scRequireExists(t, f.s, f.draft, false)
}

func TestSpecDraftWithdraw_PendingWithoutAFile(t *testing.T) { // AC-393
	t.Parallel()
	f := newSEFixture(t, "")
	putSpecDraft(t, f.s, f.id, specDraftUpdate{State: SpecDraftStatePending})
	scWithdraw(t, f.s, f.id)
	if n := len(scFailures(t, f.s, f.id)); n != 0 {
		t.Errorf("cleanup failures = %d", n)
	}
}

func TestSpecDraftWithdraw_CancelsTheSpecRunFirst(t *testing.T) { // AC-394
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	f.openSpecRun(t, "spec-1")
	// 取り消さないまま取り下げは成立しない。
	if _, err := withdrawForTest(t, f.s, f.id, "やめる"); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdraw with an open spec run = %v, want ErrConflict", err)
	}
	var statusAtCancel Status
	var draftAtCancel bool
	client := seClient{fakeContractClient: f.client, onCancel: func([]string) {
		statusAtCancel = f.show(t).Status
		draftAtCancel = scExists(t, filepath.Join(f.s.workspace, f.draft))
	}}
	n, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: f.id, AgentDecl: f.agent, ConnDecl: f.conn, Client: client})
	if err != nil || n != 1 {
		t.Fatalf("CancelOpenContractRuns = %d, %v", n, err)
	}
	if statusAtCancel != StatusClassified || !draftAtCancel {
		t.Errorf("at cancel: status = %s, draft exists = %v", statusAtCancel, draftAtCancel)
	}
	scWithdraw(t, f.s, f.id)
	scRequireExists(t, f.s, f.draft, false)
}

func TestSpecDraftWithdraw_LinkOutsideTheDraftDirectoryIsKept(t *testing.T) { // AC-395・AC-396
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	draft := filepath.Join(f.s.workspace, f.draft)
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.s.workspace, seOwnerSpec), draft); err != nil {
		t.Fatal(err)
	}
	scWithdraw(t, f.s, f.id)
	scRequireExists(t, f.s, seOwnerSpec, true)
	scRequireExists(t, f.s, f.draft, true)
	if n := len(scFailures(t, f.s, f.id)); n != 1 {
		t.Errorf("cleanup failures = %d, want 1", n)
	}
}
