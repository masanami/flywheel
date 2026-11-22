package core

// このファイルは、計画の段が持つ「計画の後にタスクを足す」処理（区切り S4c-1。docs/features/core-redesign-s4c.md）の入力側を持つ。
//
//   - 足す依頼を作る: 足せる課題（統合先を使う課題）の、済みになった expand_after のタスクで、足す依頼がまだ無いものに、
//     出どころ expand・状態 pending の足す依頼を 1 件だけ作る（同じ出どころの expand の行は 1 つ）。
//   - 判断点（J2）を「足す」の形で呼ぶ: 着手中の課題ごとに、pending の足す依頼の古いほうの 1 件だけ。
//     計画の形（selectJ2AutoTargets・prepareJ2・j2OutputSchema・validateJ2Output）とは、対象の選び方・入力の区画・出力の形を別に持つ。
//   - 出力は受け口だけ（receiveJ2ExpandOutput）。出力の検査・足す処理（上限の判定・保留・選択肢での回答・タスクの行）は持たない。
//
// 「足す」の形の run は、タスクの指定を持たない課題単位の実行で、課題の状態を変えない。費用は judgment_budget_usd.J2 で数え、
// 課題の見込みに入れない。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 「足す」の形の入力の区画の名前。計画の形の入力には「呼び出しの形」の区画を置かない。
const (
	callShapeSectionLabel = "呼び出しの形"
	// callShapeExpand は区画「呼び出しの形」の値（判断点を「足す」の形で呼ぶ）。
	callShapeExpand = "足す"

	labelExpandApprovedPlan = "承認した計画"
	labelExpandTasks        = "いまのタスク"
	labelExpandDoneResults  = "済んだタスクの結果"
	labelExpandBudget       = "上限と残り"
	labelExpandOrigin       = "足す依頼の出どころ"
)

// j2ExpandVerdict は「足す」の形の判断値。
type j2ExpandVerdict string

const (
	j2ExpandNone j2ExpandVerdict = "none"
	j2ExpandAdd  j2ExpandVerdict = "add"
	j2ExpandAsk  j2ExpandVerdict = "ask"
)

// j2ExpandRawOutput は「足す」の形の構造化出力を受ける形（受け口。中身の検査は後続）。
// Tasks は判断値が add のときの足すタスクの一覧（形は計画のタスクと同じ）、Question は ask のときの問い。
type j2ExpandRawOutput struct {
	Verdict  string      `json:"verdict"`
	Tasks    []j2RawTask `json:"tasks"`
	Question *string     `json:"question"`
}

// j2ExpandOutputSchema は「足す」の形の出力スキーマ（j2OutputSchema とは別）。タスクの項目は計画のタスクと同じ
// （j2OutputSchema の tasks の items を使う）。件数は計画のタスクの上限まで。
func j2ExpandOutputSchema() []byte {
	var plan map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &plan); err != nil {
		panic(err) // 定数の組み立ての失敗（起きない）
	}
	tasks := plan["properties"].(map[string]any)["tasks"].(map[string]any)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":  map[string]any{"type": "string", "enum": []string{string(j2ExpandNone), string(j2ExpandAdd), string(j2ExpandAsk)}},
			"tasks":    map[string]any{"type": []string{"array", "null"}, "items": tasks["items"], "maxItems": MaxPlanTasks},
			"question": map[string]any{"type": []string{"string", "null"}},
		},
		"required":             []string{"verdict"},
		"additionalProperties": false,
	}
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return b
}

// parseJ2ExpandOutput は「足す」の形の出力を受け口の形へ読む。JSON でない・判断値が閉集合の外なら ok=false。
func parseJ2ExpandOutput(data []byte) (*j2ExpandRawOutput, bool) {
	var out j2ExpandRawOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	switch j2ExpandVerdict(out.Verdict) {
	case j2ExpandNone, j2ExpandAdd, j2ExpandAsk:
		return &out, true
	}
	return nil, false
}

