package core

// このファイルは、本番反映の段（周の release の段と ID を指定しない `op exec`）が、承認待ち（pending）の
// release も読んで行う片付け（#485）を持つ:
//
//   - PR がマージ済みなら closed にする。理由は、直前の試行の結果が merged_unapproved なら merged_unapproved、
//     それ以外は merged_outside（flywheel の外でマージされた）。
//   - PR がマージされずに閉じられているときも closed にする（理由は pr_closed）。閉じた release は終端で、
//     PR が開き直されても承認待ちへ戻さない。開き直した PR の本番反映が要るときは、課題が完了でない間だけ、
//     人が `op add --kind release` で同じ ref の release を登録し直す（CreateOperation は重複を判定しない）。
//     委譲の照合（createOperation の skipDuplicate）は閉じた release を同じ ref の重複として数えて作らず、
//     課題が完了なら op add も ErrTerminalState で断られ、戻す道は無い（#539）。
//   - 課題が完了で、PR の head が J5 の検証の head と異なり、動いた中身を検証し直す必要があると確かに言えるとき
//     （取り込み以外のコミットが入った・履歴が書き換わった）は、課題を着手中へ戻して release を置き換える。
//     取り込みだけの変更は何もしない（承認の ① が取り込みだけを承認する）。取り込みだけとも中身の変更とも確かめられない
//     変更（比較の打ち切り・PR に head が無い等）は、何も書かず人間待ち（needs_human）として理由を出す。人が承認するか
//     着手中へ戻すかを選ぶ。取得の失敗・比較の口が無い場合は何もしない（次の周で読み直す）。
//     戻す判定は core の AssessReleaseReopen の 1 か所で、CLI の申告は使わない。
//
// 読み取りは GET だけで、取得に失敗した release は飛ばす（次の周で読み直す）。

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type pendingReleaseRef struct {
	opID        int64
	challengeID int64
	ref         string
}

