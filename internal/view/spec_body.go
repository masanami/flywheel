package view

import "github.com/masanami/flywheel/internal/core"

// ChallengeSpecResponse は `GET …/challenges/{id}/spec` の本文。body と unreadable_reason は、どちらか一方だけが非 null。
type ChallengeSpecResponse struct {
	Body    *string `json:"body"`
	IsDraft bool    `json:"is_draft"`
	Path    string  `json:"path"`
	// UnreadableReason は本文を読めない訳（core の閉集合の値）。
	UnreadableReason *string `json:"unreadable_reason"`
}

// FromChallengeSpec は core.ChallengeSpec を変換する。
func FromChallengeSpec(s *core.ChallengeSpec) ChallengeSpecResponse {
	out := ChallengeSpecResponse{Body: s.Body, IsDraft: s.IsDraft, Path: s.Path}
	if s.UnreadableReason != nil {
		r := string(*s.UnreadableReason)
		out.UnreadableReason = &r
	}
	return out
}
