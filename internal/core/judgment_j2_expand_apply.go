package core

// このファイルは、計画を作る判断点（J2）の「足す」の形の出力の検査と、足す処理（区切り S4c-1。docs/features/core-redesign-s4c.md）を持つ。
//
//   - 検査（validateJ2ExpandOutput）: 判断値（add・none・ask）・足すタスクの形・承認済みの版のタスクとの突き合わせ。
//     計画の形の validateJ2Output とは別に持つ（計画の形の「分類済みの課題だけ」の検査は当てない）。
//   - 足す処理（finalizeJ2ExpandOutput）: none は足す依頼を「足さない」で閉じる。add で、足した後の見込みが上限以下なら、
//     タスクの行を作って「足した」で閉じる（オーナーの承認は求めず、課題は保留にしない）。課題の状態は変えない。
//   - 検査に通らない出力は invalid_output にする（足す依頼は仕様待ちのまま、タスクの行は増えない）。
//
// 上限を超える add の保留・ask の保留は、この処理の範囲外（足す依頼は仕様待ちのまま、何も書かない）。

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
}

// validateJ2ExpandOutput は「足す」の形の出力を検査する。次のいずれかで ok=false:
//   - JSON として解釈できない・判断値が add|none|ask の外・ask で問いが空
//   - add で: タスクが 1〜12 件でない・鍵／題名／本文／完了条件が空・鍵が重なる・鍵がいまあるタスク（取りやめ・済みを含む）の鍵と重なる・
//     前提が自分自身・足すタスクの鍵でもいまある（取りやめでない）タスクの鍵でもない・足すタスクどうしの前提に輪がある・
//     リポジトリがいまあるタスクと同じ 1 つでない・宣言に無い／起動できない操作・枠（実装・レビュー対応）が無い・不正
func validateJ2ExpandOutput(data []byte, vc j2ExpandValidationContext) (*j2ExpandValidated, bool) {
	raw, ok := parseJ2ExpandOutput(data)
	if !ok {
		return nil, false
	}
	v := &j2ExpandValidated{Verdict: j2ExpandVerdict(raw.Verdict)}
	switch v.Verdict {
	case j2ExpandNone:
		return v, true
	case j2ExpandAsk:
		if raw.Question == nil || checkText("question", *raw.Question, MaxStageRunes) != nil {
			return nil, false
		}
		v.Question = *raw.Question
		return v, true
	}
	// add
	in := raw.Tasks
	if len(in) < MinPlanTasks || len(in) > MaxPlanTasks || len(vc.Repos) != 1 {
		return nil, false
	}
	byKey := make(map[string]int, len(in))
	inputs := make([]TaskInput, len(in))
	for i, t := range in {
		for _, s := range []string{t.Key, t.Title, t.Body, t.DoneCriteria} {
			if checkText("tasks", s, MaxStageRunes) != nil {
				return nil, false
			}
		}
		if _, dup := byKey[t.Key]; dup || vc.TakenKeys[t.Key] {
			return nil, false
		}
		byKey[t.Key] = i
		inputs[i] = TaskInput{Key: t.Key, DependsOn: t.DependsOn}
	}
	for _, t := range in {
		for _, k := range t.DependsOn {
			if _, own := byKey[k]; (own && k != t.Key) || (!own && vc.DepKeys[k]) {
				continue
			}
			return nil, false
		}
	}
	if hasDependencyCycle(inputs, byKey) {
		return nil, false
	}
	jc := j2ValidationContext{Agent: vc.Agent, Conn: vc.Conn, HasSource: vc.HasSource}
	for _, t := range in {
		if t.Repo != vc.Repos[0] {
			return nil, false
		}
		jt, ok := validateJ2TaskFields(t, jc, true)
		if !ok {
			return nil, false
		}
		v.Tasks = append(v.Tasks, jt)
	}
	spec, err := json.Marshal(in)
	if err != nil {
		return nil, false
	}
	v.Spec = string(spec)
	return v, true
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
	invalid := false
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		raw, ok := parseJ2ExpandOutput(structuredOutput)
		if !ok {
			invalid = true
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
		vc, ready, err := loadJ2ExpandValidationContext(ctx, tx, cid, in)
		if err != nil {
			return err
		}
		if !ready {
			return nil // 承認済みの計画が無い（足せない）
		}
		v, ok := validateJ2ExpandOutput(structuredOutput, vc.j2ExpandValidationContext)
		if !ok {
			invalid = true
			return nil
		}
		switch v.Verdict {
		case j2ExpandNone:
			_, err := setTaskAdditionStateTx(ctx, tx, rec, additionID, TaskAdditionNone, TaskAdditionUpdate{})
			return err
		case j2ExpandAdd:
			note, err := applyJ2ExpandAdd(ctx, tx, rec, cid, additionID, vc, v)
			res.Note = note
			return err
		}
		return nil // ask: 保留は、この処理の範囲外
	})
	if err != nil {
		return j2ExpandFinalized{}, err
	}
	if invalid {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
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
// 作業ログに足す依頼・足したタスク・足した後の見込み・上限を残す。上限を超える・見込みが不明のときは何も書かず、理由を返す
// （上限を超える add の保留は、この処理の範囲外）。見込みは、課題の見込みと同じ関数（sumTaskBudgets）で数え、上限は課題に記録した 1 つの額で比べる。
func applyJ2ExpandAdd(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, additionID int64, st j2ExpandState, v *j2ExpandValidated) (string, error) {
	before, known := sumTaskBudgets(st.Budgets)
	limit, limitOK := effectiveBudgetLimit(before, known, st.LimitRec)
	if !known || !limitOK {
		return "見込みが不明のため足さない", nil
	}
	bs := append([]taskBudget(nil), st.Budgets...)
	inputs := make([]TaskInput, 0, len(v.Tasks))
	for _, t := range v.Tasks {
		impl, review := taskBudgetMicros(t.BudgetImplUSD), taskBudgetMicros(t.BudgetReviewUSD)
		bs = append(bs, taskBudget{PlanVersion: st.Version, Status: TaskStatusPending, Impl: *impl, Review: *review, Known: true})
		inputs = append(inputs, TaskInput{
			Key: t.Key, Title: t.Title, Body: t.Body, DoneCriteria: t.DoneCriteria, NonGoals: t.NonGoals,
			Repo: t.Repo, Operation: t.Operation, DependsOn: t.DependsOn,
			BudgetImplMicros: impl, BudgetReviewMicros: review, ExpandAfter: t.ExpandAfter,
		})
	}
	after, _ := sumTaskBudgets(bs)
	total := after.Impl + after.Review
	if total > limit {
		return "足した後の見込みが上限を超える", nil
	}
	created, err := createTasksWith(ctx, tx, rec, cid, st.Version, inputs, createTasksOptions{Existing: st.DepIDs, AdditionID: &additionID})
	if err != nil {
		return "", err
	}
	totalUSD, limitUSD := microsToUSD(total), microsToUSD(limit)
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
	return fmt.Sprintf("足したタスク: %s", strings.Join(ids, ", ")), err
}
