package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// このファイルは、親が直接行った作業を完了として記録する道（課題 #452）を持つ。
// 対話のセッションの親が直接行う課題は、自動の計画（J2）・委譲から外すために種類が
// 親が直接行う（parent_direct）の保留に置く。親が作業を終えたら CompleteDirect（T17）で
// 成果物の参照を残して完了確認待ちへ進め、完了の承認は従来どおりオーナーが行う。
// 完了の検証（J5）は通さない。

// HoldKind は保留（hold）の種類の閉集合。ストアの値は 0014 の hold.kind。
type HoldKind string

const (
	// HoldKindHuman は人の対応を待つ保留（従来のすべての保留。既定）。
	HoldKindHuman HoldKind = "human"
	// HoldKindParentDirect は対話のセッションの親が直接行う課題の保留。
	HoldKindParentDirect HoldKind = "parent_direct"
)

// HoldKindValues は HoldKind の閉集合の写しを返す。
func HoldKindValues() []HoldKind {
	return []HoldKind{HoldKindHuman, HoldKindParentDirect}
}

// ParseHoldKind は s を保留の種類として解釈する（完全一致だけ）。
func ParseHoldKind(s string) (HoldKind, bool) {
	for _, k := range HoldKindValues() {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// DirectCompletion は親が直接行った完了の記録 1 件。ある課題にこの記録が 1 件以上あることが、
// 「親が直接行った」印になる（課題が完了した後も残る）。
type DirectCompletion struct {
	// Artifacts は成果物の参照（URL・PR・Issue コメントなど。run を介さない）。記録した順で、1 件以上。
	Artifacts []string
	// Note は短い説明。無ければ nil。
	Note      *string
	CreatedAt time.Time
}

// CompleteDirectInput は CompleteDirect の入力。
type CompleteDirectInput struct {
	// Artifacts は成果物の参照。1 件以上で、各要素は前後の空白を除いて空でなく、改行を含まない。
	Artifacts []string
	// Note は短い説明（任意。前後の空白を除いて空なら無いものとする）。
	Note *string
}

// normalizeDirectArtifacts は成果物の参照の検査と整形（前後の空白を除く）。0 件・空の要素・改行を含む要素は ok=false。
func normalizeDirectArtifacts(refs []string) ([]string, bool) {
	if len(refs) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" || strings.ContainsAny(r, "\r\n") {
			return nil, false
		}
		out = append(out, r)
	}
	return out, true
}

