package view

import "github.com/masanami/flywheel/internal/core"

// このファイルはタスク（課題の下の作業の単位）と、計画の論点・問いの JSON の形を持つ。

// Task は show の tasks の要素。DependsOn は前提のタスクの ID（"T-<n>"）。NonGoals・DependsOn は無ければ `[]`。
// source_key は対応する取り込み元の子の external_key（対応しなければ null）。
// budget_impl_usd・budget_review_usd はタスクの枠（増額を含む）、spent_usd はそのタスクの実行の費用の合計。
type Task struct {
	Body            string   `json:"body"`
	BudgetImplUSD   float64  `json:"budget_impl_usd"`
	BudgetReviewUSD float64  `json:"budget_review_usd"`
	DependsOn       []string `json:"depends_on"`
	DoneCriteria    string   `json:"done_criteria"`
	ID              string   `json:"id"`
	NonGoals        []string `json:"non_goals"`
	Operation       string   `json:"operation"`
	PlanVersion     int64    `json:"plan_version"`
	Repo            string   `json:"repo"`
	SourceKey       *string  `json:"source_key"`
	SpentUSD        float64  `json:"spent_usd"`
	Status          string   `json:"status"`
	Title           string   `json:"title"`
}

// FromTasks はタスクの一覧を変換する（空でも `[]`）。
func FromTasks(ts []core.Task) []Task {
	out := make([]Task, 0, len(ts))
	for _, t := range ts {
		out = append(out, Task{
			SourceKey: strOrNil(t.SourceKey),
			Body:      t.Body, DependsOn: append([]string{}, t.DependsOn...), DoneCriteria: t.DoneCriteria, ID: t.ID,
			NonGoals: append([]string{}, t.NonGoals...), Operation: t.Operation, PlanVersion: t.PlanVersion,
			Repo: t.Repo, Status: string(t.Status), Title: t.Title,
			BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD, SpentUSD: t.SpentUSD,
		})
	}
	return out
}

// PlanAnswer は計画の問いへの答え。choice・label は選択肢の答え（label は選ばれた選択肢のラベル）、text は文の答え。
// recommended は推奨と同じ答えか。
type PlanAnswer struct {
	Choice      *int64  `json:"choice"`
	Label       *string `json:"label"`
	Recommended bool    `json:"recommended"`
	Text        *string `json:"text"`
}

// PlanQuestion は計画の論点の問い 1 件。number は計画の中の通し番号（1 から）、recommended は options の 1 から数えた位置、
// answer は答えが記録されていなければ null。
type PlanQuestion struct {
	Answer      *PlanAnswer   `json:"answer"`
	Number      int64         `json:"number"`
	Options     []StageOption `json:"options"`
	Question    string        `json:"question"`
	Recommended int           `json:"recommended"`
}

// PlanTopic は計画の論点 1 つ。
type PlanTopic struct {
	Decision  string         `json:"decision"`
	Questions []PlanQuestion `json:"questions"`
	Title     string         `json:"title"`
}

// FromPlanTopics は計画の論点を変換する（空でも `[]`）。
func FromPlanTopics(ts []core.PlanTopicView) []PlanTopic {
	out := make([]PlanTopic, 0, len(ts))
	for _, t := range ts {
		qs := make([]PlanQuestion, 0, len(t.Questions))
		for _, q := range t.Questions {
			opts := make([]StageOption, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, StageOption{Example: o.Example, Label: o.Label})
			}
			pq := PlanQuestion{Number: q.Number, Options: opts, Question: q.Question, Recommended: q.Recommended}
			if q.Answer != nil {
				pq.Answer = &PlanAnswer{Choice: q.Answer.Choice, Label: q.Answer.Label, Recommended: q.Answer.Recommended, Text: q.Answer.Text}
			}
			qs = append(qs, pq)
		}
		out = append(out, PlanTopic{Decision: t.Decision, Questions: qs, Title: t.Title})
	}
	return out
}

// PlanTask は `show --plan --json` のタスク 1 件。id・status は承認の後だけ（承認の前は null）。depends_on は、
// 承認の前は前提のタスクの key、承認の後は前提のタスクの ID。source は対応する取り込み元の子の external_key（対応しなければ null）。
type PlanTask struct {
	DependsOn []string `json:"depends_on"`
	ID        *string  `json:"id"`
	Key       string   `json:"key"`
	Source    *string  `json:"source"`
	Status    *string  `json:"status"`
	Title     string   `json:"title"`
}

// PlanOverview は `show --plan --json` の plan。budget_usd は実装の枠とレビュー対応の枠の合計（タスクが全部枠を持つ計画では、取りやめでないタスクの枠の合計〔済みのタスクを含む。承認の後は増額を含む実効値〕。それ以外は、計画が書いた額で、書いていない枠は宣言のサイズごとの既定。その版の予算の増額があれば置き換えた額。決められないとき null）。
type PlanOverview struct {
	Body            string          `json:"body"`
	BudgetUSD       *float64        `json:"budget_usd"`
	ChallengeID     string          `json:"challenge_id"`
	Expected        []StageExpected `json:"expected"`
	NonGoals        []string        `json:"non_goals"`
	Structured      bool            `json:"structured"`
	Summary         *string         `json:"summary"`
	Tasks           []PlanTask      `json:"tasks"`
	Title           string          `json:"title"`
	Topics          []PlanTopic     `json:"topics"`
	UsesIntegration *bool           `json:"uses_integration"`
	Version         int64           `json:"version"`
}

// PlanOverviewResponse は `show --plan --json` の本文。
type PlanOverviewResponse struct {
	Plan PlanOverview `json:"plan"`
}

// FromPlanView は core.PlanView を PlanOverviewResponse へ変換する。
func FromPlanView(p *core.PlanView) PlanOverviewResponse {
	tasks := make([]PlanTask, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		pt := PlanTask{DependsOn: append([]string{}, t.DependsOn...), Key: t.Key, Source: strOrNil(t.Source), Title: t.Title}
		if t.ID != "" {
			id, st := t.ID, string(t.Status)
			pt.ID, pt.Status = &id, &st
		}
		tasks = append(tasks, pt)
	}
	expected := make([]StageExpected, 0, len(p.Expected))
	for _, e := range p.Expected {
		expected = append(expected, fromExpected(e))
	}
	return PlanOverviewResponse{Plan: PlanOverview{
		Body: p.Body, BudgetUSD: p.BudgetUSD, ChallengeID: p.ChallengeID, Expected: expected,
		NonGoals: append([]string{}, p.NonGoals...), Structured: p.Structured, Summary: p.Summary, Tasks: tasks, Title: p.Title,
		Topics: FromPlanTopics(p.Topics), UsesIntegration: p.UsesIntegration, Version: p.Version,
	}}
}
