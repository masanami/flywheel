package view

import "github.com/masanami/flywheel/internal/core"

// このファイルはタスク（課題の下の作業の単位）と、計画の論点・問いの JSON の形を持つ。

// Task は show の tasks の要素。DependsOn は前提のタスクの ID（"T-<n>"）。NonGoals・DependsOn は無ければ `[]`。
// source_key は対応する取り込み元の子の external_key（対応しなければ null）。
// budget_impl_usd・budget_review_usd はタスクの枠（増額を含む）、spent_usd はそのタスクの実行の費用の合計。
//
// artifacts・delegate・facts・landing・verification は委譲先の実行・成果物・作業ログから core が組み立てる値の口で、
// planned_flow・flow_reason はタスクの記録の、計画が選んだフローとその理由（flow の形でない委譲先・記録の無いタスクは null）。
// 値が無いときは artifacts が `[]`、delegate・landing・verification が null、facts が `{}`。
type Task struct {
	// Added は計画の後に足されたタスクの印。計画の承認で作ったタスクは null。
	Added           *TaskAdded        `json:"added"`
	Artifacts       []TaskArtifact    `json:"artifacts"`
	Body            string            `json:"body"`
	BudgetImplUSD   float64           `json:"budget_impl_usd"`
	BudgetReviewUSD float64           `json:"budget_review_usd"`
	Delegate        *TaskDelegate     `json:"delegate"`
	DependsOn       []string          `json:"depends_on"`
	DoneCriteria    string            `json:"done_criteria"`
	Facts           map[string]string `json:"facts"`
	FlowReason      *string           `json:"flow_reason"`
	ID              string            `json:"id"`
	Landing         *string           `json:"landing"`
	NonGoals        []string          `json:"non_goals"`
	Operation       string            `json:"operation"`
	PlanVersion     int64             `json:"plan_version"`
	PlannedFlow     *string           `json:"planned_flow"`
	Repo            string            `json:"repo"`
	SourceKey       *string           `json:"source_key"`
	SpentUSD        float64           `json:"spent_usd"`
	Status          string            `json:"status"`
	Title           string            `json:"title"`
	Verification    *TaskVerification `json:"verification"`
}

// TaskAdded は足されたタスクの印。origin は足す依頼の出どころ（`expand`・`followup`）、followup_id は出どころが followup のとき元のフォローアップの ID（それ以外は null）、origin_task_id は出どころのタスクの ID（無ければ null）、
// added_at は足した日時。
type TaskAdded struct {
	AddedAt      string  `json:"added_at"`
	FollowupID   *string `json:"followup_id"`
	Origin       string  `json:"origin"`
	OriginTaskID *string `json:"origin_task_id"`
}

func fromTaskAdded(a *core.TaskAdded) *TaskAdded {
	if a == nil {
		return nil
	}
	return &TaskAdded{AddedAt: FormatTimestamp(a.AddedAt), FollowupID: a.FollowupID, Origin: string(a.Origin), OriginTaskID: a.OriginTaskID}
}

// TaskAddition は課題の詳細の additions の要素（足す依頼 1 件。閉じたものも含む）。state は `pending`・`awaiting_owner`・`applied`・
// `none`・`declined`・`superseded`。task_ids はその足す依頼で足したタスクの ID。total_after_usd・limit_usd は、最後に判定したときの
// 足した後の見込みと上限（記録した値。判定していない足す依頼は、どちらも null）。
type TaskAddition struct {
	CreatedAt    string   `json:"created_at"`
	DecidedAt    *string  `json:"decided_at"`
	FollowupID   *string  `json:"followup_id"`
	ID           string   `json:"id"`
	LimitUSD     *float64 `json:"limit_usd"`
	Origin       string   `json:"origin"`
	OriginTaskID *string  `json:"origin_task_id"`
	State        string   `json:"state"`
	TaskIDs      []string `json:"task_ids"`
	// TaskTitles は足す依頼の中身の、足すタスクの題名（足す前の依頼でも読める）。API だけが入れ、CLI の --json には出さない（FromTaskAdditions は入れない。画面の口は FromTaskAdditionsForScreen を使う）。
	TaskTitles    []string `json:"task_titles"`
	TotalAfterUSD *float64 `json:"total_after_usd"`
}

// FromTaskAdditionsForScreen は FromTaskAdditions に、足すタスクの題名（task_titles）を足す。
func FromTaskAdditionsForScreen(as []core.TaskAddition) []TaskAddition {
	out := FromTaskAdditions(as)
	for i := range out {
		out[i].TaskTitles = as[i].TaskTitles()
	}
	return out
}

// FromTaskAdditions は足す依頼の一覧を変換する（空でも `[]`）。
func FromTaskAdditions(as []core.TaskAddition) []TaskAddition {
	out := make([]TaskAddition, 0, len(as))
	for _, a := range as {
		out = append(out, TaskAddition{
			CreatedAt: FormatTimestamp(a.CreatedAt), DecidedAt: ts(a.DecidedAt), FollowupID: a.FollowupID, ID: a.ID, LimitUSD: a.LimitUSD, Origin: string(a.Origin),
			OriginTaskID: a.OriginTaskID, State: string(a.State), TaskIDs: append([]string{}, a.TaskIDs...), TotalAfterUSD: a.TotalAfterUSD,
		})
	}
	return out
}

