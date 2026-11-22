package core

// このファイルは予算の 2 枠（実装枠・レビュー対応枠）の残りの計算、`flywheel budget` による
// 枠の置き換え、`status` の needs_human.budget_exhausted を持つ（親要件チケット #98
// §予算ガード・§クリティカル設計決定 4。決定 M3H3・M3P44）。
//
// 残りは計画の版ごと・枠ごとに数える。実装枠の残り ＝ 承認済みの計画の実装枠の額 −
// その版の実装枠の run の費用の合計（レビュー対応枠も同じ）。一方の残りを他方へ融通しない。
// 委譲の評価額 ＝ 実装枠の残り ＋ レビュー対応枠の残り、`--max-budget-usd`・予約額 ＝
// 実装枠の残りだけ。周の上限の評価式そのもの（evalCycleBudgetTx）は変えない。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// NotStartedRunBudget は、実装枠の残りが起動の最小額（1 USD）に満たないため委譲を起動しなかった
// ことを表す。`cycle` の not_started の閉集合（NotStartedReasonValues）に含まれる。
const NotStartedRunBudget NotStartedReason = "run_budget"

// minImplLaunchMicros は委譲を起動できる実装枠の残りの下限（1 USD。USD の 100 万分の 1 の単位）。
// 0.99 USD は止まり、1.00 USD は起動する。
const minImplLaunchMicros int64 = 1_000_000

// planBudgets は計画の版の枠の額（USD の 100 万分の 1 の単位）。
type planBudgets struct {
	Impl   int64
	Review int64
}

// bucketSpend は計画の版の枠ごとの消費の事実。
type bucketSpend struct {
	Impl   int64
	Review int64
	// ImplStopped は、その版の実装枠の最後の終了した run が budget_exhausted で、その後に
	// `flywheel budget` で枠が置き換えられていないこと（実装枠の残りを 0 以下として扱う）。
	ImplStopped bool
}

// bucketRemaining は枠ごとの残り（0 未満は 0 に丸める。一方の超過を他方の余りで埋めない）。
type bucketRemaining struct {
	Impl   int64
	Review int64
}

func remainingOf(amount, spent int64) int64 {
	if r := amount - spent; r > 0 {
		return r
	}
	return 0
}

// computeBucketRemaining は枠の額と消費から枠ごとの残りを返す。
func computeBucketRemaining(b planBudgets, sp bucketSpend) bucketRemaining {
	r := bucketRemaining{Impl: remainingOf(b.Impl, sp.Impl), Review: remainingOf(b.Review, sp.Review)}
	if sp.ImplStopped {
		r.Impl = 0
	}
	return r
}

// applyBudgetOverride は計画の額（USD）に flywheel budget の上書きを適用した枠の額を返す。
func applyBudgetOverride(implUSD, reviewUSD float64, o *planBudgetOverride) planBudgets {
	b := planBudgets{Impl: usdToMicros(implUSD), Review: usdToMicros(reviewUSD)}
	if o != nil {
		if o.ImplBudgetUSD != nil {
			b.Impl = *o.ImplBudgetUSD
		}
		if o.ReviewBudgetUSD != nil {
			b.Review = *o.ReviewBudgetUSD
		}
	}
	return b
}

