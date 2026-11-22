package core

// このファイルは、予算をタスクごとの見込みにする規則（区切り S3b）を持つ。
//
//   - タスクの枠は、タスクの記録（task.budget_impl_micros・budget_review_micros）の値。空（NULL）の過去のタスクは、
//     見直し前の決め方（承認済みの計画の額、無ければサイズごとの既定。`flywheel budget` の上書きを含む）の値として読む。
//   - `flywheel budget --task` の承認は、そのタスクの記録の値を指定した額で置き換え（足し合わせない）、承認の記録に
//     task_id を残す。タスクの枠の実効値は、その置き換えを含む記録の値。ほかのタスクの枠は変えない。
//   - 課題の見込みは、取りやめでないタスクの枠の実効値の合計（実装の枠・レビュー対応の枠ごと）。承認の前の計画の見込みも、
//     同じ関数（sumTaskBudgets）が数える（planVersionBudgets が、計画のタスクを承認の後と同じ決まりでタスクの枠にする）。
//   - 課題の上限は、見込みとは別の、承認した 1 つの額（challenge.budget_limit_micros。実装の枠とレビュー対応の枠の合計）。
//     記録の無い課題は、見込みと同じ額として読む。
//   - 使った額はタスクごと・枠ごとに run.task_id で数える。判断点（枠 judgment）の費用と、反映の依頼の run
//     （run.reflect = 1）は数えない。タスクが 1 個の計画の版では、見直し前と同じく計画の版で数える
//     （計画の作り直しをまたいで積み上がる）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrBudgetTaskRequired は、未完のタスクが 2 個以上ある課題への `flywheel budget` が、枠を改めるタスクを指定していない
// ことを表す（使い方の誤り）。ストアは変えない。
var ErrBudgetTaskRequired = errors.New("core: budget: the challenge has more than one unfinished task; specify --task")

// taskBudget はタスク 1 件の枠の実効値（USD の 100 万分の 1）。Known が false のとき、枠を決められなかった
// （記録の値が空で、見直し前の決め方の値も引けない）。
type taskBudget struct {
	TaskID      int64
	PlanVersion int64
	Status      TaskStatus
	Impl        int64
	Review      int64
	Known       bool
}

