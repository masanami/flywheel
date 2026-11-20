package core

// このファイルは、統合先を使う課題のタスクの実行を、委譲の段の先頭の状態の確認が「統合先へ入れる」まで進める規則を持つ
// （docs/features/core-redesign.md §タスクの検証と、統合先へ入れる）。
//
//   - 検証した中身（verified_ref）を持つタスクの実行がレビュー待ちのとき（landVerifiedReview）: 中身が同じなら、チェックを確かめて
//     commands.resume で「入れてよい」（{choice} が land、{approved_ref} が検証した中身）を返す。「入れてよい」が届いたのに同じ中身が
//     戻れば land_refused の保留。中身が違えば、上流の比較の口で取り込みだけかを見分ける。
//   - succeeded を読んだとき（finishIntegrationLanding）: 成果物が統合先へ入っていて入った中身が land_ref と同じならタスクを済みにする。
//
// 反映の操作は作らず、オーナーの承認も求めない。チェックの分け方・比較の判定は、反映の操作の実行と共用する
// （summarizeChecks・classifyContentMove・approvedHeadChecks）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// integrationTaskRun は row が、統合先を使う課題のタスクの契約の実行か。
func (s *Store) integrationTaskRun(ctx context.Context, row *contractRunRow) (bool, error) {
	if row.Kind != contractRunKindTask || row.TaskID == nil {
		return false, nil
	}
	var integ bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		u, err := challengeUsesIntegration(ctx, tx, row.ChallengeID)
		integ = err == nil && u != nil && *u
		return err
	})
	return integ, classifyReadWriteErr(err)
}

// taskRemoteRepo は、タスクのリポジトリ（宣言の名前）の remote（"<owner>/<name>"）。引けなければ空。
func (s *Store) taskRemoteRepo(ctx context.Context, taskID *int64, decl *ConnectorsDeclaration) (string, error) {
	if taskID == nil || decl == nil {
		return "", nil
	}
	var name string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		qerr := tx.QueryRowContext(ctx, `SELECT repo FROM task WHERE id = ?`, *taskID).Scan(&name)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		return qerr
	})
	if err = classifyReadWriteErr(err); err != nil {
		return "", err
	}
	for i := range decl.Repos {
		if decl.Repos[i].Name == name {
			return decl.Repos[i].Remote, nil
		}
	}
	return "", nil
}