// loadBucketSpend は challengeID の計画の版 planVersion の枠ごとの費用の合計と、実装枠の
// 打ち切りの有無を読む。範囲が変わる回答（hold.replan）で計画を作り直した課題だけは、前の計画の版の run の
// 費用も合計に積み上げる（使った費用を新しい計画の予算から引く。M1 §範囲が変わる回答）。終了していない run の費用（NULL）は 0 として数える（その上限額は
// 周の予約額が持つ）。実装枠の打ち切りは、最後に終わった委譲の段の実行で決める（反映の操作のために呼んだ実行〔operation_id〕は読まない）。
func loadBucketSpend(ctx context.Context, tx *sql.Tx, challengeID, planVersion int64) (bucketSpend, error) {
	var sp bucketSpend
	rows, err := tx.QueryContext(ctx,
		`SELECT budget_bucket, COALESCE(SUM(cost_usd), 0) FROM run
		 WHERE challenge_id = ? AND budget_bucket IN (?, ?) AND reflect = 0
		   AND (plan_version = ? OR (plan_version < ? AND EXISTS (SELECT 1 FROM hold h WHERE h.challenge_id = run.challenge_id AND h.replan = 1 AND h.answer IS NOT NULL)))
		 GROUP BY budget_bucket`,
		challengeID, string(budgetBucketImpl), string(budgetBucketReview), planVersion, planVersion)
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
	err = tx.QueryRowContext(ctx,
		`SELECT result, ended_at FROM run
		 WHERE challenge_id = ? AND plan_version = ? AND budget_bucket = ? AND ended_at IS NOT NULL AND reflect = 0
		 ORDER BY id DESC LIMIT 1`,
		challengeID, planVersion, string(budgetBucketImpl)).Scan(&result, &endedAtText)
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
		`SELECT decided_at FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ? AND target_version = ?
		 ORDER BY id DESC LIMIT 1`,
		challengeID, string(ApprovalKindBudget), string(ApprovalDecisionApproved), planVersion).Scan(&decidedAtText)
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

// specBudgetFields は承認済みの計画の構造化した出力から、枠の額の解決に要る項目だけを読む。
type specBudgetFields struct {
	Size            *string  `json:"size"`
	BudgetImplUSD   *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD *float64 `json:"budget_review_usd"`
}

// resolveSpecBudgets は承認済みの計画の構造化した出力と宣言（サイズの既定。nil 可）から計画の
// 枠の額を解決する（J2 の出力の検査と同じ規則: 出力の額、無ければサイズの既定）。上書きは
// 適用しない。サイズの既定が要るのに agent が nil・不正なときは ok=false。
func resolveSpecBudgets(spec string, agent *AgentDeclaration) (implUSD, reviewUSD float64, ok bool) {
	var f specBudgetFields
	if err := json.Unmarshal([]byte(spec), &f); err != nil {
		return 0, 0, false
	}
	if f.BudgetImplUSD != nil && f.BudgetReviewUSD != nil {
		return *f.BudgetImplUSD, *f.BudgetReviewUSD, true
	}
	if agent == nil || f.Size == nil {
		return 0, 0, false
	}
	pair, found := agent.SizeBudgetFor(JudgmentSize(*f.Size))
	if !found {
		return 0, 0, false
	}
	implUSD, reviewUSD = pair.Impl, pair.Review
	if f.BudgetImplUSD != nil {
		implUSD = *f.BudgetImplUSD
	}
	if f.BudgetReviewUSD != nil {
		reviewUSD = *f.BudgetReviewUSD
	}
	return implUSD, reviewUSD, true
}

// BudgetExhausted は status.needs_human.budget_exhausted の 1 件（実装枠の残りが 1 USD 未満の
// 着手中の課題）。
type BudgetExhausted struct {
	ChallengeID string
	// TaskID は実装の枠が尽きたタスク（タスクを持たない過去の課題は空）。
	TaskID             string
	PlanVersion        int
	ImplRemainingUSD   float64
	ReviewRemainingUSD float64
}

