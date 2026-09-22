package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// timestampLayout は RFC3339・UTC・ミリ秒固定の文字列表現（ストアへの保存形）。
// internal/cli.FormatTimestamp と同じ書式だが、core は cli に依存できないため
// （依存方向は cli → core）ここに正本を複製する。
const timestampLayout = "2006-01-02T15:04:05.000Z"

// formatTimestamp は t を RFC3339・UTC・ミリ秒固定の文字列にする。
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timestampLayout)
}

// parseTimestamp は formatTimestamp の逆変換。
func parseTimestamp(s string) (time.Time, error) {
	return time.Parse(timestampLayout, s)
}

// activityRow は activity テーブルへ INSERT する 1 行分のデータ。Before・After
// は marshalDiffField が返す any（string または nil）。
type activityRow struct {
	At           string
	Actor        string
	Channel      string
	Verification string
	Entity       string
	EntityID     int64
	Action       string
	Before       any
	After        any
}

// defaultInsertActivity は activity テーブルへ 1 行 INSERT する既定の実装。
// Store.insertActivity が nil のときに使う。
func defaultInsertActivity(tx *sql.Tx, row activityRow) error {
	_, err := tx.Exec(
		`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.At, row.Actor, row.Channel, row.Verification, row.Entity, row.EntityID, row.Action, row.Before, row.After,
	)
	return err
}

// marshalDiffField は m（変わった項目だけの map）を JSON オブジェクト文字列に
// する。m が空（nil または要素数0）なら nil を返す（SQL の NULL に写像される。
// H7: 変更前が無い create の before、変更の無い項目は含めない）。
func marshalDiffField(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Activity は作業ログの 1 件（§データモデル activity）。
type Activity struct {
	At           time.Time
	Actor        string
	Channel      string
	Verification string
	Entity       string
	EntityID     string // 表示形（"C-<n>" 等）
	Action       string
	Before       json.RawMessage // nil 可
	After        json.RawMessage // nil 可
}

// formatEntityID は entity の種類に応じて entityID を表示形にする。
// #9 の時点で activity へ書き込むのは challenge だけだが、後続チケット
// （#13 の operation 承認など）が同じ activity テーブルへ operation エントリを
// 足すことを見込み、既知の entity 種別は切り分けておく。
func formatEntityID(entity string, id int64) string {
	switch entity {
	case "challenge":
		return formatChallengeID(id)
	case "operation":
		return formatOperationID(id)
	default:
		return fmt.Sprintf("%d", id)
	}
}

// ListActivities は作業ログを古い順（activity.id 昇順）で返す。challengeID が
// 非 nil なら、その課題（entity="challenge"）のエントリだけに絞る。
// challengeID が指定され、対応する課題が存在しなければ ErrNotFound を返す
// （形式不正の ID も含め fail-closed）。
func (s *Store) ListActivities(ctx context.Context, challengeID *string) ([]Activity, error) {
	var cid *int64
	if challengeID != nil {
		id, ok := parseChallengeID(*challengeID)
		if !ok {
			return nil, ErrNotFound
		}
		cid = &id
	}

	var result []Activity
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if cid != nil {
			if _, err := loadChallenge(ctx, tx, *cid); err != nil {
				return err
			}
		}

		query := `SELECT at, actor, channel, verification, entity, entity_id, action, before, after FROM activity`
		args := []any{}
		if cid != nil {
			query += ` WHERE entity = ? AND entity_id = ?`
			args = append(args, "challenge", *cid)
		}
		query += ` ORDER BY id ASC`

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var (
				atStr, actor, channel, verification, entity, action string
				entityIDInt                                         int64
				before, after                                       sql.NullString
			)
			if err := rows.Scan(&atStr, &actor, &channel, &verification, &entity, &entityIDInt, &action, &before, &after); err != nil {
				return err
			}
			at, err := parseTimestamp(atStr)
			if err != nil {
				return err
			}
			a := Activity{
				At:           at,
				Actor:        actor,
				Channel:      channel,
				Verification: verification,
				Entity:       entity,
				EntityID:     formatEntityID(entity, entityIDInt),
				Action:       action,
			}
			if before.Valid {
				a.Before = json.RawMessage(before.String)
			}
			if after.Valid {
				a.After = json.RawMessage(after.String)
			}
			result = append(result, a)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Activity{}
	}
	return result, nil
}
