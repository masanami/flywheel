package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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

// maxSpecStepSummaryRunes は、保留の問いの文に載せる、失敗したステップの要約の文字数の上限。
const maxSpecStepSummaryRunes = 500

// specDraftHold は作る下書きづくりの保留の中身。
type specDraftHold struct {
	Reason SpecDraftHoldReason
	// Questions は残った問い。spec_question・spec_attempt_limit では 1 件以上、spec_failed では渡さない。記録の問いの一覧になる。
	Questions []SpecDraftQuestion
	// Detail は spec_failed の問いの文に書く失敗の内容（必須。ほかの理由では使わない）。
	Detail string
	// FailedStep は spec_failed の問いの文に書く、失敗か取り消しで終わった実行の最後のステップ（無ければ nil）。
	FailedStep *specDraftFailedStep
	// DraftExists は、下書きが通常のファイルとして在るか（spec_failed だけが使う）。保留を書く書き込みの前に確かめて渡す
	// （holdSpecDraft は自分で確かめる）。
	DraftExists bool
	// CountAttempt は、この実行を保留と同じ書き込みで回数に数える。記録を running にする前に開始が失敗したときに使う
	// （記録が pending のときだけ。running の記録は始めるときに数えてあるので、指定すると ErrValidation）。
	CountAttempt bool
	// AddAnswers は、保留と同じ書き込みで記録の答えの一覧へ足す答え（スーパーバイザーの答え）。足すだけで、前の要素は変えない。
	AddAnswers []SpecDraftAnswer
	// SupervisorAnswered は、スーパーバイザーが答えた問いの件数（spec_question の問いの文に書く。0 なら書かない）。
	SupervisorAnswered int
}

// specDraftFailedStep は実行の最後のステップ。どの値も接続ツールの出力で、Summary は LLM が書いた信頼できない文。
// 問いの文に表示するだけにする。
type specDraftFailedStep struct {
	// Step はステップの名前、Outcome は結果の語（無ければ空）。
	Step, Outcome string
	// Summary は facts のステップの要約。Outcome がステップ自身の失敗（failure）のときだけ表示する。
	Summary string
}

// lines は問いの文の、失敗したステップの行とステップの要約の行。
func (f specDraftFailedStep) lines() string {
	line := func(v string) string { return EscapeControl(oneLine(v), false) }
	outcome := line(f.Outcome)
	if outcome == "" {
		outcome = "-"
	}
	out := fmt.Sprintf("失敗したステップ: %s（結果 %s）", line(f.Step), outcome)
	// ほかの結果では、要約は前に終わったステップのもの。
	if summary, _ := truncateRunes(line(f.Summary), maxSpecStepSummaryRunes); f.Outcome == flowOutcomeFailure && summary != "" {
		out += "\nステップの要約: " + summary
	}
	return out
}

// specDraftFileExists は、下書きの場所 path に通常のファイルが在るかを返す。場所の検査に落ちるものと確かめられないものは、無いとする。
func (s *Store) specDraftFileExists(path string) bool {
	resolved, err := resolveSpecPath(s.workspace, path)
	if err != nil {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.Mode().IsRegular()
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

// holdOptions は理由と実行した回数から、保留の選択肢を作る。どの理由にも skip がある。proceed は、下書きが無い失敗の保留にだけ無い。
func (h specDraftHold) holdOptions(attempts int) []HoldOption {
	var ids []string
	switch {
	case h.Reason == SpecDraftHoldQuestion:
		ids = []string{specDraftOptionAnswer}
	case h.Reason == SpecDraftHoldFailed && attempts < SpecDraftMaxAttempts:
		ids = []string{specDraftOptionRetry}
	}
	if h.Reason != SpecDraftHoldFailed || h.DraftExists {
		ids = append(ids, specDraftOptionProceed)
	}
	ids = append(ids, specDraftOptionSkip)
	opts := make([]HoldOption, len(ids))
	for i, id := range ids {
		opts[i] = HoldOption{ID: id, Label: specDraftOptionLabels[id]}
	}
	return opts
}

// question は保留の問いの文。path は下書きの場所。問いの一覧は書かない（記録の問いの一覧が出どころ。Qn は一覧の n 番目）。
func (h specDraftHold) question(path string, attempts int) string {
	switch h.Reason {
	case SpecDraftHoldFailed:
		out := fmt.Sprintf("仕様の下書きづくりが失敗しました（実行 %d 回）。", attempts)
		if h.DraftExists {
			out += "下書き: " + path
		}
		out += "\n\n" + strings.TrimSpace(h.Detail)
		if h.FailedStep != nil {
			out += "\n\n" + h.FailedStep.lines()
		}
		return out
	case SpecDraftHoldAttemptLimit:
		return fmt.Sprintf("仕様の下書きづくりの回数の上限（%d 回）に達し、決めないと進まない問いが %d 件残りました。下書き: %s",
			SpecDraftMaxAttempts, len(h.Questions), path)
	}
	out := fmt.Sprintf("仕様の下書きに、決めないと進まない問いが %d 件残りました。下書き: %s", len(h.Questions), path)
	if h.SupervisorAnswered > 0 {
		out += fmt.Sprintf("\nスーパーバイザーが %d 件に答えました。", h.SupervisorAnswered)
	}
	return out
}

// errSpecDraftHoldNotEligible は、読み直した課題か記録が、下書きづくりの保留を作る対象でなかったことを表す内部の sentinel。
var errSpecDraftHoldNotEligible = errors.New("core: the spec draft can no longer be held")

// holdSpecDraft は runID を原因の実行として、課題を下書きづくりの保留で人間対応待ちにする（戻り先は分類済の、課題を止める保留）。
// 同じ書き込みで、記録を awaiting_owner にし、問いの一覧・保留・保留の理由を書く（答えは AddAnswers を足すだけ。回数は CountAttempt のときだけ 1 増やす）。
// 保留の選択肢と問いの文の回数は、書いた後の記録の回数。
// 作るのは、課題が分類済で、記録が pending か running のときだけ。それ以外は何も書かず held は偽。
// 止まり方は渡さないので、スーパーバイザー待ちにならない。呼び出し元の ctx の取り消しには従わない。
func (s *Store) holdSpecDraft(ctx context.Context, runID, challengeID string, h specDraftHold) (held bool, err error) {
	if err := h.validate(); err != nil {
		return false, err
	}
	if h.Reason == SpecDraftHoldFailed {
		if h.DraftExists, err = s.recordedSpecDraftExists(context.WithoutCancel(ctx), challengeID); err != nil {
			return false, err
		}
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

// recordedSpecDraftExists は、課題の記録の下書きの場所に通常のファイルが在るかを返す（記録が無ければ偽）。
func (s *Store) recordedSpecDraftExists(ctx context.Context, challengeID string) (bool, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return false, nil
	}
	var d *SpecDraft
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		d, err = loadSpecDraft(ctx, tx, cid)
		return err
	})); err != nil || d == nil {
		return false, err
	}
	return s.specDraftFileExists(d.Path), nil
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
		Questions: h.Questions, Answers: append(slices.Clone(d.Answers), h.AddAnswers...), HoldRowID: &holdID, HoldReason: &h.Reason}, tc.now)
}