// loadContractRunRow は契約の実行 id の最新の行。
func (s *Store) loadContractRunRow(ctx context.Context, id int64) (*contractRunRow, error) {
	var cur *contractRunRow
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		cur, err = scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE id = ?`, id))
		return err
	})
	return cur, classifyReadWriteErr(err)
}

func checksFailedQuestion(runID, ref string) string {
	return fmt.Sprintf("接続ツールの run（%s）の、検証した中身（%s）のチェックに失敗がある。統合先へ入れずに止めた。", runID, ref) +
		"チェックを直してから回答すると、もう一度チェックを確かめて、通っていれば委譲先へ「入れてよい」を返す。課題を取り下げることもできる。"
}

func landRefusedQuestion(runID, ref, summary, text string) string {
	q := fmt.Sprintf("接続ツールの run（%s）へ「入れてよい」を返したが、同じ中身（%s）のレビュー待ちが戻った。委譲先は、検証した中身を統合先へ入れなかった。\n接続ツールの要約: %s", runID, ref, summary)
	if t := strings.TrimSpace(text); t != "" {
		q += "\n求められた操作: " + t
	}
	return q + "\n原因を直してから回答すると、もう一度「入れてよい」を返す。課題を取り下げることもできる。"
}

func landedEndedQuestion(runID, ref string, otherContent bool) string {
	if otherContent {
		return fmt.Sprintf("接続ツールの run（%s）が「入れてよい」を受け取って終わった（成功）。統合先へ入っているが、入った中身が、検証した中身（%s）と違う。", runID, ref) +
			"入った中身を確かめてから回答すると、このタスクを着手中へ戻して委譲し直す。課題を取り下げることもできる。"
	}
	return fmt.Sprintf("接続ツールの run（%s）が「入れてよい」を受け取って終わった（成功）が、成果物は統合先へ入っていない。", runID) +
		"回答すると、このタスクを着手中へ戻して委譲し直す。課題を取り下げることもできる。"
}

// landVerifiedReview は、統合先を使う課題のタスクの実行が、成果物つきのレビュー待ちを返した（状態の確認の読み rep）とき、
// 検証した中身を持つなら、上の規則で「入れてよい」を返す・保留にする・検証した中身を改める／消す。何も変えないときは rep をそのまま返す。
func (s *Store) landVerifiedReview(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, rep *ContractRunReport) (*ContractRunReport, error) {
	ra := rep.RequestedAction
	if ra == nil || ra.Kind != contractRequestedKindReview || !hasLandOption(ra.Options) {
		return rep, nil
	}
	got, n := reviewContentRefs(rep.Artifacts)
	if n != 1 {
		return rep, nil
	}
	cur, err := s.loadContractRunRow(ctx, row.ID)
	if err != nil || cur.VerifiedRef == "" {
		return rep, err // 検証した中身が無ければ、レビュー待ちでも「入れてよい」を返さない
	}
	scope, err := s.loadReviewIntakeScope(ctx, cur.ChallengeID, cur.ExternalRunID)
	if err != nil {
		return nil, err
	}
	if scope.TaskStatus != TaskStatusVerifying {
		return rep, nil
	}
	if inProgress, err := s.challengeInProgress(ctx, cur.ChallengeID); err != nil || !inProgress {
		return rep, err // 保留中の課題へは「入れてよい」を返さない
	}
	cur.reqFacts, cur.holdSuccessEnd = connector.RequiredFacts, true
	rs := in.releaseSource()
	hold := func(q string, reason holdReason) (*ContractRunReport, error) {
		held, err := s.holdContractReview(ctx, rep, q, reason)
		rep.Held = held
		return rep, err
	}
	previous := ""
	if got != cur.VerifiedRef {
		moved, err := s.settleMovedContent(ctx, rs, cur, rep, got)
		if err != nil || !moved {
			return rep, err
		}
		previous = cur.VerifiedRef
		cur.VerifiedRef = got
		cur.movedFrom = previous
	} else if cur.LandDelivered && cur.LandRef == got {
		return hold(landRefusedQuestion(cur.ExternalRunID, got, rep.Summary, ra.Text), holdReasonLandRefused)
	}
	verdict, err := s.checkVerifiedContent(ctx, rs, cur, previous)
	if err != nil {
		return nil, err
	}
	switch verdict {
	case checkVerdictFailed:
		return hold(checksFailedQuestion(cur.ExternalRunID, cur.VerifiedRef), holdReasonChecksFailed)
	case checkVerdictWait:
		return rep, nil
	}
	return s.sendLand(ctx, in, cur, connector, binding, planVersion, cycleID, rep)
}

// settleMovedContent は、レビュー待ちの中身 got が検証した中身と違うとき、上流の比較の口で見分ける。取り込みだけ（衝突の解消が無い）なら
// 検証した中身を got に改める前提で真を返す（検証し直さない。書くのは「入れてよい」を返すとき）。そうでなければ、検証した中身と land_ref を消し、land_delivered を偽にする（偽。
// タスクは検証中のままで、次の J5 が検証し直す）。取り込み元のブランチは成果物の宛先（統合先）。
func (s *Store) settleMovedContent(ctx context.Context, rs *contractReleaseSource, cur *contractRunRow, rep *ContractRunReport, got string) (bool, error) {
	repo, err := s.taskRemoteRepo(ctx, cur.TaskID, rs.Decl)
	if err != nil {
		return false, err
	}
	dest, err := s.challengeIntegrationRef(ctx, cur.ChallengeID)
	if err != nil {
		return false, err
	}
	if branches, _ := integrationArtifactRefs(rep.Artifacts); dest == "" && len(branches) == 1 {
		dest = branches[0]
	}
	mergeInOnly := false
	if repo != "" && dest != "" && rs.Compare != nil {
		mergeInOnly = classifyContentMove(ClassifyHeadChange(context.WithoutCancel(ctx), rs.Compare, repo, dest, cur.VerifiedRef, got)) == contentMoveMergeInOnly
	}
	if mergeInOnly {
		return true, nil // 検証した中身の改めは、チェックを通って「入れてよい」を返すときに書く（待つ間は前の中身を残す）
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = NULL, land_ref = NULL, land_delivered = 0 WHERE id = ? AND verified_ref = ?`, cur.ID, cur.VerifiedRef)
		return err
	})
	return false, classifyReadWriteErr(err)
}