// listBudgetExhausted は着手中で承認済みの計画を持ち、実装枠の残りが 1 USD 未満の課題を
// ID 昇順で返す。agent が nil のときは、計画の出力が両方の額を明記する課題だけを判定できる。
func listBudgetExhausted(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) ([]BudgetExhausted, error) {
	challenges, err := listChallengesInStatuses(ctx, tx, []Status{StatusInProgress})
	if err != nil {
		return nil, err
	}
	out := make([]BudgetExhausted, 0)
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		plan, ok, err := loadApprovedPlan(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		bs, err := loadTaskBudgets(ctx, tx, cid, agent)
		if err != nil {
			return nil, err
		}
		var history *launchHistory
		if agent != nil {
			if history, err = loadLaunchHistory(ctx, tx, cid); err != nil {
				return nil, err
			}
		}
		found := false
		for _, tb := range bs {
			if int(tb.PlanVersion) == plan.Version {
				found = true // タスクの行を持つ計画の版は、未完のタスクが無くても課題の単位の判定へ落とさない
			}
			// 承認済みの計画の版の、未完のタスクだけを見る（取りやめ・済みのタスクの枠は、起動に関わらない）。
			if int(tb.PlanVersion) != plan.Version || !tb.Known {
				continue
			}
			switch tb.Status {
			case TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying:
			default:
				continue
			}
			rem, err := taskRemaining(ctx, tx, cid, tb, bs)
			if err != nil {
				return nil, err
			}
			implRem := rem.Impl
			// 費用が取れない失敗の後の再開が許される間は、人間の増額を待たない（履歴はこのタスクの単位に絞る）。
			if history != nil {
				th := history.forTask(formatTaskID(tb.TaskID))
				implRem = th.effectiveImplBudget(plan.Version, th.decideLaunchFull(plan.Version, agent), implRem)
			}
			if implRem >= minImplLaunchMicros {
				continue
			}
			out = append(out, BudgetExhausted{
				ChallengeID: ch.ID, TaskID: formatTaskID(tb.TaskID), PlanVersion: plan.Version,
				ImplRemainingUSD: microsToUSD(rem.Impl), ReviewRemainingUSD: microsToUSD(rem.Review),
			})
		}
		if found {
			continue
		}
		// タスクの行を持たない過去の課題は、見直し前の決め方で課題の単位に判定する。
		implUSD, reviewUSD, ok := resolveSpecBudgets(plan.Spec, agent)
		if !ok {
			continue
		}
		override, err := loadPlanBudgetOverride(ctx, tx, cid, int64(plan.Version))
		if err != nil {
			return nil, err
		}
		sp, err := loadBucketSpend(ctx, tx, cid, int64(plan.Version))
		if err != nil {
			return nil, err
		}
		rem := computeBucketRemaining(applyBudgetOverride(implUSD, reviewUSD, override), sp)
		implRem := rem.Impl
		// 費用が取れない失敗の後の再開が許される間は、人間の増額を待たない。
		if agent != nil {
			history, err := loadLaunchHistory(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			implRem = history.effectiveImplBudget(plan.Version, history.decideLaunchFull(plan.Version, agent), implRem)
		}
		if implRem >= minImplLaunchMicros {
			continue
		}
		out = append(out, BudgetExhausted{
			ChallengeID: ch.ID, PlanVersion: plan.Version,
			ImplRemainingUSD: microsToUSD(rem.Impl), ReviewRemainingUSD: microsToUSD(rem.Review),
		})
	}
	return out, nil
}

// --- flywheel budget ---

// BudgetPreview は PrepareBudget が返す、flywheel budget の①（読み取り）の結果。
type BudgetPreview struct {
	ChallengeID string
	Title       string
	// Version は表示時点の課題の版（BudgetRequest.ExpectedVersion にそのまま渡す）。
	Version int
	// PlanVersion は枠を置き換える承認済みの計画の版（BudgetRequest.PlanVersion に渡す）。
	PlanVersion int
	// TaskID は枠を改めるタスク（BudgetRequest.TaskID に渡す。タスクを持たない過去の課題は空）。
	TaskID string
	// CurrentKnown が true のとき、現在の枠の額と残り（USD）が有効（サイズの既定を引く宣言が
	// 無い・不正なときは false）。
	CurrentKnown       bool
	CurrentImplUSD     float64
	CurrentReviewUSD   float64
	ImplRemainingUSD   float64
	ReviewRemainingUSD float64
	// NewImplUSD は置き換え後の実装枠、NewReviewUSD は置き換え後のレビュー対応枠
	// （ReviewUnchanged が true ならレビュー対応枠は変えない）。
	NewImplUSD      float64
	NewReviewUSD    float64
	ReviewUnchanged bool
	// LimitOnly が true のとき、上限だけを変える確認（PrepareBudgetLimit）。EstimateUSD は課題の見込み、
	// CurrentLimitUSD は変える前の上限、NewLimitUSD は変えた後の上限。枠の項目は使わない。
	LimitOnly       bool
	EstimateUSD     float64
	CurrentLimitUSD float64
	NewLimitUSD     float64
}

// maxBudgetAmountUSD は flywheel budget が受け付ける額の上限（USD の 100 万分の 1 の整数への
// 変換と、評価額の足し算が桁あふれしない範囲）。
const maxBudgetAmountUSD = 1e9

func validBudgetAmount(v float64) bool {
	return v > 0 && v <= maxBudgetAmountUSD && !math.IsNaN(v)
}

// loadBudgetTarget は課題の承認済みの計画の版を返す。課題が無ければ ErrNotFound、完了なら
// ErrTerminalState、承認済みの計画（構造化した出力つき）が無ければ ErrInvalidTransition。
func loadBudgetTarget(ctx context.Context, tx *sql.Tx, cid int64) (*Challenge, approvedPlan, error) {
	current, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, approvedPlan{}, err
	}
	if IsTerminal(Table, StatusVocabulary, current.Status) {
		return nil, approvedPlan{}, ErrTerminalState
	}
	plan, ok, err := loadApprovedPlan(ctx, tx, cid)
	if err != nil {
		return nil, approvedPlan{}, err
	}
	if !ok {
		return nil, approvedPlan{}, ErrInvalidTransition
	}
	return current, plan, nil
}

