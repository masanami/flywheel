package view

import "github.com/masanami/flywheel/internal/core"

// Discrepancy は食い違いの形。
type Discrepancy struct {
	ChallengeID string `json:"challenge_id"`
	// ChallengeIDs は dependency_cycle の輪の課題の ID（昇順）。それ以外の要素では省く。
	ChallengeIDs []string `json:"challenge_ids,omitempty"`
	// CycleOnly は種類が dependency_cycle だけの要素で true（帯の件数・絞り込みに数えない）。それ以外では省く。
	CycleOnly bool     `json:"cycle_only,omitempty"`
	Kinds     []string `json:"kinds"`
	// OperationID は merged_unapproved の release の ID。上流の食い違いだけの要素では省く。
	OperationID string `json:"operation_id,omitempty"`
}

// Triage は status.needs_human.triage の要素。
type Triage struct {
	ChallengeID string `json:"challenge_id"`
	Reason      string `json:"reason"`
	RunID       string `json:"run_id"`
}

// BudgetExhausted は status.needs_human.budget_exhausted の要素。
type BudgetExhausted struct {
	ChallengeID        string  `json:"challenge_id"`
	ImplRemainingUSD   float64 `json:"impl_remaining_usd"`
	PlanVersion        int     `json:"plan_version"`
	ReviewRemainingUSD float64 `json:"review_remaining_usd"`
	// TaskID は実装の枠が尽きたタスク（タスクを持たない過去の課題は null）。
	TaskID *string `json:"task_id"`
}

// WaitingExternal は status.waiting_external.challenges の要素。
type WaitingExternal struct {
	ChallengeID string `json:"challenge_id"`
	Checks      string `json:"checks"`
	PRURL       string `json:"pr_url"`
}

// Slot は status.needs_human.slots の要素。
type Slot struct {
	Path   string  `json:"path"`
	Repo   string  `json:"repo"`
	RunID  *string `json:"run_id"`
	SlotID string  `json:"slot_id"`
}

