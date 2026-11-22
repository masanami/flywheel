package core

// このファイルは、操作（承認・回答など）が成立した後に、サイクルを始めるかどうかの判定（CheckCycleStart）と、
// サイクルを始めたきっかけ（trigger）・始めるかどうかの理由の閉じた一覧を持つ。サイクルを始める処理そのものは
// core の外側（CLI と server）が行う。判定は読み取りだけで、ストアを書かない。

import (
	"context"
	"errors"
)

// CycleStartReason は、サイクルを始めた・始めなかった理由（操作の出力の cycle.reason）。
type CycleStartReason string

// CycleStartReason の値。
const (
	CycleStartStarted        CycleStartReason = "started"
	CycleStartAlreadyRunning CycleStartReason = "already_running"
	CycleStartNoWork         CycleStartReason = "no_work"
	CycleStartDisabled       CycleStartReason = "disabled"
	CycleStartUnavailable    CycleStartReason = "unavailable"
)

// CycleStartReasonValues は CycleStartReason の閉集合の全値を返す。
func CycleStartReasonValues() []CycleStartReason {
	return []CycleStartReason{CycleStartStarted, CycleStartAlreadyRunning, CycleStartNoWork, CycleStartDisabled, CycleStartUnavailable}
}

// サイクルの記録の trigger の値のうち、決まった値（`flywheel cycle --trigger` は自由な文字列も受ける）。
const (
	CycleTriggerManual          = "manual"
	CycleTriggerUI              = "ui"
	CycleTriggerApprove         = "approve"
	CycleTriggerReject          = "reject"
	CycleTriggerAnswer          = "answer"
	CycleTriggerBudget          = "budget"
	CycleTriggerIngest          = "ingest"
	CycleTriggerFollowupPromote = "followup_promote"
	CycleTriggerContinue        = "continue"
)

// CycleTriggerValues は決まった trigger の全値を返す。
func CycleTriggerValues() []string {
	return []string{
		CycleTriggerManual, CycleTriggerUI, CycleTriggerApprove, CycleTriggerReject, CycleTriggerAnswer,
		CycleTriggerBudget, CycleTriggerIngest, CycleTriggerFollowupPromote, CycleTriggerContinue,
	}
}

// CountRoundWorkInWorkspace は connectors.json をワークスペースから読んで CountRoundWork を呼ぶ
// （connectors.json が無ければ conn は nil。不備は ErrConfigInvalid）。
func (s *Store) CountRoundWorkInWorkspace(ctx context.Context, agent *AgentDeclaration) (*RoundWork, error) {
	conn, err := LoadConnectorsDeclaration(s.workspace)
	if err != nil {
		if !errors.Is(err, ErrConfigNotFound) {
			return nil, err
		}
		conn = nil
	}
	return s.CountRoundWork(ctx, agent, conn)
}

// CheckCycleStart は、操作が成立した後に、サイクルを始めてよいかを返す（読み取りだけ）。
// 始めてよいときは CycleStartStarted（これから始める、の意味）、そうでなければその理由を返す。
//   - 宣言の auto_cycle が偽 → disabled
//   - サイクルの排他を、生きている保持者が持っている → already_running（heartbeat が回収の時間を過ぎた排他は、走っていない扱い。
//     始めたサイクルが古い排他を回収する）
//   - いま始められるもの（CountRoundWork。回りの終わりの数えと同じ。学び・改修の提案・書き出し・片付けは数えない）が 0 → no_work
//
// agent が nil（agent.json が無い）ときは、始められるものが無いので no_work。
func (s *Store) CheckCycleStart(ctx context.Context, agent *AgentDeclaration) (CycleStartReason, error) {
	if agent != nil && !agent.AutoCycle {
		return CycleStartDisabled, nil
	}
	st, err := s.ReadCycleStatus(ctx)
	if err != nil {
		return "", err
	}
	if st.State == CycleStateRunning {
		return CycleStartAlreadyRunning, nil
	}
	work, err := s.CountRoundWorkInWorkspace(ctx, agent)
	if err != nil {
		return "", err
	}
	if work.Startable == 0 {
		return CycleStartNoWork, nil
	}
	return CycleStartStarted, nil
}
