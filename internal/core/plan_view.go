package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// このファイルは、課題の最新の計画を、方針・問いと答え・タスクのツリー・予算・統合先を使うかとして読む口を持つ
// （`flywheel show --plan` と、計画の承認の要約が使う）。読み取りだけで、何も書かない。
//
// 承認の前はタスクの行が無いので、タスクのツリーは計画の構造化した出力（task_plan.spec）から読み、前提は計画の中の
// 識別子（key）で出す。承認の後は同じ計画の版のタスクの行があるので、ID（T-<番号>）と前提のタスクの ID で出す。

// PlanQuestionAnswerView は計画の問いへの答え（同じ問いに複数あれば後の行）。
// Choice・Label は選択肢の答え（Label は選ばれた選択肢のラベル）、Text は文の答え。
type PlanQuestionAnswerView struct {
	Choice      *int64
	Label       *string
	Text        *string
	Recommended bool
}

// PlanQuestionView は計画の問い 1 つ。Number は計画の中の通し番号（1 から。論点をまたいで数える）、
// Recommended は Options の 1 から数えた位置、Answer は答えが記録されていなければ nil。
type PlanQuestionView struct {
	Number      int64
	Question    string
	Options     []StageOption
	Recommended int
	Answer      *PlanQuestionAnswerView
}

// PlanTopicView は計画の論点 1 つ（題名・どう決めたか・問い 0 件以上）。
type PlanTopicView struct {
	Title     string
	Decision  string
	Questions []PlanQuestionView
}

// PlanTaskView は計画のタスク 1 つ。Key は計画の中の識別子。ID・Status は承認の後（タスクの行がある）のときだけ
// 非空。DependsOn の要素は、承認の前は前提のタスクの Key、承認の後は前提のタスクの ID（"T-<n>"）。
type PlanTaskView struct {
	Key       string
	ID        string
	Title     string
	DependsOn []string
	Status    TaskStatus
	// Source は対応する取り込み元の子の external_key（対応しなければ空）。
	Source string
}

// Identifier はタスクを指す識別子（承認の後はタスクの ID、承認の前は key）。
func (t PlanTaskView) Identifier() string {
	if t.ID != "" {
		return t.ID
	}
	return t.Key
}

// PlanView は課題の最新の計画の読み取りの結果。Structured は、計画が構造化した出力（spec）を持つか
// （持たない計画は Body だけが意味を持つ）。Summary・UsesIntegration は計画が書かなかったとき nil。
// BudgetUSD は計画の枠の額（計画が書いた額。無ければ宣言のサイズごとの既定）で、決められないとき nil。
type PlanView struct {
	ChallengeID string
	Title       string
	Status      Status
	Version     int64
	Body        string
	Structured  bool

	Summary         *string
	Expected        []StageExpected
	NonGoals        []string
	Topics          []PlanTopicView
	Tasks           []PlanTaskView
	BudgetUSD       *float64 // 実装の枠とレビュー対応の枠の合計（その版の予算の増額を当てた額）
	UsesIntegration *bool
}

// GetPlanView は課題の最新の計画を読む。課題が無ければ ErrNotFound、計画が無ければ ErrValidation。
// agent は、計画が額を書いていない枠をサイズごとの既定から決めるための宣言（nil 可。nil で既定が要るときは、
// 予算の上限を決められないものとして BudgetUSD を nil にする）。
func (s *Store) GetPlanView(ctx context.Context, id string, agent *AgentDeclaration) (*PlanView, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *PlanView
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		pv, err := loadPlanView(ctx, tx, cid, agent)
		if err != nil {
			return err
		}
		if pv == nil {
			return ErrValidation
		}
		pv.ChallengeID, pv.Title, pv.Status = c.ID, c.Title, c.Status
		out = pv
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// loadPlanView は課題の最新の計画を読む。計画が無ければ nil（エラーにしない）。ChallengeID・Title・Status は埋めない。
// agent は予算の上限をサイズごとの既定から決めるための宣言（nil 可）。
func loadPlanView(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (*PlanView, error) {
	var (
		version int64
		body    string
		spec    sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT version, body, spec FROM task_plan WHERE challenge_id = ? ORDER BY version DESC LIMIT 1`, cid).Scan(&version, &body, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pv := &PlanView{Version: version, Body: body}
	var raw j2RawOutput
	if spec.Valid && strings.TrimSpace(spec.String) != "" && json.Unmarshal([]byte(spec.String), &raw) == nil {
		pv.Structured = true
	}
	if !pv.Structured {
		return pv, nil
	}

	pv.Summary = raw.Summary
	pv.Expected = raw.Expected
	pv.NonGoals = raw.NonGoals
	pv.UsesIntegration = raw.UsesIntegration
	// 予算の上限は、委譲が使うのと同じ規則で決める（計画が書いた額。無い枠は、宣言のサイズごとの既定。
	// その版に `flywheel budget` の増額があれば、置き換えた額）。
	if implUSD, reviewUSD, ok := resolveSpecBudgets(spec.String, agent); ok {
		override, err := loadPlanBudgetOverride(ctx, tx, cid, version)
		if err != nil {
			return nil, err
		}
		b := applyBudgetOverride(implUSD, reviewUSD, override)
		sum := microsToUSD(b.Impl + b.Review)
		pv.BudgetUSD = &sum
	}

	answers, err := listPlanAnswers(ctx, tx, cid, version)
	if err != nil {
		return nil, err
	}
	byNumber := make(map[int64]PlanAnswer, len(answers))
	for _, a := range answers {
		byNumber[a.QuestionNumber] = a
	}
	var n int64
	for _, t := range raw.Topics {
		tv := PlanTopicView{Title: t.Title, Decision: t.Decision, Questions: []PlanQuestionView{}}
		for _, q := range t.Questions {
			n++
			qv := PlanQuestionView{Number: n, Question: q.Question, Options: q.Options, Recommended: q.Recommended}
			if a, ok := byNumber[n]; ok {
				qv.Answer = planAnswerView(a, q.Options)
			}
			tv.Questions = append(tv.Questions, qv)
		}
		pv.Topics = append(pv.Topics, tv)
	}

	// 承認の後は、同じ計画の版のタスクの行がある。key で突き合わせ、ID・前提のタスクの ID・状態で出す。
	rows, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	byKey := map[string]Task{}
	for _, t := range rows {
		if t.PlanVersion == version && t.Key != "" {
			byKey[t.Key] = t
		}
	}
	for _, t := range raw.Tasks {
		tv := PlanTaskView{Key: t.Key, Title: t.Title, Source: t.Source, DependsOn: append([]string{}, t.DependsOn...)}
		if row, ok := byKey[t.Key]; ok {
			tv.ID, tv.Status = row.ID, row.Status
			tv.DependsOn = append([]string{}, row.DependsOn...)
		}
		pv.Tasks = append(pv.Tasks, tv)
	}
	return pv, nil
}

func planAnswerView(a PlanAnswer, options []StageOption) *PlanQuestionAnswerView {
	v := &PlanQuestionAnswerView{Choice: a.Choice, Text: a.Text, Recommended: a.Recommended}
	if a.Choice != nil && *a.Choice >= 1 && *a.Choice <= int64(len(options)) {
		label := options[*a.Choice-1].Label
		v.Label = &label
	}
	return v
}
