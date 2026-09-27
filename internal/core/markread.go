// このファイルは `flywheel mark-read <C-ID>`（親要件チケット #51
// docs/features/m2-github-issue-ingest.md §上流の更新の観測と既読）の core の
// 公開 API（Store.MarkRead）を持つ。読んだ時点の値を、ストアにある現在の観測値
// で上書きするだけで、上流は取り直さない（`gh` を呼ばない＝QH13）。
//
// 拒否の判定順は、既存の EditChallenge（存在→終端状態→検証）に倣う:
//  1. ID の形式が不正・課題が存在しない → ErrNotFound
//  2. 課題が完了している → ErrTerminalState（QP9 と同じく、完了した課題への
//     操作は他の検証より優先して拒否する）
//  3. 対応（source_binding）が無い → ErrValidation（【仮定】。§受入基準
//     「対応の無い課題への mark-read は validation_failed」）
//
// 読んだ時点の値が現在の観測値と 1 つでも違えば、必ず観測値へそろえる。未読の
// 更新（unreadKinds）が無くても、コメントの削除で件数が減っただけの課題は値が
// 違うのでそろえる（2026-09-27 オーナー決定・PR #74。読んだ時点の件数が大きい
// まま残ると、その後に足されたコメントに upstream_commented が出ないため）。
// 値がすべて同じときだけ、何も書き込まず（版・作業ログを変えない）成功する
// （M1「値が変わらない操作は作業ログを残さない」と同じ規則＝AC-142）。

package core

import (
	"context"
	"database/sql"
	"fmt"
)

// MarkReadResult は Store.MarkRead の結果（§IF / API「`mark-read` の `--json`」）。
type MarkReadResult struct {
	ChallengeID string
	// Changed は読んだ時点の値を実際に変えたか（読んだ時点の値が観測値と
	// すべて同じなら false）。
	Changed bool
	// SourceBinding は操作後の対応の記録（`show` と同じ形）。
	SourceBinding *SourceBinding
}

// MarkRead は id の課題の「読んだ時点の値」を、ストアにある現在の観測値で
// 上書きする（§上流の更新の観測と既読）。actor は OS のログインユーザー名、
// 経路は ch、本人確認の方式は VerificationNone（QH13: mark-read は本人確認の
// ない単発の操作）。
func (s *Store) MarkRead(ctx context.Context, ch Channel, id string) (*MarkReadResult, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var result MarkReadResult
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}
		sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		if sb == nil {
			return ErrValidation
		}

		result.ChallengeID = current.ID
		// 読んだ時点の値が観測値とすべて同じときだけ、何も変えずに成功する
		// （AC-142）。未読の更新（unreadKinds）では判定しない: コメントの削除で
		// 件数が減っただけの課題も、値が違えばそろえる（AC-134・AC-135 を優先
		// する 2026-09-27 オーナー決定）。
		if sb.ReadCommentsCount == sb.CommentsCount && sb.ReadUpstreamUpdatedAt == sb.UpstreamUpdatedAt {
			result.Changed = false
			result.SourceBinding = sb.toPublic()
			return nil
		}

		newVersion := current.Version + 1
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			newVersion, formatTimestamp(rec.at), cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: mark-read: update challenge %s: expected to update 1 row, updated %d", id, affected)
		}

		before := map[string]any{}
		after := map[string]any{}
		if sb.ReadCommentsCount != sb.CommentsCount {
			before["read_comments_count"] = sb.ReadCommentsCount
			after["read_comments_count"] = sb.CommentsCount
		}
		if sb.ReadUpstreamUpdatedAt != sb.UpstreamUpdatedAt {
			before["read_upstream_updated_at"] = nullableTextColumn(sb.ReadUpstreamUpdatedAt)
			after["read_upstream_updated_at"] = nullableTextColumn(sb.UpstreamUpdatedAt)
		}
		after["version"] = newVersion

		newReadComments := sb.CommentsCount
		newReadUpstreamUpdatedAt := sb.UpstreamUpdatedAt
		_, next, err := applySourceBindingUpdate(ctx, tx, rec.at, cid, updateSourceBindingInput{
			ReadCommentsCount:     &newReadComments,
			ReadUpstreamUpdatedAt: &newReadUpstreamUpdatedAt,
		})
		if err != nil {
			return err
		}

		if err := rec.record("challenge", cid, "upstream_read", before, after); err != nil {
			return err
		}

		result.Changed = true
		result.SourceBinding = next.toPublic()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
