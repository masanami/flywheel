package view

import "github.com/masanami/flywheel/internal/core"

// このファイルはフォローアップ（課題にぶら下がる 1 行の項目）の JSON の形を持つ。
// `followup list --json`・フォローアップの一覧の API・課題の詳細の followups が同じ型を共有する。

// Followup はフォローアップ 1 件。challenge_id は課題に属さなければ null、note・reason・promoted_challenge_id・promoted_task_id は無ければ null（上げた先は、promoted_task_id があればそちら、無ければ promoted_challenge_id）。
type Followup struct {
	ChallengeID         *string `json:"challenge_id"`
	CreatedAt           string  `json:"created_at"`
	Detail              string  `json:"detail"`
	ID                  string  `json:"id"`
	Note                *string `json:"note"`
	PromotedChallengeID *string `json:"promoted_challenge_id"`
	PromotedTaskID      *string `json:"promoted_task_id"`
	Reason              *string `json:"reason"`
	Source              string  `json:"source"`
	Status              string  `json:"status"`
	Title               string  `json:"title"`
	UpdatedAt           string  `json:"updated_at"`
}

// FollowupListResponse は `followup list` とフォローアップの一覧の API の成功の形。
type FollowupListResponse struct {
	Followups []Followup `json:"followups"`
}

// FromFollowups はフォローアップの一覧を変換する（空でも `[]`）。
func FromFollowups(fs []core.Followup) []Followup {
	out := make([]Followup, 0, len(fs))
	for _, f := range fs {
		out = append(out, Followup{
			ChallengeID: f.ChallengeID, CreatedAt: FormatTimestamp(f.CreatedAt), Detail: f.Detail, ID: f.ID,
			Note: f.Note, PromotedChallengeID: f.PromotedChallengeID, PromotedTaskID: f.PromotedTaskID, Reason: f.Reason, Source: string(f.Source),
			Status: string(f.Status), Title: f.Title, UpdatedAt: FormatTimestamp(f.UpdatedAt),
		})
	}
	return out
}

// FromFollowupList はフォローアップの一覧を FollowupListResponse へ変換する。
func FromFollowupList(fs []core.Followup) FollowupListResponse {
	return FollowupListResponse{Followups: FromFollowups(fs)}
}

// FollowupPromoteResponse は `followup promote` の成功の形。followup は上げたフォローアップ。destination は上げた先の種類（"challenge" か "task"）。
// 新しい課題に上げたとき challenge が作った課題、new_challenge_reason がタスクにしなかった理由（task_addition_id は null）。
// タスクとして上げたとき task_addition_id が足す依頼の ID（challenge・new_challenge_reason は null）。
type FollowupPromoteResponse struct {
	Challenge          *Challenge `json:"challenge"`
	Destination        string     `json:"destination"`
	Followup           Followup   `json:"followup"`
	NewChallengeReason *string    `json:"new_challenge_reason"`
	TaskAdditionID     *string    `json:"task_addition_id"`
}

// FromFollowupPromote は上げた結果を FollowupPromoteResponse へ変換する。
func FromFollowupPromote(r core.FollowupPromoteResult) FollowupPromoteResponse {
	out := FollowupPromoteResponse{Destination: string(r.Dest), Followup: FromFollowups([]core.Followup{*r.Followup})[0]}
	if r.Challenge != nil {
		c := FromChallenge(*r.Challenge)
		out.Challenge = &c
	}
	if r.NewChallengeReason != "" {
		reason := r.NewChallengeReason
		out.NewChallengeReason = &reason
	}
	if r.Addition != nil {
		id := r.Addition.ID
		out.TaskAdditionID = &id
	}
	return out
}
