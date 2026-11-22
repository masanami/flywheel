package core

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// このファイルはフォローアップを上げる先の決め方（新しい課題か、元の課題のタスクか）と、タスクとして上げる書き込みを持つ。
// 上げる先は次の順で決める（core が決める。CLI は決めない）。
//   - 新しい課題を指定した（NewChallenge）→ 新しい課題。
//   - フォローアップが課題に属し、その課題が足せる課題（expandAddableReason。着手中・着手中から置かれた保留中の、統合先を使う、見込みが分かる課題）→ その課題のタスク。
//   - それ以外はすべて新しい課題（理由を NewChallengeReason へ入れる）。

// FollowupPromoteDest は上げた先の種類。
type FollowupPromoteDest string

const (
	// FollowupPromoteToChallenge は新しい課題（未分類）。
	FollowupPromoteToChallenge FollowupPromoteDest = "challenge"
	// FollowupPromoteToTask は元の課題のタスク（足す依頼を作る）。
	FollowupPromoteToTask FollowupPromoteDest = "task"
)

// FollowupPromoteOptions は PrepareFollowupPromote の指定。
type FollowupPromoteOptions struct {
	// NewChallenge は上げる先を新しい課題に決める（足す依頼は作らない）。
	NewChallenge bool
	// BudgetImplUSD・BudgetReviewUSD は足すタスクの枠（nil は宣言のサイズ S の額）。タスクとして上げるときだけ使う。
	BudgetImplUSD, BudgetReviewUSD *float64
	// RaiseLimitUSD は --raise-limit の額（上げた後の上限）。足した後の見込みが上限を超えるときだけ意味を持ち、
	// 足した後の見込みと同じ額でなければ ErrValidation。対話で受けた記録（relayed）では、超えるときに必須。
	RaiseLimitUSD *float64
}

// FollowupPromotePlan は上げる前に見せる内容（PrepareFollowupPromote の戻り値）。PromoteFollowup は同じ内容かを書き込みのときに確かめる。
type FollowupPromotePlan struct {
	Followup *Followup
	Dest     FollowupPromoteDest
	// NewChallengeReason は新しい課題に上げる理由（利用者向けの 1 文）。タスクとして上げるときは空。
	NewChallengeReason string
	// TargetChallengeID・ChallengeVersion は、タスクとして上げるときの元の課題とその版。
	TargetChallengeID string
	// TargetChallengeTitle は元の課題の題名（要約と --preview が上げる先として見せる。照合には使わない）。
	TargetChallengeTitle string
	ChallengeVersion     int
	// 足すタスクの枠・足した後の見込み・上限（USD。タスクとして上げるときだけ）。
	BudgetImplUSD, BudgetReviewUSD float64
	EstimateAfterUSD, LimitUSD     float64

	opts                                  FollowupPromoteOptions
	implMicros, reviewMicros, afterMicros int64
	limitMicros                           int64
	originTaskID                          *int64
}

// OverLimit は足した後の見込みが上限を超えるか。
func (p *FollowupPromotePlan) OverLimit() bool {
	return p.Dest == FollowupPromoteToTask && p.afterMicros > p.limitMicros
}

// CheckRaiseLimit は --raise-limit の指定を検査する（読み取りだけ。端末を開く前に断るために、CLI からも呼ぶ）。
// 指定した額は、足した後の見込みが上限を超えるときだけ許し、足した後の見込みと同じ額でなければ ErrValidation。
// relayed（対話で受けた記録）で上限を超えるときは、額の指定が必須（core は、オーナーが額を見たかを確かめられない）。
func (p *FollowupPromotePlan) CheckRaiseLimit(relayed bool) error {
	raise := p.opts.RaiseLimitUSD
	if raise != nil {
		if math.IsNaN(*raise) || math.IsInf(*raise, 0) {
			return fmt.Errorf("%w: --raise-limit must be a number", ErrValidation)
		}
		if !p.OverLimit() {
			return fmt.Errorf("%w: --raise-limit is only meaningful when the estimate after adding exceeds the limit", ErrValidation)
		}
		if usdToMicros(*raise) != p.afterMicros {
			return fmt.Errorf("%w: --raise-limit %v USD differs from the estimate after adding (%v USD)", ErrValidation, *raise, p.EstimateAfterUSD)
		}
		return nil
	}
	if relayed && p.OverLimit() {
		return fmt.Errorf("%w: the estimate after adding (%v USD) exceeds the limit (%v USD) by %v USD; with --relayed, pass --raise-limit %v (check the amounts with --preview)",
			ErrValidation, p.EstimateAfterUSD, p.LimitUSD, p.ExceedsByUSD(), p.LimitAfterUSD())
	}
	return nil
}

