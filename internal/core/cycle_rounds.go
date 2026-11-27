package core

// このファイルは、サイクルの回り（進める段を 1 回ずつ通すこと）の繰り返しの制御を持つ（区切り S4a）。
// 回りの中の段は S3d までの 1 回のサイクルと同じ（cycle_run.go の runAdvancePhases）で、ここは、回りの頭の読み直し・
// 回りの終わりの数え（CountRoundWork）・進みの判定・間隔の待ち・歯止め・終わる条件だけを持つ。
//
// 回りごとに作り直すもの: 同じ実行・同じ課題に 1 回だけ触れるための見送り。段の中のそれは、段が呼ばれるたびにストアの状態と
// 段の関数のローカルの値から作り直される（メモリ上に回りをまたいで持つ印は無い）。
// サイクルを通して持つもの: 費用の集計（周の ID で束ねた run の費用。サイクルの費用の上限の評価に入る）と、利用上限の印
// （JudgmentCycle）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// defaultCyclePollInterval は、宣言の cycle_poll_interval_sec が読めないとき（0 以下）に使う間隔。
const defaultCyclePollInterval = time.Duration(defaultCyclePollIntervalSec) * time.Second

// sleepContext は d の間待つ（ctx が取り消されたらその時点で戻り、ctx のエラーを返す）。
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// runRounds は進める段の回りを、終える条件になるまで繰り返す。段ごとの結果を回りをまたいで足し合わせて *phases へ入れる
// （進める段のすべて。skipped の段も含める）。
func (s *Store) runRounds(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle, phases *[]CyclePhaseResult, executed *[]ReleaseExecItem) (CycleEndReason, error) {
	wait := in.Wait
	if wait == nil {
		wait = s.roundWait
	}
	if wait == nil {
		wait = sleepContext
	}
	interval := time.Duration(in.AgentDecl.CyclePollIntervalSec) * time.Second
	if interval <= 0 {
		interval = defaultCyclePollInterval
	}
	var gov roundGovernor
	for round := 1; ; round++ {
		// 回りの頭で、ストアを読み直す（回りの間に記録された承認・回答・差し戻し・予算の増額・取り込みは、この回りの段が読む）。
		before, err := s.progressFingerprint(ctx)
		if err != nil {
			return "", err
		}
		res, err := s.runAdvancePhases(ctx, in, cycleID, jc, executed)
		if errors.Is(err, errCycleStopRequested) {
			// 段の切れ目で止める指示を読んだ。新しい段へ進まず、通していない段は空の結果で埋めて終える（委譲先の実行は取り消さない）。
			*phases = padAdvancePhases(in, mergeRoundPhases(*phases, res))
			return CycleEndStopped, nil
		}
		if err != nil {
			return "", err
		}
		*phases = mergeRoundPhases(*phases, res)
		if err := s.SetCycleRounds(ctx, cycleID, round); err != nil {
			return "", err
		}
		after, err := s.progressFingerprint(ctx)
		if err != nil {
			return "", err
		}
		progressed := before != after

		// 終わる条件の判定は、足し合わせる前の、この回りの段の結果で行う。
		// 同じ回りで 2 つ以上成り立つときの優先順は error → interrupted → stopped → rate_limited → budget → stalled → settled
		// （error・interrupted は段のエラーとして、この関数の外で決まる）。
		stop, err := s.cycleStopRequested(ctx, cycleID)
		if err != nil {
			return "", err
		}
		if reason, ended := roundEndReason(stop, jc.RateLimited(), roundHitCycleBudget(res)); ended {
			return reason, nil
		}
		work, err := s.countRoundWork(ctx, in.AgentDecl, in.ConnDecl, cycleDBIDOf(cycleID))
		if err != nil {
			return "", err
		}
		switch gov.decide(progressed, work, roundWaitsForChecks(res)) {
		case roundSettled:
			return CycleEndSettled, nil
		case roundStalled:
			return CycleEndStalled, nil
		case roundNext:
			continue // 待たずに次の回りへ
		}
		stopped, err := s.waitBetweenRounds(ctx, cycleID, wait, interval)
		if err != nil {
			return "", err
		}
		if stopped {
			return CycleEndStopped, nil
		}
	}
}

// roundEndReason は、この回りの終わりで成り立っている理由（止める指示・利用上限・サイクルの費用の上限）から、終える理由を選ぶ。
// 2 つ以上成り立つときは stopped → rate_limited → budget の順（settled・stalled は、数えの後で決まる）。どれも成り立たなければ ended=false。
func roundEndReason(stop, rateLimited, budget bool) (reason CycleEndReason, ended bool) {
	switch {
	case stop:
		return CycleEndStopped, true
	case rateLimited:
		return CycleEndRateLimited, true
	case budget:
		return CycleEndBudget, true
	}
	return "", false
}

// roundAction は、回りの終わりの数えと進みから決まる、次の動き。
type roundAction int