// PrepareBudget は flywheel budget の①（読み取り）。額が正の有限値でなければ、端末を開く前に
// ErrValidation で拒否する。agent は現在の額の表示にだけ使う（nil 可）。
func (s *Store) PrepareBudget(ctx context.Context, id string, agent *AgentDeclaration, implUSD float64, reviewUSD *float64) (*BudgetPreview, error) {
	return s.PrepareBudgetForTask(ctx, id, "", agent, implUSD, reviewUSD)
}

// PrepareBudgetForTask は PrepareBudget の、枠を改めるタスク taskID を指定する形。taskID が空のとき、未完のタスクが
// ちょうど 1 個ならそのタスク、2 個以上なら ErrBudgetTaskRequired。その課題のタスクでない ID は失敗する（ストアは変えない）。
func (s *Store) PrepareBudgetForTask(ctx context.Context, id, taskID string, agent *AgentDeclaration, implUSD float64, reviewUSD *float64) (*BudgetPreview, error) {
	if !validBudgetAmount(implUSD) || (reviewUSD != nil && !validBudgetAmount(*reviewUSD)) {
		return nil, ErrValidation
	}
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var p BudgetPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, plan, err := loadBudgetTarget(ctx, tx, cid)
		if err != nil {
			return err
		}
		task, err := resolveBudgetTask(ctx, tx, cid, taskID)
		if err != nil {
			return err
		}
		p = BudgetPreview{ChallengeID: current.ID, Title: current.Title, Version: current.Version,
			PlanVersion: plan.Version, NewImplUSD: implUSD, ReviewUnchanged: reviewUSD == nil}
		if task != nil {
			p.TaskID = formatTaskID(task.TaskID)
			p.PlanVersion = int(task.PlanVersion)
		}
		override, err := loadPlanBudgetOverride(ctx, tx, cid, int64(plan.Version))
		if err != nil {
			return err
		}
		iu, ru, known := resolveSpecBudgets(plan.Spec, agent)
		var b planBudgets
		var rem bucketRemaining
		if task != nil {
			bs, err := loadTaskBudgets(ctx, tx, cid, agent)
			if err != nil {
				return err
			}
			known = false
			for _, tb := range bs {
				if tb.TaskID == task.TaskID && tb.Known {
					known, b = true, planBudgets{Impl: tb.Impl, Review: tb.Review}
					if rem, err = taskRemaining(ctx, tx, cid, tb, bs); err != nil {
						return err
					}
				}
			}
		} else if known {
			b = applyBudgetOverride(iu, ru, override)
			sp, err := loadBucketSpend(ctx, tx, cid, int64(plan.Version))
			if err != nil {
				return err
			}
			rem = computeBucketRemaining(b, sp)
		}
		if known {
			p.CurrentKnown = true
			p.CurrentImplUSD, p.CurrentReviewUSD = microsToUSD(b.Impl), microsToUSD(b.Review)
			p.ImplRemainingUSD, p.ReviewRemainingUSD = microsToUSD(rem.Impl), microsToUSD(rem.Review)
		}
		switch {
		case reviewUSD != nil:
			p.NewReviewUSD = *reviewUSD
		case known:
			p.NewReviewUSD = p.CurrentReviewUSD
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &p, nil
}

// BudgetRequest は ExecuteBudget の入力（②＝書き込み）。
type BudgetRequest struct {
	ChallengeID     string
	ExpectedVersion int
	// PlanVersion は①が見せた承認済みの計画の版（その版の枠を置き換える）。
	PlanVersion int
	// TaskID は枠を改めるタスク（①が見せたもの。空なら①と同じ規則で決める）。
	TaskID  string
	ImplUSD float64
	// ReviewUSD が nil ならレビュー対応枠は変えない。
	ReviewUSD *float64
	// LimitUSD が非 nil のとき、課題の上限だけをその額にする（枠の額・タスクの指定とは同時に指定できず、ErrValidation）。
	// 見込みより小さい額は ErrValidation。課題の状態と版は変えない。
	LimitUSD *float64
	// Agent は枠の記録が空の過去のタスクの値を引くための宣言（nil 可）。見込みの計算に使う。
	Agent *AgentDeclaration
}

// budgetAgent は見込みの計算に使う宣言を返す。req.Agent が無ければ core が宣言を自分で読む（呼び出し側の都合で、上限の判定が変わらないように）。
func (s *Store) budgetAgent(req BudgetRequest) *AgentDeclaration {
	if req.Agent != nil {
		return req.Agent
	}
	agent, _ := LoadAgentDeclaration(s.Workspace())
	return agent
}

// ExecuteBudget は flywheel budget の②（書き込み）。確認が成立し、課題の版と承認済みの計画の版が
// ①の時点から変わっていないことを条件に、その計画の版の実装枠（と、指定されれば
// レビュー対応枠）を指定した額へ置き換え、承認の種類 budget の承認の記録と作業ログ
// （entity=challenge・action=approve・approval_kind=budget）を 1 つのトランザクションで書く。
// 課題の状態と版は変えない。
func (s *Store) ExecuteBudget(ctx context.Context, req BudgetRequest, att Attestation) (*Approval, error) {
	if req.LimitUSD != nil {
		return s.executeBudgetLimit(ctx, req, att)
	}
	if !validBudgetAmount(req.ImplUSD) || (req.ReviewUSD != nil && !validBudgetAmount(*req.ReviewUSD)) {
		return nil, ErrValidation
	}
	if !attestationPermits(att, req.ChallengeID, actionBudget) {
		return nil, ErrVerificationRejected
	}
	cid, ok := parseChallengeID(req.ChallengeID)
	if !ok {
		return nil, ErrNotFound
	}
	agent := s.budgetAgent(req)
	var out Approval
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionBudget, ""); err != nil {
			return err
		}
		current, plan, err := loadBudgetTarget(ctx, tx, cid)
		if err != nil {
			return err
		}
		task, err := resolveBudgetTask(ctx, tx, cid, req.TaskID)
		if err != nil {
			return err
		}
		approvedVersion := plan.Version
		if task != nil {
			approvedVersion = int(task.PlanVersion)
		}
		if current.Version != req.ExpectedVersion || approvedVersion != req.PlanVersion {
			return ErrConflict
		}
		prev, err := loadPlanBudgetOverride(ctx, tx, cid, int64(plan.Version))
		if err != nil {
			return err
		}
		if prev == nil {
			prev = &planBudgetOverride{}
		}
		impl := usdToMicros(req.ImplUSD)
		next := planBudgetOverride{ImplBudgetUSD: &impl, ReviewBudgetUSD: prev.ReviewBudgetUSD}
		if req.ReviewUSD != nil {
			review := usdToMicros(*req.ReviewUSD)
			next.ReviewBudgetUSD = &review
		}
		if impl <= 0 || (next.ReviewBudgetUSD != nil && *next.ReviewBudgetUSD <= 0) {
			return ErrValidation
		}
		// タスクの枠は、タスクの記録の値を指定した額で置き換える（増額は上乗せではなく置き換え。ほかのタスクの枠は変えない）。
		// 同じ計画の版にタスクが 1 個だけの課題は、見直し前と同じく計画の版の上書きも書く（計画の表示が読む）。
		var taskBefore map[string]any
		writePlanOverride := true
		limitBefore, limitBeforeKnown, err := currentBudgetLimit(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		if task != nil {
			bs, err := loadTaskBudgets(ctx, tx, cid, nil)
			if err != nil {
				return err
			}
			writePlanOverride = planVersionTaskCount(bs, task.PlanVersion) == 1
			var beforeImpl, beforeReview sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT budget_impl_micros, budget_review_micros FROM task WHERE id = ?`, task.TaskID).Scan(&beforeImpl, &beforeReview); err != nil {
				return err
			}
			taskBefore = map[string]any{"task_id": formatTaskID(task.TaskID), "budget_impl_usd": nullMicrosUSD(beforeImpl), "budget_review_usd": nullMicrosUSD(beforeReview)}
			if _, err := tx.ExecContext(ctx, `UPDATE task SET budget_impl_micros = ?, updated_at = ? WHERE id = ?`, impl, formatTimestamp(rec.at), task.TaskID); err != nil {
				return err
			}
			if req.ReviewUSD != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE task SET budget_review_micros = ? WHERE id = ?`, *next.ReviewBudgetUSD, task.TaskID); err != nil {
					return err
				}
			}
		}
		if writePlanOverride {
			updated, err := setPlanBudgetOverride(ctx, tx, cid, int64(plan.Version), next)
			if err != nil {
				return err
			}
			if !updated {
				return fmt.Errorf("core: budget: plan %d of %s not found", plan.Version, current.ID)
			}
		}
		now, err := insertBudgetApproval(ctx, tx, rec, att, cid, approvedVersion, taskIDColumn(task))
		if err != nil {
			return err
		}
		before := map[string]any{"impl_budget_usd": overrideUSD(prev.ImplBudgetUSD), "review_budget_usd": overrideUSD(prev.ReviewBudgetUSD)}
		after := map[string]any{
			"approval_kind": string(ApprovalKindBudget), "decision": string(ApprovalDecisionApproved),
			"target_version":    approvedVersion,
			"impl_budget_usd":   overrideUSD(next.ImplBudgetUSD),
			"review_budget_usd": overrideUSD(next.ReviewBudgetUSD),
		}
		if taskBefore != nil {
			before["task"] = taskBefore
			after["task_id"] = formatTaskID(task.TaskID)
		}
		// 枠の増額で見込みが上限を超えたときは、同じ書き込みで上限を、増額の後の見込みと同じ額まで上げる
		// （枠の増額の承認が、上限の引き上げの承認を兼ねる）。見込みが上限以下のままなら上限は変えない。
		if limitBeforeKnown {
			if err := raiseLimitToEstimate(ctx, tx, rec, cid, limitBefore, agent); err != nil {
				return err
			}
		}
		att.addRelayTo(after)
		if err := rec.record("challenge", cid, string(OpApprove), before, after); err != nil {
			return err
		}
		out = approvalOf(now, approvedVersion, att)
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// approvalOf は予算の承認の記録の戻り値を作る。
func approvalOf(now time.Time, version int, att Attestation) Approval {
	out := Approval{Kind: ApprovalKindBudget, Decision: ApprovalDecisionApproved, TargetVersion: version,
		Actor: att.Actor(), Channel: string(att.Channel()), Verification: string(att.Verification()), DecidedAt: now}
	out.RelaySession, out.RelayQuote = att.relayPtrs()
	return out
}

