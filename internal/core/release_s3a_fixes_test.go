package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S3a の仕上げ（全体のレビューの指摘）: 反映の操作のために呼んだ契約のコマンドの実行の印・終わりを読んだ契約の実行の持ち越し・
// 完了から戻す書き込みの確かめ・作業用のクローンが使用中のときの始めなかった理由・契約の引数の差し込みを実ストアで固定する。

func (f *landFixture) j5Artifacts(t *testing.T) (artifacts int, lastRunID string) {
	t.Helper()
	vc, err := f.s.loadJ5Context(context.Background(), f.detail(t, f.cid).Challenge)
	if err != nil {
		t.Fatal(err)
	}
	if vc.LastDelegate == nil {
		t.Fatal("loadJ5Context: LastDelegate = nil")
	}
	return len(vc.Artifacts), vc.LastDelegate.ID
}

// 反映の段が契約のコマンドを呼んだ実行は、反映の操作の印（run.operation_id）が付く。付かない実行（start・委譲の段の読み）とは区別できる。
func TestS3aFix_A_TheReleaseStageMarksTheRunsItCreates(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE kind = 'contract' AND operation_id = 1`); n < 2 {
		t.Errorf("marked contract runs = %d, want at least 2 (the status read and the land resume)", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE kind = 'contract' AND operation_id IS NULL`); n < 1 {
		t.Errorf("unmarked contract runs = %d, want the runs of the delegation stage to stay unmarked", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run r WHERE r.operation_id IS NOT NULL AND EXISTS (SELECT 1 FROM run_artifact a WHERE a.run_id = r.id)`); n != 0 {
		t.Errorf("marked runs with artifacts = %d, want none", n)
	}
}

// 完了の課題のタスクの実行がレビュー待ちの間に反映の段が状態を読んだ後でも、完了の検証（J5）の入力の成果物は、直前の委譲の実行が
// 取り込んだ成果物のままである。
func TestS3aFix_A_J5InputKeepsTheArtifactsOfTheLastDelegationRunAfterTheReleaseStageRead(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	before, beforeRun := f.j5Artifacts(t)
	if before == 0 {
		t.Fatal("the fixture's last delegation run has no artifacts")
	}
	f.sendLand(t)
	if after, afterRun := f.j5Artifacts(t); after != before || afterRun != beforeRun {
		t.Errorf("J5 input after the release stage: %d artifacts of %s, want %d of %s", after, afterRun, before, beforeRun)
	}
}

// 反映の操作の印が付いた、失敗した契約のコマンドの実行は、回収された start として保留にしない。印の無い同じ実行は保留にする。
func TestS3aFix_A_AMarkedFailedRunIsNotAReclaimedStart(t *testing.T) {
	t.Parallel()
	for _, marked := range []bool{true, false} {
		s := newStoreForTest(t)
		c := createAndAdvance(t, s, StatusInProgress)
		cid := mustParseChallengeID(t, c.ID)
		opID := coretest.InsertApprovedRelease(t, s.workspace, int(cid), relRef, relHead, execTS(0))
		var op any
		if marked {
			op = opID
		}
		writeForTest(t, s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, operation_id)
				VALUES (?, 'contract', 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'failed', 1000000, 'impl', ?)`, cid, op)
			return err
		})
		var active bool
		readForTest(t, s, func(tx *sql.Tx) (err error) {
			active, err = challengeHasActiveRun(context.Background(), tx, cid)
			return err
		})
		n, err := s.HoldUnrecordedContractStops(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if marked && (n != 0 || active) {
			t.Errorf("marked: held %d, active %v, want neither", n, active)
		}
		if !marked && (n != 1 || !active) {
			t.Errorf("unmarked: held %d, active %v, want a hold (control)", n, active)
		}
	}
}

