package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// このファイルは、計画の承認待ちの課題の問い（計画の spec の論点の問い）への答えの記録（`flywheel answer --choice/--text`。
// 遷移 T19）と、計画の承認で行う後始末（答えの無い問いへの推奨の記録・タスクの行の作成・統合先を使うかの記録・
// ほかの版のタスクの取りやめ）を持つ。どの書き込みも、答え・承認と同じトランザクションの中で行う。

// PlanAnswersSectionLabel は J2 の入力の区画「問いと回答」の見出し。
const PlanAnswersSectionLabel = "問いと回答"

// planQuestion は計画の問い 1 つ。Number は計画の中の通し番号（1 から。論点をまたいで数える。計画の本文の Q<n> と同じ）。
type planQuestion struct {
	Number      int64
	Text        string
	Options     []StageOption
	Recommended int
}

// planSpecView は計画の spec（構造化した出力の JSON）から読んだ、問い・タスク・統合先を使うか。
type planSpecView struct {
	Questions       []planQuestion
	Tasks           []TaskInput
	UsesIntegration *bool
	// BudgetLimitMicros・BudgetLimitReason は計画が書いた上限（USD の 100 万分の 1）と理由。省いたら nil（検査を通った計画の理由は空白を除いた値）。
	BudgetLimitMicros *int64
	BudgetLimitReason *string
}

// taskBudgetMicros は計画の出力のタスクの枠（USD）を、タスクの記録の値（USD の 100 万分の 1）にする（無ければ nil）。
func taskBudgetMicros(usd *float64) *int64 {
	if usd == nil {
		return nil
	}
	v := usdToMicros(*usd)
	return &v
}

// parsePlanSpec は spec（task_plan.spec。NULL は nil）を読む。構造化した出力を持たない計画（nil・空・JSON でない）は
// 空の view を返す。
func parsePlanSpec(spec *string) planSpecView {
	var v planSpecView
	if spec == nil || strings.TrimSpace(*spec) == "" {
		return v
	}
	var raw j2RawOutput
	if err := json.Unmarshal([]byte(*spec), &raw); err != nil {
		return v
	}
	var n int64
	for _, t := range raw.Topics {
		for _, q := range t.Questions {
			n++
			v.Questions = append(v.Questions, planQuestion{Number: n, Text: q.Question, Options: q.Options, Recommended: q.Recommended})
		}
	}
	for _, t := range raw.Tasks {
		v.Tasks = append(v.Tasks, TaskInput{
			Key: t.Key, Title: t.Title, Body: t.Body, DoneCriteria: t.DoneCriteria, NonGoals: t.NonGoals,
			Repo: t.Repo, Operation: t.Operation, DependsOn: t.DependsOn, SourceKey: t.Source,
			BudgetImplMicros: taskBudgetMicros(t.BudgetImplUSD), BudgetReviewMicros: taskBudgetMicros(t.BudgetReviewUSD),
			ExpandAfter: t.ExpandAfter != nil && *t.ExpandAfter,
		})
	}
	v.UsesIntegration = raw.UsesIntegration
	if raw.BudgetLimitUSD != nil {
		v.BudgetLimitMicros = taskBudgetMicros(raw.BudgetLimitUSD)
		if raw.BudgetLimitReason != nil {
			if r := strings.TrimSpace(*raw.BudgetLimitReason); r != "" {
				v.BudgetLimitReason = &r
			}
		}
	}
	return v
}

// loadPlanSpec は課題の計画の版 version の spec を読む。計画が無ければ ErrNotFound。
func loadPlanSpec(ctx context.Context, tx *sql.Tx, cid, version int64) (planSpecView, error) {
	var spec sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT spec FROM task_plan WHERE challenge_id = ? AND version = ?`, cid, version).Scan(&spec); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return planSpecView{}, ErrNotFound
		}
		return planSpecView{}, err
	}
	if !spec.Valid {
		return planSpecView{}, nil
	}
	return parsePlanSpec(&spec.String), nil
}

// latestPlanVersion は課題の最新の計画の版を返す（計画が無ければ ok=false）。
func latestPlanVersion(ctx context.Context, tx *sql.Tx, cid int64) (int64, bool, error) {
	var v sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_plan WHERE challenge_id = ?`, cid).Scan(&v); err != nil {
		return 0, false, err
	}
	return v.Int64, v.Valid, nil
}

