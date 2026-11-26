package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// このファイルは、下書きづくりの保留への回答の規則（問いごとの答えの検査と組み立て・選択肢ごとの記録と課題の書き換え）を持つ。
// どの保留かは下書きづくりの記録の保留の理由で見分ける。書き込みは、保留への回答（ExecuteAnswer）のトランザクションの中で行う。

// specDraftAnsweredByOwner は答えの一覧の、オーナーの答えの by の値。
const specDraftAnsweredByOwner = "owner"

const (
	// specDraftDoneByOwner は、オーナーが選択肢 proceed で進めた（spec_draft_done の detail）。
	specDraftDoneByOwner = "owner"
	// specDraftSkippedByAnswer は、選択肢 skip の回答で省いた（spec_draft_skipped の detail）。
	specDraftSkippedByAnswer = "answer"
)

var (
	// ErrSpecAnswersNotAccepted は、問いごとの答えを渡した回答の相手が、下書きの問いの保留でないこと（保留を決められないときを含む）。
	ErrSpecAnswersNotAccepted = errors.New("answers per question are accepted only for a spec draft question hold")
	// ErrSpecQuestionNeedsOption は、計画の承認待ちでない課題の下書きの問いの保留へ、計画の問いへの答えを渡したこと。
	ErrSpecQuestionNeedsOption = errors.New("the open spec draft question hold is answered with the option " + specDraftOptionAnswer)
)

// specDraftOfHold は、保留 holdID が課題 cid の下書きづくりの保留なら記録を返し、そうでなければ nil を返す。
// 返す記録の HoldReason は nil でない。
func specDraftOfHold(ctx context.Context, tx *sql.Tx, cid, holdID int64) (*SpecDraft, error) {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil || d == nil || d.HoldID == nil || *d.HoldID != formatHoldID(holdID) || d.HoldReason == nil {
		return nil, err
	}
	return d, nil
}

// requireSpecQuestionHold は、問いごとの答え items があるとき、回答の相手 d が下書きの問いの保留であることを確かめる。
func requireSpecQuestionHold(d *SpecDraft, items []PlanAnswerItem) error {
	if len(items) > 0 && (d == nil || *d.HoldReason != SpecDraftHoldQuestion) {
		return fmt.Errorf("%w: %w", ErrValidation, ErrSpecAnswersNotAccepted)
	}
	return nil
}

// checkSpecDraftAnswer は、下書きづくりの保留（記録 d）へ選択肢 option で答えてよいかを確かめる。何も書かない。
// 返す値は、option が answer のときに答えの一覧へ足す答え。proceed は下書きを読み、読めないか受入基準が 0 件なら ErrValidation。
func (s *Store) checkSpecDraftAnswer(d *SpecDraft, option string, items []PlanAnswerItem) ([]SpecDraftAnswer, error) {
	if len(items) > 0 && option != specDraftOptionAnswer {
		return nil, fmt.Errorf("%w: answers per question can only be given with the option %s", ErrValidation, specDraftOptionAnswer)
	}
	switch option {
	case specDraftOptionAnswer:
		return ownerSpecDraftAnswers(d, items)
	case specDraftOptionProceed:
		if _, err := readSpecCriteria(s.workspace, d.Path); err != nil {
			// 仕様の文書を読めない誤りの種類は引き継がない（回答の検査の誤りとして返す）。
			return nil, fmt.Errorf("%w: cannot proceed with the draft: %s", ErrValidation, err.Error())
		}
	case specDraftOptionRetry, specDraftOptionSkip:
	default:
		return nil, fmt.Errorf("%w: unknown option %q for a spec draft hold", ErrValidation, EscapeControl(option, false))
	}
	return nil, nil
}

