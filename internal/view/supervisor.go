package view

import "github.com/masanami/flywheel/internal/core"

// このファイルはスーパーバイザーの対処の記録の JSON の形を持つ（区切り S4b-2。表示・API の経路への結線は後のタスク）。

// SupervisorBasis は根拠の場所 1 件。
type SupervisorBasis struct {
	// Locator は節や行の手がかり。
	Locator string `json:"locator"`
	// Path はワークスペースの根からの相対パス。
	Path string `json:"path"`
}

// SupervisorCondition は split_followup で回した条件 1 件（入力の「回せる条件」の番号と、回した条件の文）。
type SupervisorCondition struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// SupervisorQuestion はスーパーバイザーが答えた子の問い 1 件（問い・答え・根拠）。
type SupervisorQuestion struct {
	Answer   string            `json:"answer"`
	Basis    []SupervisorBasis `json:"basis"`
	Question string            `json:"question"`
}

// FromSupervisorQuestions は答えた子の問いの一覧を変換する（空でも `[]`。根拠も空なら `[]`）。
func FromSupervisorQuestions(qs []core.SupervisorQuestion) []SupervisorQuestion {
	out := make([]SupervisorQuestion, 0, len(qs))
	for _, q := range qs {
		b := make([]SupervisorBasis, 0, len(q.Basis))
		for _, x := range q.Basis {
			b = append(b, SupervisorBasis{Locator: x.Locator, Path: x.Path})
		}
		out = append(out, SupervisorQuestion{Answer: q.Answer, Basis: b, Question: q.Question})
	}
	return out
}

// SupervisorAction は対処の記録 1 件（スーパーバイザーの判断 1 回と、core がその手を実行した結果）。
type SupervisorAction struct {
	Basis       []SupervisorBasis `json:"basis"`
	ChallengeID string            `json:"challenge_id"`
	// Conditions は回した条件。回していなければ空の配列。
	Conditions []SupervisorCondition `json:"conditions"`
	CreatedAt  string                `json:"created_at"`
	CycleID    string                `json:"cycle_id"`
	Findings   string                `json:"findings"`
	// Hand は選んだ手（SUPERVISOR_HAND_CODES の値）。判断できなかったときは null。
	Hand *string `json:"hand"`
	// HoldID は対処した保留の ID（保留には表示用の ID が無いので、内部の整数）。無ければ null。
	HoldID *int64 `json:"hold_id"`
	ID     int64  `json:"id"`
	Note   string `json:"note"`
	// Questions は答えた子の問いと答え（手が answer のときだけ。無ければ []）。
	Questions []SupervisorQuestion `json:"questions"`
	// RejectReason は断りの理由（SUPERVISOR_REJECT_REASON_CODES の値）。結果が rejected でなければ null。
	RejectReason *string `json:"reject_reason"`
	// Result は対処の結果（SUPERVISOR_RESULT_CODES の値）。
	Result string `json:"result"`
	// RunID はスーパーバイザーの判断の実行（"R-<n>"）。起動できなかったときは null。
	RunID *string `json:"run_id"`
	// StopKind は止まり方（STOP_KIND_CODES の値）。
	StopKind string  `json:"stop_kind"`
	TaskID   *string `json:"task_id"`
}

// FromSupervisorAction は core.SupervisorAction を変換する。
func FromSupervisorAction(a core.SupervisorAction) SupervisorAction {
	out := SupervisorAction{
		Basis: make([]SupervisorBasis, 0, len(a.Basis)), ChallengeID: a.ChallengeID,
		Conditions: make([]SupervisorCondition, 0, len(a.Conditions)), CreatedAt: FormatTimestamp(a.CreatedAt),
		CycleID: a.CycleID, Findings: a.Findings, HoldID: a.HoldID, ID: a.ID, Note: a.Note, Result: string(a.Result),
		Questions: FromSupervisorQuestions(a.Questions), RunID: a.RunID, StopKind: string(a.StopKind), TaskID: a.TaskID,
	}
	for _, b := range a.Basis {
		out.Basis = append(out.Basis, SupervisorBasis{Locator: b.Locator, Path: b.Path})
	}
	for _, c := range a.Conditions {
		out.Conditions = append(out.Conditions, SupervisorCondition{Number: c.Number, Text: c.Text})
	}
	if a.Hand != nil {
		v := string(*a.Hand)
		out.Hand = &v
	}
	if a.RejectReason != nil {
		v := string(*a.RejectReason)
		out.RejectReason = &v
	}
	return out
}

// FromSupervisorHandsTaken は打った手の件数（手 → 結果が executed の対処の記録の数）を変換する（無ければ `{}`）。
func FromSupervisorHandsTaken(m map[core.SupervisorHand]int) map[string]int {
	out := make(map[string]int, len(m))
	for h, n := range m {
		out[string(h)] = n
	}
	return out
}

// FromSupervisorActions は対処の記録の一覧を変換する（空でも `[]`）。
func FromSupervisorActions(as []core.SupervisorAction) []SupervisorAction {
	out := make([]SupervisorAction, 0, len(as))
	for _, a := range as {
		out = append(out, FromSupervisorAction(a))
	}
	return out
}
