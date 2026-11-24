package core

// このファイルは、計画を作る判断点（J2）の「足す」の形の出力の検査と、足す処理（区切り S4c-1。docs/features/core-redesign-s4c.md）を持つ。
//
//   - 検査（validateJ2ExpandOutput）: 判断値（add・none・ask）・足すタスクの形・承認済みの版のタスクとの突き合わせ。
//     計画の形の validateJ2Output とは別に持つ（計画の形の「分類済みの課題だけ」の検査は当てない）。
//   - 足す処理（finalizeJ2ExpandOutput）: none は足す依頼を「足さない」で閉じる。add で、足した後の見込みが上限以下なら、
//     タスクの行を作って「足した」で閉じる（オーナーの承認は求めず、課題は保留にしない）。課題の状態は変えない。
//   - 検査に通らない出力は invalid_output にする（足す依頼は仕様待ちのまま、タスクの行は増えない）。
//
// 上限を超える add は、足す依頼を「オーナー待ち」にして課題を保留にする（理由 task_add_over_limit。選択肢 add・skip・replan。タスクの行は作らない）。
// ask は課題を保留にする（理由 task_add_question。足す依頼は仕様待ちのまま。判断点が選択肢を付けていれば選択肢つき）。
// どちらも、足す依頼の hold_id にその保留を結び、検査・足す依頼の書き込み・保留の遷移（T11）を 1 つの書き込みで行う。
// 保留への回答の処理は、この処理の範囲外。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// j2ExpandValidated は検査済みの「足す」の形の出力。
type j2ExpandValidated struct {
	Verdict  j2ExpandVerdict
	Tasks    []j2Task
	Question string
	// Options は ask の問いの選択肢（無ければ空）。
	Options []string
	// Spec は判断点が返したタスクの一覧（JSON。add のときだけ）。
	Spec string
}

// j2ExpandValidationContext は「足す」の形の出力の検査が突き合わせる、宣言と課題の事実。
type j2ExpandValidationContext struct {
	Agent     *AgentDeclaration
	Conn      *ConnectorsDeclaration
	HasSource bool
	// TakenKeys は承認済みの版のタスク（未着手・取りやめ・済みを含む）の鍵。足すタスクの鍵は、これと重ならない。
	TakenKeys map[string]bool
	// DepKeys は足すタスクの前提に指せる、承認済みの版の、取りやめでないタスクの鍵。
	DepKeys map[string]bool
	// Repos は、いまあるタスクのリポジトリ（取りやめでないタスク。重複なし）。足すタスクのリポジトリは、この 1 つと同じでなければならない。
	Repos []string
	// Origin は足す依頼の出どころ（空は expand と同じ）。followup のときだけ、判断値 none とタスクが 1 個でない出力を不正にし、
	// 足すタスクの枠を、足す依頼の額（FrameImpl・FrameReview。USD の 100 万分の 1。空はその枠を足す依頼が持たない）で置き換える。
	Origin      TaskAdditionOrigin
	FrameImpl   *int64
	FrameReview *int64
	// SkipFlowReason は保存済みの足す依頼の spec を回答の段で検査し直すときに真（flow_reason の検査をしない。j2ValidationContext と同じ）。
	SkipFlowReason bool
}

// validateJ2ExpandOutput は「足す」の形の出力を検査する。次のいずれかで ok=false:
//   - JSON として解釈できない・判断値が add|none|ask の外・ask で問いが空
//   - 出どころが followup のとき: 判断値 none・タスクが 1 個でない add も不正。タスクの枠は足す依頼の額に置き換える（枠の検査はかけない。
//     足す依頼に額が無い枠だけ、出力の値を使い検査にかける）
//   - add で: タスクが 1〜12 件でない・鍵／題名／本文／完了条件が空・鍵が重なる・鍵がいまあるタスク（取りやめ・済みを含む）の鍵と重なる・
//     前提が自分自身・足すタスクの鍵でもいまある（取りやめでない）タスクの鍵でもない・足すタスクどうしの前提に輪がある・
//     リポジトリがいまあるタスクと同じ 1 つでない・宣言に無い／起動できない操作・枠（実装・レビュー対応）が無い・不正
func validateJ2ExpandOutput(data []byte, vc j2ExpandValidationContext) (*j2ExpandValidated, bool) {
	v, reason := validateJ2ExpandOutputReason(data, vc)
	return v, reason == ""
}

