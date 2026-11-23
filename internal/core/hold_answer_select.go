package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// このファイルは、回答（T12）の相手の保留を決める規則（--hold の解決・開いている保留の一覧）と、書き込みのときの保留の照合を持つ。

// openHoldInfo は開いている保留の一覧の 1 行（回答の相手を選ぶための表示用）。
type openHoldInfo struct {
	ID        int64
	Question  string
	TaskTitle string
}

// listOpenHoldsForAnswer は課題の開いている保留を、保留の番号の昇順で返す。
func listOpenHoldsForAnswer(ctx context.Context, tx *sql.Tx, cid int64) ([]openHoldInfo, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT h.id, h.question, COALESCE(t.title, '') FROM hold h LEFT JOIN task t ON t.id = h.task_id
		  WHERE h.challenge_id = ? AND h.answer IS NULL AND h.closed_at IS NULL ORDER BY h.id ASC`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []openHoldInfo
	for rows.Next() {
		var h openHoldInfo
		if err := rows.Scan(&h.ID, &h.Question, &h.TaskTitle); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// holdQuestionHead は問いの先頭（最初の行を 40 文字まで）。端末へ出すので制御文字は見える形に直す。
func holdQuestionHead(q string) string {
	if i := strings.IndexAny(q, "\r\n"); i >= 0 {
		q = q[:i]
	}
	r := []rune(q)
	if len(r) > 40 {
		q = string(r[:40]) + "…"
	}
	return EscapeControl(q, false)
}

// describeOpenHolds は開いている保留の一覧（ID・タスクの題名・問いの先頭を 1 件 1 行）。
func describeOpenHolds(holds []openHoldInfo) string {
	lines := make([]string, len(holds))
	for i, h := range holds {
		title := h.TaskTitle
		if title == "" {
			title = "（タスクに結び付かない）"
		}
		lines[i] = fmt.Sprintf("  %s  タスク: %s  問い: %s", formatHoldID(h.ID), EscapeControl(title, false), holdQuestionHead(h.Question))
	}
	return strings.Join(lines, "\n")
}

// selectAnswerHold は回答の相手の保留（開いている保留）を決める。holdArg が空なら、開いている保留が 1 つのときだけその保留
// （2 つ以上は ErrValidation で、開いている保留の一覧を文に出す。最新の保留を黙って選ばない）。
// holdArg が空でなければ H-<番号> の形で、その課題の開いている保留でなければ ErrValidation。保留の行を引いて返す（takenTitle は結び付くタスクの題名）。
func selectAnswerHold(ctx context.Context, tx *sql.Tx, cid int64, challengeID, holdArg string) (id int64, question string, fromStatus Status, raisedAt time.Time, taskTitle string, err error) {
	holds, err := listOpenHoldsForAnswer(ctx, tx, cid)
	if err != nil {
		return 0, "", "", time.Time{}, "", err
	}
	var chosen *openHoldInfo
	if holdArg == "" {
		switch len(holds) {
		case 0:
			return 0, "", "", time.Time{}, "", errNoUnansweredHold(challengeID)
		case 1:
			chosen = &holds[0]
		default:
			return 0, "", "", time.Time{}, "", fmt.Errorf("%w: challenge %s has %d open holds; specify one with --hold <H-ID>:\n%s", ErrValidation, challengeID, len(holds), describeOpenHolds(holds))
		}
	} else {
		n, ok := parseHoldID(holdArg)
		if !ok {
			return 0, "", "", time.Time{}, "", fmt.Errorf("%w: --hold must look like H-<number>, got %q", ErrValidation, EscapeControl(holdArg, false))
		}
		for i := range holds {
			if holds[i].ID == n {
				chosen = &holds[i]
			}
		}
		if chosen == nil {
			if len(holds) == 0 {
				return 0, "", "", time.Time{}, "", fmt.Errorf("%w: %s is not an open hold of challenge %s", ErrValidation, formatHoldID(n), challengeID)
			}
			return 0, "", "", time.Time{}, "", fmt.Errorf("%w: %s is not an open hold of challenge %s; open holds:\n%s", ErrValidation, formatHoldID(n), challengeID, describeOpenHolds(holds))
		}
	}
	var fs, raisedAtStr string
	if err := tx.QueryRowContext(ctx, `SELECT from_status, raised_at FROM hold WHERE id = ?`, chosen.ID).Scan(&fs, &raisedAtStr); err != nil {
		return 0, "", "", time.Time{}, "", err
	}
	raisedAt, err = parseTimestamp(raisedAtStr)
	if err != nil {
		return 0, "", "", time.Time{}, "", err
	}
	return chosen.ID, chosen.Question, Status(fs), raisedAt, chosen.TaskTitle, nil
}

// checkAnswerHoldUnchanged は、確かめた保留（holdArg。H-<番号>）が、書き込みの時点でも開いたままで、問いと選択肢が確かめたときと同じかを確かめる。
// 閉じていた・問いか選択肢が変わっていたら ErrConflict。その課題の保留でない・無い番号は ErrValidation。
func checkAnswerHoldUnchanged(ctx context.Context, tx *sql.Tx, cid int64, holdArg, question string, options []HoldOption, required bool) error {
	n, ok := parseHoldID(holdArg)
	if !ok {
		return fmt.Errorf("%w: --hold must look like H-<number>, got %q", ErrValidation, EscapeControl(holdArg, false))
	}
	var q string
	var raw sql.NullString
	var req int
	var answered, closed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT question, options, option_required, answered_at, closed_at FROM hold WHERE id = ? AND challenge_id = ?`, n, cid).
		Scan(&q, &raw, &req, &answered, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s is not a hold of this challenge", ErrValidation, formatHoldID(n))
		}
		return err
	}
	if answered.Valid || closed.Valid {
		return ErrConflict
	}
	var rp *string
	if raw.Valid {
		rp = &raw.String
	}
	cur, err := parseHoldOptions(rp)
	if err != nil {
		return err
	}
	if q != question || (req == 1) != required || len(cur) != len(options) {
		return ErrConflict
	}
	for i := range cur {
		if cur[i] != options[i] {
			return ErrConflict
		}
	}
	return nil
}