// B: 対応するタスクの実行は、その契約の実行が始まってからの実行が取り込んだ成果物だけで決める。終わった前の契約の実行の成果物は見ない。
func TestS3aFix_B_AnEndedEarlierContractRunsArtifactDoesNotMakeALaterRunCorrespond(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	c := createAndAdvance(t, s, StatusDone)
	cid := mustParseChallengeID(t, c.ID)
	insertRunWithPR := func(pr string) int64 {
		var id int64
		writeForTest(t, s, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
				VALUES (?, 'contract', 1, 's', 1, 'h', 'x', 'x', 'x', 'succeeded', 1000000, 'impl')`, cid)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
			_, err = tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref, state, base) VALUES (?, 'pr', ?, 'open', 'main')`, id, pr)
			return err
		})
		return id
	}
	first := insertRunWithPR("o/r#1")
	insertContract := func(start int64, ext string, ended bool) {
		writeForTest(t, s, func(tx *sql.Tx) error {
			e := any(nil)
			if ended {
				e = "x"
			}
			_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at, ended_at)
				VALUES (?, 'tool', ?, ?, 'waiting', 's', 1000000, 'x', 'x', ?)`, cid, ext, start, e)
			return err
		})
	}
	insertContract(first, "ext-1", true)
	second := insertRunWithPR("o/r#2")
	insertContract(second, "ext-2", false)
	var cr *contractRunRow
	readForTest(t, s, func(tx *sql.Tx) (err error) {
		cr, err = scanContractRun(tx.QueryRow(contractRunSelect + ` WHERE external_run_id = 'ext-2'`))
		return err
	})
	readForTest(t, s, func(tx *sql.Tx) error {
		if got, err := taskRunImportedRef(context.Background(), tx, cr, "o/r#1"); err != nil || got {
			t.Errorf("the ended earlier run's PR: corresponds = %v (err %v), want false", got, err)
		}
		if got, err := taskRunImportedRef(context.Background(), tx, cr, "o/r#2"); err != nil || !got {
			t.Errorf("the current run's PR: corresponds = %v (err %v), want true", got, err)
		}
		return nil
	})
}

// C: 反映の段が終わりを読んだ試行が upstream_failed になっても、次の反映の段は同じ終わりを使う（反映の依頼を新しく始めない）。
func TestS3aFix_C_AnEndReadByAnUpstreamFailedAttemptIsUsedByTheNextStage(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	if r := f.only(t); r.ended {
		t.Fatalf("contract run = %+v, want it kept open until the result is written", r)
	}
	startsBefore := len(f.client.starts)
	f.fake.getErr = nil
	f.merged()
	if item := f.one(t); item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed (the delegate succeeded and the approved content is in)", item)
	}
	if len(f.client.starts) != startsBefore {
		t.Errorf("starts = %d, want no new reflect request", len(f.client.starts))
	}
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended after the result is written", r)
	}
}

func TestS3aFix_C_AnEndReadByAnUpstreamFailedAttemptWithoutTheArtifactIsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	startsBefore := len(f.client.starts)
	f.fake.getErr = nil
	if item := f.one(t); item.Result != ExecutionResultDelegateEnded {
		t.Fatalf("item = %+v, want delegate_ended", item)
	}
	if len(f.client.starts) != startsBefore {
		t.Errorf("starts = %d, want no new reflect request", len(f.client.starts))
	}
	f.wantOpState(t, "pending")
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended after the cancel-class result is written", r)
	}
}

// C: 「入れてよい」を返した commands.resume の応答がすでに終わりだったとき、次の反映の段は landed を出す。
func TestS3aFix_C_AResumeResponseThatWasAlreadyEndedIsReadByTheNextStage(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	f.client.resumeOut = contractOut(ContractStateSucceeded, nil, nil)
	if item := f.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if r := f.only(t); r.ended {
		t.Fatalf("contract run = %+v, want it kept open after the ended resume response", r)
	}
	startsBefore := len(f.client.starts)
	f.client.queue = nil
	f.client.last = contractOut(ContractStateSucceeded, nil, nil)
	f.merged()
	if item := f.one(t); item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed", item)
	}
	if len(f.client.starts) != startsBefore {
		t.Errorf("starts = %d, want no new reflect request", len(f.client.starts))
	}
}

// D: 完了から戻す書き込みは、課題に終わっていない実行の記録があるとき・反映の操作に終わっていない試行があるとき、何も書かず競合で終わる。
func TestS3aFix_D_ReopenWritesNothingWhileARunOrAnAttemptIsUnfinished(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(f *relFixture, t *testing.T)
	}{
		{"unfinished_run", func(f *relFixture, t *testing.T) {
			coretest.InsertRun(t, f.s.workspace, coretest.InsertRunInput{
				ChallengeID: int(f.cid), Kind: "contract", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
				PID: 1, Host: "h", HeartbeatAt: execTS(0), StartedAt: execTS(0), MaxBudgetUSD: 1_000_000, BudgetBucket: "impl",
			})
		}},
		{"unfinished_attempt", func(f *relFixture, t *testing.T) {
			coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{StartedAt: execTS(0), ApprovedHead: relHead})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newRelFixture(t)
			f.setVerifiedHead(t, "v")
			f.fake.pr.HeadSHA = "hc"
			tc.setup(f, t)
			before := snapshotReopen(t, f.s, f.cid)
			res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
				OperationID: formatOperationID(f.opID), Reason: ReopenReasonContentChanged,
				Sources: ReleaseHeadSources{Checks: f.fake, Compare: compareContentChanged()},
			})
			if err == nil || res != nil || !strings.Contains(err.Error(), "unfinished") {
				t.Fatalf("res = %+v, err = %v, want a conflict", res, err)
			}
			if after := snapshotReopen(t, f.s, f.cid); !reflect.DeepEqual(after, before) {
				t.Errorf("the store changed: before %+v, after %+v", before, after)
			}
		})
	}
}

// E: 作業用のクローンが使用中で空きが無いだけのときは、始めなかった理由 slot_unavailable（承認を保つ）。宣言が決めようが無いときは unsupported。
func TestS3aFix_E_AllClonesBusyIsSlotUnavailableAndKeepsTheApproval(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.conn.Repos[0].Slots = ConnectorRepoSlots{Provider: "clone", Paths: []string{"clone1"}}
	if err := f.s.EnsureSlots(context.Background(), f.conn.Repos[0]); err != nil {
		t.Fatal(err)
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE slot SET state = 'busy'`)
		return err
	})
	res := f.exec(t)
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
	f.wantNotStarted(t, res, NotStartedSlotUnavailable)
	f.wantOpState(t, "approved")
	if at := f.attempts(t); len(at) != 0 {
		t.Errorf("attempts = %+v, want no row", at)
	}
}