// TaskDelegate はタスクの最後の契約の実行。state は ContractState の値（記録のまま）、summary は委譲先が返した要約、
// requested_kind は委譲先が求めていること（無ければ null）、run_id はその実行の ID。
// flow は委譲の実行の記録の flow（無ければ null）、steps はステップの行を始まった順にすべての種類で並べたもの（無ければ `[]`）。
type TaskDelegate struct {
	Flow          *string            `json:"flow"`
	RequestedKind *string            `json:"requested_kind"`
	RunID         string             `json:"run_id"`
	State         string             `json:"state"`
	Steps         []TaskDelegateStep `json:"steps"`
	Summary       string             `json:"summary"`
}

// TaskDelegateStep は delegate.steps の要素（ステップ 1 回ぶん）。cost_usd は USD に直した数（無ければ null）、models_used は使ったモデルの名前、
// state は `running`・`finished`・`cancelled`・`interrupted`。effort はステップの記録の、書いた effort（無ければ null）。
type TaskDelegateStep struct {
	Attempt     int64    `json:"attempt"`
	CostUnknown bool     `json:"cost_unknown"`
	CostUSD     *float64 `json:"cost_usd"`
	Effort      *string  `json:"effort"`
	EndedAt     *string  `json:"ended_at"`
	Kind        string   `json:"kind"`
	Model       *string  `json:"model"`
	ModelsUsed  []string `json:"models_used"`
	Outcome     *string  `json:"outcome"`
	StartedAt   string   `json:"started_at"`
	State       string   `json:"state"`
	Step        string   `json:"step"`
}

// TaskArtifact はタスクの実行の成果物 1 件。kind・ref・state は記録のまま、role は統合先の印（`integration`）か null。
type TaskArtifact struct {
	Kind  string  `json:"kind"`
	Ref   string  `json:"ref"`
	Role  *string `json:"role"`
	State string  `json:"state"`
}

// TaskVerification はタスクの検証の最後の記録。verdict は VerifyResult の値（記録のまま）、at は記録の時刻。
type TaskVerification struct {
	At      string `json:"at"`
	Reason  string `json:"reason"`
	Verdict string `json:"verdict"`
}

// FromTasks はタスクの一覧を変換する（空でも `[]`）。
func FromTasks(ts []core.Task) []Task {
	out := make([]Task, 0, len(ts))
	for _, t := range ts {
		out = append(out, Task{
			Added: fromTaskAdded(t.Added), Artifacts: fromTaskArtifacts(t.Detail.Artifacts), Delegate: fromTaskDelegate(t.Detail.Delegate),
			Facts: fromTaskFacts(t.Detail.Facts), Landing: fromTaskLanding(t.Detail.Landing), Verification: fromTaskVerification(t.Detail.Verification),
			SourceKey: strOrNil(t.SourceKey), PlannedFlow: strOrNil(t.PlannedFlow), FlowReason: strOrNil(t.FlowReason),
			Body: t.Body, DependsOn: append([]string{}, t.DependsOn...), DoneCriteria: t.DoneCriteria, ID: t.ID,
			NonGoals: append([]string{}, t.NonGoals...), Operation: t.Operation, PlanVersion: t.PlanVersion,
			Repo: t.Repo, Status: string(t.Status), Title: t.Title,
			BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD, SpentUSD: t.SpentUSD,
		})
	}
	return out
}

func fromTaskArtifacts(as []core.TaskArtifactView) []TaskArtifact {
	out := make([]TaskArtifact, 0, len(as))
	for _, a := range as {
		out = append(out, TaskArtifact{Kind: a.Kind, Ref: a.Ref, Role: a.Role, State: a.State})
	}
	return out
}

func fromTaskDelegate(d *core.TaskDelegate) *TaskDelegate {
	if d == nil {
		return nil
	}
	steps := make([]TaskDelegateStep, 0, len(d.Steps))
	for _, sp := range d.Steps {
		steps = append(steps, TaskDelegateStep{
			Attempt: sp.Attempt, CostUnknown: sp.CostUnknown, CostUSD: sp.CostUSD, EndedAt: ts(sp.EndedAt), Kind: sp.Kind, Model: sp.Model,
			Effort: sp.Effort, ModelsUsed: append([]string{}, sp.ModelsUsed...), Outcome: sp.Outcome, StartedAt: FormatTimestamp(sp.StartedAt), State: sp.State, Step: sp.Step,
		})
	}
	return &TaskDelegate{Flow: d.Flow, RequestedKind: d.RequestedKind, RunID: d.RunID, State: string(d.State), Steps: steps, Summary: d.Summary}
}

func fromTaskFacts(f map[string]string) map[string]string {
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

func fromTaskLanding(l *core.TaskLanding) *string {
	if l == nil {
		return nil
	}
	s := string(*l)
	return &s
}

func fromTaskVerification(v *core.TaskVerificationView) *TaskVerification {
	if v == nil {
		return nil
	}
	return &TaskVerification{At: FormatTimestamp(v.At), Reason: v.Reason, Verdict: v.Verdict}
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
// 承認の前は前提のタスクの key、承認の後は前提のタスクの ID。budget_impl_usd・budget_review_usd はタスクの枠（計画が書いたときだけ。承認の後は増額を含む実効値。書いていなければ null）。source は対応する取り込み元の子の external_key（対応しなければ null）。
type PlanTask struct {
	BudgetImplUSD   *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD *float64 `json:"budget_review_usd"`
	DependsOn       []string `json:"depends_on"`
	ID              *string  `json:"id"`
	Key             string   `json:"key"`
	Source          *string  `json:"source"`
	Status          *string  `json:"status"`
	Title           string   `json:"title"`
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
		pt := PlanTask{BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD, DependsOn: append([]string{}, t.DependsOn...), Key: t.Key, Source: strOrNil(t.Source), Title: t.Title}
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