// CompleteDirect は親が直接行った作業を完了として記録し、課題を完了確認待ちへ進める（T17）。
// 使えるのは、人間対応待ちで、いま開いている保留の種類が親が直接行うの課題だけ（別の種類の保留・
// ほかの状態は ErrInvalidTransition、完了は ErrTerminalState）。成果物の参照が 0 件か不正なら ErrValidation。
// 開いている保留は回答なしで閉じる。完了の検証（J5）は起動しない。
func (s *Store) CompleteDirect(ctx context.Context, ch Channel, id string, in CompleteDirectInput) (*Challenge, *DirectCompletion, error) {
	refs, ok := normalizeDirectArtifacts(in.Artifacts)
	if !ok {
		return nil, nil, ErrValidation
	}
	var note *string
	if in.Note != nil {
		if n := strings.TrimSpace(*in.Note); n != "" {
			note = &n
		}
	}

	var out DirectCompletion
	c, err := s.transition(ctx, ch, id, OpCompleteDirect, func(ctx context.Context, tc *transitionCtx) error {
		holdID, kind, err := loadOpenHoldKind(ctx, tc.tx, tc.id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNoUnansweredHold(tc.current.ID)
			}
			return err
		}
		if kind != HoldKindParentDirect {
			return ErrInvalidTransition
		}
		if _, err := tc.tx.ExecContext(ctx, `UPDATE hold SET closed_at = ? WHERE id = ?`, tc.nowStr, holdID); err != nil {
			return err
		}
		var noteArg any
		if note != nil {
			noteArg = *note
		}
		res, err := tc.tx.ExecContext(ctx,
			`INSERT INTO direct_completion (challenge_id, hold_id, note, created_at) VALUES (?, ?, ?, ?)`,
			tc.id, holdID, noteArg, tc.nowStr)
		if err != nil {
			return err
		}
		dcID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for i, ref := range refs {
			if _, err := tc.tx.ExecContext(ctx,
				`INSERT INTO direct_completion_artifact (direct_completion_id, position, ref) VALUES (?, ?, ?)`,
				dcID, i+1, ref); err != nil {
				return err
			}
		}
		tc.after["artifacts"] = refs
		if note != nil {
			tc.after["note"] = *note
		}
		out = DirectCompletion{Artifacts: refs, Note: note, CreatedAt: tc.now}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, &out, nil
}

// loadOpenHoldKind は課題の「現在の未回答の保留」（loadUnansweredHold と同じ、開いている最新の 1 件）の
// ID と種類を返す。行が無ければ sql.ErrNoRows をそのまま返す。
func loadOpenHoldKind(ctx context.Context, tx *sql.Tx, cid int64) (int64, HoldKind, error) {
	var id int64
	var kind string
	if err := tx.QueryRowContext(ctx,
		`SELECT id, kind FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, cid,
	).Scan(&id, &kind); err != nil {
		return 0, "", err
	}
	return id, HoldKind(kind), nil
}

// loadDirectCompletions は課題の直接行った完了の記録を古い順に読む（無ければ空のスライス）。
func loadDirectCompletions(ctx context.Context, tx *sql.Tx, cid int64) ([]DirectCompletion, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT d.id, d.note, d.created_at, a.ref
		 FROM direct_completion d LEFT JOIN direct_completion_artifact a ON a.direct_completion_id = d.id
		 WHERE d.challenge_id = ? ORDER BY d.id ASC, a.position ASC`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []DirectCompletion{}
	var lastID int64
	for rows.Next() {
		var (
			id        int64
			note, ref sql.NullString
			createdAt string
		)
		if err := rows.Scan(&id, &note, &createdAt, &ref); err != nil {
			return nil, err
		}
		if len(out) == 0 || id != lastID {
			t, err := parseTimestamp(createdAt)
			if err != nil {
				return nil, err
			}
			dc := DirectCompletion{Artifacts: []string{}, CreatedAt: t}
			if note.Valid {
				n := note.String
				dc.Note = &n
			}
			out = append(out, dc)
			lastID = id
		}
		if ref.Valid {
			last := &out[len(out)-1]
			last.Artifacts = append(last.Artifacts, ref.String)
		}
	}
	return out, rows.Err()
}

// loadCurrentDirectCompletion は、課題がいま完了確認待ちにいる理由が直接行った完了の記録（T17）であるときだけ
// その記録（最新の 1 件）を返す。完了確認待ちへ入る道は T8（検証の met）と T17 だけなので、作業ログの
// 課題の行のうち、その 2 つの最後が complete_direct かで決める（差し戻されて別の道で戻ってきたときに、
// 前の記録を承認の根拠として見せない）。
func loadCurrentDirectCompletion(ctx context.Context, tx *sql.Tx, cid int64) (*DirectCompletion, error) {
	var last string
	err := tx.QueryRowContext(ctx,
		`SELECT action FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action IN (?, ?) ORDER BY id DESC LIMIT 1`,
		cid, string(OpCompleteDirect), string(OpVerifyMet)).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if last != string(OpCompleteDirect) {
		return nil, nil
	}
	dcs, err := loadDirectCompletions(ctx, tx, cid)
	if err != nil || len(dcs) == 0 {
		return nil, err
	}
	return &dcs[len(dcs)-1], nil
}

// listParentAgentChallenges は、人間対応待ちで、いま開いている保留の種類が親が直接行うの課題を id 昇順で返す
// （status の「親のエージェントが動く番」）。
func listParentAgentChallenges(ctx context.Context, tx *sql.Tx) ([]Challenge, error) {
	rows, err := tx.QueryContext(ctx, challengeSelectColumns+` WHERE status = ? AND
		(SELECT h.kind FROM hold h WHERE h.challenge_id = challenge.id AND h.answer IS NULL AND h.closed_at IS NULL ORDER BY h.id DESC LIMIT 1) = ?
		ORDER BY id ASC`, string(StatusAwaitingHuman), string(HoldKindParentDirect))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Challenge, 0)
	for rows.Next() {
		c, err := scanChallengeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
