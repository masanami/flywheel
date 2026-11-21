package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは課題のツリー（`GET /api/v1/workspaces/{name}/tree` と、課題の詳細の API の根の tree）の応答の型を持つ。
// API 専用で、CLI の --json には出さない。表示の区分（phase）・タスクの進み・費用・予算・未のフォローアップの件数は core が決めた値。

// TreeTask は課題のツリーのタスク 1 件。progress は表示の区分（phase）が running のタスクの細かい進み（TaskProgress の値。ほかは null）。id・status は計画の承認の後だけ（承認の前は null）。depends_on は、承認の後でも
// 前提のタスクの計画の中の識別子（key）。
type TreeTask struct {
	CostUSD   float64  `json:"cost_usd"`
	DependsOn []string `json:"depends_on"`
	ID        *string  `json:"id"`
	Key       string   `json:"key"`
	Phase     string   `json:"phase"`
	Progress  *string  `json:"progress"`
	Status    *string  `json:"status"`
	Title     string   `json:"title"`
}

// TreeDependency は課題の前提の課題 1 件。
type TreeDependency struct {
	ChallengeID string `json:"challenge_id"`
	Satisfied   bool   `json:"satisfied"`
	Status      string `json:"status"`
	StatusLabel string `json:"status_label"`
	Title       string `json:"title"`
}

// TreeRelative は親か子の課題 1 件。
type TreeRelative struct {
	ChallengeID string `json:"challenge_id"`
	Status      string `json:"status"`
	StatusLabel string `json:"status_label"`
	Title       string `json:"title"`
}

// TreeChallenge は課題のツリーの課題 1 件。next_human_actions は課題の一覧の card.next_human_actions と同じ値。
// progress は統合先を使う課題の細かい進み（ChallengeProgress の値。ほかは null）。
// budget_usd は `show --plan --json` の plan.budget_usd と同じ規則の値（決められないとき・計画が無いとき null）。
type TreeChallenge struct {
	BudgetUSD        *float64         `json:"budget_usd"`
	Children         []TreeRelative   `json:"children"`
	CostUSD          float64          `json:"cost_usd"`
	Dependencies     []TreeDependency `json:"dependencies"`
	FollowupsOpen    int              `json:"followups_open"`
	ID               string           `json:"id"`
	NextHumanActions []string         `json:"next_human_actions"`
	Parent           *TreeRelative    `json:"parent"`
	Progress         *string          `json:"progress"`
	Status           string           `json:"status"`
	StatusLabel      string           `json:"status_label"`
	Tasks            []TreeTask       `json:"tasks"`
	TasksDone        int              `json:"tasks_done"`
	TasksTotal       int              `json:"tasks_total"`
	Title            string           `json:"title"`
	UpdatedAt        string           `json:"updated_at"`
	UsesIntegration  *bool            `json:"uses_integration"`
}

// TreeResponse は `GET …/tree` の本文。
type TreeResponse struct {
	Challenges []TreeChallenge `json:"challenges"`
	RecentDone []TreeChallenge `json:"recent_done"`
}

func statusLabel(s core.Status) string {
	label, _ := s.Label()
	return label
}

// FromTreeChallenge は core の課題のツリーの要素を変換する（空でも `[]`）。
func FromTreeChallenge(c core.TreeChallenge) TreeChallenge {
	acts := make([]string, 0, len(c.NextHumanActions))
	for _, a := range c.NextHumanActions {
		acts = append(acts, string(a))
	}
	tasks := make([]TreeTask, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		tt := TreeTask{CostUSD: t.CostUSD, DependsOn: append([]string{}, t.DependsOn...), ID: t.ID, Key: t.Key, Phase: string(t.Phase), Title: t.Title}
		if t.Status != nil {
			s := string(*t.Status)
			tt.Status = &s
		}
		if t.Progress != nil {
			pr := string(*t.Progress)
			tt.Progress = &pr
		}
		tasks = append(tasks, tt)
	}
	deps := make([]TreeDependency, 0, len(c.Dependencies))
	for _, d := range c.Dependencies {
		deps = append(deps, TreeDependency{ChallengeID: d.ChallengeID, Satisfied: d.Satisfied, Status: string(d.Status), StatusLabel: statusLabel(d.Status), Title: d.Title})
	}
	rel := func(r core.TreeRelative) TreeRelative {
		return TreeRelative{ChallengeID: r.ChallengeID, Status: string(r.Status), StatusLabel: statusLabel(r.Status), Title: r.Title}
	}
	var progress *string
	if c.Progress != nil {
		pr := string(*c.Progress)
		progress = &pr
	}
	var parent *TreeRelative
	if c.Parent != nil {
		p := rel(*c.Parent)
		parent = &p
	}
	kids := make([]TreeRelative, 0, len(c.Children))
	for _, k := range c.Children {
		kids = append(kids, rel(k))
	}
	return TreeChallenge{
		BudgetUSD: c.BudgetUSD, Children: kids, CostUSD: c.CostUSD, Dependencies: deps, FollowupsOpen: c.FollowupsOpen, ID: c.ID,
		NextHumanActions: acts, Parent: parent, Progress: progress, Status: string(c.Status), StatusLabel: statusLabel(c.Status), Tasks: tasks,
		TasksDone: c.TasksDone, TasksTotal: c.TasksTotal, Title: c.Title, UpdatedAt: FormatTimestamp(c.UpdatedAt), UsesIntegration: c.UsesIntegration,
	}
}

// FromChallengeTree は core の課題のツリーを応答の型へ変換する。
func FromChallengeTree(t *core.ChallengeTree) TreeResponse {
	out := TreeResponse{Challenges: make([]TreeChallenge, 0, len(t.Challenges)), RecentDone: make([]TreeChallenge, 0, len(t.RecentDone))}
	for _, c := range t.Challenges {
		out.Challenges = append(out.Challenges, FromTreeChallenge(c))
	}
	for _, c := range t.RecentDone {
		out.RecentDone = append(out.RecentDone, FromTreeChallenge(c))
	}
	return out
}