// PlanAnswerItem は 1 つの問いへの答え。Choice（選択肢の番号。1 から）か Text（文）の一方だけを持つ。
type PlanAnswerItem struct {
	Question int64
	Choice   *int64
	Text     *string
}

// PlanAnswerLine は検査を通った答え 1 件（確認の要約に使う）。
type PlanAnswerLine struct {
	Question     int64
	QuestionText string
	// Choice・ChoiceLabel は選択肢の答えのとき。Text は文の答えのとき。
	Choice      *int64
	ChoiceLabel string
	Text        *string
	// Recommended は、選択肢の答えが推奨と同じか（文の答えは常に偽）。
	Recommended bool
}

// PlanAnswerPreview は PreparePlanAnswer の結果（①＝読み取り）。
type PlanAnswerPreview struct {
	ChallengeID string
	Title       string
	Version     int   // 表示時点の課題の版（ExecutePlanAnswer の ExpectedVersion にそのまま渡す）
	PlanVersion int64 // 答える対象の計画の版（最新）
	Answers     []PlanAnswerLine
	// ReturnsToClassified は、推奨と違う選択肢か文の答えを含み、課題が分類済へ戻る（計画を作り直す）か。
	ReturnsToClassified bool
}

// resolvePlanAnswers は課題が計画の承認待ちで、items が最新の計画の問いに照らして正しいことを検査し、
// 答えの一覧（問いの番号の昇順）を返す。計画の承認待ちでなければ ErrTerminalState（完了など）か
// ErrInvalidTransition、items の不備（空・同じ問いの重複・両方か無し・計画に無い問いの番号・選択肢の範囲外・
// 空の文）は ErrValidation。何も書かない。
func resolvePlanAnswers(ctx context.Context, tx *sql.Tx, cid int64, items []PlanAnswerItem) (*Challenge, int64, []PlanAnswerLine, error) {
	current, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, 0, nil, err
	}
	if IsTerminal(Table, StatusVocabulary, current.Status) {
		return nil, 0, nil, ErrTerminalState
	}
	if current.Status != StatusAwaitingPlanApproval {
		return nil, 0, nil, ErrInvalidTransition
	}
	pv, ok, err := latestPlanVersion(ctx, tx, cid)
	if err != nil {
		return nil, 0, nil, err
	}
	if !ok {
		return nil, 0, nil, fmt.Errorf("core: challenge %s is awaiting_plan_approval but has no plan", current.ID)
	}
	view, err := loadPlanSpec(ctx, tx, cid, pv)
	if err != nil {
		return nil, 0, nil, err
	}
	if len(items) == 0 {
		return nil, 0, nil, ErrValidation
	}
	byNumber := make(map[int64]planQuestion, len(view.Questions))
	for _, q := range view.Questions {
		byNumber[q.Number] = q
	}
	seen := map[int64]bool{}
	lines := make([]PlanAnswerLine, 0, len(items))
	for _, it := range items {
		if (it.Choice == nil) == (it.Text == nil) || seen[it.Question] {
			return nil, 0, nil, ErrValidation
		}
		seen[it.Question] = true
		q, ok := byNumber[it.Question]
		if !ok {
			return nil, 0, nil, fmt.Errorf("%w: no such question Q%d in the plan", ErrValidation, it.Question)
		}
		line := PlanAnswerLine{Question: q.Number, QuestionText: q.Text}
		if it.Choice != nil {
			if *it.Choice < 1 || *it.Choice > int64(len(q.Options)) {
				return nil, 0, nil, fmt.Errorf("%w: choice %d is out of range for Q%d", ErrValidation, *it.Choice, q.Number)
			}
			c := *it.Choice
			line.Choice, line.ChoiceLabel, line.Recommended = &c, q.Options[c-1].Label, c == int64(q.Recommended)
		} else {
			if strings.TrimSpace(*it.Text) == "" {
				return nil, 0, nil, ErrValidation
			}
			t := *it.Text
			line.Text = &t
		}
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Question < lines[j].Question })
	return current, pv, lines, nil
}