// checkVerdict は検証した中身のチェックの確かめの結果。
type checkVerdict int

const (
	checkVerdictPass checkVerdict = iota
	checkVerdictFailed
	// checkVerdictWait は、終わっていないチェックがある・確かめられない（何も返さず待つ）。
	checkVerdictWait
)

// checkVerifiedContent は検証した中身（cur.VerifiedRef）のチェックを、上流の口で確かめる。チェックが 0 件のときの扱いは、反映の操作の
// 実行と同じ（previous が空でなければ、取り込みで動いた後の中身なので、前の中身にもチェックが 0 件だったと確かめられたときだけ通す）。
func (s *Store) checkVerifiedContent(ctx context.Context, rs *contractReleaseSource, cur *contractRunRow, previous string) (checkVerdict, error) {
	cs, ok := rs.Checks.(UpstreamCommitChecksSource)
	if !ok {
		return checkVerdictWait, nil
	}
	repo, err := s.taskRemoteRepo(ctx, cur.TaskID, rs.Decl)
	if err != nil || repo == "" {
		return checkVerdictWait, err
	}
	checks, err := cs.GetCommitChecks(context.WithoutCancel(ctx), repo, cur.VerifiedRef)
	if err != nil {
		return checkVerdictWait, nil // 読めなかった。次の状態の確認でやり直す
	}
	failed, pending := summarizeChecks(checks)
	switch {
	case failed:
		return checkVerdictFailed, nil
	case pending:
		return checkVerdictWait, nil
	}
	if len(checks) == 0 && previous != "" {
		approved := &approvedHeadChecks{}
		if none, _ := approved.noChecks(context.WithoutCancel(ctx), rs.Checks, repo, previous); !none {
			return checkVerdictWait, nil
		}
	}
	return checkVerdictPass, nil
}

// sendLand は「入れてよい」を commands.resume で返す。呼ぶ前に通す中身（land_ref）を書き、届いた記録（land_delivered）は resume が
// 成功してから真にする。resume が失敗したときは、保留にせず、次の機会に同じ「入れてよい」を返し直す。
func (s *Store) sendLand(ctx context.Context, in ContractPollInput, cur *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, rep *ContractRunReport) (*ContractRunReport, error) {
	command, err := fillContractArgsWith(connector.Commands.Resume, contractFill{
		ChallengeID: rep.ChallengeID, Source: binding, EmptySource: startTakesTaskFile(connector), RunID: cur.ExternalRunID,
		Answer: contractAnswer{Choice: contractChoiceLand}, ApprovedRef: cur.VerifiedRef,
	})
	if err != nil {
		return nil, err
	}
	// 読んだ後で課題が保留になった・検証した中身が変わったときは、返さない。
	from := cur.VerifiedRef
	if cur.movedFrom != "" {
		from = cur.movedFrom
	}
	ready := false
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = ?, land_ref = ?, land_delivered = 0 WHERE id = ? AND verified_ref = ?
			AND EXISTS (SELECT 1 FROM challenge WHERE id = challenge_id AND status = 'in_progress')`, cur.VerifiedRef, cur.VerifiedRef, cur.ID, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		ready = n == 1
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, classifyReadWriteErr(err)
	}
	if !ready {
		return rep, nil
	}
	cur.LandRef = cur.VerifiedRef
	rrep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, existingContractWorkDir(cur, in.WorkDir), cur, "resume", command, planVersion, cycleID, "")
	if err != nil {
		return nil, err
	}
	if rrep.Result != RunResultSucceeded {
		return rrep, nil // 届いたか分からない。届いた記録は付けず、次の機会に返し直す
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET land_delivered = 1 WHERE id = ? AND land_ref = ?`, cur.ID, cur.VerifiedRef)
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, classifyReadWriteErr(err)
	}
	// 「入れてよい」を受け付けた委譲先は、終わるまで running を返す。すぐ終わって succeeded が返ったときだけ、読んだときと同じに扱う
	// （waiting が返っても、その周では判定しない。同じ中身のレビュー待ちが次の周に戻れば、そこで land_refused になる）。
	if rrep.State == ContractStateSucceeded {
		if err := s.afterContractRead(ctx, rrep, connector, in.releaseSource()); err != nil {
			return nil, err
		}
	}
	return rrep, nil
}