// validateJ2ExpandOutputReason は validateJ2ExpandOutput の本体。検査を通れば理由は空、落ちれば、どの検査で落ちたかの短い文を返す
// （実行の記録の誤りの欄に残す。判断点の出力の中身は写さない）。
func validateJ2ExpandOutputReason(data []byte, vc j2ExpandValidationContext) (*j2ExpandValidated, string) {
	raw, ok := parseJ2ExpandOutput(data)
	if !ok {
		return nil, j2ReasonBadShape
	}
	v := &j2ExpandValidated{Verdict: j2ExpandVerdict(raw.Verdict)}
	switch v.Verdict {
	case j2ExpandNone:
		if vc.Origin == TaskAdditionOriginFollowup {
			return nil, "a followup addition cannot choose none" // フォローアップを足す依頼は、足さないを選べない（足すか問うか）
		}
		return v, ""
	case j2ExpandAsk:
		if raw.Question == nil {
			return nil, "ask requires a question"
		}
		if err := checkText("question", *raw.Question, MaxStageRunes); err != nil {
			return nil, validationReason(err)
		}
		v.Question = *raw.Question
		v.Options = raw.Options
		return v, ""
	}
	// add
	in := append([]j2RawTask(nil), raw.Tasks...)
	if len(in) < MinPlanTasks || len(in) > MaxPlanTasks {
		return nil, fmt.Sprintf("tasks must have %d-%d items", MinPlanTasks, MaxPlanTasks)
	}
	if len(vc.Repos) != 1 {
		return nil, "the existing tasks do not target exactly one repo"
	}
	// 出どころが followup のときは、枠を足す依頼の額で置き換える（出力の枠は読まない）。足す依頼の額は、タスクの枠の検査
	// （0 超・実装の枠が 1 回の上限以下）にかけない。検査は代わりの値で通し、通した後に足す依頼の額を入れる。
	followup := vc.Origin == TaskAdditionOriginFollowup
	if followup {
		if len(in) != 1 {
			return nil, "a followup addition must add exactly one task"
		}
		placeholderImpl, placeholderReview := followupPlaceholderBudgetUSD, followupPlaceholderBudgetUSD
		if vc.FrameImpl != nil {
			in[0].BudgetImplUSD = &placeholderImpl
		}
		if vc.FrameReview != nil {
			in[0].BudgetReviewUSD = &placeholderReview
		}
	}
	byKey := make(map[string]int, len(in))
	inputs := make([]TaskInput, len(in))
	for i, t := range in {
		if err := checkJ2TaskTexts(i, t); err != nil {
			return nil, validationReason(err)
		}
		if _, dup := byKey[t.Key]; dup || vc.TakenKeys[t.Key] {
			return nil, fmt.Sprintf("tasks[%d].key is already used", i)
		}
		byKey[t.Key] = i
		inputs[i] = TaskInput{Key: t.Key, DependsOn: t.DependsOn}
	}
	for i, t := range in {
		for _, k := range t.DependsOn {
			if _, own := byKey[k]; (own && k != t.Key) || (!own && vc.DepKeys[k]) {
				continue
			}
			return nil, fmt.Sprintf("tasks[%d].depends_on names itself or a key that cannot be depended on", i)
		}
	}
	if hasDependencyCycle(inputs, byKey) {
		return nil, "tasks.depends_on has a cycle"
	}
	jc := j2ValidationContext{Agent: vc.Agent, Conn: vc.Conn, HasSource: vc.HasSource, SkipFlowReason: vc.SkipFlowReason}
	for i, t := range in {
		if t.Repo != vc.Repos[0] {
			return nil, fmt.Sprintf("tasks[%d].repo differs from the repo of the existing tasks", i)
		}
		jt, reason := validateJ2TaskFields(t, jc, true)
		if reason != "" {
			return nil, fmt.Sprintf("tasks[%d]: %s", i, reason)
		}
		if followup {
			applyFollowupFrames(&jt, vc)
			in[0].BudgetImplUSD, in[0].BudgetReviewUSD = jt.BudgetImplUSD, jt.BudgetReviewUSD
		}
		v.Tasks = append(v.Tasks, jt)
	}
	spec, err := json.Marshal(in)
	if err != nil {
		return nil, j2ReasonBadShape
	}
	v.Spec = string(spec)
	return v, ""
}