// DiscrepancyKinds は食い違いの種類を文字列の一覧へ変換する（空でも `[]`）。
func DiscrepancyKinds(kinds []core.DiscrepancyKind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

// FromDiscrepancies は食い違いの一覧を変換する。
func FromDiscrepancies(ds []core.Discrepancy) []Discrepancy {
	out := make([]Discrepancy, 0, len(ds))
	for _, d := range ds {
		out = append(out, Discrepancy{ChallengeID: d.ChallengeID, Kinds: DiscrepancyKinds(d.Kinds), OperationID: d.OperationID, ChallengeIDs: d.ChallengeIDs, CycleOnly: d.CycleOnly()})
	}
	return out
}

// StatusHold は status.needs_human.holds の要素（needs_human.challenges の課題の、いま開いている保留）。status の画面の口
// （FromOverviewForScreen）だけが入れる。CLI の --json と、画面のほかの口（needs-human）には出さない（FromOverview は入れない）。
// check は保留の照合の値、options[].button は選択肢ごとの「ボタンで選べるか」（どちらも core が決めた値）。回答の口へ送る値だけを持ち、
// 問いの文は課題の詳細・questions の口から読む。
type StatusHold struct {
	ChallengeID string       `json:"challenge_id"`
	Check       string       `json:"check"`
	HoldID      string       `json:"hold_id"`
	Options     []HoldOption `json:"options"`
	// TaskTitle は保留が結び付くタスクの題名（結び付かなければ空）。
	TaskTitle string `json:"task_title"`
}

// StatusNeedsHuman は status.needs_human。
type StatusNeedsHuman struct {
	BudgetExhausted []BudgetExhausted `json:"budget_exhausted"`
	Challenges      []Challenge       `json:"challenges"`
	Discrepancies   []Discrepancy     `json:"discrepancies"`
	// Holds は needs_human.challenges の課題の、いま開いている保留（課題の ID・保留の番号の昇順）。status の画面の口だけが入れる。
	// 開いている保留が 0 件のときと、CLI の --json・画面のほかの口では、キーごと省く。
	Holds      []StatusHold `json:"holds,omitempty"`
	Operations []Operation  `json:"operations"`
	Slots      []Slot       `json:"slots"`
	Triage     []Triage     `json:"triage"`
}

// StatusParentAgent は status.parent_agent（親のエージェントが動く番）。状態は人間対応待ちのままで、
// いま開いている保留の種類が親が直接行う課題。
type StatusParentAgent struct {
	Challenges []Challenge `json:"challenges"`
}

// StatusSupervisorChecking は status.supervisor_checking（スーパーバイザーが確認中の課題。オーナー待ちに入れない）。
// 状態は人間対応待ちのままで、いま開いている保留がすべてスーパーバイザー待ち（supervisor_state が pending）の課題。
type StatusSupervisorChecking struct {
	Challenges []Challenge `json:"challenges"`
	// Count はスーパーバイザーが確認中の課題の件数（challenges の長さ）。
	Count int `json:"count"`
}

// StatusActionable は status.actionable。
type StatusActionable struct {
	Challenges []ActionableChallenge `json:"challenges"`
}

// StatusApproved は status.approved。
type StatusApproved struct {
	Operations []Operation `json:"operations"`
}

// StatusWaitingExternal は status.waiting_external。
type StatusWaitingExternal struct {
	Challenges []WaitingExternal `json:"challenges"`
}

// CycleWorkStages は status.cycle_work.stages（サイクルを回すと進むものの、段ごとの件数）。
type CycleWorkStages struct {
	Classify int `json:"classify"`
	Cleanup  int `json:"cleanup"`
	Improve  int `json:"improve"`
	Learn    int `json:"learn"`
	Plan     int `json:"plan"`
	Release  int `json:"release"`
	Run      int `json:"run"`
	// Settle は解決したが、まだ解決の日時を持たない要求の記録の数。
	Settle int `json:"settle"`
	// Supervise はスーパーバイザー待ちの保留の数（サイクルが走っていない間に残るもの）。
	Supervise int `json:"supervise"`
	Verify    int `json:"verify"`
}

// CycleWork は status.cycle_work。total は stages の値の合計。
type CycleWork struct {
	Stages CycleWorkStages `json:"stages"`
	Total  int             `json:"total"`
}

// FromCycleWork は core.CycleWork を変換する（nil は全て 0）。
func FromCycleWork(w *core.CycleWork) CycleWork {
	if w == nil {
		return CycleWork{}
	}
	st := w.Stages
	return CycleWork{
		Stages: CycleWorkStages{
			Classify: st.Classify, Cleanup: st.Cleanup, Improve: st.Improve, Learn: st.Learn,
			Plan: st.Plan, Release: st.Release, Run: st.Run, Settle: st.Settle, Supervise: st.Supervise, Verify: st.Verify,
		},
		Total: w.Total,
	}
}

// IngestableChild は status.ingestable.items[].children の要素。
type IngestableChild struct {
	ExternalKey       string `json:"external_key"`
	Number            int    `json:"number"`
	ParentExternalKey string `json:"parent_external_key"`
	Title             string `json:"title"`
	URL               string `json:"url"`
}

// IngestableItem は status.ingestable.items の要素（取り込めるものの根）。
type IngestableItem struct {
	Children          []IngestableChild `json:"children"`
	ExternalKey       string            `json:"external_key"`
	Number            int               `json:"number"`
	ParentChallengeID *string           `json:"parent_challenge_id"`
	ParentExternalKey *string           `json:"parent_external_key"`
	Repo              string            `json:"repo"`
	Title             string            `json:"title"`
	URL               string            `json:"url"`
}

// Ingestable は status.ingestable。error は上流を読めなかった理由（読めたときは null）。
type Ingestable struct {
	Error *string          `json:"error"`
	Items []IngestableItem `json:"items"`
}

// StatusFollowups は status.followups。
type StatusFollowups struct {
	Open int `json:"open"`
}

// FromIngestable は core.Ingestable を変換する（nil は null）。
func FromIngestable(in *core.Ingestable) *Ingestable {
	if in == nil {
		return nil
	}
	items := make([]IngestableItem, 0, len(in.Items))
	for _, it := range in.Items {
		kids := make([]IngestableChild, 0, len(it.Children))
		for _, k := range it.Children {
			kids = append(kids, IngestableChild{ExternalKey: k.ExternalKey, Number: k.Number, ParentExternalKey: k.ParentExternalKey, Title: k.Title, URL: k.URL})
		}
		item := IngestableItem{Children: kids, ExternalKey: it.ExternalKey, Number: it.Number, Repo: it.Repo, Title: it.Title, URL: it.URL}
		if it.ParentExternalKey != "" {
			p := it.ParentExternalKey
			item.ParentExternalKey = &p
		}
		if it.ParentChallengeID != "" {
			p := it.ParentChallengeID
			item.ParentChallengeID = &p
		}
		items = append(items, item)
	}
	return &Ingestable{Error: in.Error, Items: items}
}

// StatusResponse は `status` の成功の形。
type StatusResponse struct {
	Actionable StatusActionable `json:"actionable"`
	Approved   StatusApproved   `json:"approved"`
	// BinaryCommit は動いているバイナリのコミット（status のテキストの行と同じ値。Issue #593）。
	// ビルド情報が無いときは "unknown"。
	BinaryCommit string          `json:"binary_commit"`
	CycleWork    CycleWork       `json:"cycle_work"`
	Followups    StatusFollowups `json:"followups"`
	// Ingestable は取り込めるもの。状況の API は問い合わせに ingestable=true が付いたときだけ読み、無いときは null。
	Ingestable         *Ingestable              `json:"ingestable"`
	NeedsHuman         StatusNeedsHuman         `json:"needs_human"`
	ParentAgent        StatusParentAgent        `json:"parent_agent"`
	SupervisorChecking StatusSupervisorChecking `json:"supervisor_checking"`
	WaitingExternal    StatusWaitingExternal    `json:"waiting_external"`
}

// FromOverview は core.Overview を StatusResponse へ変換する。
func FromOverview(ov *core.Overview) StatusResponse {
	triage := make([]Triage, 0, len(ov.NeedsHumanTriage))
	for _, it := range ov.NeedsHumanTriage {
		triage = append(triage, Triage{ChallengeID: it.ChallengeID, Reason: it.Reason, RunID: it.RunID})
	}
	budget := make([]BudgetExhausted, 0, len(ov.NeedsHumanBudgetExhausted))
	for _, it := range ov.NeedsHumanBudgetExhausted {
		var taskID *string
		if it.TaskID != "" {
			id := it.TaskID
			taskID = &id
		}
		budget = append(budget, BudgetExhausted{
			ChallengeID:        it.ChallengeID,
			ImplRemainingUSD:   it.ImplRemainingUSD,
			PlanVersion:        it.PlanVersion,
			ReviewRemainingUSD: it.ReviewRemainingUSD,
			TaskID:             taskID,
		})
	}
	slots := make([]Slot, 0, len(ov.NeedsHumanSlots))
	for _, it := range ov.NeedsHumanSlots {
		slots = append(slots, Slot{Path: it.Path, Repo: it.Repo, RunID: it.RunID, SlotID: it.SlotID})
	}
	waiting := make([]WaitingExternal, 0, len(ov.WaitingExternal))
	for _, it := range ov.WaitingExternal {
		waiting = append(waiting, WaitingExternal{ChallengeID: it.ChallengeID, Checks: "pending", PRURL: it.PRURL})
	}
	binaryCommit := ov.BinaryCommit
	if binaryCommit == "" {
		binaryCommit = core.UnknownBuildValue
	}
	return StatusResponse{
		Actionable:   StatusActionable{Challenges: FromActionableChallenges(ov.ActionableChallenges, ov.UnmetDependencies)},
		Approved:     StatusApproved{Operations: FromOperations(ov.ApprovedOperations)},
		BinaryCommit: binaryCommit,
		CycleWork:    FromCycleWork(ov.CycleWork),
		Followups:    StatusFollowups{Open: ov.FollowupsOpen},
		Ingestable:   FromIngestable(ov.Ingestable),
		NeedsHuman: StatusNeedsHuman{
			BudgetExhausted: budget,
			Challenges:      FromChallenges(ov.NeedsHumanChallenges),
			Discrepancies:   FromDiscrepancies(ov.Discrepancies),
			Operations:      FromOperations(ov.NeedsHumanOperations),
			Slots:           slots,
			Triage:          triage,
		},
		ParentAgent:        StatusParentAgent{Challenges: FromChallenges(ov.ParentAgentChallenges)},
		SupervisorChecking: StatusSupervisorChecking{Challenges: FromChallenges(ov.SupervisorCheckingChallenges), Count: len(ov.SupervisorCheckingChallenges)},
		WaitingExternal:    StatusWaitingExternal{Challenges: waiting},
	}
}

// FromOverviewForScreen は FromOverview に、画面のボタンで答えるための保留の値（照合の値・選択肢ごとの button）を足す。
// 載せるのは needs_human.challenges の課題（オーナー待ち）の保留だけ。親のエージェントが動く番・スーパーバイザーが確認中の課題は
// needs_human の外の区画なので、その保留は載せない（ターミナルの status の表示と同じ範囲）。
func FromOverviewForScreen(ov *core.Overview) StatusResponse {
	out := FromOverview(ov)
	var holds []StatusHold
	for _, c := range ov.NeedsHumanChallenges {
		for _, h := range ov.OpenHolds[c.ID] {
			holds = append(holds, StatusHold{ChallengeID: c.ID, Check: h.Check, HoldID: h.HoldID, Options: FromHoldOptionsForScreen(h.Options, h.OptionButtons), TaskTitle: h.TaskTitle})
		}
	}
	out.NeedsHuman.Holds = holds
	return out
}