// E: 同時に走らせる数の確かめは、作業ディレクトリを決める処理より先に行う（上限に達していれば、決めようが無い宣言でも slot_unavailable）。
func TestS3aFix_E_TheParallelLimitIsCheckedBeforeTheWorkingDirectoryIsDecided(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.conn.Repos[0].Slots = ConnectorRepoSlots{Provider: "clone"}
	f.agent.MaxParallelRuns = 1
	other := createAndAdvance(t, f.s, StatusInProgress)
	insertOpenContractRun(t, f.s, mustParseChallengeID(t, other.ID), "other-1")
	f.wantNotStarted(t, f.exec(t), NotStartedSlotUnavailable)
	f.wantOpState(t, "approved")
}

// F: 契約の引数の差し込みは 1 回の置き換えにまとめる。{run_id} に入った値の中の {approved_ref} は置き換えない。
func TestS3aFix_F_APlaceholderInsideASubstitutedValueIsNotSubstitutedAgain(t *testing.T) {
	t.Parallel()
	got, err := fillContractArgsWith([]string{"tool", "resume", "{run_id}", "--ref", "{approved_ref}"},
		contractFill{ChallengeID: "C-1", RunID: "r{approved_ref}", ApprovedRef: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "r{approved_ref}"; got[2] != want || got[4] != "abc" {
		t.Errorf("args = %q, want {run_id} = %q (not re-substituted) and {approved_ref} = abc", got, want)
	}
	got, err = fillContractArgsWith([]string{"{note}-{challenge_id}-{issue_number}"},
		contractFill{ChallengeID: "C-1", Answer: contractAnswer{Note: "{challenge_id}"}, Source: &sourceBinding{ExternalKey: "o/r#7", URL: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "{challenge_id}-C-1-7" {
		t.Errorf("args = %q, want the note's braces left alone", got)
	}
}

// 別の反映の操作が終わりを読み、結果を書くまで開けている実行（終わりの状態で ended_at が無い）を、持ち主の操作が承認済みなら、
// 対応しない反映の操作は、unsupported（承認を取り消す結果）にせず、待つ。契約の実行の行と持ち主の操作はストアにある。
func TestS3aFix_AHeldEndedRunOfASiblingReleaseIsWaitedFor(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.holdEnd(t) // 持ち主（f.opID。承認済み）の反映の依頼が、終わり（succeeded）で ended_at が空のまま残る
	siblingID := coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), "o/other#9", relHead, execTS(0))
	var row *operationRow
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		row, err = loadOperationRow(context.Background(), tx, siblingID)
		return err
	})
	run, err := f.s.loadReleaseRun(context.Background(), row)
	if err != nil || run == nil || run.corresponds || !run.row.State.Terminal() {
		t.Fatalf("loadReleaseRun() = %+v, %v, want a held ended run that does not correspond to the sibling", run, err)
	}
	j, err := f.s.chooseReleasePath(context.Background(), row, f.opts(), run, nil, "p", "o")
	if err != nil {
		t.Fatal(err)
	}
	if j.result != ExecutionResultWaitingDelegate {
		t.Errorf("result = %s, want waiting_delegate", j.result)
	}
	if r := f.only(t); r.ended {
		t.Errorf("contract run = %+v, want it still held open for its owner", r)
	}
}

// 反映の段が終わりを読んで開けていた契約の実行は、その場で終わる呼び出し（完了から戻す前の読み）が終わった記録にする。
func TestS3aFix_C_AHeldEndedRunIsEndedByTheReadBeforeReopen(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	if r := f.only(t); r.ended {
		t.Fatalf("contract run = %+v, want it held open", r)
	}
	run := &releaseRun{opID: f.opID}
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		run, err = loadOpenRunInTx(context.Background(), tx, f.cid)
		return err
	})
	run.opID = f.opID
	opts := f.opts()
	if _, _, err := f.s.releaseContractCommand(context.Background(), opts, run, "status", contractFill{}, false); err != nil {
		t.Fatal(err)
	}
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended by the non-holding read", r)
	}
}

