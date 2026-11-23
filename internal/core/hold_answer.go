package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// このファイルは、保留への回答で選択肢を選ぶ規則（識別の解決・回答の文の組み立て・検査）と、開いている保留の問いの文と選択肢の説明の
// 書き換えを持つ。選ばれた見出しは回答の文として扱うだけで、選択肢の文から承認・予算の増額・取り下げは起こさない。

// ActivityActionHoldEdit は作業ログ（entity=challenge）の action。開いている保留の問いの文・選択肢の説明を書き換えたときに残す。
const ActivityActionHoldEdit = "hold_edit"

// resolveAnswerOption は回答（answer と option）を、保留の選択肢（opts・required）に照らして検査し、回答の文と選んだ識別（選んでいなければ nil）を返す。
// 誤りは ErrValidation（何も書かない）:
//   - option が空: 回答の文が空、または required が真（出力に選択肢の識別と見出しを示す）
//   - option が空でない: 保留が選択肢を持たない、または識別が選択肢に無い
//
// 回答の文は、選んだ選択肢の見出し。answer が空でなければ「見出し・改行・answer」。
func resolveAnswerOption(opts []HoldOption, required bool, option, answer string) (text string, chosen *string, err error) {
	if option == "" {
		if required {
			return "", nil, fmt.Errorf("%w: this hold requires --option; choose one of: %s", ErrValidation, describeHoldOptions(opts))
		}
		if strings.TrimSpace(answer) == "" {
			return "", nil, ErrValidation
		}
		return answer, nil, nil
	}
	if len(opts) == 0 {
		return "", nil, fmt.Errorf("%w: this hold has no options, so --option cannot be used", ErrValidation)
	}
	for _, o := range opts {
		if o.ID != option {
			continue
		}
		id := o.ID
		if strings.TrimSpace(answer) == "" {
			return o.Label, &id, nil
		}
		return o.Label + "\n" + answer, &id, nil
	}
	return "", nil, fmt.Errorf("%w: option %q is not one of this hold's options: %s", ErrValidation, option, describeHoldOptions(opts))
}

// describeHoldOptions は選択肢の識別と見出しの一覧（`識別 (見出し)` を `, ` 区切り）。選択肢が無ければ `(none)`。誤りの文は端末へ出るので、識別・見出しの
// 制御文字は見える形に直す（表示のときだけ。保存した値と回答の文は変えない）。
func describeHoldOptions(opts []HoldOption) string {
	if len(opts) == 0 {
		return "(none)"
	}
	parts := make([]string, len(opts))
	for i, o := range opts {
		parts[i] = fmt.Sprintf("%s (%s)", EscapeControl(o.ID, false), EscapeControl(o.Label, false))
	}
	return strings.Join(parts, ", ")
}

// loadHoldOptionsOf は保留 holdID の選択肢の並びと「識別の無い回答を断る」印を読む。
func loadHoldOptionsOf(ctx context.Context, tx *sql.Tx, holdID int64) ([]HoldOption, bool, error) {
	var raw sql.NullString
	var required int
	if err := tx.QueryRowContext(ctx, `SELECT options, option_required FROM hold WHERE id = ?`, holdID).Scan(&raw, &required); err != nil {
		return nil, false, err
	}
	var rp *string
	if raw.Valid {
		rp = &raw.String
	}
	opts, err := parseHoldOptions(rp)
	if err != nil {
		return nil, false, err
	}
	return opts, required == 1, nil
}

// answerOptionUse は、回答の処理（ExecuteAnswer）の書き込みの中で、選ばれた識別を読む core の中の使う側。選択肢つきの回答のときだけ、
// 回答を記録する前（保留がまだ開いている間）に同じ書き込みの中で呼ぶ。エラーを返せば回答ごと取り消す。回答の処理（ExecuteAnswer）が呼ぶが、Store.answerOptionUse に使う側を入れるのは試験だけで、本番の Store では nil のため何もしない（足す依頼の保留への回答は、これを介さず applyTaskAdditionAnswer が行う）。
type answerOptionUse func(ctx context.Context, tc *transitionCtx, holdID int64, optionID string) error

