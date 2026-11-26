package core

// このファイルは、計画の段が持つ「計画の後にタスクを足す」処理（区切り S4c-1。docs/features/core-redesign-s4c.md）の入力側を持つ。
//
//   - 足す依頼を作る: 足せる課題（統合先を使う課題）の、済みになった expand_after のタスクで、足す依頼がまだ無いものに、
//     出どころ expand・状態 pending の足す依頼を 1 件だけ作る（同じ出どころの expand の行は 1 つ）。
//   - 判断点（J2）を「足す」の形で呼ぶ: 着手中の課題ごとに、pending の足す依頼の古いほうの 1 件だけ。
//     計画の形（selectJ2AutoTargets・prepareJ2・j2OutputSchema・validateJ2Output）とは、対象の選び方・入力の区画・出力の形を別に持つ。
//   - 出力の検査と足す処理は judgment_j2_expand_apply.go（finalizeJ2ExpandOutput）。
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
	// 出どころが followup の足す依頼の入力にだけ入る区画。
	labelExpandFollowupOrigin = "フォローアップの出どころ"
	labelExpandFollowupTitle  = "フォローアップの題名"
	labelExpandFollowupDetail = "フォローアップの説明"
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
	// Options は ask のときだけ読む、問いの選択肢の見出し（任意。無い・null・空は選択肢なし）。
	Options []string `json:"options"`
}

// j2ExpandOutputSchema は「足す」の形の出力スキーマ（j2OutputSchema とは別）。タスクの項目は計画のタスクと同じ
// （j2OutputSchema の tasks の items を使う）。件数は計画のタスクの上限まで。
func j2ExpandOutputSchema() []byte { return j2ExpandOutputSchemaFor(TaskAdditionOriginExpand) }

// j2ExpandOutputSchemaFor は足す依頼の出どころ origin の「足す」の形の出力スキーマ。出どころが followup のときは、判断値を add・ask だけ、
// タスクを 1 個までにする（検査は、スキーマに頼らず validateJ2ExpandOutput が同じ規則で行う）。
func j2ExpandOutputSchemaFor(origin TaskAdditionOrigin) []byte {
	var plan map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &plan); err != nil {
		panic(err) // 定数の組み立ての失敗（起きない）
	}
	tasks := plan["properties"].(map[string]any)["tasks"].(map[string]any)
	verdicts, maxTasks := []string{string(j2ExpandNone), string(j2ExpandAdd), string(j2ExpandAsk)}, MaxPlanTasks
	if origin == TaskAdditionOriginFollowup {
		verdicts, maxTasks = []string{string(j2ExpandAdd), string(j2ExpandAsk)}, 1
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":  map[string]any{"type": "string", "enum": verdicts},
			"tasks":    map[string]any{"type": []string{"array", "null"}, "items": tasks["items"], "maxItems": maxTasks},
			"question": map[string]any{"type": []string{"string", "null"}, "maxLength": MaxStageRunes},
			"options":  map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}},
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

// --- 足す依頼を作る ---

// expandAdditionCandidate は足す依頼をこれから作る、済みの expand_after のタスク。
type expandAdditionCandidate struct {
	ChallengeID string
	TaskID      string
}

// expandAddable は課題 cid が足せる状態か（統合先を使い、着手中か保留中で、見込みが分かる）を返す。
func expandAddable(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (bool, error) {
	ok, _, err := expandAddableReason(ctx, tx, cid, agent)
	return ok, err
}

// expandAddableReason は expandAddable と同じ判定で、足せないときの理由（利用者向けの 1 文）も返す。
func expandAddableReason(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (bool, string, error) {
	ch, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return false, "", err
	}
	switch ch.Status {
	case StatusInProgress:
	case StatusAwaitingHuman:
		// 保留中は、着手中から置かれた保留のときだけ足せる（計画前・検証中から置かれた保留では足せない）。
		var from string
		err := tx.QueryRowContext(ctx,
			`SELECT from_status FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL AND `+challengeScopedHoldSQL+` ORDER BY id DESC LIMIT 1`, cid).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return false, "元の課題の保留が、着手中から置かれたものではない", nil
		}
		if err != nil {
			return false, "", err
		}
		if Status(from) != StatusInProgress {
			return false, "元の課題の保留が、着手中から置かれたものではない", nil
		}
	case StatusVerifying:
		return false, "元の課題が検証中のため、タスクを足せない", nil
	default:
		return false, "元の課題が着手中でも保留中でもないため、タスクを足せない（状態: " + string(ch.Status) + "）", nil
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return false, "", err
	}
	if integ == nil || !*integ {
		return false, "元の課題が統合先を使わない（タスクが 1 個の課題）ため、タスクを足せない", nil
	}
	bs, err := loadTaskBudgets(ctx, tx, cid, agent)
	if err != nil {
		return false, "", err
	}
	_, known := sumTaskBudgets(bs)
	if !known {
		return false, "枠を決められないタスクがある（見込みが不明）ため、タスクを足せない", nil
	}
	return true, "", nil
}