// followupPlaceholderBudgetUSD は、出どころが followup のタスクの枠の検査を通すための代わりの値（検査の後に足す依頼の額へ置き換える）。
const followupPlaceholderBudgetUSD = 0.000001

// applyFollowupFrames は検査済みのタスク jt の枠を、足す依頼の額で置き換える（足す依頼に額が無い枠は、出力の値のまま）。
func applyFollowupFrames(jt *j2Task, vc j2ExpandValidationContext) {
	if vc.FrameImpl != nil {
		u := microsToUSD(*vc.FrameImpl)
		jt.BudgetImplUSD = &u
	}
	if vc.FrameReview != nil {
		u := microsToUSD(*vc.FrameReview)
		jt.BudgetReviewUSD = &u
	}
}

// j2ExpandFinalized は finalizeJ2ExpandOutput の結果。
type j2ExpandFinalized struct {
	// Outcome は判断値（検査に通らなければ空）。
	Outcome string
	Result  RunResult
	// Note は足さずに待たせたときの理由（無ければ空）。
	Note string
}

// finalizeJ2ExpandOutput は run（runIDDisplay）の「足す」の形の出力を検査し、足す依頼 additionID に写す。検査・読み・書きは 1 つの
// 書き込みトランザクションで行う。検査に通らなければ run を invalid_output にし、足す依頼もタスクも変えない。
// 足す依頼が仕様待ちでない・課題が着手中でないときは、何も書かない（出力は受けた扱い）。
func (s *Store) finalizeJ2ExpandOutput(ctx context.Context, in J2AutoInput, runIDDisplay string, additionID int64, structuredOutput []byte) (j2ExpandFinalized, error) {
	runInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return j2ExpandFinalized{}, ErrValidation
	}
	actor, err := s.resolveActor()
	if err != nil {
		return j2ExpandFinalized{}, err
	}
	res := j2ExpandFinalized{Result: RunResultSucceeded}
	invalidReason := ""
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		raw, ok := parseJ2ExpandOutput(structuredOutput)
		if !ok {
			invalidReason = j2ReasonBadShape
			return nil
		}
		res.Outcome = raw.Verdict
		add, err := loadTaskAddition(ctx, tx, additionID)
		if err != nil {
			return err
		}
		cid, _ := parseChallengeID(add.ChallengeID)
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if add.State != TaskAdditionPending || ch.Status != StatusInProgress {
			return nil
		}
		if add.FollowupID != nil { // 捨てた・済みにしたフォローアップは足さない
			fn, _ := parseFollowupID(*add.FollowupID)
			if fu, err := loadFollowup(ctx, tx, fn); err != nil {
				return err
			} else if fu.Status != FollowupStatusOpen {
				return nil
			}
		}
		vc, ready, err := loadJ2ExpandValidationContext(ctx, tx, cid, in)
		if err != nil {
			return err
		}
		if !ready {
			return nil // 承認済みの計画が無い（足せない）
		}
		if err := vc.useAdditionOrigin(ctx, tx, additionID); err != nil {
			return err
		}
		v, reason := validateJ2ExpandOutputReason(structuredOutput, vc.j2ExpandValidationContext)
		if reason != "" {
			invalidReason = reason
			return nil
		}
		hold := func(question string, reason holdReason, opts HoldOptionsInput) (int64, error) {
			var holdID int64
			_, err := s.transitionInTx(ctx, tx, rec, runTransitionParams{
				actor: actor, channel: ChannelInvoker, verification: VerificationNone,
				id: ch.ID, op: OpHold, preceding: NoStatus, runID: &runInt,
			}, cid, func(ctx context.Context, tc *transitionCtx) error {
				var err error
				holdID, err = s.insertHoldWithOccurrenceOptions(ctx, tc, question, reason, "", opts)
				return err
			})
			return holdID, err
		}
		switch v.Verdict {
		case j2ExpandNone:
			_, err := setTaskAdditionStateTx(ctx, tx, rec, additionID, TaskAdditionNone, TaskAdditionUpdate{})
			return err
		case j2ExpandAdd:
			note, err := applyJ2ExpandAdd(ctx, tx, rec, cid, additionID, vc, v, hold)
			res.Note = note
			return err
		case j2ExpandAsk:
			holdID, err := hold(v.Question, holdReasonTaskAddQuestion, holdOptionsFromLabels(v.Options))
			if err != nil {
				return err
			}
			// 足す依頼は仕様待ちのまま、保留だけを結ぶ。
			if _, err := setTaskAdditionStateTx(ctx, tx, rec, additionID, TaskAdditionPending, TaskAdditionUpdate{HoldID: &holdID}); err != nil {
				return err
			}
			res.Note = fmt.Sprintf("判断点の問いで保留にした: H-%d", holdID)
		}
		return nil
	})
	if err != nil {
		return j2ExpandFinalized{}, err
	}
	if invalidReason != "" {
		if err := s.invalidateRunOutputWithReason(ctx, runIDDisplay, "invalid J2 expand output: "+invalidReason); err != nil {
			return j2ExpandFinalized{}, err
		}
		return j2ExpandFinalized{Result: RunResultInvalidOutput}, nil
	}
	return res, nil
}