// tidyPendingReleases は承認待ちの release を ID の昇順に読み、片付けた結果を返す。
func (s *Store) tidyPendingReleases(ctx context.Context, opts ReleaseExecOptions) ([]ReleaseTidyItem, error) {
	var pending []pendingReleaseRef
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, challenge_id, ref FROM operation WHERE kind = ? AND state = ? AND ref IS NOT NULL ORDER BY id ASC`,
			string(OperationKindRelease), string(OperationStatePending))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p pendingReleaseRef
			if err := rows.Scan(&p.opID, &p.challengeID, &p.ref); err != nil {
				return err
			}
			pending = append(pending, p)
		}
		return rows.Err()
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}
	if len(pending) == 0 {
		return nil, nil
	}
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = s.currentTime
	}

	var out []ReleaseTidyItem
	for _, p := range pending {
		repo, number, ok := parseReleasePullRequestRef(p.ref)
		if !ok {
			continue
		}
		pr, err := opts.Checks.GetPullRequestChecks(ctx, repo, number)
		if err != nil {
			continue
		}
		switch pr.State {
		case string(artifactStateMerged):
			item, err := s.closeMergedPendingRelease(ctx, actor, p, opts, now)
			if err != nil {
				return out, err
			}
			if item != nil {
				out = append(out, *item)
			}
		case string(artifactStateClosed):
			item, err := s.closePendingRelease(ctx, actor, p, opts, now, false)
			if err != nil {
				return out, err
			}
			if item != nil {
				out = append(out, *item)
			}
		case string(artifactStateOpen):
			item, err := s.reopenIfVerifiedContentChanged(ctx, p, pr, opts)
			if err != nil {
				return out, err
			}
			if item != nil {
				out = append(out, *item)
			}
		}
	}
	return out, nil
}

// closeMergedPendingRelease は、PR がマージ済みの承認待ちの release を closed にする（課題の版も 1 上げる）。
// 読んだ後で別の呼び出しが状態を変えていれば何もしない（nil）。
func (s *Store) closeMergedPendingRelease(ctx context.Context, actor string, p pendingReleaseRef, opts ReleaseExecOptions, now func() time.Time) (*ReleaseTidyItem, error) {
	return s.closePendingRelease(ctx, actor, p, opts, now, true)
}

// closePendingRelease は承認待ちの release を closed にする。merged が false なら、PR がマージされずに閉じられた
// （理由は pr_closed）。true なら理由は merged_outside か merged_unapproved。
func (s *Store) closePendingRelease(ctx context.Context, actor string, p pendingReleaseRef, opts ReleaseExecOptions, now func() time.Time, merged bool) (*ReleaseTidyItem, error) {
	var item *ReleaseTidyItem
	err := s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		rec.at = now()
		row, err := loadOperationRow(ctx, tx, p.opID)
		if err != nil {
			return err
		}
		if row.State != OperationStatePending {
			return nil
		}
		reason := ClosedReasonPRClosed
		if merged {
			reason = ClosedReasonMergedOutside
			if forbidden, err := reapprovalForbidden(ctx, tx, p.opID); err != nil {
				return err
			} else if forbidden {
				reason = ClosedReasonMergedUnapproved
			}
		}
		chal, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		nowStr := formatTimestamp(rec.at)
		res, err := tx.ExecContext(ctx,
			`UPDATE operation SET state = ?, version = ?, closed_reason = ?, closed_at = ? WHERE id = ? AND version = ? AND state = ?`,
			string(OperationStateClosed), row.Version+1, string(reason), nowStr, p.opID, row.Version, string(OperationStatePending))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("core: close pending release %s: expected to update 1 row, updated %d", formatOperationID(p.opID), n)
		}
		if err := rec.record("operation", p.opID, ActivityActionOpClose,
			map[string]any{"state": string(OperationStatePending)},
			map[string]any{"state": string(OperationStateClosed), "version": row.Version + 1, "closed_reason": string(reason), "trigger": opts.Trigger}); err != nil {
			return err
		}
		if err := bumpChallengeVersionForOperation(ctx, tx, rec, row.ChallengeID, chal.Version, p.opID, ActivityActionOpClose, nowStr, Attestation{}); err != nil {
			return err
		}
		item = &ReleaseTidyItem{OperationID: formatOperationID(p.opID), ChallengeID: formatChallengeID(row.ChallengeID), Action: ReleaseTidyClosed, ClosedReason: reason}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// reopenIfVerifiedContentChanged は、完了の課題の承認待ちの release の PR の head が J5 の検証の head と異なるとき、
// core が自分で比較して（AssessReleaseReopen）、動いた中身を検証し直す必要があると確かに言えれば課題を着手中へ戻し、
// 取り込みだけとも中身の変更とも確かめられなければ（比較の打ち切り・head 無し等）何も書かず人間待ち（needs_human）として返す。
func (s *Store) reopenIfVerifiedContentChanged(ctx context.Context, p pendingReleaseRef, pr UpstreamPullRequestChecks, opts ReleaseExecOptions) (*ReleaseTidyItem, error) {
	src := ReleaseHeadSources{Checks: opts.Checks, Compare: opts.Compare}
	opText := formatOperationID(p.opID)
	as, err := s.assessReleaseReopen(ctx, opText, src, &pr)
	if err != nil {
		return nil, err
	}
	switch as.Verdict {
	case ReleaseReopenNeedsHuman:
		return &ReleaseTidyItem{
			OperationID: opText, ChallengeID: formatChallengeID(p.challengeID), Action: ReleaseTidyNeedsHuman, WaitReason: as.Detail,
		}, nil
	case ReleaseReopenRequired:
		res, err := s.reopenChallengeForRelease(ctx, ChannelCLI, opText, ReopenReasonContentChanged, as)
		if err != nil {
			return nil, err
		}
		if !res.Reopened {
			return nil, nil
		}
		return &ReleaseTidyItem{
			OperationID: res.ClosedOperationID, ChallengeID: res.ChallengeID, Action: ReleaseTidyReopened,
			ReopenReason: ReopenReasonContentChanged, NewOperationID: res.NewOperationID,
		}, nil
	}
	return nil, nil
}