// legacyPlanBudgets は課題の計画の版 planVersion の、見直し前の決め方の枠の額（計画の額、無ければサイズごとの既定に、
// `flywheel budget` の上書きを適用した値）を返す。決められなければ ok=false。
func legacyPlanBudgets(ctx context.Context, tx *sql.Tx, cid, planVersion int64, agent *AgentDeclaration) (planBudgets, bool, error) {
	var spec sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT spec FROM task_plan WHERE challenge_id = ? AND version = ?`, cid, planVersion).Scan(&spec)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !spec.Valid) {
		return planBudgets{}, false, nil
	}
	if err != nil {
		return planBudgets{}, false, err
	}
	implUSD, reviewUSD, ok := resolveSpecBudgets(spec.String, agent)
	if !ok {
		return planBudgets{}, false, nil
	}
	override, err := loadPlanBudgetOverride(ctx, tx, cid, planVersion)
	if err != nil {
		return planBudgets{}, false, err
	}
	return applyBudgetOverride(implUSD, reviewUSD, override), true, nil
}

// fillSoleTaskBudgets は、タスクが 1 個の計画で、タスクが枠のキーを持たないとき、見直し前の決め方の値をそのタスクの
// 枠にする（計画の承認で、タスクの記録へ入れる値）。決められなければ（宣言が無い等）空のまま残す。
func fillSoleTaskBudgets(ctx context.Context, tx *sql.Tx, cid, planVersion int64, tasks []TaskInput, agent *AgentDeclaration) error {
	if len(tasks) != 1 || (tasks[0].BudgetImplMicros != nil && tasks[0].BudgetReviewMicros != nil) {
		return nil
	}
	b, ok, err := legacyPlanBudgets(ctx, tx, cid, planVersion, agent)
	if err != nil || !ok {
		return err
	}
	impl, review := soleTaskFrames(tasks[0].BudgetImplMicros, tasks[0].BudgetReviewMicros, b)
	tasks[0].BudgetImplMicros, tasks[0].BudgetReviewMicros = &impl, &review
	return nil
}

// soleTaskFrames は、タスクが 1 個の計画の枠を、タスクが書いた枠はそのまま、書かなかった側だけ既定 def で埋めて返す
// （承認でタスクへ記録する値と、承認の前の要約が出す値の、共通の決め方）。
func soleTaskFrames(impl, review *int64, def planBudgets) (int64, int64) {
	i, r := def.Impl, def.Review
	if impl != nil {
		i = *impl
	}
	if review != nil {
		r = *review
	}
	return i, r
}

// loadTaskBudgets は課題のタスク全部（取りやめを含む）の枠の実効値を ID の昇順で返す。
func loadTaskBudgets(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) ([]taskBudget, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, plan_version, status, budget_impl_micros, budget_review_micros FROM task WHERE challenge_id = ? ORDER BY id`, cid)
	if err != nil {
		return nil, err
	}
	type rec struct {
		tb           taskBudget
		impl, review sql.NullInt64
	}
	var recs []rec
	for rows.Next() {
		var r rec
		var status string
		if err := rows.Scan(&r.tb.TaskID, &r.tb.PlanVersion, &status, &r.impl, &r.review); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.tb.Status = TaskStatus(status)
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	type legacy struct {
		b  planBudgets
		ok bool
	}
	cache := map[int64]legacy{}
	out := make([]taskBudget, 0, len(recs))
	for _, r := range recs {
		tb := r.tb
		tb.Known = true
		if r.impl.Valid && r.review.Valid {
			tb.Impl, tb.Review = r.impl.Int64, r.review.Int64
			out = append(out, tb)
			continue
		}
		l, seen := cache[tb.PlanVersion]
		if !seen {
			b, ok, err := legacyPlanBudgets(ctx, tx, cid, tb.PlanVersion, agent)
			if err != nil {
				return nil, err
			}
			l = legacy{b, ok}
			cache[tb.PlanVersion] = l
		}
		switch {
		case r.impl.Valid:
			tb.Impl = r.impl.Int64
		case l.ok:
			tb.Impl = l.b.Impl
		default:
			tb.Known = false
		}
		switch {
		case r.review.Valid:
			tb.Review = r.review.Int64
		case l.ok:
			tb.Review = l.b.Review
		default:
			tb.Known = false
		}
		out = append(out, tb)
	}
	return out, nil
}

// sumTaskBudgets は取りやめでないタスクの枠の実効値の合計（課題の見込み）を返す。見込みを計算する唯一の関数で、承認の後
// （loadTaskBudgets の値）も承認の前（planVersionBudgets の値）もこれで数える。済みのタスクの枠は入れ、取りやめのタスクの枠は
// 入れない。対象のタスクが無い・枠を決められないタスクがあるとき ok=false（見込みは不明）。
func sumTaskBudgets(bs []taskBudget) (planBudgets, bool) {
	var sum planBudgets
	n := 0
	for _, b := range bs {
		if b.Status == TaskStatusCancelled {
			continue
		}
		if !b.Known {
			return planBudgets{}, false
		}
		sum.Impl += b.Impl
		sum.Review += b.Review
		n++
	}
	return sum, n > 0
}

// planTaskFrame は計画の出力のタスク 1 件の枠の指定（nil は省いた側）。
type planTaskFrame struct {
	Impl, Review *int64
}