// insertBudgetApproval は承認の種類 budget の承認の記録を書き、その時刻を返す。予算の承認は target_version に
// 計画の版を持ち、approval.plan_version は NULL のまま（計画の承認は target_version に課題の版を持ち、plan_version に
// 計画の版を持つ）。taskCol はタスクの指定（タスクを指さない承認は nil）。
func insertBudgetApproval(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, cid int64, version int, taskCol any) (time.Time, error) {
	nowStr := formatTimestamp(rec.at)
	now, err := parseTimestamp(nowStr)
	if err != nil {
		return time.Time{}, err
	}
	relaySession, relayQuote := att.relayColumns()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at, relay_session, relay_quote, task_id)
		 VALUES (?, NULL, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
		cid, string(ApprovalKindBudget), string(ApprovalDecisionApproved), version,
		att.Actor(), string(att.Channel()), string(att.Verification()), nowStr, relaySession, relayQuote, taskCol); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// currentBudgetLimit は課題の上限の額（記録が無ければ見込みと同じ額）を返す。見込みが不明なら ok=false。
func currentBudgetLimit(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (int64, bool, error) {
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return 0, false, err
	}
	est, known := sumTaskBudgets(bs)
	recorded, _, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return 0, false, err
	}
	lim, ok := effectiveBudgetLimit(est, known, recorded)
	return lim, ok, nil
}

