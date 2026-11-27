package view

import "github.com/masanami/flywheel/internal/core"

// SpecDraft は課題の詳細の spec_draft（下書きづくりの記録）。
type SpecDraft struct {
	Answers  []SpecDraftAnswer `json:"answers"`
	Attempts int               `json:"attempts"`
	BasePath *string           `json:"base_path"`
	// HoldID は開いている下書きづくりの保留の表示用の ID、HoldReason はその理由。保留が無ければどちらも null。
	HoldID      *string             `json:"hold_id"`
	HoldReason  *string             `json:"hold_reason"`
	MaxAttempts int                 `json:"max_attempts"`
	Path        string              `json:"path"`
	Questions   []SpecDraftQuestion `json:"questions"`
	Reason      string              `json:"reason"`
	Repo        string              `json:"repo"`
	State       string              `json:"state"`
}

// SpecDraftAnswer は spec_draft.answers[] の要素。
type SpecDraftAnswer struct {
	Answer  string           `json:"answer"`
	Attempt int              `json:"attempt"`
	Basis   []SpecDraftBasis `json:"basis"`
	// By は答えた者。知らない値もそのまま出す。
	By string `json:"by"`
	// Kind は要素の種類（答えか添え書き）。
	Kind     string `json:"kind"`
	Question string `json:"question"`
}

// SpecDraftBasis は答えの根拠の場所。
type SpecDraftBasis struct {
	Locator string `json:"locator"`
	Path    string `json:"path"`
}

// SpecDraftQuestion は spec_draft.questions[] の要素。
type SpecDraftQuestion struct {
	Options  []SpecDraftOption `json:"options"`
	Question string            `json:"question"`
	Reason   string            `json:"reason"`
	// Recommended は推奨の選択肢の、1 から数えた位置。
	Recommended int `json:"recommended"`
}

// SpecDraftOption は問いの選択肢。
type SpecDraftOption struct {
	Label string `json:"label"`
}

// fromSpecDraft は下書きづくりの記録を変換する（記録が無ければ nil。一覧は空でも `[]`）。
func fromSpecDraft(d *core.SpecDraft) *SpecDraft {
	if d == nil {
		return nil
	}
	questions := make([]SpecDraftQuestion, 0, len(d.Questions))
	for _, q := range d.Questions {
		options := make([]SpecDraftOption, 0, len(q.Options))
		for _, o := range q.Options {
			options = append(options, SpecDraftOption{Label: o.Label})
		}
		questions = append(questions, SpecDraftQuestion{Options: options, Question: q.Question, Reason: q.Reason, Recommended: q.Recommended})
	}
	var holdReason *string
	if d.HoldReason != nil {
		r := string(*d.HoldReason)
		holdReason = &r
	}
	return &SpecDraft{
		Answers: fromSpecDraftAnswers(d.Answers), Attempts: d.Attempts, BasePath: d.BasePath, HoldID: d.HoldID, HoldReason: holdReason,
		MaxAttempts: core.SpecDraftMaxAttempts, Path: d.Path, Questions: questions, Reason: d.Reason, Repo: d.Repo, State: string(d.State),
	}
}

// fromSpecDraftAnswers は答えの一覧を変換する（空でも `[]`）。課題の詳細と計画の承認の要約が同じ形で出す。
func fromSpecDraftAnswers(as []core.SpecDraftAnswer) []SpecDraftAnswer {
	out := make([]SpecDraftAnswer, 0, len(as))
	for _, a := range as {
		basis := make([]SpecDraftBasis, 0, len(a.Basis))
		for _, b := range a.Basis {
			basis = append(basis, SpecDraftBasis{Locator: b.Locator, Path: b.Path})
		}
		out = append(out, SpecDraftAnswer{Answer: a.Answer, Attempt: a.Attempt, Basis: basis, By: a.By, Kind: string(a.Kind), Question: a.Question})
	}
	return out
}