const (
	// roundSettled は、いま始められるものも作業中の実行も無いので、回りの繰り返しを終える（settled）。
	roundSettled roundAction = iota
	// roundStalled は、作業中の実行が無いまま進みの無い回りが続いたので、回りの繰り返しを終える（stalled）。
	roundStalled
	// roundNext は、待たずに次の回りへ進む。
	roundNext
	// roundWait は、間隔の待ちを 1 回置いてから次の回りへ進む。
	roundWait
)

// roundGovernor は、回りの終わりの数えと進みから、次の動きを決める。歯止め（進みの無い回りの連続の数）を持つ。
type roundGovernor struct {
	// noProgress は、作業中の実行が無いまま、いま始められるものがあって進みの無かった回りの連続の数。
	noProgress int
}

// decide は、回りの終わりの数え w と、その回りに進みがあったか（progressed）から、次の動きを返す。
//   - いま始められるもの 0・作業中の実行 0 → roundSettled
//   - いま始められるもの 1 以上・進み有り → roundNext（連続の数は 0 に戻る）
//   - いま始められるもの 0・作業中の実行 1 以上 → roundWait（作業中の実行を読み直すだけの回り。連続の数は 0 に戻る）
//   - いま始められるもの 1 以上・進み無し・作業中の実行 1 以上 → roundWait（連続の数は 0 に戻る）
//   - 作業中の実行が 0 でも、チェック待ちがあれば、作業中の実行が 1 以上のときと同じ
//   - いま始められるもの 1 以上・進み無し・作業中の実行 0 → 連続の数を 1 足し、3 に達したら roundStalled、達しなければ roundWait
//
// checkWait は、この回りの段の結果にチェック待ち（roundWaitsForChecks）があること。チェック待ちは作業中の実行と同じ扱いにする
// （待ち続け、進みの無い回りの連続の数に数えず、settled にも stalled にもしない）。
func (g *roundGovernor) decide(progressed bool, w *RoundWork, checkWait bool) roundAction {
	waiting := w.Active > 0 || checkWait
	switch {
	case w.Startable == 0 && !waiting:
		return roundSettled
	case w.Startable > 0 && progressed:
		g.noProgress = 0
		return roundNext
	case w.Startable == 0 || waiting:
		g.noProgress = 0
		return roundWait
	}
	g.noProgress++
	if g.noProgress >= cycleStalledRounds {
		return roundStalled
	}
	return roundWait
}

// cycleStopPollStep は、間隔の待ちを区切って止める指示を読む長さの上限。
const cycleStopPollStep = 5 * time.Second

// errCycleStopRequested は、段の切れ目で止める指示を読んだことを runAdvancePhases が呼び出し元へ知らせる内部の印。
var errCycleStopRequested = errors.New("core: stop requested")

