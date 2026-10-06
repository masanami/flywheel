package core

import (
	"context"
	"errors"
	"sync/atomic"
)

// NotStartedReason は周で起動しなかった課題の理由（§IF / API「cycle の JSON
// 出力」の not_started[].reason の閉集合）。S1 の値は枠超過の rate_limited・
// 周の上限額（#83）の cycle_budget・J2 の上流の取得の失敗（#85）の
// upstream_fetch_failed の 3 つ。
type NotStartedReason string

// NotStartedRateLimited は、同じ周で枠超過を記録したため起動しなかったことを示す。
const NotStartedRateLimited NotStartedReason = "rate_limited"

// NotStartedCycleBudget は、周の上限額（既消費額＋予約額＋評価額）を超える
// ため起動しなかったことを示す（#83・§予算ガード「周の上限で起動しなかった
// 判断の呼び出し・委譲は…理由（cycle_budget）とともに示す」）。
const NotStartedCycleBudget NotStartedReason = "cycle_budget"

// NotStartedUpstreamFetchFailed は、J2 の起動の直前の上流の取得（本文・全コメント・
// 参照先の Issue）に失敗したため起動しなかったことを示す（#85・§J2 計画「上流の
// 取得に失敗したら、J2 を起動せず、その課題の結果に失敗として示す」）。
const NotStartedUpstreamFetchFailed NotStartedReason = "upstream_fetch_failed"

// NotStartedSlotUnavailable は、委譲の候補のリポジトリに使えるスロットが無く起動しなかった
// ことを表す（S2。`run` の NotStarted だけが使う）。`cycle` の not_started の閉集合
// （NotStartedReasonValues）に含まれる。
const NotStartedSlotUnavailable NotStartedReason = "slot_unavailable"

// NotStartedPlanUnavailable は、計画の承認が済んでいるのに承認済みの計画を引けず（承認に計画の版が
// 無い・版の計画が無い・構造化した出力が無い）委譲を起動しなかったことを表す。理由は Detail に付く。
// `cycle` の not_started の閉集合（NotStartedReasonValues）に含まれる。
const NotStartedPlanUnavailable NotStartedReason = "plan_unavailable"

// notStartedReasonValues は NotStartedReason の閉集合（仕様の列挙の順）。
var notStartedReasonValues = []NotStartedReason{
	NotStartedCycleBudget,
	NotStartedRateLimited,
	NotStartedRunBudget,
	NotStartedSlotUnavailable,
	NotStartedFailureLimit,
	NotStartedReworkLimit,
	NotStartedUpstreamFetchFailed,
	NotStartedSerialized,
	NotStartedWaitingExternal,
	NotStartedPlanUnavailable,
}

// NotStartedReasonValues は NotStartedReason の閉集合（§IF / API「cycle の JSON 出力」の
// not_started[].reason。S2 の値を含む）の写しを返す（CLI のテストが仕様の列挙と双方向に
// 照合するため。IngestOutcomeValues と同じ形）。
func NotStartedReasonValues() []NotStartedReason {
	return append([]NotStartedReason(nil), notStartedReasonValues...)
}

// NotStarted は周で起動しなかった課題 1 件（not_started の 1 要素）。
type NotStarted struct {
	ChallengeID string
	Reason      NotStartedReason
	// Detail は人が読む補足（例: 取得の失敗の原因）。JSON 出力の形
	// （challenge_id・reason）には含めず、テキスト出力にだけ使う。空なら無し。
	Detail string
}

// JudgmentAutoItem は `--auto` の個別の操作（`classify --auto`・`plan --auto`）が
// 処理した課題 1 件の結果（§IF / API「`--auto` の個別の操作…は cycle の phases の
// 1 要素と同じ形」の items の要素）。判断点ごとに別の型を持たず、1 つの型を共有する
// （`cycle` の段〈#86〉も同じ形で返す）。
type JudgmentAutoItem struct {
	ChallengeID string
	RunID       string
	Result      RunResult
	// Outcome は判断点の判定（J1: mine/not_mine/uncertain、J2: plan/uncertain）。
	// run が succeeded でなかった・core の再検査に落ちた（invalid_output）場合は
	// 空文字列。
	Outcome string
	// Status は写した後の課題の状態コード。写さなかった場合は nil。
	Status *string
	// Note は人が読む補足（例: 計画は登録したが読んだ記録を付けられなかった理由）。
	// JSON 出力の形には含めず、テキスト出力にだけ使う。空なら無し。
	Note string
}

// JudgmentAutoResult は `--auto` の個別の操作の出力（items と not_started）。
type JudgmentAutoResult struct {
	Items      []JudgmentAutoItem
	NotStarted []NotStarted
	// SerialGroups は委譲の段（run）が作った直列化グループ（他の段では nil）。
	SerialGroups []SerialGroup
}

// JudgmentCycle は 1 つの周の中の判断の呼び出しを束ね、§枠超過の「枠超過を
// 1 件でも記録した周は、その周の残りの判断の呼び出しと委譲を起動しない」
// を守る。周ごとにゼロ値で作り、周をまたいで使い回さない。判断の段は逐次、
// 委譲の段は直列化グループごとに並行に使う（枠超過の記録は並行に呼んでも安全）。
//
// #83: NewJudgmentCycle で周の ID（"Y-<n>"）を渡すと、この周が起動する
// すべての run にその ID を紐づけ、Store.RunJudgment の§予算ガードの評価を
// 受けさせる。ゼロ値（周の ID 無し）のままだと、#83 より前と同じ振る舞い
// （run の cycle_id は NULL・予算の評価を行わない）を保つ。
type JudgmentCycle struct {
	cycleID     string
	rateLimited atomic.Bool
}

// NewJudgmentCycle は、周 cycleID（"Y-<n>"。Store.BeginCycle が返した値）の
// 中の判断の呼び出しを束ねる JudgmentCycle を返す。
func NewJudgmentCycle(cycleID string) *JudgmentCycle {
	return &JudgmentCycle{cycleID: cycleID}
}

// RateLimited は、この周で枠超過を記録した run があったかを返す
// （cycle の JSON 出力の rate_limited）。
func (c *JudgmentCycle) RateLimited() bool { return c.rateLimited.Load() }

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
	if c.rateLimited.Load() {
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
		c.rateLimited.Store(true)
	}
	return res, nil, nil
}