// planVersionBudgets は、承認を待っている（か承認した）計画の版 version の見込みを数えるための、課題のタスクの枠の一覧を返す。
// bs は loadTaskBudgets の値（課題の全タスク）、tasks は計画の出力のタスク、def は計画の額（resolveSpecBudgets に
// `flywheel budget` の上書きを当てた値。決められなければ nil）。
//   - この版の行が、計画のタスクの数だけある（承認の後）ときは、その行の実効値（`flywheel budget --task` の増額を含む）。
//     行が揃っていない（承認の前）ときは計画のタスク。枠を省いた側は、soleTaskFrames と同じに def で埋め、def が無ければ決められない。
//     計画がタスクを持たない過去の形は、枠を省いた 1 件として def で埋める。
//   - ほかの版のタスクは、済み（done）だけを残す（未完のタスクは、この版の承認で取りやめになる）。
func planVersionBudgets(version int64, tasks []planTaskFrame, def *planBudgets, bs []taskBudget) []taskBudget {
	var out []taskBudget
	rows := 0
	for _, b := range bs {
		if b.PlanVersion == version {
			rows++
		}
	}
	if len(tasks) > 0 && rows >= len(tasks) { // 足したタスク（同じ版の行）があれば、行の数は計画のタスクより多い
		for _, b := range bs {
			if b.PlanVersion == version {
				out = append(out, b)
			}
		}
	} else {
		if len(tasks) == 0 {
			tasks = []planTaskFrame{{}}
		}
		for _, t := range tasks {
			tb := taskBudget{PlanVersion: version, Status: TaskStatusPending, Known: true}
			switch {
			case t.Impl != nil && t.Review != nil:
				tb.Impl, tb.Review = *t.Impl, *t.Review
			case def != nil:
				tb.Impl, tb.Review = soleTaskFrames(t.Impl, t.Review, *def)
			default:
				tb.Known = false
				if t.Impl != nil {
					tb.Impl = *t.Impl
				}
				if t.Review != nil {
					tb.Review = *t.Review
				}
			}
			out = append(out, tb)
		}
	}
	for _, b := range bs {
		if b.PlanVersion != version && b.Status == TaskStatusDone {
			out = append(out, b)
		}
	}
	return out
}