// cycleStopRequested は、サイクルの記録に止める指示（stop_requested_at）が入っているかを読む。
func (s *Store) cycleStopRequested(ctx context.Context, cycleID string) (bool, error) {
	id, ok := parseCycleID(cycleID)
	if !ok {
		return false, ErrNotFound
	}
	var requested bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT stop_requested_at IS NOT NULL FROM cycle WHERE id = ?`, id).Scan(&requested)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return requested, nil
}

// waitBetweenRounds は間隔 d の待ちを置く。待ちは長くても cycleStopPollStep ごとに区切り、区切りごとにロックの heartbeat を更新して
// 止める指示を読む（読んだら待ちを打ち切り、stopped=true を返す）。ロックを失っていた（別のサイクルが回収した）ときは、
// 回りの繰り返しを続けず、ErrLockLost で終える（失ったまま段を重ねると、回収した側のサイクルと並んで動く）。
func (s *Store) waitBetweenRounds(ctx context.Context, cycleID string, wait func(context.Context, time.Duration) error, d time.Duration) (stopped bool, _ error) {
	if err := s.HeartbeatCycleLock(ctx, cycleID); errors.Is(err, ErrLockLost) {
		return false, err
	}
	for remaining := d; remaining > 0; {
		step := min(remaining, cycleStopPollStep)
		err := wait(ctx, step)
		// 待ちの間に ctx が取り消されても、周を閉じる側がロックを扱う。更新は取り消しに依存させない。
		if hbErr := s.HeartbeatCycleLock(context.WithoutCancel(ctx), cycleID); errors.Is(hbErr, ErrLockLost) {
			return false, hbErr
		}
		if err != nil {
			return false, err
		}
		remaining -= step
		stop, err := s.cycleStopRequested(ctx, cycleID)
		if err != nil {
			return false, err
		}
		if stop {
			return true, nil
		}
	}
	return false, nil
}

// padAdvancePhases は、回りの途中で止めて通さなかった進める段を、空の結果で埋める（出力の phases は進める段のすべてを常に持つ）。
func padAdvancePhases(in CycleRunInput, acc []CyclePhaseResult) []CyclePhaseResult {
	for i := len(acc); i < len(advancePhaseOrder); i++ {
		p := advancePhaseOrder[i]
		r := CyclePhaseResult{Phase: p, Skipped: advancePhaseSkipped(in, p)}
		if p == CyclePhaseRelease {
			r.ReleaseItems = []ReleaseExecItem{}
		}
		acc = append(acc, r)
	}
	return acc
}

// advancePhaseOrder は進める段の順。
var advancePhaseOrder = []CyclePhase{CyclePhaseRelease, CyclePhaseSettle, CyclePhaseIngest, CyclePhaseClassify, CyclePhasePlan, CyclePhaseRun, CyclePhaseVerify, CyclePhaseSupervise}

// roundHitCycleBudget は、この回りの段の結果に、起動しなかった理由がサイクルの費用の上限（cycle_budget）のものが 1 件でもあるか。
// 課題の枠（run_budget）は含めない。
func roundHitCycleBudget(round []CyclePhaseResult) bool {
	for _, p := range round {
		for _, ns := range p.NotStarted {
			if ns.Reason == NotStartedCycleBudget {
				return true
			}
		}
	}
	return false
}

// roundWaitsForChecks は、この回りの段の結果に、チェックが終わっていないための待ちが 1 件でもあるか
// （release の段の items の waiting_checks、または完了の検証が見送った waiting_external）。回りの終わりの数えの中で上流は読み直さない。
// チェックの失敗（checks_failed）と、待っても解けない waiting_checks（WaitNotResolving。承認した head のチェックが読めない・読む手段が無い）は含めない。
func roundWaitsForChecks(round []CyclePhaseResult) bool {
	for _, p := range round {
		for _, it := range p.ReleaseItems {
			if it.Result == ExecutionResultWaitingChecks && !it.WaitNotResolving {
				return true
			}
		}
		for _, ns := range p.NotStarted {
			if ns.Reason == NotStartedWaitingExternal {
				return true
			}
		}
	}
	return false
}

// mergeRoundPhases は、回りの段の結果を足し合わせる。段ごとに 1 要素のまま（対象の一覧と起動しなかったものの一覧は連結し、
// 1 つだけの値〔取り込みの結果・書き出しの結果など〕は最後の回りのもの）。
func mergeRoundPhases(acc, round []CyclePhaseResult) []CyclePhaseResult {
	if len(acc) == 0 {
		return append([]CyclePhaseResult{}, round...)
	}
	for i := range round {
		a, r := &acc[i], round[i]
		a.Skipped = r.Skipped
		if r.Ingest != nil {
			a.Ingest = r.Ingest
		}
		a.Items = append(a.Items, r.Items...)
		a.ResolvedRequirements = append(a.ResolvedRequirements, r.ResolvedRequirements...)
		a.NotStarted = append(a.NotStarted, r.NotStarted...)
		a.ReleaseItems = append(a.ReleaseItems, r.ReleaseItems...)
		a.ReleaseTidied = append(a.ReleaseTidied, r.ReleaseTidied...)
		a.ImproveItems = append(a.ImproveItems, r.ImproveItems...)
		a.SuperviseProcessed += r.SuperviseProcessed
		for h, n := range r.SuperviseHands {
			if a.SuperviseHands == nil {
				a.SuperviseHands = map[SupervisorHand]int{}
			}
			a.SuperviseHands[h] += n
		}
	}
	return acc
}

// progressFingerprint は「進み」の判定に使う、ストアの状態の指紋を返す。回りの前と後で指紋が変わったことを、進みとする。
// 見るのは、課題の状態・タスクの進み・契約の実行の記録した状態（終わったかどうかを含む）と「入れてよい」を返した印・
// 不可逆操作の状態・保留の開閉。失敗や見送りの記録（実行の記録・作業ログ）は見ない（増えただけの回りは、進みに数えない）。
func (s *Store) progressFingerprint(ctx context.Context) (string, error) {
	h := sha256.New()
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		queries := []string{
			`SELECT 'c', id, status FROM challenge ORDER BY id`,
			`SELECT 't', id, status FROM task ORDER BY id`,
			`SELECT 'k', id, state || '|' || COALESCE(ended_at, '') || '|' || land_delivered || '|' || COALESCE(land_ref, '') || '|' || COALESCE(requested_kind, '') || '|' || COALESCE(wait_hold_id, '') || '|' || COALESCE(verified_ref, '') || '|' || COALESCE(fix_verified_ref, '') || '|' || COALESCE(fix_pending_ref, '') || '|' || held_success FROM contract_run ORDER BY id`,
			`SELECT 'o', id, state FROM operation ORDER BY id`,
			`SELECT 'h', id, COALESCE(answered_at, '') || '|' || COALESCE(closed_at, '') FROM hold ORDER BY id`,
		}
		for _, q := range queries {
			rs, err := tx.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			for rs.Next() {
				var kind string
				var id int64
				var v string
				if err := rs.Scan(&kind, &id, &v); err != nil {
					_ = rs.Close()
					return err
				}
				_, _ = fmt.Fprintf(h, "%s:%d:%s\n", kind, id, v)
			}
			if err := rs.Err(); err != nil {
				_ = rs.Close()
				return err
			}
			_ = rs.Close()
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
