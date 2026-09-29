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

// readValues は markReadWithValues が「読んだ時点の値」として書く値
// （#85・決定 M3P14。J2 の直前の取得で得たコメント数と更新日時の文字列）。
type readValues struct {
	CommentsCount     int
	UpstreamUpdatedAt string
}

// markReadOptions は markReadWithValues の任意の指定。ゼロ値は MarkRead と同じ
// （ストアの観測値でそろえ、run に紐づけない）。
type markReadOptions struct {
	// RunID が非空なら、作業ログの原因の run（"R-<n>"。判断の呼び出し）として
	// 記録する（activity.run_id。#85: J2 が付けた読んだ記録を、人間の mark-read
	// と区別できるようにする）。形式が不正なら ErrValidation。経路が
	// ChannelInvoker でなければ ErrValidation（run に由来する変更は経路 invoker で
	// だけ記録する＝transitionAsInvoker と同じ規則）。
	RunID string
	// Values が非 nil なら、ストアの観測値ではなくこの値を読んだ時点の値として
	// 書く（取得と記録の間に取り込まれたコメントを読まずに既読にしない＝
	// QH14）。CommentsCount が負なら ErrValidation。呼び出し元が取得した値を
	// 渡す経路（invoker）だけの機能なので、経路が ChannelInvoker でなければ
	// ErrValidation（人間の経路〈cli〉は、ストアの観測値でそろえる MarkRead だけ）。
	Values *readValues
}

// MarkRead は id の課題の「読んだ時点の値」を、ストアにある現在の観測値で
// 上書きする（§上流の更新の観測と既読）。actor は OS のログインユーザー名、
// 経路は ch、本人確認の方式は VerificationNone（QH13: mark-read は本人確認の
// ない単発の操作）。
func (s *Store) MarkRead(ctx context.Context, ch Channel, id string) (*MarkReadResult, error) {
	return s.markReadWithValues(ctx, ch, id, markReadOptions{})
}

// markReadWithValues は MarkRead に、読んだ時点の値と原因の run の ID を渡す形を
// 足したもの（#85・決定 M3P14「core の mark-read の API に値を渡す形を足す」）。
// 拒否の判定順・「値が同じなら何も書かない」規則は MarkRead と同じ。
func (s *Store) markReadWithValues(ctx context.Context, ch Channel, id string, opt markReadOptions) (*MarkReadResult, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	if (opt.RunID != "" || opt.Values != nil) && ch != ChannelInvoker {
		return nil, ErrValidation
	}
	var runID *int64
	if opt.RunID != "" {
		n, ok := parseRunID(opt.RunID)
		if !ok {
			return nil, ErrValidation
		}
		runID = &n
	}
	if opt.Values != nil && opt.Values.CommentsCount < 0 {
		return nil, ErrValidation
	}
	actor, err := resolveActor()
	if err != nil {
		return nil, err
	}

	var result MarkReadResult
	err = s.mutateAsRun(ctx, actor, ch, VerificationNone, runID, func(tx *sql.Tx, rec *activityRecorder) error {
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
		// 書く値: 既定はストアの観測値。opt.Values があればそれ（J2 の直前の
		// 取得で得た値。ストアの観測値は使わない＝QH14）。
		targetComments := sb.CommentsCount
		targetUpdatedAt := sb.UpstreamUpdatedAt
		if opt.Values != nil {
			targetComments = opt.Values.CommentsCount
			targetUpdatedAt = opt.Values.UpstreamUpdatedAt
		}
		// 読んだ時点の値が書く値とすべて同じときだけ、何も変えずに成功する
		// （AC-142）。未読の更新（unreadKinds）では判定しない: コメントの削除で
		// 件数が減っただけの課題も、値が違えばそろえる（AC-134・AC-135 を優先
		// する 2026-09-27 オーナー決定）。
		if sb.ReadCommentsCount == targetComments && sb.ReadUpstreamUpdatedAt == targetUpdatedAt {
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
		if sb.ReadCommentsCount != targetComments {
			before["read_comments_count"] = sb.ReadCommentsCount
			after["read_comments_count"] = targetComments
		}
		if sb.ReadUpstreamUpdatedAt != targetUpdatedAt {
			before["read_upstream_updated_at"] = nullableTextColumn(sb.ReadUpstreamUpdatedAt)
			after["read_upstream_updated_at"] = nullableTextColumn(targetUpdatedAt)
		}
		after["version"] = newVersion

		newReadComments := targetComments
		newReadUpstreamUpdatedAt := targetUpdatedAt
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