// landedContent は、統合先へ入った中身の確かめの結果。
type landedContent int

const (
	// landedUnreadable は、上流から読めなかった（何も書かず、次の状態の確認でやり直す）。
	landedUnreadable landedContent = iota
	landedNotIn
	landedSameContent
	landedOtherContent
)

// landedContent は arts の pr のうち、branch（統合先）へマージ済みの PR を上流から読み、入っているか・入った中身（PR の head）が
// landRef と同じかを返す。
func (rs *contractReleaseSource) landedContent(ctx context.Context, arts []ContractArtifact, branch, landRef string) landedContent {
	if rs == nil || rs.Reconcile == nil || rs.Checks == nil || branch == "" {
		return landedUnreadable
	}
	for _, a := range arts {
		pr, ok, err := rs.lookupArtifactPR(ctx, a)
		if err != nil {
			return landedUnreadable
		}
		if !ok || pr.State != string(artifactStateMerged) || pr.Base != branch {
			continue
		}
		repo, number, parsed := parseReleasePullRequestRef(pr.URL)
		if !parsed {
			return landedUnreadable
		}
		full, err := rs.Checks.GetPullRequestChecks(ctx, repo, number)
		if err != nil || full.HeadSHA == "" {
			return landedUnreadable
		}
		if full.HeadSHA == landRef {
			return landedSameContent
		}
		return landedOtherContent
	}
	return landedNotIn
}

// finishIntegrationLanding は、「入れてよい」を返し済みのタスクの実行（land_ref がある）の succeeded を読んだとき、成果物を上流から読んで確かめる。
// 入っていて同じ中身ならタスクを済みにし、契約の実行を終わった記録にし、統合先の中身の識別を成果物の記録に残す（課題の状態は変えない）。
// 入っていなければ delegate_ended、中身が違えば merged_unverified で保留にする（タスクは済みにしない。保留にできたら契約の実行を終わった記録にする）。
// 上流から読めなければ何も書かない。
func (s *Store) finishIntegrationLanding(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource, cid int64, landRef, recorded string) error {
	scope, err := s.loadReviewIntakeScope(ctx, cid, rep.ExternalRunID)
	if err != nil {
		return err
	}
	if scope.TaskID == nil || scope.TaskStatus == TaskStatusCancelled {
		return s.closeContractRunRow(ctx, cid, rep.ExternalRunID) // 取りやめのタスクの実行の終わりでは保留にしない
	}
	if recorded == "" {
		if branches, _ := integrationArtifactRefs(rep.Artifacts); len(branches) == 1 {
			recorded = branches[0]
		}
	}
	switch rs.landedContent(context.WithoutCancel(ctx), rep.Artifacts, recorded, landRef) {
	case landedUnreadable:
		return nil
	case landedSameContent:
		return s.completeIntegrationLanding(ctx, rep, cid, scope)
	case landedNotIn:
		return s.holdLandedEnd(ctx, rep, cid, landedEndedQuestion(rep.ExternalRunID, landRef, false), holdReasonDelegateEnded)
	}
	return s.holdLandedEnd(ctx, rep, cid, landedEndedQuestion(rep.ExternalRunID, landRef, true), holdReasonMergedUnverified)
}