// receiveJ2ExpandOutput は「足す」の形の出力の受け口。判断値を読むだけで、課題・足す依頼・タスクを変えない
// （出力の検査と足す処理は後続の区切りが持つ）。読めたときだけ判断値を返す。
func receiveJ2ExpandOutput(structuredOutput []byte) string {
	if out, ok := parseJ2ExpandOutput(structuredOutput); ok {
		return out.Verdict
	}
	return ""
}

// --- 足す依頼を作る ---

// expandAdditionCandidate は足す依頼をこれから作る、済みの expand_after のタスク。
type expandAdditionCandidate struct {
	ChallengeID string
	TaskID      string
}

// expandAddable は課題 cid が足せる状態か（統合先を使い、着手中か保留中で、見込みが分かる）を返す。
func expandAddable(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (bool, error) {
	ch, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return false, err
	}
	switch ch.Status {
	case StatusInProgress:
	case StatusAwaitingHuman:
		// 保留中は、着手中から置かれた保留のときだけ足せる（計画前・検証中から置かれた保留では足せない）。
		var from string
		err := tx.QueryRowContext(ctx,
			`SELECT from_status FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, cid).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if Status(from) != StatusInProgress {
			return false, nil
		}
	default:
		return false, nil
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return false, err
	}
	if integ == nil || !*integ {
		return false, nil
	}
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return false, err
	}
	_, known := sumTaskBudgets(bs)
	return known, nil
}

// selectExpandAdditionCandidates は足す依頼を作る対象（課題の ID の昇順・タスクの ID の昇順）を返す。
func selectExpandAdditionCandidates(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) ([]expandAdditionCandidate, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT t.challenge_id, t.id FROM task t JOIN challenge c ON c.id = t.challenge_id
		 WHERE t.expand_after = 1 AND t.status = ? AND c.status IN (?, ?) AND c.uses_integration = 1
		   AND t.plan_version = (SELECT plan_version FROM approval WHERE challenge_id = t.challenge_id AND kind = ? AND decision = ? ORDER BY id DESC LIMIT 1)
		   AND NOT EXISTS (SELECT 1 FROM task_addition a WHERE a.origin = ? AND a.origin_task_id = t.id)
		 ORDER BY t.challenge_id, t.id`,
		string(TaskStatusDone), string(StatusInProgress), string(StatusAwaitingHuman),
		string(ApprovalKindPlan), string(ApprovalDecisionApproved), string(TaskAdditionOriginExpand))
	if err != nil {
		return nil, err
	}
	type pair struct{ cid, tid int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.cid, &p.tid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	var out []expandAdditionCandidate
	addable := map[int64]bool{}
	checked := map[int64]bool{}
	for _, p := range pairs {
		if !checked[p.cid] {
			ok, err := expandAddable(ctx, tx, p.cid, agent)
			if err != nil {
				return nil, err
			}
			addable[p.cid], checked[p.cid] = ok, true
		}
		if addable[p.cid] {
			out = append(out, expandAdditionCandidate{ChallengeID: formatChallengeID(p.cid), TaskID: formatTaskID(p.tid)})
		}
	}
	return out, nil
}

// createExpandAdditions は、済みの expand_after のタスクについて、足す依頼（expand・pending）を、まだ無いものにだけ 1 件ずつ作る。
// 返り値は作った足す依頼の数。保留中の課題にも作る（判断点の呼び出しはしない）。
func (s *Store) createExpandAdditions(ctx context.Context, agent *AgentDeclaration) (int, error) {
	var cands []expandAdditionCandidate
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		cands, err = selectExpandAdditionCandidates(ctx, tx, agent)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cands {
		_, err := s.CreateTaskAddition(ctx, ChannelInvoker, c.ChallengeID, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: c.TaskID})
		if err != nil {
			if errors.Is(err, ErrValidation) {
				continue // 並行して作られた（同じ出どころの expand は 1 つ）
			}
			return n, err
		}
		n++
	}
	return n, nil
}

// --- 判断点を「足す」の形で呼ぶ ---

// expandTarget は「足す」の形で呼ぶ 1 件（課題と、その課題の古いほうの pending の足す依頼の出どころのタスク）。
type expandTarget struct {
	Challenge  int64
	Addition   int64
	OriginTask *int64
}

// selectJ2ExpandTargets は判断点を「足す」の形で呼ぶ対象を、課題の優先度→ID の順で返す。対象は、着手中で、統合先を使い、見込みが
// 分かり、タスクに結び付かない実行が無く、pending の足す依頼を持つ課題。課題ごとに古いほうの 1 件だけ。保留中・見込みが不明の課題は
// 含まない。別のタスクの実行が着手中でも対象になる。
func selectJ2ExpandTargets(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) (out []expandTarget, excluded []NotStarted, err error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusInProgress)
	if err != nil {
		return nil, nil, err
	}
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		var aid int64
		var origin sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT id, origin_task_id FROM task_addition WHERE challenge_id = ? AND origin = ? AND state = ? ORDER BY id LIMIT 1`,
			cid, string(TaskAdditionOriginExpand), string(TaskAdditionPending)).Scan(&aid, &origin)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if ok, err := expandAddable(ctx, tx, cid, agent); err != nil {
			return nil, nil, err
		} else if !ok {
			continue
		}
		busy, err := taskLessHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if busy {
			continue
		}
		tg := expandTarget{Challenge: cid, Addition: aid}
		if origin.Valid {
			v := origin.Int64
			tg.OriginTask = &v
		}
		// 周の中で自動で選ぶ対象の共通の規則（取り込み元の食い違いで外れる課題は、対象にせず理由を示す）。
		if ex, err := challengeAutoExclusion(ctx, tx, cid); err != nil {
			return nil, nil, err
		} else if ex != nil {
			excluded = append(excluded, *ex)
			continue
		}
		out = append(out, tg)
	}
	return out, excluded, nil
}

// prepareJ2Expand は「足す」の形の入力の区画を組み立てる（先頭は「呼び出しの形」。値は「足す」）。
// 見込みが不明になっていたとき（選んだ後に変わった）は、区画を返さず ok=false にする（呼ばない）。
func (s *Store) prepareJ2Expand(ctx context.Context, ch Challenge, in J2AutoInput, tg expandTarget) (_ []JudgmentDataSection, ok bool, _ error) {
	cid, parsed := parseChallengeID(ch.ID)
	if !parsed {
		return nil, false, ErrNotFound
	}
	yes := true
	var (
		pv       *PlanView
		tasks    []Task
		bs       []taskBudget
		limitR   *int64
		hasSrc   bool
		approved int64
	)
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if pv, err = loadPlanView(ctx, tx, cid, in.AgentDecl); err != nil {
			return err
		}
		var okV bool
		if approved, okV, err = approvedPlanVersionOf(ctx, tx, cid); err != nil {
			return err
		} else if !okV {
			return nil
		}
		all, err := listTasks(ctx, tx, cid)
		if err != nil {
			return err
		}
		// いまのタスクは、承認済みの版のタスクだけ（前の版のタスクの鍵は、足すタスクの前提に使えない）。
		for _, t := range all {
			if t.PlanVersion == approved {
				tasks = append(tasks, t)
			}
		}
		sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		hasSrc = sb != nil
		if err = fillTaskDetails(ctx, tx, cid, tasks, &yes); err != nil {
			return err
		}
		if bs, err = loadTaskBudgets(ctx, tx, cid, in.AgentDecl); err != nil {
			return err
		}
		limitR, _, err = challengeBudgetLimit(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, false, err
	}
	est, known := sumTaskBudgets(bs)
	if !known || approved == 0 {
		return nil, false, nil
	}
	limit, _ := effectiveBudgetLimit(est, known, limitR)
	estimate := est.Impl + est.Review

	origin := ""
	for _, t := range tasks {
		if tg.OriginTask != nil && t.ID == formatTaskID(*tg.OriginTask) {
			origin = fmt.Sprintf("足す依頼の出どころ（続きを決めるタスク）: %s（%s）", taskLabel(t), oneLine(t.Title))
		}
	}
	sections := []JudgmentDataSection{
		{Label: callShapeSectionLabel, Content: callShapeExpand},
		{Label: labelChallenge, Content: fmt.Sprintf("タイトル: %s\n\n説明:\n%s", ch.Title, ch.Description)},
		{Label: labelExpandApprovedPlan, Content: formatExpandPlanSection(ch, pv)},
		{Label: labelExpandTasks, Content: formatExpandTasksSection(tasks)},
	}
	if origin != "" {
		sections = append(sections, JudgmentDataSection{Label: labelExpandOrigin, Content: origin})
	}
	if t := formatExpandDoneResultsSection(tasks); t != "" {
		sections = append(sections, JudgmentDataSection{Label: labelExpandDoneResults, Content: t})
	}
	sections = append(sections, JudgmentDataSection{Label: labelExpandBudget, Content: fmt.Sprintf(
		"上限: %s USD\n上限までの残り（上限 − 見込み）: %s USD\n見込み: %s USD",
		formatUSDAmount(microsToUSD(limit)), formatUSDAmount(microsToUSD(limit-estimate)), formatUSDAmount(microsToUSD(estimate)))})
	sections = append(sections, JudgmentDataSection{Label: "対象リポジトリと操作の宣言", Content: formatJ2DeclarationSection(in.ConnDecl, hasSrc, in.AgentDecl)})
	return sections, true, nil
}

// taskLabel はタスクを指す名前（鍵。鍵の無いタスクは ID）。
func taskLabel(t Task) string {
	if t.Key != "" {
		return t.Key
	}
	return t.ID
}

// formatExpandPlanSection は承認した計画の方針・完了条件・やらないことを整形する。
func formatExpandPlanSection(ch Challenge, pv *PlanView) string {
	var b strings.Builder
	b.WriteString("方針:\n")
	switch {
	case pv != nil && pv.Summary != nil && strings.TrimSpace(*pv.Summary) != "":
		b.WriteString(*pv.Summary)
	case pv != nil:
		b.WriteString(pv.Body)
	}
	b.WriteString("\n\n完了条件:\n" + ch.DoneCriteria + "\n\nやらないこと:\n")
	nonGoals := ch.NonGoals
	if len(nonGoals) == 0 && pv != nil {
		nonGoals = pv.NonGoals
	}
	if len(nonGoals) == 0 {
		b.WriteString("  （なし）\n")
	}
	for _, g := range nonGoals {
		b.WriteString("  - " + g + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatExpandTasksSection はいまのタスクの鍵・題名・状態・前提を整形する（前提は鍵で書く。鍵の無いタスクは ID）。
func formatExpandTasksSection(tasks []Task) string {
	label := map[string]string{}
	for _, t := range tasks {
		label[t.ID] = t.ID
		if t.Key != "" {
			label[t.ID] = t.Key
		}
	}
	var b strings.Builder
	for i, t := range tasks {
		deps := make([]string, 0, len(t.DependsOn))
		for _, d := range t.DependsOn {
			deps = append(deps, label[d])
		}
		dep := "なし"
		if len(deps) > 0 {
			dep = strings.Join(deps, ", ")
		}
		fmt.Fprintf(&b, "%d. 鍵: %s｜題名: %s｜状態: %s｜前提: %s\n", i+1, label[t.ID], oneLine(t.Title), t.Status, dep)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatExpandDoneResultsSection は済んだタスクの結果（委譲先の要約・成果物の一覧・タスクの検証の結果）を整形する。
func formatExpandDoneResultsSection(tasks []Task) string {
	var b strings.Builder
	for _, t := range tasks {
		if t.Status != TaskStatusDone {
			continue
		}
		name := t.Key
		if name == "" {
			name = t.ID
		}
		fmt.Fprintf(&b, "■ %s（%s）\n", name, oneLine(t.Title))
		summary := "（なし）"
		if t.Detail.Delegate != nil && strings.TrimSpace(t.Detail.Delegate.Summary) != "" {
			summary = oneLine(t.Detail.Delegate.Summary)
		}
		b.WriteString("  委譲先の要約: " + summary + "\n  成果物:\n")
		if len(t.Detail.Artifacts) == 0 {
			b.WriteString("    （なし）\n")
		}
		for _, a := range t.Detail.Artifacts {
			fmt.Fprintf(&b, "    - %s %s\n", a.Kind, a.Ref)
		}
		if v := t.Detail.Verification; v != nil {
			fmt.Fprintf(&b, "  タスクの検証: %s｜%s\n", v.Verdict, oneLine(v.Reason))
		} else {
			b.WriteString("  タスクの検証: （記録なし）\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// expandAutoJ2 は計画の段の「足す」の処理（足す依頼を作る→判断点を「足す」の形で呼ぶ）。PlanAutoJ2 の後に呼ぶ。
// 保留中の課題・見込みが不明の課題・タスクに結び付かない実行が着手中の課題では判断点を呼ばない。出力は受け口で受けるだけで、
// 課題・足す依頼・タスクを変えない。
func (s *Store) expandAutoJ2(ctx context.Context, in J2AutoInput) (*J2AutoResult, error) {
	if in.AgentDecl == nil || in.ConnDecl == nil || in.Invoker == nil || in.CycleID == "" {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		return nil, ErrValidation
	}
	result := &J2AutoResult{}
	if _, err := s.createExpandAdditions(ctx, in.AgentDecl); err != nil {
		return nil, err
	}
	var targets []expandTarget
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		var ex []NotStarted
		targets, ex, err = selectJ2ExpandTargets(ctx, tx, in.AgentDecl)
		result.NotStarted = append(result.NotStarted, ex...)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	jc := in.Cycle
	if jc == nil {
		jc = NewJudgmentCycle(in.CycleID)
	}
	for _, tg := range targets {
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(tg.Challenge))
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusInProgress {
			continue
		}
		item, ns, err := s.runJ2ExpandOnce(ctx, in, jc, *ch, tg)
		if err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue
			}
			return nil, err
		}
		if ns != nil {
			result.NotStarted = append(result.NotStarted, *ns)
			continue
		}
		if item != nil {
			result.Items = append(result.Items, *item)
		}
	}
	return result, nil
}

// runJ2ExpandOnce は判断点を「足す」の形で 1 回起動し、出力を受け口へ渡す。実行の記録はタスクの指定を持たない。
func (s *Store) runJ2ExpandOnce(ctx context.Context, in J2AutoInput, jc *JudgmentCycle, ch Challenge, tg expandTarget) (*J2AutoItem, *NotStarted, error) {
	if jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}
	maxBudget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ2)
	if err := s.checkCycleBudget(ctx, in.CycleID, maxBudget); err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}
	sections, ok, err := s.prepareJ2Expand(ctx, ch, in, tg)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, nil // 選んだ後に見込みが不明になった（呼ばない）
	}
	res, ns, err := jc.RunJudgment(ctx, s, RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ2,
		MaxBudgetUSD: maxBudget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: j2ExpandOutputSchema(),
		Invoker:      in.Invoker,
		TaskLess:     true,
	})
	if err != nil || ns != nil {
		return nil, ns, err
	}
	item := J2AutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		item.Outcome = receiveJ2ExpandOutput(res.StructuredOutput)
	}
	return &item, nil, nil
}