// raiseLimitToEstimate は、いまの見込みが上限 before を超えるとき、上限を見込みと同じ額まで上げて作業ログに残す。
// 理由は変えない。超えなければ何もしない。
func raiseLimitToEstimate(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, before int64, agent *AgentDeclaration) error {
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return err
	}
	est, known := sumTaskBudgets(bs)
	if !known || est.Impl+est.Review <= before {
		return nil
	}
	after := est.Impl + est.Review
	_, reason, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return err
	}
	if err := setChallengeBudgetLimit(ctx, tx, cid, &after, reason); err != nil {
		return err
	}
	return rec.record("challenge", cid, ActivityActionBudgetLimit, map[string]any{"limit_usd": microsToUSD(before)}, map[string]any{"limit_usd": microsToUSD(after), "verification": string(rec.verification)})
}

// PrepareBudgetLimit は flywheel budget --limit の①（読み取り）。上限は見込み以上でなければならない
// （見込みより小さい額・見込みが不明の課題は ErrValidation。端末を開く前に断る）。ストアは変えない。
func (s *Store) PrepareBudgetLimit(ctx context.Context, id string, agent *AgentDeclaration, limitUSD float64) (*BudgetPreview, error) {
	if !validBudgetAmount(limitUSD) {
		return nil, ErrValidation
	}
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var p BudgetPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, plan, err := loadBudgetTarget(ctx, tx, cid)
		if err != nil {
			return err
		}
		est, cur, err := budgetLimitBounds(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		if usdToMicros(limitUSD) < est {
			return fmt.Errorf("%w: budget limit %v is smaller than the estimate %v", ErrValidation, limitUSD, microsToUSD(est))
		}
		p = BudgetPreview{ChallengeID: current.ID, Title: current.Title, Version: current.Version, PlanVersion: plan.Version,
			LimitOnly: true, EstimateUSD: microsToUSD(est), CurrentLimitUSD: microsToUSD(cur), NewLimitUSD: limitUSD}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &p, nil
}

// budgetLimitBounds は課題の見込みと、いまの上限（USD の 100 万分の 1）を返す。見込みが不明なら ErrValidation。
func budgetLimitBounds(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (estimate, limit int64, err error) {
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return 0, 0, err
	}
	est, known := sumTaskBudgets(bs)
	if !known {
		return 0, 0, fmt.Errorf("%w: the estimate is unknown; cannot set the budget limit", ErrValidation)
	}
	recorded, _, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return 0, 0, err
	}
	lim, _ := effectiveBudgetLimit(est, known, recorded)
	return est.Impl + est.Review, lim, nil
}