// challengeBudgetLimit は課題の上限の記録（challenge.budget_limit_micros・budget_limit_reason）を読む。記録が無ければ nil。
func challengeBudgetLimit(ctx context.Context, tx *sql.Tx, cid int64) (*int64, *string, error) {
	var limit sql.NullInt64
	var reason sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT budget_limit_micros, budget_limit_reason FROM challenge WHERE id = ?`, cid).Scan(&limit, &reason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	var l *int64
	var r *string
	if limit.Valid {
		v := limit.Int64
		l = &v
	}
	if reason.Valid {
		v := reason.String
		r = &v
	}
	return l, r, nil
}

// effectiveBudgetLimit は課題の上限の額（USD の 100 万分の 1）を返す。記録の無い課題は見込みと同じ額。見込みが不明なら ok=false。
func effectiveBudgetLimit(estimate planBudgets, estimateKnown bool, recorded *int64) (int64, bool) {
	if !estimateKnown {
		return 0, false
	}
	if recorded != nil {
		return *recorded, true
	}
	return estimate.Impl + estimate.Review, true
}

// setChallengeBudgetLimit は課題の上限と理由を書く（nil は NULL に戻す）。課題が無ければ ErrNotFound。
func setChallengeBudgetLimit(ctx context.Context, tx *sql.Tx, cid int64, limit *int64, reason *string) error {
	var reasonCol any
	if reason != nil {
		reasonCol = *reason
	}
	res, err := tx.ExecContext(ctx, `UPDATE challenge SET budget_limit_micros = ?, budget_limit_reason = ? WHERE id = ?`, int64PtrColumn(limit), reasonCol, cid)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// planVersionTaskCount は、課題の計画の版 planVersion のタスクの数（取りやめを含む）。
func planVersionTaskCount(bs []taskBudget, planVersion int64) int {
	n := 0
	for _, b := range bs {
		if b.PlanVersion == planVersion {
			n++
		}
	}
	return n
}

// loadTaskBucketSpend はタスク taskID の、枠ごとの費用の合計（run.task_id で数える。判断点の費用と反映の依頼の run は数えない）と、
// 実装の枠の打ち切りの有無を読む。打ち切りは、そのタスクの最後に終わった実装の枠の run が budget_exhausted で、その後に
// そのタスクを指す（か、タスクを指さない過去の）予算の承認が無いこと。
func loadTaskBucketSpend(ctx context.Context, tx *sql.Tx, taskID, planVersion int64) (bucketSpend, error) {
	var sp bucketSpend
	rows, err := tx.QueryContext(ctx,
		`SELECT budget_bucket, COALESCE(SUM(cost_usd), 0) FROM run
		 WHERE task_id = ? AND reflect = 0 AND budget_bucket IN (?, ?) GROUP BY budget_bucket`,
		taskID, string(budgetBucketImpl), string(budgetBucketReview))
	if err != nil {
		return sp, err
	}
	for rows.Next() {
		var bucket string
		var sum int64
		if err := rows.Scan(&bucket, &sum); err != nil {
			_ = rows.Close()
			return sp, err
		}
		if bucket == string(budgetBucketImpl) {
			sp.Impl = sum
		} else {
			sp.Review = sum
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return sp, err
	}
	_ = rows.Close()

	var result, endedAtText sql.NullString
	var challengeID int64
	err = tx.QueryRowContext(ctx,
		`SELECT challenge_id, result, ended_at FROM run
		 WHERE task_id = ? AND budget_bucket = ? AND ended_at IS NOT NULL AND reflect = 0
		 ORDER BY id DESC LIMIT 1`,
		taskID, string(budgetBucketImpl)).Scan(&challengeID, &result, &endedAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return sp, nil
	}
	if err != nil {
		return sp, err
	}
	if result.String != string(RunResultBudgetExhausted) {
		return sp, nil
	}
	endedAt, err := parseTimestamp(endedAtText.String)
	if err != nil {
		return sp, err
	}
	var decidedAtText string
	err = tx.QueryRowContext(ctx,
		`SELECT decided_at FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ?
		   AND (task_id = ? OR (task_id IS NULL AND target_version = ?))
		 ORDER BY id DESC LIMIT 1`,
		challengeID, string(ApprovalKindBudget), string(ApprovalDecisionApproved), taskID, planVersion).Scan(&decidedAtText)
	if errors.Is(err, sql.ErrNoRows) {
		sp.ImplStopped = true
		return sp, nil
	}
	if err != nil {
		return sp, err
	}
	decidedAt, err := parseTimestamp(decidedAtText)
	if err != nil {
		return sp, err
	}
	sp.ImplStopped = decidedAt.Before(endedAt)
	return sp, nil
}

// taskRemaining はタスク tb の、枠ごとの残りを返す。同じ計画の版にタスクが 1 個だけなら、見直し前と同じ数え方
// （計画の版で数え、計画の作り直しをまたいで積み上げる）。統合先を使う課題、または 2 個以上なら、そのタスクの run だけを数える
// （同じ課題のほかのタスクが使った額は引かない）。
func taskRemaining(ctx context.Context, tx *sql.Tx, cid int64, tb taskBudget, bs []taskBudget) (bucketRemaining, error) {
	var sp bucketSpend
	usesIntegration, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return bucketRemaining{}, err
	}
	// 統合先を使う課題は、タスクが 1 個の版でも、そのタスクの run だけを数える（取りやめ・済みの前の版のタスクが
	// 使った額を、新しい版のタスクの枠から引かない）。
	if (usesIntegration == nil || !*usesIntegration) && planVersionTaskCount(bs, tb.PlanVersion) == 1 {
		sp, err = loadBucketSpend(ctx, tx, cid, tb.PlanVersion)
	} else {
		sp, err = loadTaskBucketSpend(ctx, tx, tb.TaskID, tb.PlanVersion)
	}
	if err != nil {
		return bucketRemaining{}, err
	}
	return computeBucketRemaining(planBudgets{Impl: tb.Impl, Review: tb.Review}, sp), nil
}

// loadChallengeSpent は課題が使った額（取りやめのタスクが使った額と、タスクに結び付かない実行の費用を含む）を返す。
// 判断点の費用と反映の依頼の run は数えない。
func loadChallengeSpent(ctx context.Context, tx *sql.Tx, cid int64) (bucketSpend, error) {
	var sp bucketSpend
	rows, err := tx.QueryContext(ctx,
		`SELECT budget_bucket, COALESCE(SUM(cost_usd), 0) FROM run
		 WHERE challenge_id = ? AND reflect = 0 AND budget_bucket IN (?, ?) GROUP BY budget_bucket`,
		cid, string(budgetBucketImpl), string(budgetBucketReview))
	if err != nil {
		return sp, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var bucket string
		var sum int64
		if err := rows.Scan(&bucket, &sum); err != nil {
			return sp, err
		}
		if bucket == string(budgetBucketImpl) {
			sp.Impl = sum
		} else {
			sp.Review = sum
		}
	}
	return sp, rows.Err()
}

// TaskBudget はタスク 1 件の枠と使った額（USD）。
type TaskBudget struct {
	TaskID           string
	Status           TaskStatus
	ImplUSD          float64
	ReviewUSD        float64
	SpentUSD         float64
	ImplRemainingUSD float64
	// Known が false のとき、枠を決められなかった（記録の値が空で、見直し前の決め方の値も引けない）。
	Known bool
}

// ChallengeBudget は課題の見込み（取りやめでないタスクの枠の合計）・承認した上限・使った額（取りやめのタスクを含む）。
type ChallengeBudget struct {
	ImplEstimateUSD   float64
	ReviewEstimateUSD float64
	// EstimateKnown が false のとき、見込みを決められなかった（タスクが無い・枠を決められないタスクがある）。
	EstimateKnown bool
	// LimitUSD は課題の上限（実装の枠とレビュー対応の枠を足した 1 つの額）。記録の無い課題は見込みと同じ額。
	// LimitKnown が false のとき（見込みが不明のとき）は決められない。
	LimitUSD   float64
	LimitKnown bool
	// LimitReason は上限の理由（計画の承認で記録した値）。記録が無ければ nil。
	LimitReason    *string
	ImplSpentUSD   float64
	ReviewSpentUSD float64
	Tasks          []TaskBudget
}

// GetChallengeBudget は課題のタスクごとの枠・使った額と、課題の見込み・上限・使った額を返す。agent は枠の記録が空の
// 過去のタスクの、見直し前の決め方の値を引くために使う（nil 可）。
func (s *Store) GetChallengeBudget(ctx context.Context, challengeID string, agent *AgentDeclaration) (*ChallengeBudget, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, ErrNotFound
	}
	var out ChallengeBudget
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		bs, err := loadTaskBudgets(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		est, known := sumTaskBudgets(bs)
		if known {
			out.EstimateKnown = true
			out.ImplEstimateUSD, out.ReviewEstimateUSD = microsToUSD(est.Impl), microsToUSD(est.Review)
		}
		recorded, reason, err := challengeBudgetLimit(ctx, tx, cid)
		if err != nil {
			return err
		}
		if lim, ok := effectiveBudgetLimit(est, known, recorded); ok {
			out.LimitKnown, out.LimitUSD = true, microsToUSD(lim)
		}
		if recorded != nil {
			out.LimitReason = reason
		}
		sp, err := loadChallengeSpent(ctx, tx, cid)
		if err != nil {
			return err
		}
		out.ImplSpentUSD, out.ReviewSpentUSD = microsToUSD(sp.Impl), microsToUSD(sp.Review)
		for _, b := range bs {
			tsp, err := loadTaskBucketSpend(ctx, tx, b.TaskID, b.PlanVersion)
			if err != nil {
				return err
			}
			tb := TaskBudget{
				TaskID: formatTaskID(b.TaskID), Status: b.Status, Known: b.Known,
				ImplUSD: microsToUSD(b.Impl), ReviewUSD: microsToUSD(b.Review),
				SpentUSD: microsToUSD(tsp.Impl + tsp.Review),
			}
			if b.Known {
				rem, err := taskRemaining(ctx, tx, cid, b, bs)
				if err != nil {
					return err
				}
				tb.ImplRemainingUSD = microsToUSD(rem.Impl)
			}
			out.Tasks = append(out.Tasks, tb)
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// loadChallengeDetailBudget は課題の詳細の予算を組み立てる。計画の承認待ちの課題は、承認を待っている版の計画の見込み
// （loadPlanView の BudgetUSD）で、上限は計画が上限を書くまで見込みと同じ額。それ以外は、タスクの枠の合計（見込み）と、
// 課題に記録した上限（無ければ見込みと同じ額）。見込みが不明なら、見込みも上限も nil。
func loadChallengeDetailBudget(ctx context.Context, tx *sql.Tx, cid int64, status Status, agent *AgentDeclaration) (ChallengeDetailBudget, error) {
	var out ChallengeDetailBudget
	if status == StatusAwaitingPlanApproval {
		pv, err := loadPlanView(ctx, tx, cid, agent)
		if err != nil {
			return out, err
		}
		if pv != nil && pv.BudgetUSD != nil {
			est, lim, reason := planBudgetLimit(pv)
			out.EstimateUSD, out.LimitUSD, out.LimitReason = &est, &lim, reason
		}
		return out, nil
	}
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return out, err
	}
	est, known := sumTaskBudgets(bs)
	if !known {
		return out, nil
	}
	recorded, reason, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	lim, _ := effectiveBudgetLimit(est, known, recorded)
	e, l := microsToUSD(est.Impl+est.Review), microsToUSD(lim)
	out.EstimateUSD, out.LimitUSD = &e, &l
	if recorded != nil {
		out.LimitReason = reason
	}
	return out, nil
}

// planBudgetLimit は承認を待っている版の計画 pv（BudgetUSD が非 nil）の見込み・上限・上限の理由を返す。上限は、計画が書かなければ見込みと同じ額。
// 理由は、上限が見込みより大きいときだけ返す（同じ額なら nil）。計画の承認の要約と課題の詳細が同じ規則を使うための 1 か所。
func planBudgetLimit(pv *PlanView) (est, lim float64, reason *string) {
	est = *pv.BudgetUSD
	lim = est
	if pv.BudgetLimitUSD != nil {
		lim = *pv.BudgetLimitUSD
	}
	if lim > est {
		reason = pv.BudgetLimitReason
	}
	return est, lim, reason
}

// ActivityActionBudgetLimit は作業ログ（entity=challenge）の action。課題の上限を書いたときに残す（前の額・後の額）。
const ActivityActionBudgetLimit = "budget_limit"

// setChallengeBudgetLimitChecked は課題 id の上限を limitUSD にし、理由を reason（空は記録しない）にして、作業ログに残す。
// 上限は見込み以上でなければならない（見込みより小さい額・見込みが不明の課題は ErrValidation）。課題が無ければ ErrNotFound。
// agent は枠の記録が空の過去のタスクの値を引くための宣言（nil 可）。課題の版・状態は変えない。
// 本人確認も承認の記録も持たないので公開しない（`--limit` などの承認つきの口が、承認の記録と同じ書き込みで使う）。
func (s *Store) setChallengeBudgetLimitChecked(ctx context.Context, ch Channel, id string, limitUSD float64, reason string, agent *AgentDeclaration) error {
	cid, ok := parseChallengeID(id)
	if !ok {
		return ErrNotFound
	}
	if !(limitUSD > 0) {
		return fmt.Errorf("%w: budget limit must be greater than 0", ErrValidation)
	}
	return s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		bs, err := loadTaskBudgets(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		est, known := sumTaskBudgets(bs)
		if !known {
			return fmt.Errorf("%w: the estimate of %s is unknown; cannot set its budget limit", ErrValidation, id)
		}
		limit := usdToMicros(limitUSD)
		if limit < est.Impl+est.Review {
			return fmt.Errorf("%w: budget limit %v is smaller than the estimate %v", ErrValidation, limitUSD, microsToUSD(est.Impl+est.Review))
		}
		recorded, _, err := challengeBudgetLimit(ctx, tx, cid)
		if err != nil {
			return err
		}
		before, _ := effectiveBudgetLimit(est, known, recorded)
		var reasonPtr *string
		if strings.TrimSpace(reason) != "" {
			reasonPtr = &reason
		}
		if err := setChallengeBudgetLimit(ctx, tx, cid, &limit, reasonPtr); err != nil {
			return err
		}
		after := map[string]any{"limit_usd": limitUSD}
		if reasonPtr != nil {
			after["reason"] = reason
		}
		return rec.record("challenge", cid, ActivityActionBudgetLimit, map[string]any{"limit_usd": microsToUSD(before)}, after)
	})
}

// resolveBudgetTask は `flywheel budget` が枠を改めるタスクを決める。taskID が空でないときは、その課題の未完のタスクで
// なければ（ほかの課題のタスク・存在しない・済み・取りやめ）失敗する。空のときは、未完のタスクがちょうど 1 個ならそのタスク、
// 2 個以上なら ErrBudgetTaskRequired、タスクを持たない過去の課題は nil（課題の計画の版の枠を置き換える見直し前の形）。
func resolveBudgetTask(ctx context.Context, tx *sql.Tx, cid int64, taskID string) (*taskBudget, error) {
	bs, err := loadTaskBudgets(ctx, tx, cid, nil)
	if err != nil {
		return nil, err
	}
	var open []taskBudget
	for _, b := range bs {
		switch b.Status {
		case TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying:
			open = append(open, b)
		}
	}
	if taskID != "" {
		id, ok := parseTaskID(taskID)
		if !ok {
			return nil, fmt.Errorf("%w: malformed task id %q", ErrValidation, taskID)
		}
		for _, b := range bs {
			if b.TaskID == id {
				for _, o := range open {
					if o.TaskID == id {
						return &o, nil
					}
				}
				return nil, fmt.Errorf("%w: task %s is already finished or cancelled", ErrValidation, taskID)
			}
		}
		return nil, fmt.Errorf("%w: task %s is not a task of this challenge", ErrNotFound, taskID)
	}
	switch len(open) {
	case 0:
		if len(bs) > 0 {
			return nil, fmt.Errorf("%w: the challenge has no unfinished task", ErrValidation)
		}
		return nil, nil
	case 1:
		return &open[0], nil
	}
	return nil, ErrBudgetTaskRequired
}

// fillTaskBudgets は課題の詳細のために、タスクの枠（増額を含む実効値）と、そのタスクの実行の費用の合計を tasks へ入れる。
// agent は枠の記録が空の過去のタスクの、見直し前の決め方の値を引くための宣言（nil 可。引けなければ 0）。
func fillTaskBudgets(ctx context.Context, tx *sql.Tx, cid int64, tasks []Task, agent *AgentDeclaration) error {
	if len(tasks) == 0 {
		return nil
	}
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return err
	}
	byID := make(map[string]taskBudget, len(bs))
	for _, b := range bs {
		byID[formatTaskID(b.TaskID)] = b
	}
	for i := range tasks {
		b, ok := byID[tasks[i].ID]
		if !ok {
			continue
		}
		sp, err := loadTaskBucketSpend(ctx, tx, b.TaskID, b.PlanVersion)
		if err != nil {
			return err
		}
		tasks[i].SpentUSD = microsToUSD(sp.Impl + sp.Review)
		// 決められない枠は 0 のまま（記録した片方の枠は、もう片方が決められなくても出す）。
		tasks[i].BudgetImplUSD, tasks[i].BudgetReviewUSD = microsToUSD(b.Impl), microsToUSD(b.Review)
	}
	return nil
}