// heldReaders は、ended_at IS NULL を見る読み手が、課題 cid の契約の実行をどう読むかをまとめて返す。
type heldReaders struct {
	parallel  int  // 同時に走らせる数（activeDelegationCount）
	active    bool // 課題の実行中の判定（challengeHasActiveRun）
	open      bool // HasOpenContractRun（取り下げ・計画の作り直しの前の確かめ）
	withdraw  bool // 取り下げの確かめ（hasOpenContractRunRow）
	polled    int  // 委譲の段が読む行（課題が完了でないタスクの実行）
	unbindErr bool // 子の記録の付け替えの確かめ（checkUnbindSafeForChildren）
	cycleWork int  // サイクルの仕事の有無（終わっていない契約の実行の数）
}

func readHeld(t *testing.T, s *Store, cid int64) heldReaders {
	t.Helper()
	var r heldReaders
	readForTest(t, s, func(tx *sql.Tx) (err error) {
		ctx := context.Background()
		if r.parallel, err = activeDelegationCount(ctx, tx); err != nil {
			return err
		}
		if r.active, err = challengeHasActiveRun(ctx, tx, cid); err != nil {
			return err
		}
		if r.withdraw, err = hasOpenContractRunRow(ctx, tx, cid); err != nil {
			return err
		}
		r.unbindErr = checkUnbindSafeForChildren(ctx, tx, cid, formatChallengeID(cid)) != nil
		if err = tx.QueryRow(`SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL AND kind = 'task' AND challenge_id NOT IN (SELECT id FROM challenge WHERE status = 'done')`).Scan(&r.polled); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL`).Scan(&r.cycleWork)
	})
	var err error
	if r.open, err = s.HasOpenContractRun(context.Background(), formatChallengeID(cid)); err != nil {
		t.Fatal(err)
	}
	return r
}

// holdEnd は反映の依頼を始めて「入れてよい」を返し、上流を読めない試行で委譲先の終わり（succeeded）を読ませる
// （契約の実行は state が終わりで、ended_at は空のまま）。
func (f *reflFixture) holdEnd(t *testing.T) {
	t.Helper()
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	if r := f.only(t); r.ended || r.state != string(ContractStateSucceeded) {
		t.Fatalf("contract run = %+v, want a succeeded run held open", r)
	}
	f.fake.getErr = nil
}

// C: 終わりを開けている間、ended_at IS NULL を見る読み手は、仕様どおり「終わっていない実行」と読む（次の反映の段が結果を書くまで）。
// 結果を書く前に操作が承認済みでなくなり、試行が競合（ErrConflict）で閉じたら、その実行は終わった記録になり、
// どの読み手も走っている実行と読まない。書き出しと再構築の後も同じ。
func TestS3aFix_C_AHeldEndIsClosedWhenTheAttemptEndsInConflictAfterTheApprovalIsGone(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.holdEnd(t)
	if got := readHeld(t, f.s, f.cid); !got.open || !got.withdraw || got.cycleWork != 1 || got.polled != 0 {
		t.Fatalf("while held: %+v, want the run read as unfinished and not read by the delegation stage", got)
	}
	f.merged()
	revoked := false
	f.fake.onGet = func() {
		if revoked {
			return
		}
		revoked = true
		// 上流の読みの最中に、別の経路が承認を取り消す（版も進む）。結果を書く書き込みは競合で終わる。
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET state = 'pending', version = version + 1 WHERE id = ?`, f.opID)
			return err
		})
	}
	res := f.exec(t)
	if !revoked {
		t.Fatal("the upstream read hook did not run")
	}
	f.wantNotStarted(t, res, NotStartedInProgress)
	if r := f.only(t); !r.ended {
		t.Fatalf("contract run = %+v, want ended (no approved operation is left to write a result)", r)
	}
	want := heldReaders{}
	if got := readHeld(t, f.s, f.cid); got != want {
		t.Errorf("after the conflict: %+v, want no reader to see a running run", got)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, f.s)); err != nil {
		t.Fatal(err)
	}
	if got := readHeld(t, dst, f.cid); got != want {
		t.Errorf("after export and import: %+v, want no reader to see a running run", got)
	}
}