// raiseBudgetLimit は課題 cid の上限を before から after にし（理由は変えない）、承認の記録（種類 budget・タスクの指定なし。
// 対象の版は承認済みの計画の版 planVersion）と作業ログ（承認・上限の前後の額）を、呼び出し元のトランザクションの中で書き、
// 承認の時刻を返す。`--limit`（executeBudgetLimit）と、足す依頼の保留への選択肢 add の回答（applyTaskAdditionAdd）が使う。
func raiseBudgetLimit(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, cid int64, planVersion int, before, after int64) (time.Time, error) {
	_, reason, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return time.Time{}, err
	}
	if err := setChallengeBudgetLimit(ctx, tx, cid, &after, reason); err != nil {
		return time.Time{}, err
	}
	now, err := insertBudgetApproval(ctx, tx, rec, att, cid, planVersion, nil)
	if err != nil {
		return time.Time{}, err
	}
	beforeLimit := map[string]any{"limit_usd": microsToUSD(before)}
	approveAfter := map[string]any{
		"approval_kind": string(ApprovalKindBudget), "decision": string(ApprovalDecisionApproved),
		"target_version": planVersion, "limit_usd": microsToUSD(after),
	}
	att.addRelayTo(approveAfter)
	if err := rec.record("challenge", cid, string(OpApprove), beforeLimit, approveAfter); err != nil {
		return time.Time{}, err
	}
	limitAfter := map[string]any{"limit_usd": microsToUSD(after), "verification": string(att.Verification())}
	if err := rec.record("challenge", cid, ActivityActionBudgetLimit, map[string]any{"limit_usd": microsToUSD(before)}, limitAfter); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// executeBudgetLimit は ExecuteBudget の、上限だけを変える形（--limit）。確認が成立し、課題の版と承認済みの計画の版が
