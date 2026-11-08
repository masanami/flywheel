package core

// このファイルは、完了の照合（成果物の種類 pr）が取得の失敗で終わった課題の、次の周での照合の
// やり直し（#487・決定 2026-11-08 オーナー）を持つ。
//
// 取得の失敗の後、課題は着手中のまま、終了した委譲の run（結果 succeeded・結末 completed）だけを
// 持つ。そのまま委譲の対象に選ぶと新しい子を起動してしまうため、`run`（ID を省いた経路と ID を
// 指定した経路の両方）は、委譲の前にやり直しの対象を見分け、子を起動せず照合だけを行う:
//
//   - 対象: 照合の取得の失敗の連続回数が 1 以上で、最後の委譲の run が succeeded で終わっていて、
//     その run より後に保留が無い着手中の課題
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
	var branch sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT reconcile_fetch_failures, reconcile_fetch_branch FROM challenge WHERE id = ?`, cid).Scan(&n, &branch)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && n == 0) {
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
	if last.EndedAt == nil || last.Result != RunResultSucceeded {
		return nil, nil
	}
	for _, hd := range h.Holds {
		if holdIsAfterRun(hd, last) {
			return nil, nil
		}
	}
	return &pendingReconcileRetry{ChallengeID: cid, Run: last, Branch: branch.String}, nil
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
		// 前の照合のときに検査を通った報告が、宣言の変更で通らなくなった。やり直しの前提が崩れたので
		// 写さず、そのまま返す（回数は残し、課題は着手中のまま。人が状態を見て扱う）。
		return &JudgmentAutoItem{ChallengeID: ch.ID, RunID: p.Run.ID, Result: RunResultSucceeded,
			Note: "the reconciliation was not retried: the report of " + p.Run.ID + " no longer passes the check: " + reason}, nil, nil
	}

	rec := &reconciliation{}
	if p.Branch != "" && plausibleBranchName(p.Branch) {
		rec.Branch, rec.BranchFrom = p.Branch, "slot"
		if report.Branch != nil && strings.TrimSpace(*report.Branch) == p.Branch {
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

	status, note, err := s.applyReconciliation(ctx, dc, p.Run.ID, rec, RunResultSucceeded, report, in.CycleID)
	if err != nil {
		return nil, nil, err
	}
	msg := "retried the reconciliation of " + p.Run.ID + " (no delegation was launched)"
	if note != "" {
		msg += "; " + note
	}
	return &JudgmentAutoItem{ChallengeID: ch.ID, RunID: p.Run.ID, Result: RunResultSucceeded, Outcome: string(report.Outcome),
		Status: status, Note: msg}, nil, nil
}
