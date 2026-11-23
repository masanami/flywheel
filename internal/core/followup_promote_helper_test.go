package core

import (
	"context"
)

// promoteViaPlan は上げる前の確認（PrepareFollowupPromote）と上げる書き込み（PromoteFollowup）を続けて呼び、
// 新しい課題（--new-challenge）として上げた結果を (課題, フォローアップ) で返す。タスクとして上がったときは課題が nil。
func promoteViaPlan(ctx context.Context, s *Store, id string, att Attestation) (*Challenge, *Followup, error) {
	plan, err := s.PrepareFollowupPromote(ctx, id, FollowupPromoteOptions{NewChallenge: true}, nil)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.PromoteFollowup(ctx, plan, att, nil)
	if err != nil {
		return nil, nil, err
	}
	return res.Challenge, res.Followup, nil
}