func (s *Store) holdLandedEnd(ctx context.Context, rep *ContractRunReport, cid int64, question string, reason holdReason) error {
	held, err := s.holdContractReview(ctx, rep, question, reason)
	rep.Held = held
	if err != nil || !held {
		return err
	}
	return s.closeContractRunRow(ctx, cid, rep.ExternalRunID)
}

// closeContractRunRow は課題 cid の契約の実行 externalRunID を、終わった記録にする。
func (s *Store) closeContractRunRow(ctx context.Context, cid int64, externalRunID string) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ? WHERE challenge_id = ? AND external_run_id = ? AND ended_at IS NULL`,
			formatTimestamp(s.currentTime()), cid, externalRunID)
		return err
	})
	return classifyReadWriteErr(err)
}

// completeIntegrationLanding はタスクを済みにし、契約の実行を終わった記録にし、出力の統合先の中身の識別（役割が統合先の commit）を、
// この読みの run の成果物に残す（同じトランザクション）。
func (s *Store) completeIntegrationLanding(ctx context.Context, rep *ContractRunReport, cid int64, scope reviewIntakeScope) error {
	runInt, ok := parseRunID(rep.RunID)
	if !ok {
		return nil
	}
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	wctx := context.WithoutCancel(ctx)
	return s.mutateAsRun(wctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		ctx := wctx
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ? WHERE challenge_id = ? AND external_run_id = ? AND ended_at IS NULL`,
			formatTimestamp(s.currentTime()), cid, rep.ExternalRunID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // 別の周が先に終えた
		}
		if _, err := setTaskStatus(ctx, tx, rec, *scope.TaskID, TaskStatusDone); err != nil {
			return err
		}
		verifiedAt := s.currentTime()
		for _, a := range rep.Artifacts {
			if a.Kind != "commit" || a.Role != contractRoleIntegration {
				continue
			}
			var have int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_artifact WHERE run_id = ? AND kind = 'commit' AND ref = ? AND role = ?`,
				runInt, a.Ref, a.Role).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				continue
			}
			if _, err := insertRunArtifact(ctx, tx, runInt, insertRunArtifactInput{Kind: artifactKindCommit, Ref: a.Ref, Role: a.Role, VerifiedAt: &verifiedAt}); err != nil {
				return err
			}
		}
		return nil
	})
}

// resumeIntegrationTaskAfterAnswer は、統合先へ入れる間の保留（checks_failed・land_refused・delegate_ended・merged_unverified。
// どれもタスクに結び付く）への回答で課題が着手中へ戻るとき、そのタスクの、終わっていない契約の実行の通す中身（land_ref）を消し、
// 届いた記録（land_delivered）を偽にする（検証した中身は残す。次の状態の確認が、決まりどおりに進め直す）。ほかのタスクの実行と、
// 反映の依頼の記録は変えない。delegate_ended・merged_unverified では、タスクの契約の実行がもう終わっているなら、タスクを着手中へ戻す
// （次の委譲の段が委譲し直す）。
func resumeIntegrationTaskAfterAnswer(ctx context.Context, tc *transitionCtx, reason holdReason, taskID int64) error {
	switch reason {
	case holdReasonChecksFailed, holdReasonLandRefused, holdReasonDelegateEnded, holdReasonMergedUnverified:
	default:
		return nil
	}
	if _, err := tc.tx.ExecContext(ctx, `UPDATE contract_run SET land_ref = NULL, land_delivered = 0
		WHERE challenge_id = ? AND task_id = ? AND kind = 'task' AND ended_at IS NULL`, tc.id, taskID); err != nil {
		return err
	}
	if reason != holdReasonDelegateEnded && reason != holdReasonMergedUnverified {
		return nil
	}
	var open int
	if err := tc.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND task_id = ? AND kind = 'task' AND ended_at IS NULL`,
		tc.id, taskID).Scan(&open); err != nil || open > 0 {
		return err
	}
	var st string
	if err := tc.tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, taskID).Scan(&st); err != nil {
		return err
	}
	if TaskStatus(st) == TaskStatusDone || TaskStatus(st) == TaskStatusCancelled {
		return nil
	}
	_, err := setTaskStatus(ctx, tc.tx, tc.rec, taskID, TaskStatusInProgress)
	return err
}
