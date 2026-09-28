package core

import (
	"context"
	"errors"
)

// NotStartedReason は周で起動しなかった課題の理由（§IF / API「cycle の JSON
// 出力」の not_started[].reason の閉集合）。本チケットが置くのは枠超過の
// rate_limited と、周の上限額（#83）の cycle_budget だけで、
// upstream_fetch_failed などは後続（#86）が足す。
type NotStartedReason string

// NotStartedRateLimited は、同じ周で枠超過を記録したため起動しなかったことを示す。
const NotStartedRateLimited NotStartedReason = "rate_limited"

// NotStartedCycleBudget は、周の上限額（既消費額＋予約額＋評価額）を超える
// ため起動しなかったことを示す（#83・§予算ガード「周の上限で起動しなかった
// 判断の呼び出し・委譲は…理由（cycle_budget）とともに示す」）。
const NotStartedCycleBudget NotStartedReason = "cycle_budget"

// NotStarted は周で起動しなかった課題 1 件（not_started の 1 要素）。
type NotStarted struct {
	ChallengeID string
	Reason      NotStartedReason
}

// JudgmentCycle は 1 つの周の中の判断の呼び出しを束ね、§枠超過の「枠超過を
// 1 件でも記録した周は、その周の残りの判断の呼び出しと委譲を起動しない」
// を守る。周ごとにゼロ値で作り、周をまたいで使い回さない。並行に使わない
// （判断の段は逐次＝§アーキテクチャ決定「委譲の段だけ並列」）。
//
// #83: NewJudgmentCycle で周の ID（"Y-<n>"）を渡すと、この周が起動する
// すべての run にその ID を紐づけ、Store.RunJudgment の§予算ガードの評価を
// 受けさせる。ゼロ値（周の ID 無し）のままだと、#83 より前と同じ振る舞い
// （run の cycle_id は NULL・予算の評価を行わない）を保つ。
type JudgmentCycle struct {
	cycleID     string
	rateLimited bool
}

// NewJudgmentCycle は、周 cycleID（"Y-<n>"。Store.BeginCycle が返した値）の
// 中の判断の呼び出しを束ねる JudgmentCycle を返す。
func NewJudgmentCycle(cycleID string) *JudgmentCycle {
	return &JudgmentCycle{cycleID: cycleID}
}

// RateLimited は、この周で枠超過を記録した run があったかを返す
// （cycle の JSON 出力の rate_limited）。
func (c *JudgmentCycle) RateLimited() bool { return c.rateLimited }

// RunJudgment は、この周で枠超過が未記録なら s.RunJudgment を呼び、返った
// run が枠超過なら以後の起動を止める。既に枠超過を記録していれば起動も
// 記録もせず（課題の状態と版を変えない）、理由 rate_limited の NotStarted を
// 返す。c に周の ID があれば、呼び出しごとに in.CycleID へその ID を差し込み
// （呼び出し元が個別に渡す必要は無い）、s.RunJudgment が
// ErrBudgetExceeded を返したら、周を止めずに理由 cycle_budget の NotStarted
// へ翻訳する（§予算ガード「周の上限で起動しなかった…周は、LLM を呼ばない
// 処理…を続ける」＝この周の次の課題の評価は独立に行う）。
// 返り値は run の結果と NotStarted のどちらか一方だけが非 nil（エラー時は両方 nil）。
func (c *JudgmentCycle) RunJudgment(ctx context.Context, s *Store, in RunJudgmentInput) (*RunJudgmentResult, *NotStarted, error) {
	if c.rateLimited {
		return nil, &NotStarted{ChallengeID: in.ChallengeID, Reason: NotStartedRateLimited}, nil
	}
	if c.cycleID != "" {
		cid := c.cycleID
		in.CycleID = &cid
	}
	res, err := s.RunJudgment(ctx, in)
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: in.ChallengeID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}
	if res.RateLimited {
		c.rateLimited = true
	}
	return res, nil, nil
}
