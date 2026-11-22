package core

// このファイルは、完了の照合（成果物の種類 pr）が取得の失敗で終わった課題の、次の周での照合の
// やり直し（#487・決定 2026-11-08 オーナー）を持つ。
//
// 取得の失敗の後、課題は着手中のまま、終了した委譲の run（結果 succeeded・結末 completed）だけを
// 持つ。そのまま委譲の対象に選ぶと新しい子を起動してしまうため、`run`（ID を省いた経路と ID を
// 指定した経路の両方）は、委譲の前にやり直しの対象を見分け、子を起動せず照合だけを行う:
//
//   - 対象: 照合の取得の失敗の連続回数が 1 以上、または「照合をやり直す」印（スーパーバイザーの
//     reverify が付ける reconcile_supervisor_action_id）があり、照合した run（reconcile_fetch_run_id）が
//     最後の委譲の run で、succeeded で終わっていて、その run より後に保留も J5 の run も無い着手中の
//     課題（人が提出して J5 が差し戻した・回答で再開した課題は、通常の起動の判定に任せる）。
//     印のある課題は、その印の対処の記録が閉じた保留を「その run より後の保留」に数えない
//   - 調べる先: 最後の委譲の run の報告（ブランチ・pr_urls）と、前の照合で調べたブランチ
//     （challenge.reconcile_fetch_branch。報告にブランチが無くスロットのブランチを調べた場合）
//   - 記録: 見つかった成果物は、その run の成果物として足す（同じ種類・参照の行が既にあれば足さない）
//   - 写像: applyReconciliation（照合の最初の回と同じ規則。回数の増減もそこで行う）

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// pendingReconcileRetry は照合のやり直しの対象の課題の、やり直す run と前の照合で調べたブランチ。
type pendingReconcileRetry struct {
	ChallengeID int64
	Run         runRow
	Branch      string
}