func planAnswersReturnToClassified(lines []PlanAnswerLine) bool {
	for _, l := range lines {
		if !l.Recommended {
			return true
		}
	}
	return false
}

// PreparePlanAnswer は計画の問いへの答えの①（読み取り）。何も書かない。
func (s *Store) PreparePlanAnswer(ctx context.Context, id string, items []PlanAnswerItem) (*PlanAnswerPreview, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *PlanAnswerPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, pv, lines, err := resolvePlanAnswers(ctx, tx, cid, items)
		if err != nil {
			return err
		}
		out = &PlanAnswerPreview{
			ChallengeID: current.ID, Title: current.Title, Version: current.Version, PlanVersion: pv,
			Answers: lines, ReturnsToClassified: planAnswersReturnToClassified(lines),
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// PlanAnswerRequest は ExecutePlanAnswer の入力（②＝書き込み）。
type PlanAnswerRequest struct {
	ChallengeID     string
	ExpectedVersion int
	Answers         []PlanAnswerItem
}

// writePlanAnswers は lines の答えを plan_answer へ書き、作業ログの after へ載せる answers を返す。
func writePlanAnswers(ctx context.Context, tx *sql.Tx, cid, planVersion int64, lines []PlanAnswerLine, att Attestation, at time.Time) ([]PlanAnswer, []map[string]any, error) {
	rows := make([]PlanAnswer, 0, len(lines))
	logged := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		a, err := recordPlanAnswer(ctx, tx, PlanAnswerInput{
			ChallengeID: cid, PlanVersion: planVersion, QuestionNumber: l.Question, Choice: l.Choice, Text: l.Text,
			Recommended: l.Recommended, Actor: att.Actor(), Channel: att.Channel(), Verification: att.Verification(),
			RelaySession: att.currentEvidence().Session, RelayQuote: att.currentEvidence().Quote,
		}, at)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, *a)
		entry := map[string]any{"question": l.Question, "recommended": l.Recommended}
		if l.Choice != nil {
			entry["choice"] = *l.Choice
		} else {
			entry["text"] = *l.Text
		}
		logged = append(logged, entry)
	}
	return rows, logged, nil
}

