package core

import "context"

// NotStartedReason は周で起動しなかった課題の理由（§IF / API「cycle の JSON
// 出力」の not_started[].reason の閉集合）。本チケットが置くのは枠超過の
// rate_limited だけで、cycle_budget・upstream_fetch_failed などは後続（#83・#86）が足す。
type NotStartedReason string

// NotStartedRateLimited は、同じ周で枠超過を記録したため起動しなかったことを示す。
const NotStartedRateLimited NotStartedReason = "rate_limited"

// NotStarted は周で起動しなかった課題 1 件（not_started の 1 要素）。
type NotStarted struct {
	ChallengeID string
	Reason      NotStartedReason
}

// JudgmentCycle は 1 つの周の中の判断の呼び出しを束ね、§枠超過の「枠超過を
// 1 件でも記録した周は、その周の残りの判断の呼び出しと委譲を起動しない」
// を守る。周ごとにゼロ値で作り、周をまたいで使い回さない。並行に使わない
// （判断の段は逐次＝§アーキテクチャ決定「委譲の段だけ並列」）。
type JudgmentCycle struct {
	rateLimited bool
}

// RateLimited は、この周で枠超過を記録した run があったかを返す
// （cycle の JSON 出力の rate_limited）。
func (c *JudgmentCycle) RateLimited() bool { return c.rateLimited }

// RunJudgment は、この周で枠超過が未記録なら s.RunJudgment を呼び、返った
// run が枠超過なら以後の起動を止める。既に枠超過を記録していれば起動も
// 記録もせず（課題の状態と版を変えない）、理由 rate_limited の NotStarted を返す。
// 返り値は run の結果と NotStarted のどちらか一方だけが非 nil（エラー時は両方 nil）。
func (c *JudgmentCycle) RunJudgment(ctx context.Context, s *Store, in RunJudgmentInput) (*RunJudgmentResult, *NotStarted, error) {
	if c.rateLimited {
		return nil, &NotStarted{ChallengeID: in.ChallengeID, Reason: NotStartedRateLimited}, nil
	}
	res, err := s.RunJudgment(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	if res.RateLimited {
		c.rateLimited = true
	}
	return res, nil, nil
}
