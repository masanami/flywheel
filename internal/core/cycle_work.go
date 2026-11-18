package core

// このファイルは、サイクルを回すと進むものの数え（CountCycleWork）を持つ。LLM を呼ばず、上流を読まず、
// 状態を変えない。各段が対象を選ぶ関数を、段と同じ形で呼んで数える（条件を写さない）。
// 迷う所は多めに数える（数えが 0 なのに進められるものがある、という食い違いを作らない。
// 逆は何もしないサイクルが 1 回回るだけ）。

import (
	"context"
	"database/sql"
	"errors"
)

// CycleNotStartedNothingToAdvance は、サイクルを回すと進むものが無く周を始めなかった理由（cycle --json の not_started_reason）。
const CycleNotStartedNothingToAdvance = "nothing_to_advance"

// CycleWorkStages はサイクルを回すと進むものの、段ごとの件数。
type CycleWorkStages struct {
	Release  int
	Classify int
	Plan     int
	Run      int
	Verify   int
	Learn    int
	Improve  int
	Cleanup  int
}

// CycleWork はサイクルを回すと進むものの数え。Total は Stages の合計。
type CycleWork struct {
	Total  int
	Stages CycleWorkStages
}

// sum は段の件数の合計を返す。
func (st CycleWorkStages) sum() int {
	return st.Release + st.Classify + st.Plan + st.Run + st.Verify + st.Learn + st.Improve + st.Cleanup
}

// CountCycleWork はサイクルを回すと進むものを数える（読み取りだけ。ストアを書かない）。
// agent が nil（agent.json が無い）なら全段 0。conn が nil（connectors.json が無い）なら、反映・計画・委譲・
// 完了の検証の段は飛ぶので 0。
func (s *Store) CountCycleWork(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration) (*CycleWork, error) {
	var st CycleWorkStages
	if agent == nil {
		return &CycleWork{}, nil
	}

	var runTargets []int64
	var contractRuns int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, _, err := selectJ1AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Classify = len(ids)

		learn, err := selectJ6AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Learn = len(learn)

		if st.Improve, err = s.countImprove(ctx, tx, agent); err != nil {
			return err
		}

		stale, err := s.loadStaleRuns(ctx, tx)
		if err != nil {
			return err
		}
		st.Cleanup = len(stale)

		if conn == nil {
			return nil
		}
		plan, _, err := selectJ2AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Plan = len(plan)
		verify, _, err := selectJ5AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Verify = len(verify)
		if runTargets, _, err = selectDelegationTargets(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL`).Scan(&contractRuns)
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if conn != nil {
		releases, err := s.ListExecutableReleases(ctx)
		if err != nil {
			return nil, err
		}
		st.Release = len(releases)
		n, err := s.countRunnableDelegations(ctx, agent, conn, runTargets)
		if err != nil {
			return nil, err
		}
		st.Run = n + contractRuns
	}
	return &CycleWork{Total: st.sum(), Stages: st}, nil
}

// CountCycleWorkInWorkspace は connectors.json をワークスペースから読んで CountCycleWork を呼ぶ
// （status が使う。connectors.json が無ければ conn は nil。不備は ErrConfigInvalid）。
func (s *Store) CountCycleWorkInWorkspace(ctx context.Context, agent *AgentDeclaration) (*CycleWork, error) {
	conn, err := LoadConnectorsDeclaration(s.workspace)
	if err != nil {
		if !errors.Is(err, ErrConfigNotFound) {
			return nil, err
		}
		conn = nil
	}
	return s.CountCycleWork(ctx, agent, conn)
}

// countImprove は改修提案の段が判断点を呼ぶ条件（ImproveJ8 の自動の呼び出しと同じ）を満たせば 1、満たさなければ 0。
func (s *Store) countImprove(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) (int, error) {
	threshold := improveThreshold(agent)
	jc, err := loadJ8ContextTx(ctx, tx, threshold)
	if err != nil {
		return 0, err
	}
	if jc.Triggering < threshold {
		return 0, nil
	}
	hit, err := loadJ8LimitHit(ctx, tx, judgmentFailureLimit(agent))
	if err != nil {
		return 0, err
	}
	if hit != nil {
		return 0, nil
	}
	return 1, nil
}

// countRunnableDelegations は委譲の対象（selectDelegationTargets の選んだ課題）のうち、サイクルを回しても
// 何も進まないと LLM を呼ばずに決まるもの（plan_unavailable・run_budget）を除いた件数。
// 完了の照合のやり直し（子を起動しない）は進むものとして数える。連続の失敗の上限・やり直しの上限に達した課題も
// 数える（委譲の段が、その課題を人間対応待ちにしてオーナーへ見せる。数えから外すと、ほかに進むものが無いときに
// サイクルが始まらず、課題が着手中のまま、どこにも出ずに止まる）。判定は委譲の段と同じ関数（loadPlanCandidate・
// loadDelegationContext・implBudgetExhausted）で行い、書き込みをしない。
func (s *Store) countRunnableDelegations(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration, targets []int64) (int, error) {
	in := DelegateInput{AgentDecl: agent, ConnDecl: conn}
	retries, rest, err := s.splitReconcileRetries(ctx, targets)
	if err != nil {
		return 0, err
	}
	n := len(retries)
	for _, cid := range rest {
		_, ok, _, err := s.loadPlanCandidate(ctx, in, cid)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue // plan_unavailable
		}
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
		if err != nil {
			return 0, err
		}
		dc, err := s.loadDelegationContext(ctx, in, *ch)
		if err != nil {
			if errors.Is(err, ErrStoreError) {
				return 0, err
			}
			n++ // 読み切れない理由は多めに数える
			continue
		}
		if !dc.advancesWhenTheStageRuns() {
			continue // run_budget（状況の「予算切れ」に出る。サイクルを回しても変わらない）
		}
		n++
	}
	return n, nil
}

// advancesWhenTheStageRuns は、委譲の段がこの課題に着くと何かが進むか（delegateOnce と同じ順で見る）。
// 連続の失敗の上限・やり直しの上限に達していれば、段がその課題を人間対応待ちにするので、進む（実装枠が尽きていても、
// 段は先に上限を見る）。上限に達しておらず、実装枠が尽きていれば、段は起動しないので、進まない（run_budget）。
// それ以外は、起動するので、進む。
func (dc *delegationContext) advancesWhenTheStageRuns() bool {
	if dc.LimitHit != nil || dc.ReworkHit != nil {
		return true
	}
	return !dc.implBudgetExhausted()
}