// C: 競合で試行が閉じても、操作が承認済みのままなら、終わりを開けたままにする（次の反映の段が同じ終わりを使って landed を出す）。
func TestS3aFix_C_AHeldEndStaysOpenAfterAConflictWhileTheOperationIsStillApproved(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.holdEnd(t)
	f.merged()
	bumped := false
	f.fake.onGet = func() {
		if bumped {
			return
		}
		bumped = true
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET version = version + 1 WHERE id = ?`, f.opID)
			return err
		})
	}
	f.wantNotStarted(t, f.exec(t), NotStartedInProgress)
	if r := f.only(t); r.ended {
		t.Fatalf("contract run = %+v, want it still held open (the operation is still approved)", r)
	}
	starts := len(f.client.starts)
	if item := f.one(t); item.Result != ExecutionResultLanded || len(f.client.starts) != starts {
		t.Fatalf("item = %+v starts %d, want landed without a new reflect request", item, len(f.client.starts))
	}
}

// 別の反映の操作が開けている終わりの実行: 持ち主が承認済みの間は待ち、持ち主が承認済みでなくなったら、対応しない操作は
// 待ち続けずにその実行を閉じて、実行が無いときの道（反映の依頼を始める）へ進む。
func TestS3aFix_AHeldEndOfASiblingWhoseApprovalIsGoneIsClosedAndNotWaitedForForever(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.otherOperationRequest(t)
	// 持ち主（別の反映の操作）が終わりを読んで、結果を書く前の状態。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET state = 'succeeded' WHERE challenge_id = ? AND ended_at IS NULL`, f.cid)
		return err
	})
	starts := len(f.client.starts)
	if item := f.one(t); item.Result != ExecutionResultWaitingDelegate || len(f.client.starts) != starts {
		t.Fatalf("item = %+v, want waiting_delegate while the owner is approved", item)
	}
	if rows := f.rows(t); len(rows) != 1 || rows[0].ended {
		t.Fatalf("rows = %+v, want the owner's run still held open", rows)
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending', version = version + 1 WHERE challenge_id = ? AND id <> ?`, f.cid, f.opID)
		return err
	})
	f.client.startOut = contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "ext-2" })
	if item := f.one(t); item.Result != ExecutionResultReflectStarted {
		t.Fatalf("item = %+v, want reflect_started (the stale run is closed, not waited for)", item)
	}
	rows := f.rows(t)
	if len(rows) != 2 || !rows[0].ended || rows[1].ended || rows[1].opID.Int64 != f.opID {
		t.Fatalf("rows = %+v, want the stale run ended and a new request for this operation", rows)
	}
	if len(f.client.cancels) != 0 {
		t.Errorf("cancels = %v, want no command sent to the ended run", f.client.cancels)
	}
}