// j2ExpandState は足す処理が読む、課題の承認済みの版のタスクの状態。
type j2ExpandState struct {
	j2ExpandValidationContext
	Version  int64
	DepIDs   map[string]int64
	Budgets  []taskBudget
	LimitRec *int64
}

// useAdditionOrigin は検査の事実に、足す依頼 additionID の出どころと、出どころが followup のときの枠を足す。
func (st *j2ExpandState) useAdditionOrigin(ctx context.Context, tx *sql.Tx, additionID int64) error {
	var origin string
	var impl, review sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT origin, budget_impl_micros, budget_review_micros FROM task_addition WHERE id = ?`, additionID).Scan(&origin, &impl, &review); err != nil {
		return err
	}
	st.Origin = TaskAdditionOrigin(origin)
	if st.Origin != TaskAdditionOriginFollowup {
		return nil
	}
	if impl.Valid {
		v := impl.Int64
		st.FrameImpl = &v
	}
	if review.Valid {
		v := review.Int64
		st.FrameReview = &v
	}
	return nil
}

// loadJ2ExpandValidationContext は課題 cid の、承認済みの版のタスクから、検査の事実を作る。承認済みの版が無ければ ready=false。
func loadJ2ExpandValidationContext(ctx context.Context, tx *sql.Tx, cid int64, in J2AutoInput) (j2ExpandState, bool, error) {
	version, ok, err := approvedPlanVersionOf(ctx, tx, cid)
	if err != nil || !ok {
		return j2ExpandState{}, false, err
	}
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return j2ExpandState{}, false, err
	}
	st := j2ExpandState{Version: version, DepIDs: map[string]int64{}}
	st.Agent, st.Conn = in.AgentDecl, in.ConnDecl
	st.TakenKeys, st.DepKeys = map[string]bool{}, map[string]bool{}
	repos := map[string]bool{}
	for _, t := range all {
		if t.PlanVersion != version {
			continue
		}
		if t.Key != "" {
			st.TakenKeys[t.Key] = true
		}
		if t.Status == TaskStatusCancelled {
			continue
		}
		repos[t.Repo] = true
		if t.Key != "" {
			st.DepKeys[t.Key] = true
			id, _ := parseTaskID(t.ID)
			st.DepIDs[t.Key] = id
		}
	}
	for r := range repos {
		st.Repos = append(st.Repos, r)
	}
	sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
	if err != nil {
		return j2ExpandState{}, false, err
	}
	st.HasSource = sb != nil
	if st.Budgets, err = loadTaskBudgets(ctx, tx, cid, in.AgentDecl); err != nil {
		return j2ExpandState{}, false, err
	}
	if st.LimitRec, _, err = challengeBudgetLimit(ctx, tx, cid); err != nil {
		return j2ExpandState{}, false, err
	}
	return st, true, nil
}

// applyJ2ExpandAdd は検査済みの add を、足した後の見込みと上限で判定して書く。上限以下ならタスクの行を作り、足す依頼を「足した」で閉じ、
// 作業ログに足す依頼・足したタスク・足した後の見込み・上限を残す。上限を超えるときは、タスクの行を作らず保留を立てる（hold）。
// 見込みが不明のときは何も書かず、理由を返す。見込みは、課題の見込みと同じ関数（sumTaskBudgets）で数え、上限は課題に記録した 1 つの額で比べる。
func applyJ2ExpandAdd(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, additionID int64, st j2ExpandState, v *j2ExpandValidated, hold j2ExpandHolder) (string, error) {
	before, known := sumTaskBudgets(st.Budgets)
	limit, limitOK := effectiveBudgetLimit(before, known, st.LimitRec)
	if !known || !limitOK {
		return "見込みが不明のため足さない", nil
	}
	bs := append([]taskBudget(nil), st.Budgets...)
	inputs := j2ExpandTaskInputs(v.Tasks)
	for _, in := range inputs {
		bs = append(bs, taskBudget{PlanVersion: st.Version, Status: TaskStatusPending, Impl: *in.BudgetImplMicros, Review: *in.BudgetReviewMicros, Known: true})
	}
	after, _ := sumTaskBudgets(bs)
	total := after.Impl + after.Review
	if total > limit {
		totalUSD, limitUSD := microsToUSD(total), microsToUSD(limit)
		holdID, err := hold(overLimitQuestion(v.Tasks, totalUSD, limitUSD, microsToUSD(total-limit)), holdReasonTaskAddOverLimit, overLimitHoldOptions(totalUSD))
		if err != nil {
			return "", err
		}
		if _, err := setTaskAdditionStateTx(ctx, tx, rec, additionID, TaskAdditionAwaitingOwner,
			TaskAdditionUpdate{Spec: &v.Spec, TotalAfterUSD: &totalUSD, LimitUSD: &limitUSD, HoldID: &holdID}); err != nil {
			return "", err
		}
		return fmt.Sprintf("足した後の見込みが上限を超えるため保留にした: H-%d", holdID), nil
	}
	totalUSD, limitUSD := microsToUSD(total), microsToUSD(limit)
	return createExpandedTasks(ctx, tx, rec, cid, additionID, st, v, inputs, totalUSD, limitUSD)
}

// j2ExpandTaskInputs は検査済みの足すタスクを、タスクの行を作る入力にする（枠は検査済みで、必ずある）。
func j2ExpandTaskInputs(tasks []j2Task) []TaskInput {
	inputs := make([]TaskInput, 0, len(tasks))
	for _, t := range tasks {
		inputs = append(inputs, TaskInput{
			Key: t.Key, Title: t.Title, Body: t.Body, DoneCriteria: t.DoneCriteria, NonGoals: t.NonGoals,
			Repo: t.Repo, Operation: t.Operation, DependsOn: t.DependsOn,
			BudgetImplMicros: taskBudgetMicros(t.BudgetImplUSD), BudgetReviewMicros: taskBudgetMicros(t.BudgetReviewUSD), ExpandAfter: t.ExpandAfter,
			PlannedFlow: t.Flow, FlowReason: t.FlowReason,
		})
	}
	return inputs
}

// createExpandedTasks は検査済みの足すタスクの行を作り（承認済みの版・未着手・足す依頼を指す）、足す依頼を「足した」で閉じ、作業ログへ残す。
// 上限以下で足す処理と、保留への回答（足す）が共有する。totalUSD・limitUSD は足す依頼に記録する足した後の見込みと上限。足したタスクの ID の文を返す。
func createExpandedTasks(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, additionID int64, st j2ExpandState, v *j2ExpandValidated, inputs []TaskInput, totalUSD, limitUSD float64) (string, error) {
	created, err := createTasksWith(ctx, tx, rec, cid, st.Version, inputs, createTasksOptions{Existing: st.DepIDs, AdditionID: &additionID})
	if err != nil {
		return "", err
	}
	if _, err := setTaskAdditionStateTx(ctx, tx, rec, additionID, TaskAdditionApplied, TaskAdditionUpdate{Spec: &v.Spec, TotalAfterUSD: &totalUSD, LimitUSD: &limitUSD}); err != nil {
		return "", err
	}
	ids := make([]string, 0, len(created))
	for _, t := range created {
		ids = append(ids, t.ID)
	}
	err = rec.record("task_addition", additionID, "add_tasks", nil, map[string]any{
		"task_addition_id": formatTaskAdditionID(additionID),
		"task_ids":         ids,
		"total_after_usd":  totalUSD,
		"limit_usd":        limitUSD,
	})
	if err != nil {
		return "", err
	}
	if err := recordFollowupPromotedTask(ctx, tx, rec, additionID, created); err != nil {
		return "", err
	}
	return fmt.Sprintf("足したタスク: %s", strings.Join(ids, ", ")), nil
}

// recordFollowupPromotedTask は、出どころが followup の足す依頼 additionID で足したタスク（created の先頭）を、フォローアップの
// 上げた先のタスク（promoted_task_id）に記録する。上げた先の課題（promoted_challenge_id）は変えない。出どころが followup でなければ何もしない。
func recordFollowupPromotedTask(ctx context.Context, tx *sql.Tx, rec *activityRecorder, additionID int64, created []Task) error {
	var fid sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT followup_id FROM task_addition WHERE id = ?`, additionID).Scan(&fid); err != nil {
		return err
	}
	if !fid.Valid || len(created) == 0 {
		return nil
	}
	tid, _ := parseTaskID(created[0].ID)
	f, err := loadFollowup(ctx, tx, fid.Int64)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE followup SET promoted_task_id = ?, updated_at = ? WHERE id = ?`, tid, formatTimestamp(rec.at), fid.Int64); err != nil {
		return err
	}
	before := map[string]any{"promoted_task_id": nil}
	if f.PromotedTaskID != nil {
		before["promoted_task_id"] = *f.PromotedTaskID
	}
	return rec.record("followup", fid.Int64, ActivityActionFollowupPromote, before, map[string]any{
		"promoted_task_id": formatTaskID(tid), "task_addition_id": formatTaskAdditionID(additionID),
	})
}

// j2ExpandHolder は、同じ書き込みの中で課題を保留にし（T11）、保留の内部整数 ID を返す口。
type j2ExpandHolder func(question string, reason holdReason, opts HoldOptionsInput) (int64, error)

// 上限を超える足す依頼の保留の、選択肢の識別。回答の処理（後続）が、この識別で読む。
const (
	taskAddOptionAdd    = "add"
	taskAddOptionSkip   = "skip"
	taskAddOptionReplan = "replan"
)

// overLimitQuestion は task_add_over_limit の保留の問い。足すタスクの題名と枠・足した後の見込み・上限・超える額・計画の作り直しの制約を書く。
// 超える額 overUSD は、呼び出し側が整数（USD の 100 万分の 1）の引き算から作って渡す（浮動小数の引き算の誤差を文に出さない）。
func overLimitQuestion(tasks []j2Task, totalUSD, limitUSD, overUSD float64) string {
	var b strings.Builder
	b.WriteString("足すと上限を超えるので、足してよいかを決めてください。\n足すタスク:\n")
	for _, t := range tasks {
		impl, review := 0.0, 0.0
		if t.BudgetImplUSD != nil {
			impl = *t.BudgetImplUSD
		}
		if t.BudgetReviewUSD != nil {
			review = *t.BudgetReviewUSD
		}
		fmt.Fprintf(&b, "- %s（枠: 実装 $%s・レビュー対応 $%s）\n", t.Title, formatUSDAmount(impl), formatUSDAmount(review))
	}
	fmt.Fprintf(&b, "足した後の見込み: $%s\n上限: $%s\n超える額: $%s\n", formatUSDAmount(totalUSD), formatUSDAmount(limitUSD), formatUSDAmount(overUSD))
	b.WriteString("動いている作業が終わるまで、計画の作り直しは選べない。")
	return b.String()
}

// overLimitHoldOptions は task_add_over_limit の保留の選択肢（識別の無い回答を断る）。add の説明に、上げた後の上限（足した後の見込み）を書く。
func overLimitHoldOptions(totalUSD float64) HoldOptionsInput {
	return HoldOptionsInput{
		Required: true,
		Options: []HoldOption{
			{ID: taskAddOptionAdd, Label: "足す", Description: fmt.Sprintf("上限を $%s（足した後の見込み）に上げて、タスクを足す", formatUSDAmount(totalUSD))},
			{ID: taskAddOptionSkip, Label: "足さずに進める", Description: "タスクを足さず、いまの計画のまま進める"},
			{ID: taskAddOptionReplan, Label: "計画を作り直す", Description: "計画を作り直す（動いている作業が終わるまで選べない）"},
		},
	}
}
