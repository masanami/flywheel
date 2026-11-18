package view

import "github.com/masanami/flywheel/internal/core"

// このファイルはフォローアップ（課題にぶら下がる 1 行の項目）の JSON の形を持つ。
// `followup list --json`・フォローアップの一覧の API・課題の詳細の followups が同じ型を共有する。

// Followup はフォローアップ 1 件。challenge_id は課題に属さなければ null、note・reason・promoted_challenge_id は無ければ null。
type Followup struct {
	ChallengeID         *string `json:"challenge_id"`
	CreatedAt           string  `json:"created_at"`
	Detail              string  `json:"detail"`
	ID                  string  `json:"id"`
	Note                *string `json:"note"`
	PromotedChallengeID *string `json:"promoted_challenge_id"`
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
			Note: f.Note, PromotedChallengeID: f.PromotedChallengeID, Reason: f.Reason, Source: string(f.Source),
			Status: string(f.Status), Title: f.Title, UpdatedAt: FormatTimestamp(f.UpdatedAt),
		})
	}
	return out
}

// FromFollowupList はフォローアップの一覧を FollowupListResponse へ変換する。
func FromFollowupList(fs []core.Followup) FollowupListResponse {
	return FollowupListResponse{Followups: FromFollowups(fs)}
}

// FollowupPromoteResponse は `followup promote` の成功の形。followup は上げたフォローアップ、challenge は作った課題。
type FollowupPromoteResponse struct {
	Challenge Challenge `json:"challenge"`
	Followup  Followup  `json:"followup"`
}

// FromFollowupPromote は上げたフォローアップと作った課題を FollowupPromoteResponse へ変換する。
func FromFollowupPromote(c core.Challenge, f core.Followup) FollowupPromoteResponse {
	return FollowupPromoteResponse{Challenge: FromChallenge(c), Followup: FromFollowups([]core.Followup{f})[0]}
}