// ExceedsByUSD は足した後の見込みが上限を超える額（超えなければ 0）。
func (p *FollowupPromotePlan) ExceedsByUSD() float64 {
	if !p.OverLimit() {
		return 0
	}
	return microsToUSD(p.afterMicros - p.limitMicros)
}

// LimitAfterUSD は上げた後の上限（超えるときは足した後の見込み、超えないときは上限のまま）。
func (p *FollowupPromotePlan) LimitAfterUSD() float64 {
	if p.OverLimit() {
		return p.EstimateAfterUSD
	}
	return p.LimitUSD
}

// FollowupPromoteResult は PromoteFollowup の戻り値。Dest が challenge なら Challenge、task なら Addition が入る。
type FollowupPromoteResult struct {
	Dest      FollowupPromoteDest
	Challenge *Challenge
	Addition  *TaskAddition
	Followup  *Followup
	// NewChallengeReason は新しい課題に上げたときの理由（書き込みのときに数え直した値。見せた後に状態が変わっていれば、見せた値と違いうる）。
	NewChallengeReason string
}

// computeFollowupPromotePlan は f の上げる先を決め、タスクなら枠・足した後の見込み・上限を数える（読み取りだけ）。
func computeFollowupPromotePlan(ctx context.Context, tx *sql.Tx, f *Followup, opts FollowupPromoteOptions, agent *AgentDeclaration) (*FollowupPromotePlan, error) {
	p := &FollowupPromotePlan{Followup: f, Dest: FollowupPromoteToChallenge, opts: opts}
	// 枠の指定は、上げる先に関わらず先に検査する（効かない指定を黙って受けない）。
	impl, review, err := followupTaskFrames(opts, agent)
	if err != nil {
		return nil, err
	}
	if opts.NewChallenge {
		p.NewChallengeReason = "--new-challenge の指定"
		return p, nil
	}
	if f.ChallengeID == nil {
		p.NewChallengeReason = "フォローアップが課題に属さない"
		return p, nil
	}
	cid, _ := parseChallengeID(*f.ChallengeID)
	ok, reason, err := expandAddableReason(ctx, tx, cid, agent)
	if err != nil {
		return nil, err
	}
	if !ok {
		p.NewChallengeReason = reason
		return p, nil
	}
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return nil, err
	}
	before, known := sumTaskBudgets(bs)
	rec, _, err := challengeBudgetLimit(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	limit, limitOK := effectiveBudgetLimit(before, known, rec)
	if !known || !limitOK {
		p.NewChallengeReason = "枠を決められないタスクがある（見込みが不明）ため、タスクを足せない"
		return p, nil
	}
	ch, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var origin sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM followup WHERE id = ?`, followupNum(f)).Scan(&origin); err != nil {
		return nil, err
	}
	if origin.Valid {
		v := origin.Int64
		p.originTaskID = &v
	}
	p.Dest = FollowupPromoteToTask
	p.TargetChallengeID, p.ChallengeVersion, p.TargetChallengeTitle = *f.ChallengeID, ch.Version, ch.Title
	p.implMicros, p.reviewMicros = impl, review
	p.afterMicros = before.Impl + before.Review + impl + review
	p.limitMicros = limit
	p.BudgetImplUSD, p.BudgetReviewUSD = microsToUSD(impl), microsToUSD(review)
	p.EstimateAfterUSD, p.LimitUSD = microsToUSD(p.afterMicros), microsToUSD(limit)
	return p, nil
}

func followupNum(f *Followup) int64 {
	n, _ := parseFollowupID(f.ID)
	return n
}

// followupTaskFrames は足すタスクの枠（USD の 100 万分の 1）を返す。指定が無い側は宣言のサイズ S の額。負の額は ErrValidation。
func followupTaskFrames(opts FollowupPromoteOptions, agent *AgentDeclaration) (impl, review int64, err error) {
	def := defaultSizeBudgetsUSD.S
	if agent != nil {
		def = agent.SizeBudgetsUSD.S
	}
	implUSD, reviewUSD := def.Impl, def.Review
	if opts.BudgetImplUSD != nil {
		implUSD = *opts.BudgetImplUSD
	}
	if opts.BudgetReviewUSD != nil {
		reviewUSD = *opts.BudgetReviewUSD
	}
	for _, v := range []float64{implUSD, reviewUSD} {
		if math.IsNaN(v) || v < 0 || v > maxBudgetAmountUSD {
			return 0, 0, fmt.Errorf("%w: a task budget must be between 0 and %v USD", ErrValidation, maxBudgetAmountUSD)
		}
	}
	return usdToMicros(implUSD), usdToMicros(reviewUSD), nil
}

// PrepareFollowupPromote は上げる前の本人確認の要約のために、id のフォローアップの上げる先と、タスクなら課題の版・枠・足した後の見込み・上限を返す
// （読み取りだけ）。存在しなければ ErrNotFound、上げてよくなければ ErrValidation（PromoteFollowup と同じ規則）。
func (s *Store) PrepareFollowupPromote(ctx context.Context, id string, opts FollowupPromoteOptions, agent *AgentDeclaration) (*FollowupPromotePlan, error) {
	n, ok := parseFollowupID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *FollowupPromotePlan
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		f, err := loadFollowup(ctx, tx, n)
		if err != nil {
			return err
		}
		if err := checkPromotable(ctx, tx, f); err != nil {
			return err
		}
		out, err = computeFollowupPromotePlan(ctx, tx, f, opts, agent)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	// 指定した --raise-limit は、要約を作る前（端末を開く前）に検査する。
	if out.opts.RaiseLimitUSD != nil {
		if err := out.CheckRaiseLimit(false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PromoteFollowup は PrepareFollowupPromote が返した plan のとおりに上げる（1 回に 1 件。本人確認の対象はフォローアップの ID）。
// 書き込みのトランザクションの中で上げる先を決め直し、plan と違えば（上げる先・課題の版・枠・足した後の見込み・上限）何も書かず ErrConflict。
// タスクとして上げるときは、足す依頼（出どころ followup・仕様待ち）を作る。足した後の見込みが上限を超えるときは、同じ書き込みで上限を足した後の見込みまで上げる（relayed では --raise-limit の額が要る。CheckRaiseLimit）。
func (s *Store) PromoteFollowup(ctx context.Context, plan *FollowupPromotePlan, att Attestation, agent *AgentDeclaration) (*FollowupPromoteResult, error) {
	if plan == nil || plan.Followup == nil {
		return nil, ErrValidation
	}
	id := plan.Followup.ID
	if !attestationPermits(att, id, actionFollowupPromote) {
		return nil, ErrVerificationRejected
	}
	var res *FollowupPromoteResult
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionFollowupPromote, ""); err != nil {
			return err
		}
		n, ok := parseFollowupID(id)
		if !ok {
			return ErrNotFound
		}
		f, err := loadFollowup(ctx, tx, n)
		if err != nil {
			return err
		}
		if err := checkPromotable(ctx, tx, f); err != nil {
			return err
		}
		cur, err := computeFollowupPromotePlan(ctx, tx, f, plan.opts, agent)
		if err != nil {
			return err
		}
		if !plan.sameAs(cur) {
			return ErrConflict
		}
		if err := cur.CheckRaiseLimit(att.verification == VerificationRelayed); err != nil {
			return err
		}
		if cur.Dest == FollowupPromoteToTask {
			res, err = promoteFollowupToTask(ctx, tx, rec, att, f, cur)
			return err
		}
		ch, fu, err := promoteFollowup(ctx, tx, rec, att, id)
		if err != nil {
			return err
		}
		res = &FollowupPromoteResult{Dest: FollowupPromoteToChallenge, Challenge: ch, Followup: fu, NewChallengeReason: cur.NewChallengeReason}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// sameAs は plan が見せた内容と、書き込みのときに数え直した cur が同じかを返す。
func (p *FollowupPromotePlan) sameAs(cur *FollowupPromotePlan) bool {
	if p.Dest != cur.Dest || p.TargetChallengeID != cur.TargetChallengeID || p.ChallengeVersion != cur.ChallengeVersion {
		return false
	}
	return p.implMicros == cur.implMicros && p.reviewMicros == cur.reviewMicros && p.afterMicros == cur.afterMicros && p.limitMicros == cur.limitMicros
}

// promoteFollowupToTask は元の課題へ、出どころ followup の足す依頼（仕様待ち）を作る。フォローアップの状態・上げた先の列は変えない
// （上げた先のタスクの記録は、足したときに行う）。
func promoteFollowupToTask(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, f *Followup, p *FollowupPromotePlan) (*FollowupPromoteResult, error) {
	cid, _ := parseChallengeID(p.TargetChallengeID)
	if p.OverLimit() {
		// 上げる指示が、上限の引き上げの承認を兼ねる。上限を足した後の見込みまで上げ、前の額・後の額・本人確認の方式を残す。
		after := p.afterMicros
		_, reason, err := challengeBudgetLimit(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := setChallengeBudgetLimit(ctx, tx, cid, &after, reason); err != nil {
			return nil, err
		}
		if err := rec.record("challenge", cid, ActivityActionBudgetLimit, map[string]any{"limit_usd": microsToUSD(p.limitMicros)},
			map[string]any{"limit_usd": microsToUSD(after), "verification": string(att.Verification())}); err != nil {
			return nil, err
		}
	}
	fid := followupNum(f)
	var origin any
	if p.originTaskID != nil {
		origin = *p.originTaskID
	}
	r, err := tx.ExecContext(ctx,
		`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, created_at, followup_id, budget_impl_micros, budget_review_micros) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cid, string(TaskAdditionOriginFollowup), origin, string(TaskAdditionPending), formatTimestamp(rec.at), fid, p.implMicros, p.reviewMicros)
	if err != nil {
		return nil, err
	}
	aid, err := r.LastInsertId()
	if err != nil {
		return nil, err
	}
	after := map[string]any{
		"challenge_id": p.TargetChallengeID, "origin": string(TaskAdditionOriginFollowup), "state": string(TaskAdditionPending),
		"followup_id": f.ID, "budget_impl_usd": p.BudgetImplUSD, "budget_review_usd": p.BudgetReviewUSD,
	}
	if p.originTaskID != nil {
		after["origin_task_id"] = formatTaskID(*p.originTaskID)
	}
	if err := rec.record("task_addition", aid, "create", nil, after); err != nil {
		return nil, err
	}
	// その課題の上げる依頼が開いているときは、作ってあった反映の操作を、足す依頼を作る書き込みと同じ書き込みで閉じる
	// （開いている上げる依頼の実行は、次の回りの反映の段が、閉じた操作を見て取り消す）。
	if err := closeReflectOperationsForAddition(ctx, tx, rec, cid); err != nil {
		return nil, err
	}
	fafter := map[string]any{"task_addition_id": formatTaskAdditionID(aid), "challenge_id": p.TargetChallengeID}
	att.addRelayTo(fafter)
	if err := rec.record("followup", fid, ActivityActionFollowupPromote, nil, fafter); err != nil {
		return nil, err
	}
	ta, err := loadTaskAddition(ctx, tx, aid)
	if err != nil {
		return nil, err
	}
	fu, err := loadFollowup(ctx, tx, fid)
	if err != nil {
		return nil, err
	}
	return &FollowupPromoteResult{Dest: FollowupPromoteToTask, Addition: ta, Followup: fu}, nil
}

// closeReflectOperationsForAddition は、課題 cid の上げる依頼（契約の実行の種類 reflect で、終わっていないもの）が開いているとき、
// 待っている反映の操作（承認待ち・承認済み）を閉じる。上げる依頼が開いていなければ何もしない。
func closeReflectOperationsForAddition(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) error {
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND kind = ? AND ended_at IS NULL`,
		cid, contractRunKindReflect).Scan(&open); err != nil {
		return err
	}
	if open == 0 {
		return nil
	}
	tc := &transitionCtx{tx: tx, rec: rec, id: cid, nowStr: formatTimestamp(rec.at)}
	return closeReflectOperations(ctx, tc, cid, "", "フォローアップをタスクとして上げたため、反映の操作を閉じた")
}
