package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// このファイルは、画面のボタン（経路 ui・本人確認 none）で受ける、選ぶだけの回答の規則を持つ: 選択肢ごとの「ボタンで選べるか」・保留の照合の値・
// 経路 ui の回答の限り（回答の書き込みの中で強制する）。経路 cli・chat の回答には効かない。

// holdOptionButtonOK は、保留の理由 reason の選択肢 optionID を、画面のボタンで選べるか。保留の理由と選択肢の識別だけで決め、選択肢の文は読まない。
// 選べないのは、足す依頼の保留（上限を超える足す依頼）の「足す」（上限が上がる）と「計画を作り直す」（範囲が変わる）だけ。
// 分けるかの保留（理由を持たない）はどれも選べる。その split は元の課題の取り下げを伴うが、取り下げを伴う回答をボタンで受けない
// 決まりの例外。
func holdOptionButtonOK(reason holdReason, optionID string) bool {
	if reason != holdReasonTaskAddOverLimit {
		return true
	}
	return optionID != taskAddOptionAdd && optionID != taskAddOptionReplan
}

// holdOptionButtons は opts と同じ並びの、選択肢ごとの「ボタンで選べるか」。
func holdOptionButtons(reason holdReason, opts []HoldOption) []bool {
	out := make([]bool, len(opts))
	for i, o := range opts {
		out[i] = holdOptionButtonOK(reason, o.ID)
	}
	return out
}

// holdCheckValue は保留の照合の値。問いの文と、選択肢（識別・見出し・説明）と、識別なしの回答を断るかから作る。どれかが変われば値が変わる。
// 作り方はここに閉じ、呼び出し側は受けた値をそのまま返す。保存しない（保留の行からその場で作る）。
func holdCheckValue(question string, opts []HoldOption, required bool) string {
	if opts == nil {
		opts = []HoldOption{}
	}
	b, _ := json.Marshal(struct {
		Question string       `json:"q"`
		Options  []HoldOption `json:"o"`
		Required bool         `json:"r"`
	}{question, opts, required})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkUIAnswer は、経路 ui の回答の限りを、回答の書き込みの中（保留がまだ開いている間）で確かめる。何も書かない。誤りの種類:
//   - ErrValidation: 保留の識別が H-<番号>の形でない・照合の値が空・計画を作り直す指定・選択肢の識別が無い・添え書きが空でない・保留に無い識別
//   - ErrNotFound: その課題に無い保留
//   - ErrConflict: 保留が閉じていた・照合の値がいまの値と違う（問いか選択肢が、読んだ後に変わった）
//   - ErrVerificationRejected: 選択肢を持たない保留・ボタンで選べない選択肢（経路で受けない操作）
func checkUIAnswer(ctx context.Context, tx *sql.Tx, cid int64, req AnswerRequest) error {
	n, ok := parseHoldID(req.HoldID)
	if !ok {
		return fmt.Errorf("%w: a hold must be given as H-<number>", ErrValidation)
	}
	if req.HoldCheck == "" {
		return fmt.Errorf("%w: the check value of the hold is required", ErrValidation)
	}
	var question string
	var raw, reason, answered, closed sql.NullString
	var required int
	if err := tx.QueryRowContext(ctx, `SELECT question, options, option_required, reason, answered_at, closed_at FROM hold WHERE id = ? AND challenge_id = ?`, n, cid).
		Scan(&question, &raw, &required, &reason, &answered, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
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
	opts, err := parseHoldOptions(rp)
	if err != nil {
		return err
	}
	if holdCheckValue(question, opts, required == 1) != req.HoldCheck {
		return ErrConflict
	}
	if len(opts) == 0 {
		return fmt.Errorf("%w: a hold without options cannot be answered from the screen", ErrVerificationRejected)
	}
	if req.Replan {
		return fmt.Errorf("%w: a screen answer cannot ask for a replan", ErrValidation)
	}
	if req.Option == "" {
		return fmt.Errorf("%w: a screen answer needs an option", ErrValidation)
	}
	if strings.TrimSpace(req.Answer) != "" {
		return fmt.Errorf("%w: a screen answer cannot carry a note", ErrValidation)
	}
	for _, o := range opts {
		if o.ID != req.Option {
			continue
		}
		if !holdOptionButtonOK(holdReason(reason.String), o.ID) {
			return fmt.Errorf("%w: this option cannot be chosen from the screen", ErrVerificationRejected)
		}
		return nil
	}
	return fmt.Errorf("%w: option %q is not one of this hold's options", ErrValidation, EscapeControl(req.Option, false))
}