// HoldTextEdit は開いている保留の書き換えの指定。Question が nil なら問いの文は変えない。Descriptions は選択肢の識別→新しい説明
// （空文字は説明を消す）。保留の選択肢に無い識別・空の問いの文は ErrValidation。選択肢の識別・見出し・件数は変えない。
type HoldTextEdit struct {
	Question     *string
	Descriptions map[string]string
}

// editHoldText は保留 holdID（課題 cid の、開いている保留）の問いの文と選択肢の説明を書き換え、作業ログへ残す。課題の版は進めない
// （遷移の書き込みの中から呼ぶ形。版はその遷移が進める）。開いていない保留は ErrInvalidTransition。
func editHoldText(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, holdID int64, edit HoldTextEdit, extraAfter map[string]any) (changed bool, err error) {
	var question string
	var raw sql.NullString
	var answered, closed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT question, options, answered_at, closed_at FROM hold WHERE id = ? AND challenge_id = ?`, holdID, cid).
		Scan(&question, &raw, &answered, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if answered.Valid || closed.Valid {
		return false, ErrInvalidTransition
	}
	before := map[string]any{}
	after := map[string]any{}
	newQuestion := question
	if edit.Question != nil {
		if strings.TrimSpace(*edit.Question) == "" {
			return false, fmt.Errorf("%w: hold question must not be empty", ErrValidation)
		}
		if *edit.Question != question {
			before["question"] = question
			after["question"] = *edit.Question
			newQuestion = *edit.Question
		}
	}
	var rp *string
	if raw.Valid {
		rp = &raw.String
	}
	opts, err := parseHoldOptions(rp)
	if err != nil {
		return false, err
	}
	var newOptions any
	if raw.Valid {
		newOptions = raw.String
	}
	if len(edit.Descriptions) > 0 {
		byID := map[string]int{}
		for i, o := range opts {
			byID[o.ID] = i
		}
		bd, ad := map[string]any{}, map[string]any{}
		for id, d := range edit.Descriptions {
			i, ok := byID[id]
			if !ok {
				return false, fmt.Errorf("%w: option %q is not one of this hold's options", ErrValidation, id)
			}
			if opts[i].Description != d {
				bd[id], ad[id] = opts[i].Description, d
				opts[i].Description = d
			}
		}
		if len(ad) > 0 {
			before["option_descriptions"], after["option_descriptions"] = bd, ad
			b, err := jsonMarshalOptions(opts)
			if err != nil {
				return false, err
			}
			newOptions = b
		}
	}
	if len(after) == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hold SET question = ?, options = ? WHERE id = ?`, newQuestion, newOptions, holdID); err != nil {
		return false, err
	}
	after["hold_id"] = holdID
	for k, v := range extraAfter {
		after[k] = v
	}
	return true, rec.record("challenge", cid, ActivityActionHoldEdit, before, after)
}

// EditHoldText は課題 id の開いている保留の問いの文と選択肢の説明を書き換える（課題の版を 1 つ進め、作業ログに残す）。
// CLI・API の口は持たない。回答の処理と同じ書き込みの中から呼ぶ形は editHoldText（版は遷移が進める）。
func (s *Store) EditHoldText(ctx context.Context, ch Channel, id string, edit HoldTextEdit) (*Challenge, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}
		if current.Status != StatusAwaitingHuman {
			return ErrInvalidTransition
		}
		// 開いている保留が 2 つ以上のとき、最新の保留を黙って選ばない（ErrValidation）。
		holdID, _, _, _, _, err := selectAnswerHold(ctx, tx, cid, current.ID, "")
		if err != nil {
			return err
		}
		extra := map[string]any{"version": current.Version + 1}
		changed, err := editHoldText(ctx, tx, rec, cid, holdID, edit, extra)
		if err != nil {
			return err
		}
		if !changed {
			return nil // 何も変わらない指定は版を進めず、作業ログも残さない
		}
		res, err := tx.ExecContext(ctx, `UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			current.Version+1, formatTimestamp(rec.at), cid, current.Version)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("core: edit hold of %s: expected to update 1 row, updated %d", id, n)
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	d, err := s.GetChallenge(ctx, id)
	if err != nil {
		return nil, err
	}
	return &d.Challenge, nil
}