// ①の時点から変わっていないことを条件に、上限を指定した額にし、承認の記録（種類 budget・タスクの指定なし）と作業ログ
// （承認・上限の前後の額）を 1 つのトランザクションで書く。理由は変えない。課題の状態と版は変えない。
func (s *Store) executeBudgetLimit(ctx context.Context, req BudgetRequest, att Attestation) (*Approval, error) {
	if !validBudgetAmount(*req.LimitUSD) {
		return nil, ErrValidation
	}
	if req.ImplUSD != 0 || req.ReviewUSD != nil || req.TaskID != "" {
		return nil, fmt.Errorf("%w: --limit cannot be combined with a task frame", ErrValidation)
	}
	if !attestationPermits(att, req.ChallengeID, actionBudget) {
		return nil, ErrVerificationRejected
	}
	cid, ok := parseChallengeID(req.ChallengeID)
	if !ok {
		return nil, ErrNotFound
	}
	agent := s.budgetAgent(req)
	var out Approval
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionBudget, ""); err != nil {
			return err
		}
		current, plan, err := loadBudgetTarget(ctx, tx, cid)
		if err != nil {
			return err
		}
		if current.Version != req.ExpectedVersion || plan.Version != req.PlanVersion {
			return ErrConflict
		}
		est, before, err := budgetLimitBounds(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		limit := usdToMicros(*req.LimitUSD)
		if limit < est {
			return fmt.Errorf("%w: budget limit %v is smaller than the estimate %v", ErrValidation, *req.LimitUSD, microsToUSD(est))
		}
		now, err := raiseBudgetLimit(ctx, tx, rec, att, cid, plan.Version, before, limit)
		if err != nil {
			return err
		}
		out = approvalOf(now, plan.Version, att)
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// taskIDColumn は承認の記録の task_id（タスクを指さない承認は NULL）。
func taskIDColumn(t *taskBudget) any {
	if t == nil {
		return nil
	}
	return t.TaskID
}

func nullMicrosUSD(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return microsToUSD(v.Int64)
}

func overrideUSD(p *int64) any {
	if p == nil {
		return nil
	}
	return microsToUSD(*p)
}

// budgetShortfallDetail は NotStarted の Detail に使う、枠の残りの説明。
func budgetShortfallDetail(rem bucketRemaining) string {
	return fmt.Sprintf("implementation budget remaining %s USD is below the launch minimum of %s USD",
		strings.TrimSpace(formatUSDAmount(microsToUSD(rem.Impl))), strings.TrimSpace(formatUSDAmount(microsToUSD(minImplLaunchMicros))))
}