// ExecutePlanAnswer は計画の問いへの答えの②（書き込み）。書き込みの前に、att が操作の種類 answer_plan を
// 受けられる組（登録簿）であることを照らす（画面・本人確認なしの組は ErrVerificationRejected）。
// 推奨と違う選択肢か文の答えが 1 件でもあれば課題を分類済へ戻し（T19）、推奨と同じ選択肢だけなら
// 状態を変えずに記録だけを行い、課題の版を 1 つ上げる。答え・課題の更新・作業ログは 1 つのトランザクションで書く。
// 版が ExpectedVersion と違えば ErrConflict。
func (s *Store) ExecutePlanAnswer(ctx context.Context, req PlanAnswerRequest, att Attestation) (*Challenge, []PlanAnswer, error) {
	if !attestationPermits(att, req.ChallengeID, actionAnswerPlan) {
		return nil, nil, ErrVerificationRejected
	}
	cid, ok := parseChallengeID(req.ChallengeID)
	if !ok {
		return nil, nil, ErrNotFound
	}

	// 戻すか記録だけかは、版が一致すれば（一致しなければ ErrConflict）読み取りの時点から変わらない。
	var returns bool
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		_, _, lines, err := resolvePlanAnswers(ctx, tx, cid, req.Answers)
		returns = planAnswersReturnToClassified(lines)
		return err
	})); err != nil {
		return nil, nil, err
	}

	var written []PlanAnswer
	if returns {
		apply := func(ctx context.Context, tc *transitionCtx) error {
			_, pv, lines, err := resolvePlanAnswers(ctx, tc.tx, tc.id, req.Answers)
			if err != nil {
				return err
			}
			rows, logged, err := writePlanAnswers(ctx, tc.tx, tc.id, pv, lines, att, tc.now)
			if err != nil {
				return err
			}
			written = rows
			tc.after["answers"] = logged
			att.addRelayTo(tc.after)
			return nil
		}
		c, err := s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, OpAnswerPlan, NoStatus, nil, apply)
		if err != nil {
			return nil, nil, err
		}
		return c, written, nil
	}

	var result *Challenge
	err := s.mutateAsRun(ctx, att.actor, att.channel, att.verification, nil, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionAnswerPlan, ""); err != nil {
			return err
		}
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if current.Version != req.ExpectedVersion {
			return ErrConflict
		}
		_, pv, lines, err := resolvePlanAnswers(ctx, tx, cid, req.Answers)
		if err != nil {
			return err
		}
		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}
		rows, logged, err := writePlanAnswers(ctx, tx, cid, pv, lines, att, now)
		if err != nil {
			return err
		}
		newVersion := current.Version + 1
		res, err := tx.ExecContext(ctx, `UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`, newVersion, nowStr, cid, current.Version)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("core: answer plan %s: expected to update 1 row, updated %d", req.ChallengeID, n)
		}
		after := map[string]any{"version": newVersion, "answers": logged}
		att.addRelayTo(after)
		if err := rec.record("challenge", cid, string(OpAnswerPlan), nil, after); err != nil {
			return err
		}
		written = rows
		current.Version, current.UpdatedAt = newVersion, now
		result = current
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}
	return result, written, nil
}

// onPlanApproved は計画の承認（T5）の書き込みと同じトランザクションの中で、承認した版 planVersion の計画について
// 次を行う: ① 答えが記録されていない問いに、推奨の選択肢を答えとして記録する（承認と同じ人・経路・本人確認。
// recommended は 1。すでに答えのある問いには足さない）。② 同じ課題のほかの版のタスクのうち済みでも取りやめでも
// ないものを取りやめにする。③ 承認した版のタスクの行と前提の行を作る（状態は pending）。④ 統合先を使うかを課題へ
// 記録する。構造化した出力を持たない計画（spec が NULL か、タスクを持たない過去の形）は、その部分を行わない。
func onPlanApproved(ctx context.Context, tc *transitionCtx, att Attestation, planVersion int64, agent *AgentDeclaration) error {
	view, err := loadPlanSpec(ctx, tc.tx, tc.id, planVersion)
	if err != nil {
		return err
	}
	if err := fillSoleTaskBudgets(ctx, tc.tx, tc.id, planVersion, view.Tasks, agent); err != nil {
		return err
	}
	if len(view.Questions) > 0 {
		answered, err := listPlanAnswers(ctx, tc.tx, tc.id, planVersion)
		if err != nil {
			return err
		}
		have := make(map[int64]bool, len(answered))
		for _, a := range answered {
			have[a.QuestionNumber] = true
		}
		for _, q := range view.Questions {
			if have[q.Number] {
				continue
			}
			rec := int64(q.Recommended)
			if _, err := recordPlanAnswer(ctx, tc.tx, PlanAnswerInput{
				ChallengeID: tc.id, PlanVersion: planVersion, QuestionNumber: q.Number, Choice: &rec, Recommended: true,
				Actor: att.Actor(), Channel: att.Channel(), Verification: att.Verification(),
				RelaySession: att.currentEvidence().Session, RelayQuote: att.currentEvidence().Quote,
			}, tc.now); err != nil {
				return err
			}
		}
	}
	if _, err := setTasksStatus(ctx, tc.tx, tc.rec, tc.id,
		taskFilter{Statuses: []TaskStatus{TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying}}, TaskStatusCancelled); err != nil {
		return err
	}
	if len(view.Tasks) > 0 {
		if _, err := createTasks(ctx, tc.tx, tc.rec, tc.id, planVersion, view.Tasks); err != nil {
			return err
		}
	}
	if view.UsesIntegration != nil {
		if err := setChallengeUsesIntegration(ctx, tc.tx, tc.id, view.UsesIntegration); err != nil {
			return err
		}
	}
	// 上限は承認した版の値で置き換える（省いたら NULL。前の版の上限を残さない）。計画の表には持たず、承認した値だけを課題が持つ。
	if err := setChallengeBudgetLimit(ctx, tc.tx, tc.id, view.BudgetLimitMicros, view.BudgetLimitReason); err != nil {
		return err
	}
	// 作り直しの承認では、前の版の見込みに対する足す依頼は意味を失う。閉じていないものを取り替えで閉じる。
	return supersedeOpenTaskAdditions(ctx, tc.tx, tc.rec, tc.id)
}