// A: 実装枠の打ち切りの読み（最後に終わった実装枠の実行が budget_exhausted か）は、反映の操作のために呼んだ実行を読まない。
func TestS3aFix_A_TheImplStopIsReadFromTheLastDelegationRunNotAReleaseStageRun(t *testing.T) {
	t.Parallel()
	for _, marked := range []bool{true, false} {
		s := newStoreForTest(t)
		c := createAndAdvance(t, s, StatusInProgress)
		cid := mustParseChallengeID(t, c.ID)
		opID := coretest.InsertApprovedRelease(t, s.workspace, int(cid), relRef, relHead, execTS(0))
		var op any
		if marked {
			op = opID
		}
		writeForTest(t, s, func(tx *sql.Tx) error {
			const ins = `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, operation_id)
				VALUES (?, 'contract', 1, 1, 's', 1, 'h', 'x', ?, ?, ?, 1000000, 'impl', ?)`
			if _, err := tx.Exec(ins, cid, "2026-11-01T00:00:00.000Z", "2026-11-01T00:00:01.000Z", "budget_exhausted", nil); err != nil {
				return err
			}
			_, err := tx.Exec(ins, cid, "2026-11-01T00:00:02.000Z", "2026-11-01T00:00:03.000Z", "succeeded", op)
			return err
		})
		var sp bucketSpend
		readForTest(t, s, func(tx *sql.Tx) (err error) {
			sp, err = loadBucketSpend(context.Background(), tx, cid, 1)
			return err
		})
		if sp.ImplStopped != marked {
			t.Errorf("marked=%v: ImplStopped = %v, want %v", marked, sp.ImplStopped, marked)
		}
	}
}
