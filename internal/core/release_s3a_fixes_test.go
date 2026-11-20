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

// 別の反映の操作が終わりを読み、結果を書くまで開けている実行（終わりの状態で ended_at が無い）を、対応しない反映の操作は、
// unsupported（承認を取り消す結果）にせず、待つ。
func TestS3aFix_AHeldEndedRunOfASiblingReleaseIsWaitedFor(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	row := &operationRow{ID: f.opID, ChallengeID: f.cid}
	run := &releaseRun{row: &contractRunRow{ExternalRunID: "ext-1", State: ContractStateSucceeded}, corresponds: false}
	j, err := f.s.chooseReleasePath(context.Background(), row, f.opts(), run, nil, "p", "o")
	if err != nil {
		t.Fatal(err)
	}
	if j.result != ExecutionResultWaitingDelegate {
		t.Errorf("result = %s, want waiting_delegate", j.result)
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