// unaddedExpandTasks は、済みの expand_after のタスクで、足す依頼（出どころ expand）がまだ無いものを、課題の ID の昇順・
// タスクの ID の昇順で返す（課題が足せるかは見ない）。only が非 nil ならその課題だけ。足す依頼を作る対象の選び方と、
// 上げる依頼・検証中へ進める条件・「いま始められるもの」の数えが、この 1 つの規則を共有する。
func unaddedExpandTasks(ctx context.Context, tx *sql.Tx, only *int64) ([]expandPair, error) {
	q := `SELECT t.challenge_id, t.id FROM task t JOIN challenge c ON c.id = t.challenge_id
		 WHERE t.expand_after = 1 AND t.status = ? AND c.status IN (?, ?) AND c.uses_integration = 1
		   AND t.plan_version = (SELECT plan_version FROM approval WHERE challenge_id = t.challenge_id AND kind = ? AND decision = ? ORDER BY id DESC LIMIT 1)
		   AND NOT EXISTS (SELECT 1 FROM task_addition a WHERE a.origin = ? AND a.origin_task_id = t.id)`
	args := []any{string(TaskStatusDone), string(StatusInProgress), string(StatusAwaitingHuman),
		string(ApprovalKindPlan), string(ApprovalDecisionApproved), string(TaskAdditionOriginExpand)}
	if only != nil {
		q += ` AND t.challenge_id = ?`
		args = append(args, *only)
	}
	rows, err := tx.QueryContext(ctx, q+` ORDER BY t.challenge_id, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var pairs []expandPair
	for rows.Next() {
		var p expandPair
		if err := rows.Scan(&p.cid, &p.tid); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// expandPair は課題と、その課題の済みの expand_after のタスク（内部整数 ID）。
type expandPair struct{ cid, tid int64 }

// selectExpandAdditionCandidates は足す依頼を作る対象（課題の ID の昇順・タスクの ID の昇順）を返す。
func selectExpandAdditionCandidates(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) ([]expandAdditionCandidate, error) {
	pairs, err := unaddedExpandTasks(ctx, tx, nil)
	if err != nil {
		return nil, err
	}
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

// expandAdditionStillCandidate は、課題 cid のタスク tid が、いまも足す依頼を作る対象か（selectExpandAdditionCandidates が選ぶ条件と
// 同じ。条件の正本は unaddedExpandTasks と expandAddable の 2 つ）を返す。作る書き込みのトランザクションの中で確かめ直すのに使う。
func expandAdditionStillCandidate(ctx context.Context, tx *sql.Tx, cid, tid int64, agent *AgentDeclaration) (bool, error) {
	pairs, err := unaddedExpandTasks(ctx, tx, &cid)
	if err != nil {
		return false, err
	}
	found := false
	for _, p := range pairs {
		if p.tid == tid {
			found = true
		}
	}
	if !found {
		return false, nil
	}
	return expandAddable(ctx, tx, cid, agent)
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
	return s.createExpandAdditionsFrom(ctx, agent, cands)
}

// createExpandAdditionsFrom は選んだ候補 cands に足す依頼を作る。1 件ごとに、作る書き込みの中で候補の条件を確かめ直す。
func (s *Store) createExpandAdditionsFrom(ctx context.Context, agent *AgentDeclaration, cands []expandAdditionCandidate) (int, error) {
	n := 0
	for _, c := range cands {
		cid, _ := parseChallengeID(c.ChallengeID)
		tid, _ := parseTaskID(c.TaskID)
		created := false
		err := s.mutate(ctx, ChannelInvoker, func(tx *sql.Tx, rec *activityRecorder) error {
			// 候補を選んだ後に状態が変わっていたら作らない（選ぶ条件を、作る書き込みの中で確かめ直す）。
			if ok, err := expandAdditionStillCandidate(ctx, tx, cid, tid, agent); err != nil || !ok {
				return err
			}
			if _, err := createTaskAdditionTx(ctx, tx, rec, cid, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: c.TaskID}); err != nil {
				return err
			}
			created = true
			return nil
		})
		if err != nil {
			if errors.Is(err, ErrValidation) {
				continue // 並行して作られた（同じ出どころの expand は 1 つ）
			}
			return n, err
		}
		if created {
			n++
		}
	}
	return n, nil
}

// --- 判断点を「足す」の形で呼ぶ ---

// expandTarget は「足す」の形で呼ぶ 1 件（課題と、その課題の古いほうの pending の足す依頼の出どころのタスク）。
type expandTarget struct {
	Challenge  int64
	Addition   int64
	OriginTask *int64
	// Origin は足す依頼の出どころ。Followup は出どころが followup のときの、元のフォローアップ（内部整数 ID）。
	Origin   TaskAdditionOrigin
	Followup *int64
}

// expandCallable は着手中の課題 cid について、判断点を「足す」の形で呼べるかを返す（足せる・タスクに結び付かない実行が無い）。
// 周の中で自動で選ぶ対象の共通の規則（取り込み元の食い違い）で外れる課題は、ex に理由を返す。
// 対象の選び方（selectJ2ExpandTargets）と「いま始められるもの」の数え（countExpandWork）が、この 1 つの判定を使う。
func expandCallable(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (callable bool, ex *NotStarted, err error) {
	if ok, err := expandAddable(ctx, tx, cid, agent); err != nil || !ok {
		return false, nil, err
	}
	if busy, err := taskLessHasActiveRun(ctx, tx, cid); err != nil || busy {
		return false, nil, err
	}
	ex, err = challengeAutoExclusion(ctx, tx, cid)
	return ex == nil, ex, err
}

// selectJ2ExpandTargets は判断点を「足す」の形で呼ぶ対象を、課題の優先度→ID の順で返す。対象は、着手中で、統合先を使い、見込みが
// 分かり、タスクに結び付かない実行が無く、pending の足す依頼（出どころ expand・followup）を持つ課題。課題ごとに古いほうの 1 件だけ。保留中・見込みが不明の課題は
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
		var origin, followup sql.NullInt64
		var kind string
		err := tx.QueryRowContext(ctx,
			`SELECT id, origin, origin_task_id, followup_id FROM task_addition WHERE challenge_id = ? AND state = ? AND (origin = ? OR (origin = ? AND followup_id IN (SELECT id FROM followup WHERE status = ?))) ORDER BY id LIMIT 1`,
			cid, string(TaskAdditionPending), string(TaskAdditionOriginExpand), string(TaskAdditionOriginFollowup), string(FollowupStatusOpen)).Scan(&aid, &kind, &origin, &followup)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		callable, ex, err := expandCallable(ctx, tx, cid, agent)
		if err != nil {
			return nil, nil, err
		}
		if ex != nil {
			excluded = append(excluded, *ex)
			continue
		}
		if !callable {
			continue
		}
		tg := expandTarget{Challenge: cid, Addition: aid, Origin: TaskAdditionOrigin(kind)}
		if origin.Valid {
			v := origin.Int64
			tg.OriginTask = &v
		}
		if followup.Valid {
			v := followup.Int64
			tg.Followup = &v
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
		qaText   string
		fu       *Followup
		criteria *planAcceptanceCriteria
		allTasks []Task
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
		allTasks = all
		ap, found, err := loadApprovedPlanAnySpec(ctx, tx, cid)
		if err != nil {
			return err
		}
		if found {
			// 受入基準の一覧は承認した計画のものを使う（仕様の文書は読み直さない）。
			if criteria, err = loadPlanAcceptanceCriteria(ap.Spec); err != nil {
				return err
			}
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
		if limitR, _, err = challengeBudgetLimit(ctx, tx, cid); err != nil {
			return err
		}
		if tg.Followup != nil {
			if fu, err = loadFollowup(ctx, tx, *tg.Followup); err != nil {
				return err
			}
		}
		qaText, err = expandQuestionAnswerText(ctx, tx, tg.Addition)
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
			if tg.Origin == TaskAdditionOriginFollowup {
				origin = fmt.Sprintf("フォローアップを書いたタスク: %s（%s）", taskLabel(t), oneLine(t.Title))
			} else {
				origin = fmt.Sprintf("足す依頼の出どころ（続きを決めるタスク）: %s（%s）", taskLabel(t), oneLine(t.Title))
			}
		}
	}
	sections := []JudgmentDataSection{
		{Label: callShapeSectionLabel, Content: callShapeExpand},
		{Label: labelChallenge, Content: fmt.Sprintf("タイトル: %s\n\n説明:\n%s", ch.Title, ch.Description)},
		{Label: labelExpandApprovedPlan, Content: formatExpandPlanSection(ch, pv)},
		{Label: labelExpandTasks, Content: formatExpandTasksSection(tasks)},
	}
	if criteria != nil {
		// 済みの印は、前の版を含むすべての済みのタスクの受け持ちに付ける。
		sections = append(sections, JudgmentDataSection{Label: AcceptanceCriteriaSectionLabel, Content: formatAcceptanceCriteriaSection(*criteria, allTasks)})
	}
	if origin != "" {
		label := labelExpandOrigin
		if tg.Origin == TaskAdditionOriginFollowup {
			label = labelExpandFollowupOrigin
		}
		sections = append(sections, JudgmentDataSection{Label: label, Content: origin})
	}
	if fu != nil {
		sections = append(sections,
			JudgmentDataSection{Label: labelExpandFollowupTitle, Content: fu.Title},
			JudgmentDataSection{Label: labelExpandFollowupDetail, Content: fu.Detail})
	}
	if qaText != "" {
		sections = append(sections, JudgmentDataSection{Label: labelHoldRecord, Content: qaText})
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

// expandQuestionAnswerText は、足す依頼 aid の保留（理由 task_add_question）が回答済みのとき、その問いと回答（問いを重ねたときはすべて）を
// 計画の形の「保留の記録」と同じ書式（formatHoldsSection）で返す。選択肢の識別で答えたときの回答の文は、選んだ選択肢の見出しを含む。
// 該当しなければ空文字列。
func expandQuestionAnswerText(ctx context.Context, tx *sql.Tx, aid int64) (string, error) {
	if aid == 0 {
		return "", nil
	}
	// 保留が記録している足す依頼（task_addition_id）で選ぶ。問いを重ねても、この足す依頼の回答済みの問いを古い順にすべて渡す。
	rows, err := tx.QueryContext(ctx, `SELECT question, answer FROM hold
		WHERE task_addition_id = ? AND reason = ? AND answer IS NOT NULL
		ORDER BY id`, aid, string(holdReasonTaskAddQuestion))
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var holds []Hold
	for rows.Next() {
		var q, ans string
		if err := rows.Scan(&q, &ans); err != nil {
			return "", err
		}
		holds = append(holds, Hold{Question: q, Answer: &ans})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return formatHoldsSection(holds), nil
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
// 保留中の課題・見込みが不明の課題・タスクに結び付かない実行が着手中の課題では判断点を呼ばない。出力は検査して足す依頼に写す
// （finalizeJ2ExpandOutput）。課題の状態は変えない。
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
		// 判断点の連続失敗の上限に達していれば、いまある連続失敗の保留にする（課題が着手中であることを前提に呼ぶ）。
		held, skip, err := s.holdForJudgmentFailureLimit(ctx, *ch, JudgmentJ2, StatusInProgress, in.AgentDecl, in.CycleID)
		if err != nil {
			return nil, err
		}
		if held != nil {
			result.NotStarted = append(result.NotStarted, *held)
			continue
		}
		if skip {
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
		OutputSchema: j2ExpandOutputSchemaFor(tg.Origin),
		Invoker:      in.Invoker,
		TaskLess:     true,
	})
	if err != nil || ns != nil {
		return nil, ns, err
	}
	item := J2AutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		fin, err := s.finalizeJ2ExpandOutput(ctx, in, res.RunID, tg.Addition, res.StructuredOutput)
		if err != nil {
			return nil, nil, err
		}
		item.Outcome, item.Result, item.Note = fin.Outcome, fin.Result, fin.Note
	}
	return &item, nil, nil
}