// ownerSpecDraftAnswers は残った問いの全部への、オーナーの答えを問いの並びの順に作る。items に無い問いの答えは、推奨の選択肢の見出し。
// 無い問いの番号・無い選択肢の番号・同じ問いへの 2 つの答え・番号と文の両方か無し・空の文は ErrValidation。
func ownerSpecDraftAnswers(d *SpecDraft, items []PlanAnswerItem) ([]SpecDraftAnswer, error) {
	given := map[int64]PlanAnswerItem{}
	for _, it := range items {
		if it.Question < 1 || it.Question > int64(len(d.Questions)) {
			return nil, fmt.Errorf("%w: no such question Q%d", ErrValidation, it.Question)
		}
		if _, dup := given[it.Question]; dup {
			return nil, fmt.Errorf("%w: Q%d is answered twice", ErrValidation, it.Question)
		}
		if (it.Choice == nil) == (it.Text == nil) {
			return nil, fmt.Errorf("%w: Q%d needs either a choice or a text", ErrValidation, it.Question)
		}
		if it.Text != nil && strings.TrimSpace(*it.Text) == "" {
			return nil, fmt.Errorf("%w: the text for Q%d is empty", ErrValidation, it.Question)
		}
		if n := len(d.Questions[it.Question-1].Options); it.Choice != nil && (*it.Choice < 1 || *it.Choice > int64(n)) {
			return nil, fmt.Errorf("%w: choice %d is out of range for Q%d", ErrValidation, *it.Choice, it.Question)
		}
		given[it.Question] = it
	}
	answers := make([]SpecDraftAnswer, len(d.Questions))
	for i, q := range d.Questions {
		choice := int64(q.Recommended)
		var text *string
		if it, ok := given[int64(i+1)]; ok {
			if it.Choice != nil {
				choice = *it.Choice
			}
			text = it.Text
		}
		// 実行の回数は問いを出した後、次の実行を始めるまで変わらない。
		a := SpecDraftAnswer{Attempt: d.Attempts, Question: q.Question, By: specDraftAnsweredByOwner, Basis: []SpecDraftBasis{}}
		if text != nil {
			a.Answer = *text
		} else {
			if choice < 1 || choice > int64(len(q.Options)) {
				return nil, fmt.Errorf("core: spec draft question %d has no recommended option", i+1)
			}
			a.Answer = q.Options[choice-1].Label
		}
		answers[i] = a
	}
	return answers, nil
}

// applySpecDraftAnswer は、下書きづくりの保留への回答を、記録と課題と作業ログへ書く（回答の遷移の書き込みの中で呼ぶ）。
// answers は checkSpecDraftAnswer が返した、足す答え。問いの一覧と実行の回数は変えない。
func applySpecDraftAnswer(ctx context.Context, tc *transitionCtx, d *SpecDraft, option string, answers []SpecDraftAnswer) error {
	u := specDraftUpdate{State: SpecDraftStatePending, Attempts: d.Attempts, Questions: d.Questions,
		Answers: append(slices.Clone(d.Answers), answers...)}
	switch option {
	case specDraftOptionProceed:
		u.State = SpecDraftStateDone
	case specDraftOptionSkip:
		u.State = SpecDraftStateSkipped
	}
	if err := updateSpecDraft(ctx, tc.tx, tc.id, u, tc.now); err != nil {
		return err
	}
	switch option {
	case specDraftOptionProceed:
		path := d.Path
		tc.current.SpecPath = &path
		if d.BasePath != nil {
			tc.current.SpecBasePath = d.BasePath
		}
		return writeSpecDraftDone(ctx, tc.tx, tc.rec, tc.id, d, specDraftDoneByOwner)
	case specDraftOptionSkip:
		return tc.rec.record("challenge", tc.id, ActivityActionSpecDraftSkipped, nil, map[string]any{"detail": specDraftSkippedByAnswer})
	}
	return nil
}

// writeSpecDraftDone は、記録 d を done にする書き込みの中で、課題の仕様の文書の場所を書き、作業ログ spec_draft_done（細目 detail）を
// 残す。仕様の文書の場所は、記録を done にするのと同じ書き込みの中だけで書く。置き換える元は、無ければ書かない。
func writeSpecDraftDone(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64, d *SpecDraft, detail string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE challenge SET spec_path = ?, spec_base_path = COALESCE(?, spec_base_path) WHERE id = ?`,
		d.Path, d.BasePath, cid); err != nil {
		return err
	}
	after := map[string]any{"detail": detail, "spec_path": d.Path}
	if d.BasePath != nil {
		after["spec_base_path"] = *d.BasePath
	}
	return rec.record("challenge", cid, ActivityActionSpecDraftDone, nil, after)
}

// specQuestionHoldOpen は課題 cid に、開いている下書きの問いの保留があるか。
func specQuestionHoldOpen(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil || d == nil || d.HoldReason == nil {
		return false, err
	}
	return *d.HoldReason == SpecDraftHoldQuestion, nil
}