// loadPendingReconcileRetry は課題 cid が照合のやり直しの対象なら、その内容を返す（対象でなければ nil）。
func loadPendingReconcileRetry(ctx context.Context, tx *sql.Tx, cid int64) (*pendingReconcileRetry, error) {
	var n int
	var runID, mark sql.NullInt64
	var branch sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT reconcile_fetch_failures, reconcile_fetch_run_id, reconcile_fetch_branch, reconcile_supervisor_action_id FROM challenge WHERE id = ?`, cid).
		Scan(&n, &runID, &branch, &mark)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ((n == 0 && !mark.Valid) || !runID.Valid)) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h, err := loadLaunchHistory(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if len(h.Runs) == 0 {
		return nil, nil
	}
	last := h.Runs[len(h.Runs)-1]
	if runIntID(last) != runID.Int64 || last.EndedAt == nil || last.Result != RunResultSucceeded {
		return nil, nil
	}
	for _, v := range h.Verifications {
		if runIntID(v) > runID.Int64 {
			return nil, nil
		}
	}
	closedByMark, err := holdsClosedByAction(ctx, tx, cid, mark)
	if err != nil {
		return nil, err
	}
	for _, hd := range h.Holds {
		if closedByMark[hd.ID] {
			continue
		}
		if holdIsAfterRun(hd, last) {
			return nil, nil
		}
	}
	return &pendingReconcileRetry{ChallengeID: cid, Run: last, Branch: branch.String}, nil
}

// holdsClosedByAction は課題 cid の保留のうち、対処の記録 action が閉じた（hold.supervisor_action_id が action の）ものの ID の集合を返す。
// action が無ければ空。
func holdsClosedByAction(ctx context.Context, tx *sql.Tx, cid int64, action sql.NullInt64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if !action.Valid {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM hold WHERE challenge_id = ? AND supervisor_action_id = ?`, cid, action.Int64)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// clearReconcileSupervisorMark は課題の「照合をやり直す」印を NULL に戻す（印が無ければ何もしない）。
// 照合の結果を写す書き込み（検証中へ進む・保留を作る・取得の失敗を数える）と同じトランザクションで呼ぶ。
func clearReconcileSupervisorMark(ctx context.Context, tx *sql.Tx, challengeID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE challenge SET reconcile_supervisor_action_id = NULL WHERE id = ? AND reconcile_supervisor_action_id IS NOT NULL`, challengeID)
	return err
}

// splitReconcileRetries は委譲の対象の課題の ID を、照合のやり直しの対象とそれ以外に分ける
// （どちらも ids の順を保つ）。
func (s *Store) splitReconcileRetries(ctx context.Context, ids []int64) (retries []*pendingReconcileRetry, rest []int64, err error) {
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		for _, cid := range ids {
			p, err := loadPendingReconcileRetry(ctx, tx, cid)
			if err != nil {
				return err
			}
			if p != nil {
				retries = append(retries, p)
			} else {
				rest = append(rest, cid)
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}
	return retries, rest, nil
}

// retryReconciliation は課題 ch の照合だけをやり直す（子を起動しない）。宣言・計画が委譲の前提を
// 満たさなければ（ErrValidation）notStarted（plan_unavailable）を返す。
func (s *Store) retryReconciliation(ctx context.Context, in DelegateInput, ch Challenge, p *pendingReconcileRetry) (*JudgmentAutoItem, *NotStarted, error) {
	dc, err := s.loadDelegationContext(ctx, in, ch)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedPlanUnavailable, Detail: err.Error()}, nil
		}
		return nil, nil, err
	}
	report, reason := checkDelegationReport([]byte(p.Run.Output), in.ConnDecl.HumanQuestionKinds)
	if reason != "" || report == nil {
		// 前の照合のときに検査を通った報告が、宣言の変更で通らなくなった。照合をやり直せないので、
		// 着手中のまま残さず人間対応待ちにする（回数は戻さない）。
		q := "照合をやり直せない（取得の失敗の後、またはスーパーバイザーが照合のやり直しを選んだ後）: 委譲の run " + p.Run.ID + " の報告が現在の宣言の検査を通らない（" + reason + "）。\n" +
			"子は完了を報告したが、成果物の有無を確かめられていない（宣言の成果物の種類: " + dc.Operation.Artifacts + "）。"
		status, note, err := s.mapReconciliationStop(ctx, dc, p.Run.ID, q, holdReasonArtifactMissing, in.CycleID, false, StopKindArtifactMissing)
		if err != nil {
			return nil, nil, err
		}
		return &JudgmentAutoItem{ChallengeID: ch.ID, RunID: p.Run.ID, Result: RunResultSucceeded, Status: status,
			Note: "the reconciliation could not be retried: the report of " + p.Run.ID + " no longer passes the check" + map[bool]string{true: "; " + note, false: ""}[note != ""]}, nil, nil
	}

	rec := &reconciliation{}
	// 調べるブランチは、前の照合で調べたブランチ（reconcile_fetch_branch）。「見つからない」で保留になった照合の後は
	// この欄が消えているので、最初の照合と同じに、報告のブランチから決め直す（スロットは解放済みで、作業ツリーのブランチは読めない）。
	branch, from := p.Branch, "slot"
	if branch == "" && report.Branch != nil {
		branch, from = strings.TrimSpace(*report.Branch), "report"
	}
	if branch != "" && plausibleBranchName(branch) {
		rec.Branch, rec.BranchFrom = branch, from
		if report.Branch != nil && strings.TrimSpace(*report.Branch) == branch {
			rec.BranchFrom = "report"
		}
		lookupBranch(ctx, in, dc.Repo.Remote, rec)
	}
	if report.Outcome == DelegationOutcomeCompleted && dc.Operation.Artifacts == "pr" {
		s.gatherReportedPRs(ctx, in, dc, rec, report.PRURLs)
	}

	runIDInt, _ := parseRunID(p.Run.ID)
	verifiedAt := s.currentTime()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, a := range rec.artifactInputs(&verifiedAt) {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_artifact WHERE run_id = ? AND kind = ? AND ref = ?`,
				runIDInt, string(a.Kind), a.Ref).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if _, err := insertRunArtifact(ctx, tx, runIDInt, a); err != nil {
				return err
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}

	status, note, err := s.applyReconciliation(ctx, dc, p.Run.ID, rec, RunResultSucceeded, report, in.CycleID, nil)
	if err != nil {
		return nil, nil, err
	}
	// 印は、結果を写す書き込みの中で消える。結果を写さなかった経路（課題が着手中でなくなっていた・
	// 検証の対象でない報告）でも、同じ印で次の周にやり直し続けないよう、ここで消す。
	if err := s.db.Write(ctx, func(tx *sql.Tx) error { return clearReconcileSupervisorMark(ctx, tx, p.ChallengeID) }); classifyReadWriteErr(err) != nil {
		return nil, nil, classifyReadWriteErr(err)
	}
	msg := "retried the reconciliation of " + p.Run.ID + " (no delegation was launched)"
	if note != "" {
		msg += "; " + note
	}
	return &JudgmentAutoItem{ChallengeID: ch.ID, RunID: p.Run.ID, Result: RunResultSucceeded, Outcome: string(report.Outcome),
		Status: status, Note: msg}, nil, nil
}
