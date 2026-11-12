package core

// このファイルは、完了の承認まで済んだ課題の release が本番反映でマージできなくなったとき、課題を完了から
// 着手中へ戻して検証を通って立て直す書き込み（docs/features/m4.5-learning-and-self-improvement.md
// §本番反映の立て直し。遷移表の T18）を持つ。
//
// 呼べるのは ReopenChallengeForRelease（と同じ処理を本番反映の書き込みのトランザクションの中で呼ぶ
// reopenForReleaseInTx）だけで、CLI のコマンドは無い。完了した課題をほかの理由で戻す口にはしない:
// 戻す理由は ReopenReason の 3 値（マージできない・連続の上限・検証の後の中身の変更）に限る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ReopenForReleaseInput は ReopenChallengeForRelease の入力。
type ReopenForReleaseInput struct {
	// OperationID は立て直す release（"OP-<n>"）。
	OperationID string
	// Reason は戻す理由（ReopenReason の値）。閉集合外は ErrValidation。
	Reason ReopenReason
}

// ReopenForReleaseResult は ReopenChallengeForRelease の結果。
type ReopenForReleaseResult struct {
	// Reopened は課題を完了から着手中へ戻し、release を置き換えたかどうか。戻さなかった（課題が完了でない・
	// 操作が承認待ちか承認済みの release でない）ときは false で、何も書かない。
	Reopened    bool
	ChallengeID string
	// ClosedOperationID は取り下げた古い release、NewOperationID は作った新しい release（戻さなかったときは空）。
	ClosedOperationID string
	NewOperationID    string
}

// ReopenChallengeForRelease は opID の release の課題が完了なら、同じトランザクションで次を行う（T18）:
// 課題を着手中へ戻し（版 +1）、有効な前の完了の承認に「置き換えられた」日時と理由を記録し、古い release を
// closed（理由 superseded）にし、同じ種類・要約・参照の未承認の release を 1 件作って古い操作と結び、
// 戻した記録（challenge_reopen）と作業ログを残す。
//
// 課題が完了でない・操作が release でない・状態が pending か approved でないときは、何も書かず
// Reopened=false を返す。reason が閉集合外なら ErrValidation、opID が存在しなければ ErrNotFound。
// 新しい release は、戻した課題が検証を通って次の完了の承認を受けるとき、D12 で同時に承認される。
func (s *Store) ReopenChallengeForRelease(ctx context.Context, ch Channel, in ReopenForReleaseInput) (*ReopenForReleaseResult, error) {
	if _, ok := ParseReopenReason(string(in.Reason)); !ok {
		return nil, ErrValidation
	}
	opID, ok := parseOperationID(in.OperationID)
	if !ok {
		return nil, ErrNotFound
	}
	actor, err := resolveActor()
	if err != nil {
		return nil, err
	}
	var res *ReopenForReleaseResult
	err = s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		res, err = reopenForReleaseInTx(ctx, tx, rec, opID, in.Reason, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// reopenForReleaseInTx は ReopenChallengeForRelease の本体。呼び出し元のトランザクションの中で動く
// （本番反映の段は、試行を閉じる書き込みと同じトランザクションで呼ぶ）。
// extra は課題の作業ログの after に足す項目（本番反映の段が試行の結果・契機を載せる。nil 可）。
func reopenForReleaseInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, opID int64, reason ReopenReason, extra map[string]any) (*ReopenForReleaseResult, error) {
	row, err := loadOperationRow(ctx, tx, opID)
	if err != nil {
		return nil, err
	}
	res := &ReopenForReleaseResult{ChallengeID: formatChallengeID(row.ChallengeID), ClosedOperationID: formatOperationID(opID)}
	if row.Kind != string(OperationKindRelease) || (row.State != OperationStateApproved && row.State != OperationStatePending) {
		return res, nil
	}
	current, err := loadChallenge(ctx, tx, row.ChallengeID)
	if err != nil {
		return nil, err
	}
	tr, ok := LookupRestricted(current.Status, OpReopenForRelease)
	if !ok {
		return res, nil
	}

	nowStr := formatTimestamp(rec.at)

	// 同じ課題のほかの承認待ち・承認済みの release も置き換える（前の完了の承認が置き換えられるので、
	// その承認で同時に承認された release を、課題が着手中の間に実行させない）。実行の試行が終わっていない
	// release は、その試行の書き込みと衝突させないために置き換えない。新しい操作を作る前に選ぶ。
	siblings, err := siblingReleasesToSupersede(ctx, tx, row.ChallengeID, opID)
	if err != nil {
		return nil, err
	}
	newID, err := supersedeReleaseInTx(ctx, tx, rec, row, opID, nowStr)
	if err != nil {
		return nil, err
	}
	var siblingNew []string
	for _, sid := range siblings {
		srow, err := loadOperationRow(ctx, tx, sid)
		if err != nil {
			return nil, err
		}
		nid, err := supersedeReleaseInTx(ctx, tx, rec, srow, sid, nowStr)
		if err != nil {
			return nil, err
		}
		siblingNew = append(siblingNew, formatOperationID(nid))
	}

	// 有効な前の完了の承認を「置き換えられた」にする（置き換えた承認は、次の完了の承認が成立したときに結ぶ）。
	var supersededApproval sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT MAX(id) FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ? AND superseded_at IS NULL`,
		row.ChallengeID, string(ApprovalKindCompletion), string(ApprovalDecisionApproved)).Scan(&supersededApproval)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE approval SET superseded_at = ?, superseded_reason = ? WHERE challenge_id = ? AND kind = ? AND decision = ? AND superseded_at IS NULL`,
		nowStr, string(reason), row.ChallengeID, string(ApprovalKindCompletion), string(ApprovalDecisionApproved)); err != nil {
		return nil, err
	}

	var approvalArg any
	if supersededApproval.Valid {
		approvalArg = supersededApproval.Int64
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, superseded_approval_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		row.ChallengeID, string(reason), opID, newID, approvalArg, nowStr); err != nil {
		return nil, err
	}

	// 課題を着手中へ戻す。
	newVersion := current.Version + 1
	upd, err := tx.ExecContext(ctx,
		`UPDATE challenge SET status = ?, version = ?, updated_at = ? WHERE id = ? AND version = ? AND status = ?`,
		string(tr.Target.Fixed), newVersion, nowStr, row.ChallengeID, current.Version, string(current.Status))
	if err != nil {
		return nil, err
	}
	if n, err := upd.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, fmt.Errorf("core: reopen for release: expected to reopen challenge %s, updated %d rows", res.ChallengeID, n)
	}

	newOpID := formatOperationID(newID)
	challengeAfter := map[string]any{
		"status": string(tr.Target.Fixed), "version": newVersion, "reason": string(reason),
		"operation_id": formatOperationID(opID), "new_operation_id": newOpID,
	}
	if len(siblingNew) > 0 {
		challengeAfter["superseded_sibling_new_operation_ids"] = siblingNew
	}
	for k, v := range extra {
		challengeAfter[k] = v
	}
	if err := rec.record("challenge", row.ChallengeID, string(OpReopenForRelease),
		map[string]any{"status": string(current.Status)}, challengeAfter); err != nil {
		return nil, err
	}

	res.Reopened = true
	res.NewOperationID = newOpID
	return res, nil
}