// loadPlanAnswersSection は J2 の入力の区画「問いと回答」の本文を返す。課題のこれまでの版の計画の問い（文・選択肢）と、
// 記録された答え（選ばれた選択肢のラベルか、文）を版の順に並べる。答えが 1 件も記録されていなければ空。
func loadPlanAnswersSection(ctx context.Context, tx *sql.Tx, cid int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, spec FROM task_plan WHERE challenge_id = ? ORDER BY version`, cid)
	if err != nil {
		return "", err
	}
	type planRow struct {
		version int64
		spec    sql.NullString
	}
	var plans []planRow
	for rows.Next() {
		var p planRow
		if err := rows.Scan(&p.version, &p.spec); err != nil {
			_ = rows.Close()
			return "", err
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("人間が前の版の計画の問いに答えた内容。人間の決定であり、「推奨と違う」答えと文の答えは新しい計画に反映する。\n\n")
	has := false
	for _, p := range plans {
		var spec *string
		if p.spec.Valid {
			spec = &p.spec.String
		}
		view := parsePlanSpec(spec)
		if len(view.Questions) == 0 {
			continue
		}
		answers, err := listPlanAnswers(ctx, tx, cid, p.version)
		if err != nil {
			return "", err
		}
		if len(answers) == 0 {
			continue
		}
		has = true
		byQ := make(map[int64]PlanAnswer, len(answers))
		for _, a := range answers {
			byQ[a.QuestionNumber] = a
		}
		fmt.Fprintf(&b, "計画の版 %d の問いと答え:\n", p.version)
		for _, q := range view.Questions {
			fmt.Fprintf(&b, "\nQ%d. %s\n", q.Number, oneLine(q.Text))
			for j, o := range q.Options {
				fmt.Fprintf(&b, "  %d) %s", j+1, oneLine(o.Label))
				if j+1 == q.Recommended {
					b.WriteString("【推奨】")
				}
				b.WriteString("\n")
			}
			a, ok := byQ[q.Number]
			switch {
			case !ok:
				b.WriteString("  答え: まだ記録されていない\n")
			case a.Text != nil:
				b.WriteString("  答え（文）: " + oneLine(*a.Text) + "\n")
			case a.Choice != nil && *a.Choice >= 1 && *a.Choice <= int64(len(q.Options)):
				fmt.Fprintf(&b, "  答え: %d) %s", *a.Choice, oneLine(q.Options[*a.Choice-1].Label))
				if a.Recommended {
					b.WriteString("（推奨と同じ）")
				} else {
					b.WriteString("（推奨と違う）")
				}
				b.WriteString("\n")
			default:
				fmt.Fprintf(&b, "  答え: 選択肢 %d\n", derefInt64(a.Choice))
			}
		}
		b.WriteString("\n")
	}
	if !has {
		return "", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
