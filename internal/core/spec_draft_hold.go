package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// このファイルは、下書きづくりの保留を作る口（理由・問いの文・選択肢を組み立てて、保留と記録を 1 つの書き込みで書く）を持つ。
// 起動の失敗・実行の終わり・行き止まりの検出は、どれもこの口を呼ぶ。

// 下書きづくりの保留の選択肢の識別の閉集合。
const (
	specDraftOptionAnswer  = "answer"
	specDraftOptionProceed = "proceed"
	specDraftOptionRetry   = "retry"
	specDraftOptionSkip    = "skip"
)

var specDraftOptionLabels = map[string]string{
	specDraftOptionAnswer:  "推奨のまま進める",
	specDraftOptionProceed: "いまの下書きで計画へ進む",
	specDraftOptionRetry:   "やり直す",
	specDraftOptionSkip:    "仕様の文書なしで計画する",
}

// specDraftRecommendedMark は推奨の選択肢の見出しの後ろに表示のときだけ付ける印。
const specDraftRecommendedMark = "（推奨）"

// specDraftHold は作る下書きづくりの保留の中身。
type specDraftHold struct {
	Reason SpecDraftHoldReason
	// Questions は残った問い。spec_question・spec_attempt_limit では 1 件以上、spec_failed では渡さない。記録の問いの一覧になる。
	Questions []SpecDraftQuestion
	// Detail は spec_failed の問いの文に書く失敗の内容（必須。ほかの理由では使わない）。
	Detail string
	// CountAttempt は、この実行を保留と同じ書き込みで回数に数える。記録を running にする前に開始が失敗したときに使う
	// （記録が pending のときだけ。running の記録は始めるときに数えてあるので、指定すると ErrValidation）。
	CountAttempt bool
}

func (h specDraftHold) validate() error {
	if !slices.Contains(SpecDraftHoldReasonValues, h.Reason) {
		return fmt.Errorf("%w: spec draft hold reason %q", ErrValidation, h.Reason)
	}
	if h.Reason == SpecDraftHoldFailed {
		if len(h.Questions) > 0 || strings.TrimSpace(h.Detail) == "" {
			return fmt.Errorf("%w: %s needs a detail and takes no questions", ErrValidation, h.Reason)
		}
		return nil
	}
	if len(h.Questions) == 0 {
		return fmt.Errorf("%w: %s needs questions", ErrValidation, h.Reason)
	}
	for i, q := range h.Questions {
		if q.Recommended < 1 || q.Recommended > len(q.Options) {
			return fmt.Errorf("%w: recommended option of question %d is out of range", ErrValidation, i+1)
		}
	}
	return nil
}

// holdOptions は理由と実行した回数から、保留の選択肢を作る。どの理由にも proceed と skip がある。
func (h specDraftHold) holdOptions(attempts int) []HoldOption {
	var ids []string
	switch {
	case h.Reason == SpecDraftHoldQuestion:
		ids = []string{specDraftOptionAnswer}
	case h.Reason == SpecDraftHoldFailed && attempts < SpecDraftMaxAttempts:
		ids = []string{specDraftOptionRetry}
	}
	ids = append(ids, specDraftOptionProceed, specDraftOptionSkip)
	opts := make([]HoldOption, len(ids))
	for i, id := range ids {
		opts[i] = HoldOption{ID: id, Label: specDraftOptionLabels[id]}
	}
	return opts
}

// question は保留の問いの文。path は下書きの場所。
func (h specDraftHold) question(path string, attempts int) string {
	if h.Reason == SpecDraftHoldFailed {
		return fmt.Sprintf("仕様の下書きづくりが失敗しました（実行 %d 回）。下書き: %s\n\n%s", attempts, path, strings.TrimSpace(h.Detail))
	}
	var b strings.Builder
	if h.Reason == SpecDraftHoldAttemptLimit {
		fmt.Fprintf(&b, "仕様の下書きづくりの回数の上限（%d 回）に達し、決めないと進まない問いが %d 件残りました。下書き: %s\n",
			SpecDraftMaxAttempts, len(h.Questions), path)
	} else {
		fmt.Fprintf(&b, "仕様の下書きに、決めないと進まない問いが %d 件残りました。下書き: %s\n", len(h.Questions), path)
	}
	// 問いの番号 Qn は、記録の問いの一覧の n 番目（答えはこの番号で受ける）。
	for i, q := range h.Questions {
		fmt.Fprintf(&b, "\nQ%d. %s\n", i+1, oneLine(q.Question))
		for j, o := range q.Options {
			mark := ""
			if j+1 == q.Recommended {
				mark = specDraftRecommendedMark
			}
			fmt.Fprintf(&b, "  %d. %s%s\n", j+1, oneLine(o.Label), mark)
		}
		fmt.Fprintf(&b, "  推奨の理由: %s\n", oneLine(q.Reason))
	}
	return strings.TrimRight(b.String(), "\n")
}

// errSpecDraftHoldNotEligible は、読み直した課題か記録が、下書きづくりの保留を作る対象でなかったことを表す内部の sentinel。
var errSpecDraftHoldNotEligible = errors.New("core: the spec draft can no longer be held")

// holdSpecDraft は runID を原因の実行として、課題を下書きづくりの保留で人間対応待ちにする（戻り先は分類済の、課題を止める保留）。
// 同じ書き込みで、記録を awaiting_owner にし、問いの一覧・保留・保留の理由を書く（答えは変えない。回数は CountAttempt のときだけ 1 増やす）。
// 保留の選択肢と問いの文の回数は、書いた後の記録の回数。
// 作るのは、課題が分類済で、記録が pending か running のときだけ。それ以外は何も書かず held は偽。
// 止まり方は渡さないので、スーパーバイザー待ちにならない。呼び出し元の ctx の取り消しには従わない。
func (s *Store) holdSpecDraft(ctx context.Context, runID, challengeID string, h specDraftHold) (held bool, err error) {
	if err := h.validate(); err != nil {
		return false, err
	}
	_, err = s.transitionAsInvoker(context.WithoutCancel(ctx), runID, challengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			return raiseSpecDraftHold(ctx, tc, h)
		})
	if errors.Is(err, errSpecDraftHoldNotEligible) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		return false, nil
	}
	return err == nil, err
}

// raiseSpecDraftHold は保留の行と記録を、保留へ進める遷移の書き込みの中で書く。
func raiseSpecDraftHold(ctx context.Context, tc *transitionCtx, h specDraftHold) error {
	if tc.current.Status != StatusClassified {
		return errSpecDraftHoldNotEligible
	}
	d, err := loadSpecDraft(ctx, tc.tx, tc.id)
	if err != nil {
		return err
	}
	if d == nil || (d.State != SpecDraftStatePending && d.State != SpecDraftStateRunning) {
		return errSpecDraftHoldNotEligible
	}
	attempts := d.Attempts
	if h.CountAttempt {
		if d.State != SpecDraftStatePending {
			return fmt.Errorf("%w: the attempt of a %s spec draft is already counted", ErrValidation, d.State)
		}
		attempts++
	}
	holdID, err := insertHoldWithOptions(ctx, tc, h.question(d.Path, attempts), HoldKindHuman, StatusClassified, nil,
		HoldOptionsInput{Options: h.holdOptions(attempts), Required: true})
	if err != nil {
		return err
	}
	return updateSpecDraft(ctx, tc.tx, tc.id, specDraftUpdate{State: SpecDraftStateAwaitingOwner, Attempts: attempts,
		Questions: h.Questions, Answers: d.Answers, HoldRowID: &holdID, HoldReason: &h.Reason}, tc.now)
}