// supersedeReleaseInTx は release 1 件（row。状態は pending か approved）を closed（理由 superseded）にし、
// 同じ種類・要約・参照の未承認の release を作って古い操作と結び、両方の作業ログを残す。新しい操作の内部 ID を返す。
func supersedeReleaseInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, row *operationRow, opID int64, nowStr string) (int64, error) {
	ins, err := tx.ExecContext(ctx,
		`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, supersedes_operation_id) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		row.ChallengeID, row.Kind, row.Summary, nullableNullString(row.Ref), string(OperationStatePending), nowStr, opID)
	if err != nil {
		return 0, err
	}
	newID, err := ins.LastInsertId()
	if err != nil {
		return 0, err
	}
	oldRes, err := tx.ExecContext(ctx,
		`UPDATE operation SET state = ?, version = ?, closed_reason = ?, closed_at = ? WHERE id = ? AND version = ? AND state = ?`,
		string(OperationStateClosed), row.Version+1, string(ClosedReasonSuperseded), nowStr, opID, row.Version, string(row.State))
	if err != nil {
		return 0, err
	}
	if n, err := oldRes.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, fmt.Errorf("core: reopen for release: expected to close operation %s, updated %d rows", formatOperationID(opID), n)
	}
	newOpID := formatOperationID(newID)
	if err := rec.record("operation", opID, ActivityActionOpClose,
		map[string]any{"state": string(row.State)},
		map[string]any{"state": string(OperationStateClosed), "version": row.Version + 1, "closed_reason": string(ClosedReasonSuperseded), "superseded_by": newOpID}); err != nil {
		return 0, err
	}
	var refVal any
	if row.Ref.Valid {
		refVal = row.Ref.String
	}
	if err := rec.record("operation", newID, "op_add", nil, map[string]any{
		"operation_id": newOpID, "kind": row.Kind, "summary": row.Summary, "ref": refVal,
		"state": string(OperationStatePending), "supersedes_operation_id": formatOperationID(opID),
	}); err != nil {
		return 0, err
	}
	return newID, nil
}

// siblingReleasesToSupersede は課題 cid の、exceptOpID 以外の承認待ち・承認済みの release のうち、終わっていない
// 実行の試行が無いものの内部 ID を昇順に返す。
func siblingReleasesToSupersede(ctx context.Context, tx *sql.Tx, cid, exceptOpID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?) AND id <> ? ORDER BY id ASC`,
		cid, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved), exceptOpID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []int64
	for _, id := range ids {
		last, err := latestOperationExecution(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if last != nil && last.open() {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// nullableNullString は sql.NullString を SQL のバインド値（NULL 相当の nil か文字列）にする。
func nullableNullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// linkSupersededApprovals は、本番反映のやり直しで置き換えられた前の完了の承認（置き換えた承認がまだ無いもの）に、
// いま成立した完了の承認 newApprovalID を結ぶ。完了の承認（T13・T14）の書き込みと同じトランザクションで呼ぶ。
func linkSupersededApprovals(ctx context.Context, tx *sql.Tx, challengeID, newApprovalID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE approval SET superseded_by_approval_id = ?
		 WHERE challenge_id = ? AND kind = ? AND superseded_at IS NOT NULL AND superseded_by_approval_id IS NULL AND id <> ?`,
		newApprovalID, challengeID, string(ApprovalKindCompletion), newApprovalID)
	return err
}

// labelReopenReason は委譲のブリーフを書く判断点（J3）の入力で、完了から戻した理由を渡すデータの区画の名前。
const labelReopenReason = "本番反映のやり直し"

// reopenNote は、課題を完了から着手中へ戻した記録のうち、その課題の最後の委譲より新しいもの。
type reopenNote struct {
	Reason ReopenReason
	// Ref は立て直す release の参照（PR の URL。参照が無い release は空）。
	Ref string
}

// loadReopenNote は、課題の最後の戻した記録が、その課題の最後の委譲の run の開始より新しいときだけ、その記録を返す
// （委譲の run が無ければ、戻した記録があれば返す）。追従の委譲が一度起動した後は返さない。
func loadReopenNote(ctx context.Context, tx *sql.Tx, challengeID int64) (*reopenNote, error) {
	var reason string
	var ref sql.NullString
	var createdAt string
	err := tx.QueryRowContext(ctx,
		`SELECT r.reason, o.ref, r.created_at FROM challenge_reopen r JOIN operation o ON o.id = r.operation_id
		 WHERE r.challenge_id = ? ORDER BY r.id DESC LIMIT 1`, challengeID).Scan(&reason, &ref, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lastRun sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(started_at) FROM run WHERE challenge_id = ? AND kind IN ('delegate', 'contract')`, challengeID).Scan(&lastRun); err != nil {
		return nil, err
	}
	if lastRun.Valid && lastRun.String >= createdAt {
		return nil, nil
	}
	return &reopenNote{Reason: ReopenReason(reason), Ref: ref.String}, nil
}

// formatReopenSection は J3 の入力の区画「本番反映のやり直し」の本文。課題が完了の承認を受けた後に本番反映の段で
// 着手中へ戻されたこと、戻した理由と PR、委譲先にやらせること（既定ブランチへ追従し、検査を通し、同じ PR へ
// 反映する。新しい PR を作らない）を書く。
func formatReopenSection(n *reopenNote, defaultBranch string) string {
	pr := "この課題の PR"
	if n.Ref != "" {
		pr = "PR " + n.Ref
	}
	var b strings.Builder
	b.WriteString("この課題は完了の承認を受けたが、本番反映の段で着手中へ戻された。\n")
	switch n.Reason {
	case ReopenReasonContentChanged:
		fmt.Fprintf(&b, "理由: 検証の後に、%s へ既定ブランチ（%s）の取り込み以外の変更が入った。入った変更を含めた中身を、もう一度検証する。\n", pr, defaultBranch)
	case ReopenReasonLimitReached:
		fmt.Fprintf(&b, "理由: 本番反映の段で、%s が既定ブランチ（%s）へマージできない状態が連続の上限に達した。\n", pr, defaultBranch)
	default:
		fmt.Fprintf(&b, "理由: 本番反映の段で、%s が既定ブランチ（%s）へマージできなくなった。\n", pr, defaultBranch)
	}
	fmt.Fprintf(&b, "やること: 作業ブランチを既定ブランチ（%s）へ追従させ、衝突があれば解消し、リポジトリの検査（テスト等）を通して、同じ PR へ反映する。新しい PR を作らない。", defaultBranch)
	return b.String()
}
