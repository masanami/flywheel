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
	Release int
	// Settle は片付けの段の対象（解決したが、まだ解決の日時を持たない要求の記録）の数。
	Settle   int
	Classify int
	Plan     int
	Run      int
	Verify   int
	// Supervise はスーパーバイザー待ちの保留の数。
	Supervise int
	Learn     int
	Improve   int
	Cleanup   int
}

// CycleWork はサイクルを回すと進むものの数え。Total は Stages の合計。
type CycleWork struct {
	Total  int
	Stages CycleWorkStages
}

// sum は段の件数の合計を返す。
func (st CycleWorkStages) sum() int {
	return st.Release + st.Settle + st.Classify + st.Plan + st.Run + st.Verify + st.Supervise + st.Learn + st.Improve + st.Cleanup
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
	var contractRuns, blockedRuns, specDrafts int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, _, err := selectJ1AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Classify = len(ids)

		settle, err := selectRequirementSettleTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Settle = len(settle)

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

		if st.Supervise, err = countSupervisorPendingHolds(ctx, tx, agent, 0); err != nil {
			return err
		}

		if conn == nil {
			return nil
		}
		plan, _, err := selectJ2AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Plan = len(plan)
		// 計画の後に足す処理（足す依頼を作る・判断点を「足す」の形で呼ぶ）も、計画の段が進める。
		expand, err := countExpandWork(ctx, tx, agent)
		if err != nil {
			return err
		}
		st.Plan += expand
		verify, _, err := selectJ5AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Verify = len(verify)
		picked, err := selectDelegationTargetsFor(ctx, tx, s.workspace, conn)
		if err != nil {
			return err
		}
		runTargets, blockedRuns = picked.Targets, len(picked.Blocked)
		// 下書きづくりを始められる課題と、行き止まりの下書きづくり（失敗の保留にする）も、委譲の段が進める。
		startable, stalls, err := selectSpecDraftWork(ctx, tx, nil)
		if err != nil {
			return err
		}
		specDrafts = len(startable) + len(stalls)
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
		// 上げる依頼を始められる課題（全部のタスクが済み、開いた契約の実行が無い、統合先を使う着手中の課題）も、委譲の段が進める。
		reflects, err := s.reflectCandidates(ctx, nil, agent)
		if err != nil {
			return nil, err
		}
		st.Run = n + contractRuns + blockedRuns + len(reflects) + specDrafts
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
// サイクルが始まらず、課題が着手中のまま、どこにも出ずに止まる）。判定は委譲の段と同じ関数（loadPlanCandidates〔起動するタスクごとに数える〕・
// loadDelegationContextForTask・implBudgetExhausted）で行い、書き込みをしない。
func (s *Store) countRunnableDelegations(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration, targets []int64) (int, error) {
	p, err := s.runnableDelegationParts(ctx, agent, conn, targets)
	if err != nil {
		return 0, err
	}
	return p.retries + p.holds + p.launches, nil
}

// delegationParts は委譲の対象の課題の、委譲の段が進めるものの内訳（runnableDelegationParts の結果）。
type delegationParts struct {
	// retries は完了の照合のやり直し（子を起動しない）。holds は連続の失敗・やり直しの上限に達していて、段が人間対応待ちにするもの。
	retries, holds int
	// launches は起動できるタスク（課題の枠で起動できないものを除く）。同時に走らせる数の上限は見ていない。
	launches int
}

// runnableDelegationParts は countRunnableDelegations の数え方（判定は委譲の段と同じ関数で行う）を、内訳つきで返す。
func (s *Store) runnableDelegationParts(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration, targets []int64) (delegationParts, error) {
	in := DelegateInput{AgentDecl: agent, ConnDecl: conn}
	var p delegationParts
	retries, rest, err := s.splitReconcileRetries(ctx, targets)
	if err != nil {
		return p, err
	}
	p.retries = len(retries)
	for _, cid := range rest {
		ones, ok, _, err := s.loadPlanCandidates(ctx, in, cid)
		if err != nil {
			return p, err
		}
		if !ok {
			continue // plan_unavailable
		}
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
		if err != nil {
			return p, err
		}
		// 起動するタスクごとに数える。
		for _, one := range ones {
			dc, err := s.loadDelegationContextForTask(ctx, in, *ch, one.cand.TaskSeq)
			if err != nil {
				if errors.Is(err, ErrStoreError) {
					return p, err
				}
				p.launches++ // 読み切れない理由は多めに数える
				continue
			}
			if dc.LimitHit != nil || dc.ReworkHit != nil {
				p.holds++
				continue
			}
			if dc.implBudgetExhausted() {
				continue // run_budget（状況の「予算切れ」に出る。サイクルを回しても変わらない）
			}
			p.launches++
		}
	}
	return p, nil
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

// RoundWork は回りの終わりの数え（CountRoundWork の結果）。LLM を呼ばず、上流を読まず、状態を変えない。
// CountCycleWork（サイクルを始めるかの数え。多めに数える）とは別物で、サイクルを続けるか・待つか・終えるかを決める。
type RoundWork struct {
	// Startable は、いま始められるもの（次の回りで進めうるもの）の件数。Stages はその段ごとの内訳
	// （Settle・Learn・Improve・Cleanup は常に 0）。
	Startable int
	Stages    CycleWorkStages
	// Active は作業中の実行の件数（委譲先が作業している実行。同時に走らせる数に数える実行と同じ決まり〔activeDelegationCount〕）。
	// オーナーへの問いを返して待っているだけの実行・完了の承認を待っているだけの課題の実行は数えない。
	Active int
}

// CountRoundWork は、回りの終わりに「いま始められるもの」と「作業中の実行」を数える（読み取りだけ。ストアを書かない）。
//
// いま始められるものに数えるもの: 実行できる反映・分類・計画・完了の検証（課題の単位・タスクの単位・コンフリクトの解消の確かめを含む）の対象・
// 起動できるタスク・上げる依頼を始められる課題・flywheel が返事か再開を返す番の実行（回答が記録された保留・差し戻しを返せる実行）。
// 数えないもの: オーナーの承認・回答を待っているだけのもの・前提が終わっていないタスク・同時に走らせる数の上限で待つタスク・
// 課題の枠（run_budget）で起動できないタスク・学び・改修の提案・書き出し・片付けの対象・委譲先が作業している実行。
//
// agent が nil なら全て 0。conn が nil（connectors.json が無い）なら、反映・計画・委譲・完了の検証は飛ぶので 0。
// 排他を放した後の「いま始められるもの」の数えも、この関数を使う。
func (s *Store) CountRoundWork(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration) (*RoundWork, error) {
	return s.countRoundWork(ctx, agent, conn, 0)
}

// countRoundWork は CountRoundWork の本体。cycleDBID が 0 でないとき、そのサイクルで見送った（手 defer）スーパーバイザー待ちの保留は数えない
// （見送りだけで次の回りへ進まない）。
func (s *Store) countRoundWork(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration, cycleDBID int64) (*RoundWork, error) {
	w := &RoundWork{}
	if agent == nil {
		return w, nil
	}
	var st CycleWorkStages
	var runTargets []int64
	var answered, heldSuccess, unheldStops, landable int
	var reviewRuns []*contractRunRow
	planVersions := map[int64]*int64{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, _, err := selectJ1AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Classify = len(ids)
		active, err := activeDelegationCount(ctx, tx)
		if err != nil {
			return err
		}
		w.Active = active
		if st.Supervise, err = countSupervisorPendingHolds(ctx, tx, agent, cycleDBID); err != nil {
			return err
		}
		if conn == nil {
			return nil
		}
		plan, _, err := selectJ2AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		st.Plan = len(plan)
		// 計画の後に足す処理（足す依頼を作る・判断点を「足す」の形で呼ぶ）も、計画の段が進める。
		expand, err := countExpandWork(ctx, tx, agent)
		if err != nil {
			return err
		}
		st.Plan += expand
		verify, _, err := selectJ5AutoTargets(ctx, tx)
		if err != nil {
			return err
		}
		tasks, err := selectJ5TaskTargets(ctx, tx)
		if err != nil {
			return err
		}
		fixes, _, err := selectJ5FixTargets(ctx, tx, nil)
		if err != nil {
			return err
		}
		st.Verify = len(verify) + len(tasks) + len(fixes)
		picked, err := selectDelegationTargetsFor(ctx, tx, s.workspace, conn)
		if err != nil {
			return err
		}
		runTargets = picked.Targets
		st.Run = len(picked.Blocked) // 進めようが無い課題は、委譲の段が保留にする
		// 回答が記録された保留を持つ実行（resume で答えを返す番）。
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN hold h ON h.id = k.wait_hold_id JOIN challenge c ON c.id = k.challenge_id
			WHERE k.ended_at IS NULL AND k.state = 'waiting' AND h.answered_at IS NOT NULL
			AND `+taskMayProceedSQL("c", "k.task_id")).Scan(&answered); err != nil {
			return err
		}
		// 保留の間に成功していた実行（検証中へ進める番）と、保留にし損ねた停止（保留にする番）。
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id WHERE `+heldSuccessWhere).Scan(&heldSuccess); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id WHERE `+unheldContractStopWhere).Scan(&unheldStops); err != nil {
			return err
		}
		// 検証した中身を持つ、統合先を使う課題のタスクの実行（レビュー待ち）。「入れてよい」を返す番（まだ返していない中身）。チェックが通っているかは、
		// 上流を読まない数えでは見ない（多めに数える。チェックが終わっていなければ、次の状態の確認は何も返さず、進み無しの回りになる）。
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id JOIN task t ON t.id = k.task_id
			WHERE k.ended_at IS NULL AND k.state = 'waiting' AND k.requested_kind = 'review' AND COALESCE(k.verified_ref, '') <> ''
			AND NOT (k.land_delivered = 1 AND k.land_ref = k.verified_ref)
			AND `+taskMayProceedSQL("c", "k.task_id")+` AND COALESCE(c.uses_integration, 0) = 1 AND t.status = 'verifying'`).Scan(&landable); err != nil {
			return err
		}
		// 着手中の課題の、レビュー待ちの実行（差し戻しを返す番かは、下で決める）。
		rs, err := tx.QueryContext(ctx, contractRunSelect+` WHERE ended_at IS NULL AND kind = 'task' AND state = 'waiting' AND requested_kind = 'review'
			AND EXISTS (SELECT 1 FROM challenge c WHERE c.id = contract_run.challenge_id AND `+taskMayProceedSQL("c", "contract_run.task_id")+`) ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			r, err := scanContractRun(rs)
			if err != nil {
				return err
			}
			reviewRuns = append(reviewRuns, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		for _, r := range reviewRuns {
			if p, found, err := loadApprovedPlanAnySpec(ctx, tx, r.ChallengeID); err != nil {
				return err
			} else if found {
				v := int64(p.Version)
				planVersions[r.ChallengeID] = &v
			}
		}
		return nil
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
		parts, err := s.runnableDelegationParts(ctx, agent, conn, runTargets)
		if err != nil {
			return nil, err
		}
		// 同時に走らせる数の上限で待つタスクは数えない（作業中の実行が上限に達していれば起動できない）。
		room := agent.MaxParallelRuns - w.Active
		if room < 0 {
			room = 0
		}
		launches := parts.launches
		if launches > room {
			launches = room
		}
		replies := 0
		for _, r := range reviewRuns {
			rr, err := s.pendingReviewReply(ctx, r, planVersions[r.ChallengeID], agent)
			if err != nil {
				return nil, err
			}
			if rr != nil && !rr.Skip {
				replies++
			}
		}
		// 上げる依頼を始められる課題も、委譲の段が進める。
		reflects, err := s.reflectCandidates(ctx, nil, agent)
		if err != nil {
			return nil, err
		}
		st.Run += parts.retries + parts.holds + launches + answered + heldSuccess + unheldStops + landable + replies + len(reflects)
	}
	w.Stages = st
	w.Startable = st.sum()
	return w, nil
}

// countSupervisorPendingHolds は、開いたままのスーパーバイザー待ち（supervisor_state が pending）の保留の数
// （人間対応待ちの課題のもの）。サイクルが走っていない間に残るものを、次のサイクルで処理できるものとして数える。
// 宣言の supervisor が偽の間は、残っていてもスーパーバイザー待ちとして扱わないので 0（行は書き換えない）。
// cycleDBID が 0 でないとき、そのサイクルで見送った保留は数えない。
func countSupervisorPendingHolds(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration, cycleDBID int64) (int, error) {
	if !supervisorEnabled(agent) {
		return 0, nil
	}
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hold h JOIN challenge c ON c.id = h.challenge_id WHERE `+supervisorPendingWhere+supervisorNotDeferredIn,
		cycleDBID, cycleDBID).Scan(&n)
	return n, err
}
