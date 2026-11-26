package core

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画・決定 M3P14・M3P22）が持つ、J2（計画）固有の出力の検査・予算の既定の
// 解決・計画の本文の整形・課題への写像・対象の選び方・`plan --auto` の本体
// （Store.PlanAutoJ2）を持つ。判断点に依らない共通の部分は judgment_common.go、
// 上流の入力の組み立ては judgment_j2_upstream.go に置く。
//
// 1 回の流れ（§機能全体の設計「1 つの run の流れ」）:
//
//	① 上流の取得（J2 の起動の直前）→ 書き込みトランザクションで run を記録（RunJudgment）
//	② トランザクションの外で子を待つ（RunJudgment）
//	③ 書き込みトランザクションで結果を記録 → 出力を再検査 → 課題の現在の状態を
//	   読み直して写す（遷移元でなくなっていれば写さず、run の出力だけを残す）

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// --- J2 の出力スキーマ・検査 ---

// 計画の件数の上限（J2 の出力の tasks・topics・topics[].questions）。
const (
	MinPlanTasks         = 1
	MaxPlanTasks         = 12
	MaxPlanTopics        = 5
	MaxQuestionsPerTopic = 3
)

// j2RawOutput は J2 の構造化出力（`--json-schema` に渡した形）をそのまま受ける
// 生の形（§IF / API「判断点の出力」J2 の行）。
type j2RawOutput struct {
	Verdict         string       `json:"verdict"`
	Summary         *string      `json:"summary"`
	Topics          []j2RawTopic `json:"topics"`
	DoneCriteria    *string      `json:"done_criteria"`
	Tasks           []j2RawTask  `json:"tasks"`
	UsesIntegration *bool        `json:"uses_integration"`
	Size            *string      `json:"size"`
	BudgetImplUSD   *float64     `json:"budget_impl_usd"`
	BudgetReviewUSD *float64     `json:"budget_review_usd"`
	// BudgetLimitUSD・BudgetLimitReason は課題の上限（実装の枠とレビュー対応の枠を足した 1 つの額）と、見込みより大きいときの理由（どちらも省ける）。
	BudgetLimitUSD    *float64 `json:"budget_limit_usd"`
	BudgetLimitReason *string  `json:"budget_limit_reason"`
	CrossRepo         *bool    `json:"cross_repo"`
	RelatedRepos      []string `json:"related_repos"`
	Question          *string  `json:"question"`
	// Expected・NonGoals・Figure は段階の欄（無い・null は「書かない」）。
	Expected []StageExpected `json:"expected"`
	NonGoals []string        `json:"non_goals"`
	Figure   *string         `json:"figure"`
	// DependsOn は省略可の依存先の課題 ID（M45P3）。ストアを読む検査は写像の中で行う。
	DependsOn []string `json:"depends_on"`
}

// j2RawTopic は J2 の出力の論点 1 つ（題名・どう決めたか・問い 0〜3 件）の生の形。
type j2RawTopic struct {
	Title     string       `json:"title"`
	Decision  string       `json:"decision"`
	Questions []StageIssue `json:"questions"`
}

// j2RawTask は J2 の出力のタスク 1 つの生の形。
type j2RawTask struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	DoneCriteria string   `json:"done_criteria"`
	NonGoals     []string `json:"non_goals"`
	DependsOn    []string `json:"depends_on"`
	Repo         string   `json:"repo"`
	Operation    string   `json:"operation"`
	// BudgetImplUSD・BudgetReviewUSD はタスクの実装の枠・レビュー対応の枠（任意。タスクが 2 個以上の計画では両方が必須）。
	BudgetImplUSD   *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD *float64 `json:"budget_review_usd"`
	// Source は対応する取り込み元の子の external_key（任意。無い・null・空は子に対応しない）。
	Source string `json:"source"`
	// ExpandAfter は、このタスクの後に、続きのタスクを足すかを決めるか（任意。統合先を使う計画だけ真にできる）。
	ExpandAfter *bool `json:"expand_after"`
	// FlowReason は flow の形の委譲先の操作を選んだ理由（flow の形の操作では必須。そうでない操作では空以外が不正）。
	FlowReason *string `json:"flow_reason"`
}

// j2Topic は検査済みの論点 1 つ。
type j2Topic struct {
	Title     string
	Decision  string
	Questions []StageIssue
}

// j2Task は検査済みのタスク 1 つ（リポジトリと操作は宣言にあり、起動できる操作）。
type j2Task struct {
	Key          string
	Title        string
	Body         string
	DoneCriteria string
	NonGoals     []string
	DependsOn    []string
	Repo         string
	Operation    string
	// Source は対応する取り込み元の子の external_key（"" は対応しない）。
	Source string
	// BudgetImplUSD・BudgetReviewUSD はタスクの枠の出力の値そのもの（無ければ nil）。
	BudgetImplUSD   *float64
	BudgetReviewUSD *float64
	// ExpandAfter は続きのタスクを足すかを決めるタスクか（出力の値。無い・null は偽）。
	ExpandAfter bool
	// Flow は選んだ操作の flow（flow の形の委譲先の操作だけ。そうでなければ空）。FlowReason は出力の flow_reason（flow の形のときだけ。空白を除かない）。
	Flow       string
	FlowReason string
}

// j2ValidatedOutput は core の閉集合・必須の組・宣言との突き合わせを通った J2 の
// 出力。plan のときは Size〜Spec が、uncertain のときは Question が、parent_direct のときは
// Question と ParentDirectCriteria が有効である。
type j2ValidatedOutput struct {
	Verdict      J2Verdict
	Summary      string
	Size         JudgmentSize
	DoneCriteria string
	// Topics は plan のときの論点（検査済み。0〜5 件）。
	Topics []j2Topic
	// Tasks は plan のときのタスク（検査済み。1〜12 件）。
	Tasks []j2Task
	// UsesIntegration は plan のときの統合先を使うか（タスクが 2 件以上なら真）。
	UsesIntegration bool
	// BudgetImplUSD・BudgetReviewUSD は出力の値そのもの（null は nil のまま）。
	BudgetImplUSD   *float64
	BudgetReviewUSD *float64
	// ResolvedImplUSD・ResolvedReviewUSD は null をサイズの既定へ解決した額
	// （§J2・決定 M3P22。人は計画の承認でこの額を承認する）。
	ResolvedImplUSD   float64
	ResolvedReviewUSD float64
	// BudgetLimitUSD・BudgetLimitReason は出力の上限と理由（省いたら nil。理由は空白を除いた値で、無い・空白だけは nil）。
	BudgetLimitUSD    *float64
	BudgetLimitReason *string
	CrossRepo         bool
	RelatedRepos      []string
	Question          *string
	// ParentDirectCriteria は parent_direct のときの、親が満たすべき達成条件（検査済み。空白を除いた値）。
	// 理由は Question に入る。
	ParentDirectCriteria string
	// Expected・NonGoals・Figure は plan のときの段階の欄（検査済み。無い・0 個・null は
	// nil。Expected が nil なら課題のいまの値を残す）。
	Expected []StageExpected
	NonGoals []string
	Figure   *string
	// DependsOn は plan のときの依存先の課題 ID（出力のまま。存在・自己参照・循環は
	// 計画を登録する書き込みの中で検査する。無い・null・空は nil）。
	DependsOn []string
	// OpenChildKeys は出力を検査した時点で上流で開いていた取り込み元の子の external_key（記録した順）。
	// 計画の本文の「タスクにしなかった取り込み元の子」に使う。子の検査をしない検査（委譲の再検査）では空。
	OpenChildKeys []string
	// Spec は構造化した出力の JSON を空白を除いて詰めたもの（task_plan.spec。
	// null の額も出力のまま残す＝「spec は構造化した出力と一致する」）。
	Spec []byte
}

// soleTask は計画のタスクがちょうど 1 件のときそのタスクを返す。委譲（見直しの途中のつなぎ）は、
// 1 件の計画だけを起動する。2 件以上の計画は起動しない。
func (v *j2ValidatedOutput) soleTask() (*j2Task, bool) {
	if len(v.Tasks) != 1 {
		return nil, false
	}
	return &v.Tasks[0], true
}

// j2ValidationContext は出力の検査が突き合わせる、宣言と課題の事実。
type j2ValidationContext struct {
	Agent *AgentDeclaration
	Conn  *ConnectorsDeclaration
	// HasSource は課題が取り込み元の対応（source_binding）を持つか。持たない課題で
	// 取り込み元の差し込み（{issue_number}・{issue_url}・{external_key}）を持つ
	// 操作を選んだ出力は不正（§J2）。
	HasSource bool
	// CheckSource が真のとき、タスクの source は OpenChildKeys（上流で開いている子の external_key。
	// 子が無ければ空）のどれかでなければならない。偽のとき source の値の突き合わせはしない
	// （承認済みの計画を委譲の段で読み直すとき。子はその後に閉じうる）。同じ source を 2 つのタスクが
	// 持つ計画は、どちらでも不正。
	CheckSource   bool
	OpenChildKeys []string
	// HasDoneTasks は課題に済み（done）のタスクがあるか。あるとき、統合先を使わない計画は不正（済みのタスクは統合先へ入っている）。
	HasDoneTasks bool
	// DoneRepos は課題の済みのタスクのリポジトリ（重複なし）。統合先を使う計画のタスクのリポジトリは、これと合わせて 1 つでなければ不正。
	DoneRepos []string
	// KeptBudgets は課題の済み（done）のタスクの枠の実効値（loadTaskBudgets の値のうち済みのもの）。計画の見込みに含まれる
	// 「残る済みのタスク」で、上限の検査が見込みを数えるのに使う（計画の承認で取りやめにならないのは済みのタスクだけ）。
	KeptBudgets []taskBudget
	// CheckLimit が真のとき、上限を見込みと比べる（J2 の出力の確定だけが立てる）。偽のとき（承認済みの計画を委譲の段で読み直すとき）は、
	// 上限の額と理由の形だけを検査する。見込みは承認の後に動きうる（宣言の既定・済みのタスク）うえ、読み直しは済みのタスクの枠を持たない。
	CheckLimit bool
	// SkipFlowReason が真のとき、flow_reason の検査と planned_flow の書き足しをしない（承認済みの計画・保留中の足す依頼の spec を、
	// 委譲の段・回答の段で検査し直すとき。S2 より前に承認した計画は flow_reason を持たず、宣言もその後に変わりうる）。
	SkipFlowReason bool
}

// j2OutputSchema は J2 の `--json-schema` に渡すスキーマを返す（§IF / API
// 「判断点の出力」の J2 の行から機械的に組み立てる。閉集合の値は
// J2VerdictValues・JudgmentSizeValues の定義を直接参照し、手で書き写さない）。
// `uncertain` は question 以外を null または空にしてよいので、必須は verdict だけ
// にする（plan の必須の組は core が検査し直す）。
func j2OutputSchema() []byte {
	// core が文字数を検査する文字列には、検査と同じ定数の上限を型にも書く（型で通って検査で落ちる出力を作らせない）。
	boundedString := func(maxRunes int) map[string]any {
		return map[string]any{"type": "string", "maxLength": maxRunes}
	}
	stageString := boundedString(MaxStageRunes)
	nullableStageString := map[string]any{"type": []string{"string", "null"}, "maxLength": MaxStageRunes}
	verdicts := make([]string, 0, len(J2VerdictValues))
	for _, v := range J2VerdictValues {
		verdicts = append(verdicts, string(v))
	}
	sizes := []any{string(JudgmentSizeS), string(JudgmentSizeM), string(JudgmentSizeL), nil}
	nullableString := map[string]any{"type": []string{"string", "null"}}
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	questionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": stageString,
			"options": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label":   stageString,
						"example": nullableStageString,
					},
					"required":             []string{"label"},
					"additionalProperties": false,
				},
			},
			"recommended": map[string]any{"type": "integer"},
		},
		"required":             []string{"question", "options", "recommended"},
		"additionalProperties": false,
	}
	topicsSchema := map[string]any{
		"type":     "array",
		"maxItems": MaxPlanTopics,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":    stageString,
				"decision": stageString,
				"questions": map[string]any{
					"type":     "array",
					"maxItems": MaxQuestionsPerTopic,
					"items":    questionSchema,
				},
			},
			"required":             []string{"title", "decision", "questions"},
			"additionalProperties": false,
		},
	}
	nullableNumber := map[string]any{"type": []string{"number", "null"}}
	tasksSchema := map[string]any{
		"type":     "array",
		"maxItems": MaxPlanTasks,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":           boundedString(MaxStageRunes),
				"title":         boundedString(MaxStageRunes),
				"body":          boundedString(MaxTaskBodyRunes),
				"done_criteria": boundedString(MaxStageRunes),
				"non_goals":     map[string]any{"type": []string{"array", "null"}, "maxItems": MaxNonGoals, "items": stageString},
				"depends_on":    stringArray,
				"repo":          map[string]any{"type": "string"},
				"operation":     map[string]any{"type": "string"},
				"source":        nullableString,
				"flow_reason":   map[string]any{"type": []string{"string", "null"}, "maxLength": MaxFlowReasonRunes},

				"budget_impl_usd":   nullableNumber,
				"budget_review_usd": nullableNumber,
				"expand_after":      map[string]any{"type": []string{"boolean", "null"}},
			},
			"required":             []string{"key", "title", "body", "done_criteria", "non_goals", "depends_on", "repo", "operation"},
			"additionalProperties": false,
		},
	}
	expectedSchema := map[string]any{
		"type":     []string{"array", "null"},
		"maxItems": MaxExpected,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":   map[string]any{"type": "string", "enum": expectedKindStrings()},
				"scene":  stageString,
				"before": stageString,
				"after":  stageString,
			},
			"required":             []string{"kind", "scene", "before", "after"},
			"additionalProperties": false,
		},
	}
	nonGoalsSchema := map[string]any{
		"type":     []string{"array", "null"},
		"maxItems": MaxNonGoals,
		"items":    stageString,
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":             map[string]any{"type": "string", "enum": verdicts},
			"summary":             nullableString,
			"topics":              topicsSchema,
			"done_criteria":       nullableString,
			"expected":            expectedSchema,
			"non_goals":           nonGoalsSchema,
			"figure":              nullableString,
			"depends_on":          stringArray,
			"tasks":               tasksSchema,
			"uses_integration":    map[string]any{"type": []string{"boolean", "null"}},
			"size":                map[string]any{"type": []string{"string", "null"}, "enum": sizes},
			"budget_impl_usd":     nullableNumber,
			"budget_review_usd":   nullableNumber,
			"budget_limit_usd":    nullableNumber,
			"budget_limit_reason": nullableStageString,
			"cross_repo":          map[string]any{"type": []string{"boolean", "null"}},
			"related_repos":       stringArray,
			"question":            nullableString,
		},
		"required":             []string{"verdict"},
		"additionalProperties": false,
	}
	// map[string]any のキーは常にアルファベット順で出力されるため決定的。schema は
	// 常に Marshal 可能な静的なリテラルなのでエラーは起こりえない。
	b, _ := json.Marshal(schema)
	return b
}

// checkOperationLaunchable は接続ツール connector の操作 op を、取り込み元の有無が hasSource の課題で起動できるかを
// 検査する（できなければ ErrValidation）。J2 の出力の検査と、form: cli の委譲の起動（StartContractDelegation）が
// この同じ関数を通る（J2 で検査した後に宣言が変わっても、検査していない引数で起動しない）。
//   - cli 形態: op.Invocation と commands（start・status・resume・cancel・reflect）の引数が、cliCommandPlaceholders の表のそれぞれの差し込みの閉集合の中の値だけを要する
//   - 取り込み元の無い課題: op.Invocation も cli の commands も、取り込み元の差し込みを要しない
//     （commands.start に {task_file} を書いた cli の宣言は、commands について要してよい）
func checkOperationLaunchable(connector *Connector, op *ConnectorOperation, hasSource bool) error {
	if connector == nil || op == nil {
		return fmt.Errorf("%w: unknown connector or operation", ErrValidation)
	}
	if connector.UsesContractCommands() {
		if err := validateInvocationPlaceholders(op.Invocation); err != nil {
			return fmt.Errorf("%w: operation %q: %s", ErrValidation, op.ID, err.Error())
		}
		if err := validateCLICommandPlaceholders(connector); err != nil {
			return err
		}
	}
	if !hasSource && invocationNeedsSource(op.Invocation) {
		return fmt.Errorf("%w: operation %q needs the source issue but the challenge has none", ErrValidation, op.ID)
	}
	// commands.start に {task_file} を書いた宣言は、取り込み元の無いタスクも起動する（取り込み元の差し込みは空の文字列で渡る）。
	if !hasSource && !startTakesTaskFile(connector) && cliCommandsNeedSource(connector) {
		return fmt.Errorf("%w: connector %q commands need the source issue but the challenge has none", ErrValidation, connector.ID)
	}
	return nil
}

// cliCommandPlaceholders はコマンドごとの差し込みの閉集合（form: cli の commands）。
// start は invocation と同じ 4 つに {task_file}・{task_id}・{uses_integration}、status・cancel は 4 つのうち取り込み元の
// 3 つと {challenge_id}・{run_id}、resume は status の 5 つに {answer}・{choice}・{note}・{approved_ref}、
// reflect は {artifact_ref}・{challenge_id} だけ（取り込み元の差し込みは持たない）。
var cliCommandPlaceholders = map[string]map[string]bool{
	"start": {
		"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true,
		"{task_file}": true, "{task_id}": true, "{uses_integration}": true,
	},
	"status": {
		"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true, "{run_id}": true,
	},
	"cancel": {
		"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true, "{run_id}": true,
	},
	"resume": {
		"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true, "{run_id}": true,
		"{answer}": true, "{choice}": true, "{note}": true, "{approved_ref}": true,
	},
	"reflect": {"{artifact_ref}": true, "{challenge_id}": true},
}

// checkCLICommandPlaceholders は cli 形態の commands の引数が、コマンドごとの差し込みの閉集合の中の値だけを要するか検査する
// （エラーの種類は呼び出し側が付ける。宣言の読み込みと、計画・起動の検査が同じ表を通る）。
func checkCLICommandPlaceholders(c *Connector) error {
	if c.Form == ConnectorFormFlow {
		return nil // form: flow の commands は宣言に無い（起動の時に決まる）
	}
	if c.Commands == nil {
		return fmt.Errorf("connector %q (form cli) has no commands", c.ID)
	}
	for _, g := range []struct {
		name string
		args []string
	}{
		{"start", c.Commands.Start}, {"status", c.Commands.Status},
		{"resume", c.Commands.Resume}, {"cancel", c.Commands.Cancel}, {"reflect", c.Commands.Reflect},
	} {
		for _, a := range g.args {
			for _, m := range invocationPlaceholderPattern.FindAllString(a, -1) {
				if !cliCommandPlaceholders[g.name][m] {
					return fmt.Errorf("connector %q commands.%s has a disallowed placeholder %q", c.ID, g.name, m)
				}
			}
		}
	}
	return nil
}

// validateCLICommandPlaceholders は checkCLICommandPlaceholders の結果を ErrValidation で返す（計画の登録と起動の直前）。
func validateCLICommandPlaceholders(c *Connector) error {
	if err := checkCLICommandPlaceholders(c); err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}
	return nil
}

// cliCommandsNeedSource は cli 形態の接続ツールの commands（start・status・resume・cancel）のどれかが、
// 取り込み元の差し込みを要するか。4 つとも実際の起動に使うので、取り込み元の無い課題ではどれも要してはならない。
func cliCommandsNeedSource(c *Connector) bool {
	if !c.UsesContractCommands() || c.Commands == nil {
		return false
	}
	for _, args := range [][]string{c.Commands.Start, c.Commands.Status, c.Commands.Resume, c.Commands.Cancel} {
		for _, a := range args {
			if invocationNeedsSource(a) {
				return true
			}
		}
	}
	return false
}

// invocationNeedsSource は操作の invocation が取り込み元の差し込みを持つかを返す
// （{challenge_id} は取り込み元の差し込みではない）。
func invocationNeedsSource(invocation string) bool {
	for _, ph := range []string{"{issue_number}", "{issue_url}", "{external_key}"} {
		if strings.Contains(invocation, ph) {
			return true
		}
	}
	return false
}

// findConnectorRepo は name の宣言のリポジトリと、そのリポジトリの接続ツールを返す。
func (d *ConnectorsDeclaration) findConnectorRepo(name string) (*ConnectorRepo, *Connector) {
	for i := range d.Repos {
		if d.Repos[i].Name != name {
			continue
		}
		for j := range d.Connectors {
			if d.Connectors[j].ID == d.Repos[i].Connector {
				return &d.Repos[i], &d.Connectors[j]
			}
		}
		return &d.Repos[i], nil
	}
	return nil, nil
}

// validateJ2Output は data（run の StructuredOutput）を検査する
// （§結果の判別「core が閉集合と必須の組…を検査し直す」・§J2）。次のいずれかで
// ok=false（run の結果は invalid_output になる）:
//   - JSON として解釈できない・判定が plan|uncertain|parent_direct の外
//   - uncertain で問いが無い・空
//   - plan で: 統合先を使うかが無い／サイズが S・M・L の外または無い／達成条件が空／
//     複数リポジトリにまたがるかが無い／額が 0 以下／実装枠（null はサイズの既定へ解決した額）が
//     max_run_budget_usd を超える／関係するリポジトリが宣言に無い／
//     タスクが 1〜12 件でない・key が空か重なる・題名・何をするか・完了条件が空・
//     前提が自分自身・計画に無い key・循環／タスクの対象リポジトリが宣言に無い・
//     そのリポジトリの接続ツールに操作が無い・起動できない操作（取り込み元の無い課題で
//     取り込み元の差し込みを要する操作を含む）／タスクが 2 件以上で統合先を使わない／
//     論点が 6 件以上・問いが 1 つの論点に 4 件以上・選択肢が 2〜5 個でない・推奨が選択肢の外
func validateJ2Output(data []byte, vc j2ValidationContext) (*j2ValidatedOutput, bool) {
	v, reason := validateJ2OutputReason(data, vc)
	return v, reason == ""
}

// j2ReasonBadShape は、出力が受け口の形に読めなかったときの理由（JSON の誤りの文は、出力の中身を含みうるので写さない）。
const j2ReasonBadShape = "output does not match the expected shape"

// validationReason は検査の誤り err を、実行の記録に残す理由の文にする（ErrValidation の前置きを外し、理由の形をそろえる）。
func validationReason(err error) string {
	return strings.TrimPrefix(err.Error(), ErrValidation.Error()+": ")
}

// validateJ2OutputReason は validateJ2Output の本体。検査を通れば理由は空、落ちれば、どの検査で落ちたかの短い文を返す
// （実行の記録の誤りの欄に残す。判断点の出力の中身は写さない）。
func validateJ2OutputReason(data []byte, vc j2ValidationContext) (*j2ValidatedOutput, string) {
	var raw j2RawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, j2ReasonBadShape
	}

	switch J2Verdict(raw.Verdict) {
	case J2VerdictUncertain:
		if raw.Question == nil || strings.TrimSpace(*raw.Question) == "" {
			return nil, "uncertain requires a non-blank question"
		}
		// depends_on は型の検証だけ（上の Unmarshal）。uncertain では依存を書かない。
		return &j2ValidatedOutput{Verdict: J2VerdictUncertain, Question: raw.Question}, ""
	case J2VerdictParentDirect:
		// 理由（question）と親の達成条件（done_criteria）の両方が必須。どちらかが空・欠けなら不正。
		if raw.Question == nil || strings.TrimSpace(*raw.Question) == "" ||
			raw.DoneCriteria == nil || strings.TrimSpace(*raw.DoneCriteria) == "" {
			return nil, "parent_direct requires a non-blank question and done_criteria"
		}
		reason := strings.TrimSpace(*raw.Question)
		return &j2ValidatedOutput{
			Verdict:              J2VerdictParentDirect,
			Question:             &reason,
			ParentDirectCriteria: strings.TrimSpace(*raw.DoneCriteria),
		}, ""
	case J2VerdictPlan:
	default:
		return nil, "verdict is not one of the allowed values"
	}

	// cross_repo は意思決定の主体の判定（S2）が読む安全に関わる項目、uses_integration は
	// 統合先を使うかの決定なので、無い出力を既定値と読まず、不正とする。
	if raw.Size == nil || raw.DoneCriteria == nil || raw.CrossRepo == nil || raw.UsesIntegration == nil {
		return nil, "plan requires size, done_criteria, cross_repo and uses_integration"
	}
	tasks, reason := validateJ2Tasks(raw.Tasks, vc)
	if reason != "" {
		return nil, reason
	}
	// 2 件以上のタスクは統合先へ入れる（1 件ならどちらでもよい）。
	if len(tasks) >= 2 && !*raw.UsesIntegration {
		return nil, "a plan with 2 or more tasks must set uses_integration"
	}
	// 済みのタスクがあれば、タスクの件数に関わらず統合先を使う（済みのタスクの成果は統合先に集まっている）。
	if vc.HasDoneTasks && !*raw.UsesIntegration {
		return nil, "a plan that keeps done tasks must set uses_integration"
	}
	// 統合先を使う計画のタスクは、リポジトリが 1 つ（済みのタスクがあれば、済みのタスクのリポジトリと同じ）。
	// 上げる依頼が、済みのタスクのリポジトリが 1 つのときだけ始まるのと同じ基準。
	if *raw.UsesIntegration {
		repos := map[string]bool{}
		for _, r := range vc.DoneRepos {
			repos[r] = true
		}
		for _, t := range tasks {
			repos[t.Repo] = true
		}
		if len(repos) > 1 {
			return nil, "a plan that uses integration must target one repo"
		}
	}
	// expand_after は統合先を使う計画のタスクだけが真にできる（続きのタスクは統合先へ入る）。
	if !*raw.UsesIntegration {
		for _, t := range tasks {
			if t.ExpandAfter {
				return nil, "expand_after requires uses_integration"
			}
		}
	}
	topics, reason := validateJ2Topics(raw.Topics)
	if reason != "" {
		return nil, reason
	}

	size := JudgmentSize(*raw.Size)
	pair, ok := vc.Agent.SizeBudgetFor(size)
	if !ok {
		return nil, "size has no budget in the agent declaration"
	}
	if strings.TrimSpace(*raw.DoneCriteria) == "" {
		return nil, "done_criteria must not be blank"
	}

	impl, review := pair.Impl, pair.Review
	if raw.BudgetImplUSD != nil {
		if !validBudgetAmount(*raw.BudgetImplUSD) {
			return nil, "budget_impl_usd is not a valid amount"
		}
		impl = *raw.BudgetImplUSD
	}
	if raw.BudgetReviewUSD != nil {
		if !validBudgetAmount(*raw.BudgetReviewUSD) {
			return nil, "budget_review_usd is not a valid amount"
		}
		review = *raw.BudgetReviewUSD
	}
	// タスクが全部枠を持つ計画では、計画の値（規模の既定）は読まない。1 回の実行の上限額との比べはタスクの枠だけにする。
	if !allTasksHaveBudgets(tasks) && impl > vc.Agent.MaxRunBudgetUSD {
		return nil, "budget_impl_usd exceeds the per-run limit"
	}

	limitUSD, limitReason, reason := validateJ2BudgetLimit(raw, tasks, impl, review, vc)
	if reason != "" {
		return nil, reason
	}

	for _, r := range raw.RelatedRepos {
		if related, _ := vc.Conn.findConnectorRepo(r); related == nil {
			return nil, "related_repos names a repo that is not declared"
		}
	}

	expected, err := NormalizeExpected(raw.Expected)
	if err != nil {
		return nil, validationReason(err)
	}
	nonGoals, err := NormalizeNonGoals(raw.NonGoals)
	if err != nil {
		return nil, validationReason(err)
	}
	// figure は空文字・空白だけを不正とする（消すときは null）。
	if raw.Figure != nil {
		if err := CheckFigure(*raw.Figure); err != nil {
			return nil, validationReason(err)
		}
	}

	var spec bytes.Buffer
	if err := json.Compact(&spec, data); err != nil {
		return nil, "output is not valid JSON"
	}

	v := &j2ValidatedOutput{
		Verdict:           J2VerdictPlan,
		Size:              size,
		DoneCriteria:      *raw.DoneCriteria,
		Topics:            topics,
		Tasks:             tasks,
		UsesIntegration:   *raw.UsesIntegration,
		BudgetImplUSD:     raw.BudgetImplUSD,
		BudgetReviewUSD:   raw.BudgetReviewUSD,
		ResolvedImplUSD:   impl,
		ResolvedReviewUSD: review,
		BudgetLimitUSD:    limitUSD,
		BudgetLimitReason: limitReason,
		RelatedRepos:      raw.RelatedRepos,
		Expected:          expected,
		NonGoals:          nonGoals,
		Figure:            raw.Figure,
		DependsOn:         raw.DependsOn,
		Question:          raw.Question,
		Spec:              spec.Bytes(),
	}
	if !vc.SkipFlowReason {
		v.Spec = addPlannedFlow(v.Spec, tasks)
	}
	if vc.CheckSource {
		v.OpenChildKeys = vc.OpenChildKeys
	}
	if raw.Summary != nil {
		v.Summary = *raw.Summary
	}
	v.CrossRepo = *raw.CrossRepo
	return v, ""
}

// validateJ2Tasks は plan のタスクを検査する（1〜12 件・key・必須の文字列・前提・対象リポジトリと操作）。
// 対象リポジトリと操作には、見直し前に根の repo・operation へ掛けていた検査（findConnectorRepo と
// checkOperationLaunchable）をタスクごとに掛ける。取り込み元の有無は課題の単位（vc.HasSource）で見る。
func validateJ2Tasks(in []j2RawTask, vc j2ValidationContext) ([]j2Task, string) {
	if len(in) < MinPlanTasks || len(in) > MaxPlanTasks {
		return nil, fmt.Sprintf("tasks must have %d-%d items", MinPlanTasks, MaxPlanTasks)
	}
	byKey := make(map[string]int, len(in))
	inputs := make([]TaskInput, len(in))
	openChild := make(map[string]bool, len(vc.OpenChildKeys))
	for _, k := range vc.OpenChildKeys {
		openChild[k] = true
	}
	usedSource := map[string]bool{}
	for i, t := range in {
		// source は、あれば上流で開いている子の記録にある値で、タスクをまたいで重ならない。
		if t.Source != "" {
			if strings.TrimSpace(t.Source) != t.Source || usedSource[t.Source] || (vc.CheckSource && !openChild[t.Source]) {
				return nil, fmt.Sprintf("tasks[%d].source has surrounding whitespace, is used twice, or is not an open child", i)
			}
			usedSource[t.Source] = true
		}
		if err := checkJ2TaskTexts(i, t); err != nil {
			return nil, validationReason(err)
		}
		if _, dup := byKey[t.Key]; dup {
			return nil, fmt.Sprintf("tasks[%d].key is used twice", i)
		}
		byKey[t.Key] = i
		inputs[i] = TaskInput{Key: t.Key, DependsOn: t.DependsOn}
	}
	for i, t := range in {
		for _, k := range t.DependsOn {
			if _, ok := byKey[k]; !ok || k == t.Key {
				return nil, fmt.Sprintf("tasks[%d].depends_on names itself or a key that is not in the plan", i)
			}
		}
	}
	if hasDependencyCycle(inputs, byKey) {
		return nil, "tasks.depends_on has a cycle"
	}
	out := make([]j2Task, 0, len(in))
	for i, t := range in {
		jt, reason := validateJ2TaskFields(t, vc, len(in) >= 2)
		if reason != "" {
			return nil, fmt.Sprintf("tasks[%d]: %s", i, reason)
		}
		out = append(out, jt)
	}
	return out, ""
}

// checkJ2TaskTexts は、位置 i のタスクの鍵・題名・本文・達成条件が、空白だけでなく上限以内かを検査する（計画のタスクと、足すタスクで共通）。
// 本文だけ上限が別（MaxTaskBodyRunes）。
func checkJ2TaskTexts(i int, t j2RawTask) error {
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"key", t.Key, MaxStageRunes},
		{"title", t.Title, MaxStageRunes},
		{"body", t.Body, MaxTaskBodyRunes},
		{"done_criteria", t.DoneCriteria, MaxStageRunes},
	} {
		if err := checkText(fmt.Sprintf("tasks[%d].%s", i, f.name), f.value, f.max); err != nil {
			return err
		}
	}
	return nil
}

// validateJ2TaskFields はタスク 1 件の、対象リポジトリ・操作・取りやめでない文字列・枠の検査を行う（計画のタスクと、足すタスクで共通）。
// requireBudgets が真のとき、実装の枠とレビュー対応の枠の両方を必ず書く。
func validateJ2TaskFields(t j2RawTask, vc j2ValidationContext, requireBudgets bool) (j2Task, string) {
	repo, connector := vc.Conn.findConnectorRepo(t.Repo)
	if repo == nil || connector == nil {
		return j2Task{}, "repo is not declared"
	}
	var op *ConnectorOperation
	for i := range connector.Operations {
		if connector.Operations[i].ID == t.Operation {
			op = &connector.Operations[i]
			break
		}
	}
	if op == nil {
		return j2Task{}, "operation is not declared for the repo"
	}
	// 起動できる操作だけを選べる。委譲の起動（StartContractDelegation）も同じ関数で検査する。
	if checkOperationLaunchable(connector, op, vc.HasSource) != nil {
		return j2Task{}, "operation cannot be launched for this challenge"
	}
	flow, flowReason, reason := "", "", ""
	if !vc.SkipFlowReason {
		flow, flowReason, reason = checkJ2FlowReason(connector, op, t.FlowReason)
	} else if connector.Form == ConnectorFormFlow {
		// 検査し直しでは理由を求めないが、タスクの記録へ写す値は落とさない（保存済みの出力の理由と、いまの宣言の操作の flow）。
		flow = op.Flow
		if t.FlowReason != nil {
			flowReason = *t.FlowReason
		}
	}
	if reason != "" {
		return j2Task{}, reason
	}
	nonGoals, err := NormalizeNonGoals(t.NonGoals)
	if err != nil {
		return j2Task{}, validationReason(err)
	}
	// タスクの枠は、書いたら正で、実装の枠は 1 回の実行の上限額以下。タスクが 2 個以上の計画では、両方を必ず書く。
	for _, b := range []*float64{t.BudgetImplUSD, t.BudgetReviewUSD} {
		if b != nil && !validBudgetAmount(*b) {
			return j2Task{}, "task budget is not a valid amount"
		}
	}
	if t.BudgetImplUSD != nil && vc.Agent != nil && *t.BudgetImplUSD > vc.Agent.MaxRunBudgetUSD {
		return j2Task{}, "budget_impl_usd exceeds the per-run limit"
	}
	if requireBudgets && (t.BudgetImplUSD == nil || t.BudgetReviewUSD == nil) {
		return j2Task{}, "budget_impl_usd and budget_review_usd are required"
	}
	return j2Task{
		Key: t.Key, Title: t.Title, Body: t.Body, DoneCriteria: t.DoneCriteria,
		NonGoals: nonGoals, DependsOn: t.DependsOn, Repo: t.Repo, Operation: t.Operation, Source: t.Source,
		BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD,
		ExpandAfter: t.ExpandAfter != nil && *t.ExpandAfter,
		Flow:        flow, FlowReason: flowReason,
	}, ""
}

// MaxFlowReasonRunes は flow_reason の文字数の上限（2000 文字は通し、2001 文字は不正）。
const MaxFlowReasonRunes = 2000

// flowDisplayNames は flow の識別子の表示名の表（判断点の入力と計画の本文で使う）。表に無い識別子は識別子だけで出す。
var flowDisplayNames = map[string]string{
	"ticket":       "標準の実装",
	"ticket-light": "簡略な実装",
	"reflect":      "統合ブランチを上げる",
}

// flowLabel は「<名前>（<識別子>）」（表に無い識別子は識別子だけ）。
func flowLabel(flow string) string {
	if name, ok := flowDisplayNames[flow]; ok {
		return name + "（" + flow + "）"
	}
	return flow
}

// checkJ2FlowReason は選んだ操作 op の flow_reason を検査する。flow の形の委譲先の操作では、あり・空白だけでなく・2000 文字以内が必須。
// そうでない操作では、空でない値が不正（無い・null・空の文字列は通る）。flow の形のとき、操作の flow と出力の理由を返す。
func checkJ2FlowReason(connector *Connector, op *ConnectorOperation, reason *string) (string, string, string) {
	if connector.Form != ConnectorFormFlow {
		if reason != nil && *reason != "" {
			return "", "", "flow_reason must be empty unless the operation is a flow"
		}
		return "", "", ""
	}
	if reason == nil || strings.TrimSpace(*reason) == "" {
		return "", "", "flow_reason is required for a flow operation and must not be blank"
	}
	if utf8.RuneCountInString(*reason) > MaxFlowReasonRunes {
		return "", "", fmt.Sprintf("flow_reason must be at most %d characters", MaxFlowReasonRunes)
	}
	return op.Flow, *reason, ""
}

// addPlannedFlow は検査済みの出力の JSON（空白を除いた spec）の tasks[] の、flow の形の操作を選んだ要素へ planned_flow
// （そのときの宣言の操作の flow）を足す。ほかの欄と並びは変えない。
func addPlannedFlow(spec []byte, tasks []j2Task) []byte {
	var top struct {
		Tasks json.RawMessage `json:"tasks"`
	}
	var elems []json.RawMessage
	if json.Unmarshal(spec, &top) != nil || json.Unmarshal(top.Tasks, &elems) != nil || len(elems) != len(tasks) {
		return spec
	}
	changed := false
	for i, e := range elems {
		if tasks[i].Flow == "" || len(e) < 2 || e[len(e)-1] != '}' {
			continue
		}
		pf, _ := json.Marshal(tasks[i].Flow)
		buf := append(append([]byte{}, e[:len(e)-1]...), []byte(`,"planned_flow":`)...)
		elems[i] = append(append(buf, pf...), '}')
		changed = true
	}
	if !changed {
		return spec
	}
	// json.Marshal は RawMessage の < > & を \u 表記へ換えるので使わず、要素をそのまま , でつなぐ。
	newTasks := []byte("[")
	for i, e := range elems {
		if i > 0 {
			newTasks = append(newTasks, ',')
		}
		newTasks = append(newTasks, e...)
	}
	newTasks = append(newTasks, ']')
	// tasks の値は空白を除いた spec の中に 1 度だけ、そのままの並びで現れる（値の中の引用符は \" に換わるので文字列とは重ならない）。
	var compactOld bytes.Buffer
	if json.Compact(&compactOld, top.Tasks) != nil {
		return spec
	}
	return bytes.Replace(spec, compactOld.Bytes(), newTasks, 1)
}

// validateJ2BudgetLimit は計画の上限（budget_limit_usd）と理由（budget_limit_reason）を検査する。省いた上限は nil（見込みと同じ額）。
// 上限は、見込み以上でなければならず、見込みより大きいときは空白だけでない理由が要る。見込みは、承認の後の課題の見込みと同じ関数
// （planVersionBudgets・sumTaskBudgets）で数える: この計画のタスクの枠（無い枠は計画の額 impl・review で埋める）に、
// 残る済みのタスクの枠（vc.KeptBudgets）を足した合計。見込みを決められないときは見込みとの比べをしない。
// 返す理由は空白を除いた値（上限が無い・理由が空白だけなら nil）。
func validateJ2BudgetLimit(raw j2RawOutput, tasks []j2Task, impl, review float64, vc j2ValidationContext) (*float64, *string, string) {
	if raw.BudgetLimitUSD == nil {
		return nil, nil, ""
	}
	if !validBudgetAmount(*raw.BudgetLimitUSD) {
		return nil, nil, "budget_limit_usd is not a valid amount"
	}
	var reason *string
	if raw.BudgetLimitReason != nil {
		if r := strings.TrimSpace(*raw.BudgetLimitReason); r != "" {
			if err := checkText("budget_limit_reason", r, MaxStageRunes); err != nil {
				return nil, nil, validationReason(err)
			}
			reason = &r
		}
	}
	if !vc.CheckLimit {
		return raw.BudgetLimitUSD, reason, ""
	}
	frames := make([]planTaskFrame, 0, len(tasks))
	for _, t := range tasks {
		frames = append(frames, planTaskFrame{Impl: taskBudgetMicros(t.BudgetImplUSD), Review: taskBudgetMicros(t.BudgetReviewUSD)})
	}
	// 計画の版はまだ無いので、どの行にも当たらない 0 を渡す（済みのタスクは版が違うものとして残り、計画のタスクが数えられる）。
	def := planBudgets{Impl: usdToMicros(impl), Review: usdToMicros(review)}
	if est, known := sumTaskBudgets(planVersionBudgets(0, frames, &def, vc.KeptBudgets)); known {
		limit := usdToMicros(*raw.BudgetLimitUSD)
		total := est.Impl + est.Review
		if limit < total {
			return nil, nil, "budget_limit_usd is below the estimate"
		}
		if limit > total && reason == nil {
			return nil, nil, "budget_limit_usd above the estimate requires budget_limit_reason"
		}
	}
	return raw.BudgetLimitUSD, reason, ""
}

// validateJ2Topics は plan の論点を検査する（0〜5 件。題名・どう決めたかは空にできない。問いは
// 論点ごとに 0〜3 件で、問いの文・選択肢 2〜5 個・推奨が選択肢の位置を指す）。
func validateJ2Topics(in []j2RawTopic) ([]j2Topic, string) {
	if len(in) > MaxPlanTopics {
		return nil, fmt.Sprintf("topics must have at most %d items", MaxPlanTopics)
	}
	out := make([]j2Topic, 0, len(in))
	for i, t := range in {
		if err := checkText(fmt.Sprintf("topics[%d].title", i), t.Title, MaxStageRunes); err != nil {
			return nil, validationReason(err)
		}
		if err := checkText(fmt.Sprintf("topics[%d].decision", i), t.Decision, MaxStageRunes); err != nil {
			return nil, validationReason(err)
		}
		if len(t.Questions) > MaxQuestionsPerTopic {
			return nil, fmt.Sprintf("topics[%d].questions must have at most %d items", i, MaxQuestionsPerTopic)
		}
		for _, q := range t.Questions {
			if err := checkStageQuestion(q); err != nil {
				return nil, fmt.Sprintf("topics[%d]: %s", i, validationReason(err))
			}
		}
		out = append(out, j2Topic(t))
	}
	return out, ""
}

// --- 計画の本文の整形 ---

func formatUSDAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// UntaskedChildrenHeading は計画の本文で、どのタスクも指さない取り込み元の子を並べる見出し。
const UntaskedChildrenHeading = "タスクにしなかった取り込み元の子"

// untaskedChildKeys は、どのタスクも source で指さない、上流で開いている子の external_key を返す。
func untaskedChildKeys(v *j2ValidatedOutput) []string {
	used := map[string]bool{}
	for _, t := range v.Tasks {
		if t.Source != "" {
			used[t.Source] = true
		}
	}
	var out []string
	for _, k := range v.OpenChildKeys {
		if !used[k] {
			out = append(out, k)
		}
	}
	return out
}

// allTasksHaveBudgets は、タスクが 1 個以上あり、全部が実装の枠・レビュー対応の枠の両方を書いているか。
func allTasksHaveBudgets(tasks []j2Task) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.BudgetImplUSD == nil || t.BudgetReviewUSD == nil {
			return false
		}
	}
	return true
}

// formatJ2PlanBody は検査済みの plan の出力を、計画の本文（Markdown）へ決定的に
// 整形する（§J2「出力を計画の本文へ決定的に整形し」）。サイズ・実装枠・レビュー対応枠の額・
// 統合先を使うか・論点（題名・どう決めたか）・問い（番号・文・選択肢のラベル・推奨の印）・
// タスクのツリー（題名・前提・対象リポジトリ・操作）を必ず含む（人間は計画の承認で、この
// 額とタスクを承認する＝設計書 §9）。時刻・乱数など、出力以外の値は含めない。
func formatJ2PlanBody(v *j2ValidatedOutput) string {
	var b strings.Builder
	b.WriteString("# 計画\n\n")
	// 人が承認する事実（サイズ・予算・統合先）を先頭に置き、出力の自由記述（方針・
	// 論点・完了条件）はその後に置く。自由記述の中に見出しや額を装った文があっても、
	// 承認する事実が先に読めるようにするため。
	b.WriteString("## サイズと予算\n\n")
	b.WriteString("- サイズ: " + string(v.Size) + "\n")
	if allTasksHaveBudgets(v.Tasks) {
		// タスクが枠を持つ計画の枠は、この計画のタスクの枠の合計（計画の値ではない）。本文は登録のときに決まる文なので、済みのタスクの枠は足さない
		// （課題の上限は、承認の要約が別に出す）。
		var impl, review int64
		for _, t := range v.Tasks {
			impl += usdToMicros(*t.BudgetImplUSD)
			review += usdToMicros(*t.BudgetReviewUSD)
		}
		b.WriteString("- 実装枠（この計画のタスクの枠の合計）: " + formatUSDAmount(microsToUSD(impl)) + " USD\n")
		b.WriteString("- レビュー対応枠（この計画のタスクの枠の合計）: " + formatUSDAmount(microsToUSD(review)) + " USD\n")
	} else {
		b.WriteString("- 実装枠: " + formatUSDAmount(v.ResolvedImplUSD) + " USD\n")
		b.WriteString("- レビュー対応枠: " + formatUSDAmount(v.ResolvedReviewUSD) + " USD\n")
	}
	b.WriteString("- 統合先を使う: " + yesNo(v.UsesIntegration) + "\n")
	b.WriteString("- 複数リポジトリにまたがる: " + yesNo(v.CrossRepo) + "\n")
	if len(v.RelatedRepos) > 0 {
		b.WriteString("- 関係するリポジトリ: " + strings.Join(v.RelatedRepos, "、") + "\n")
	}
	b.WriteString("\n## タスク\n\n")
	for i, t := range v.Tasks {
		fmt.Fprintf(&b, "%d. %s\n", i+1, oneLine(t.Title))
		b.WriteString("   - 対象リポジトリ: " + t.Repo + "\n")
		b.WriteString("   - 操作: " + t.Operation + "\n")
		if t.Flow != "" {
			b.WriteString("   - フロー: " + flowLabel(t.Flow) + "\n")
			b.WriteString("   - フローを選んだ理由: " + oneLine(t.FlowReason) + "\n")
		}
		if t.BudgetImplUSD != nil && t.BudgetReviewUSD != nil {
			b.WriteString("   - 枠: 実装 " + formatUSDAmount(*t.BudgetImplUSD) + " USD・レビュー対応 " + formatUSDAmount(*t.BudgetReviewUSD) + " USD\n")
		}
		if t.Source != "" {
			b.WriteString("   - 取り込み元の子: " + oneLine(t.Source) + "\n")
		}
		if len(t.DependsOn) == 0 {
			b.WriteString("   - 前提: なし\n")
			continue
		}
		titles := make([]string, 0, len(t.DependsOn))
		for _, k := range t.DependsOn {
			for _, o := range v.Tasks {
				if o.Key == k {
					titles = append(titles, oneLine(o.Title))
				}
			}
		}
		b.WriteString("   - 前提: " + strings.Join(titles, "、") + "\n")
	}
	if untasked := untaskedChildKeys(v); len(untasked) > 0 {
		b.WriteString("\n## " + UntaskedChildrenHeading + "\n\n")
		for _, k := range untasked {
			b.WriteString("- " + oneLine(k) + "\n")
		}
	}
	if s := strings.TrimSpace(v.Summary); s != "" {
		b.WriteString("\n## 方針\n\n" + s + "\n")
	}
	if len(v.Topics) > 0 {
		b.WriteString("\n## 論点\n")
		n := 0
		for i, t := range v.Topics {
			fmt.Fprintf(&b, "\n### %d. %s\n\n", i+1, oneLine(t.Title))
			b.WriteString("決めたこと: " + oneLine(t.Decision) + "\n")
			for _, q := range t.Questions {
				n++
				fmt.Fprintf(&b, "\nQ%d. %s\n", n, oneLine(q.Question))
				for j, o := range q.Options {
					fmt.Fprintf(&b, "  %d) %s", j+1, oneLine(o.Label))
					if o.Example != nil {
						b.WriteString("（例: " + oneLine(*o.Example) + "）")
					}
					if j+1 == q.Recommended {
						b.WriteString("【推奨】")
					}
					b.WriteString("\n")
				}
			}
		}
	}
	b.WriteString("\n## 完了条件\n\n" + strings.TrimSpace(v.DoneCriteria) + "\n")
	return b.String()
}

// --- J2 の対象の選び方 ---

// selectJ2AutoTargets は ID を省略した `plan --auto`・`cycle` の計画の段が対象に
// する課題の内部整数 ID を、優先度→ID の昇順で返す（§判断点の共通の規則）。
// 対象は分類済で、終了していない run を持たず、取り込み元の食い違いで外れない
// （challengeAutoExclusion）課題。食い違いで外した課題は excluded に理由つきで返す
// （not_started に出す。#440）。
func selectJ2AutoTargets(ctx context.Context, tx *sql.Tx) (out []int64, excluded []NotStarted, err error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusClassified)
	if err != nil {
		return nil, nil, err
	}
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		active, err := challengeHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if active {
			continue
		}
		ex, err := challengeAutoExclusion(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if ex != nil {
			excluded = append(excluded, *ex)
			continue
		}
		out = append(out, cid)
	}
	return out, excluded, nil
}

// j2StillEligibleForAuto は、対象選び（selectJ2AutoTargets）から実際に起動する
// までの間に、上流・ポリシーの状態が変わっていないかを起動の直前に読み直して
// 確かめる（`plan --auto` はサイクルの排他ロックを取らないため、並行する
// 取り込みの進行と時間差が生じうる。J1 の j1StillEligibleForAuto と同じ理由）。
// 終了していない run の有無は RunJudgment の①が最終防衛として検査する。
// 外れたときは、その理由（not_started の 1 要素）を返す。nil なら起動してよい。
func (s *Store) j2StillEligibleForAuto(ctx context.Context, cid int64) (*NotStarted, error) {
	var excluded *NotStarted
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ex, err := challengeAutoExclusion(ctx, tx, cid)
		excluded = ex
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return excluded, nil
}

// --- J2 の入力の組み立て ---

// J2 の入力の区画の名前。
const (
	labelChallenge  = "課題"
	labelHoldRecord = "保留の記録"
)

// j2Prepared は J2 の 1 回の起動の準備（入力の区画と、写像で使う事実）。
type j2Prepared struct {
	Sections []JudgmentDataSection
	// HasSource は課題が取り込み元の対応を持つか（出力の検査に使う）。
	HasSource bool
	// OpenChildKeys は課題の、上流で開いている取り込み元の子の external_key（記録した順。出力の検査に使う）。
	OpenChildKeys []string
	// HasDoneTasks は課題に済み（done）のタスクがあるか（出力の検査に使う）。
	HasDoneTasks bool
	// DoneRepos は課題の済みのタスクのリポジトリ（重複なし。出力の検査に使う）。
	DoneRepos []string
	// KeptBudgets は課題の済みのタスクの枠の実効値（上限の検査が見込みを数えるのに使う）。
	KeptBudgets []taskBudget
	// Read が非 nil のときだけ、計画を登録した後に読んだ記録を付けてよい
	// （取り込み元の対応があり、最新のコメントをすべて入力へ含めた場合）。値は
	// J2 の直前の取得で得たコメント数と更新日時（ストアの観測値ではない）。
	Read *readValues
}

// parseExternalKey は "<owner>/<name>#<番号>" を repo と番号へ分ける。
func parseExternalKey(key string) (repo string, number int, ok bool) {
	i := strings.LastIndex(key, "#")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return key[:i], n, true
}

// prepareJ2 は ch の J2 の入力を組み立てる。仕様の文書の場所を持つ課題では文書の受入基準を読み、読めなければ
// NotStarted（spec_unreadable）を返し、上流の取得もしない。取り込み元の対応のある課題では、
// ここ（J2 の起動の直前）で上流の本文・全コメント・参照先の Issue を取得する
// （対応の無い課題では上流の取得を呼ばない）。取得に失敗したら NotStarted
// （upstream_fetch_failed）を返し、run を作らない＝状態と版を変えない。
func (s *Store) prepareJ2(ctx context.Context, ch Challenge, in J2AutoInput) (*j2Prepared, *NotStarted, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, nil, ErrNotFound
	}

	var holds []Hold
	var sb *sourceBinding
	var children []SourceChild
	var replan *replanNote
	var planRejections []CompletionRejection
	var completionRejections []CompletionRejection
	var planAnswersText string
	var doneTasks []Task
	var keptBudgets []taskBudget
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		all, err := listTasks(ctx, tx, cid)
		if err != nil {
			return err
		}
		bs, err := loadTaskBudgets(ctx, tx, cid, in.AgentDecl)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if b.Status == TaskStatusDone {
				keptBudgets = append(keptBudgets, b)
			}
		}
		// 済みのタスクは、統合先を使う課題のものだけを扱う（統合先を使わない課題の動きは変えない）。
		if integ, err := challengeUsesIntegration(ctx, tx, cid); err != nil {
			return err
		} else if integ != nil && *integ {
			for _, t := range all {
				if t.Status == TaskStatusDone {
					doneTasks = append(doneTasks, t)
				}
			}
		}
		pa, err := loadPlanAnswersSection(ctx, tx, cid)
		if err != nil {
			return err
		}
		planAnswersText = pa
		prs, err := loadPlanRejections(ctx, tx, cid)
		if err != nil {
			return err
		}
		planRejections = prs
		if completionRejections, err = loadCompletionRejectionsForReplan(ctx, tx, cid); err != nil {
			return err
		}
		hs, err := loadHolds(ctx, tx, cid)
		if err != nil {
			return err
		}
		holds = hs
		if replan, err = loadReplanNote(ctx, tx, cid); err != nil {
			return err
		}
		// 回答より後に作られた計画がある（作り直した計画を差し戻された）ときは、古い回答を最新の指示として渡さない。
		if replan != nil && replan.PlanCreatedAt > replan.AnsweredAt {
			replan = nil
		}
		b, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		sb = b
		allChildren, err := loadSourceChildren(ctx, tx, cid)
		if err != nil {
			return err
		}
		for _, c := range allChildren {
			if c.UpstreamState == string(upstreamStateOpen) {
				children = append(children, c)
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}

	posContent, err := os.ReadFile(filepath.Join(s.workspace, in.AgentDecl.PositionFile))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read position_file %q: %w", ErrConfigInvalid, in.AgentDecl.PositionFile, err)
	}

	var criteria *planAcceptanceCriteria
	if ch.SpecPath != nil {
		items, err := readSpecCriteria(s.workspace, *ch.SpecPath)
		if err != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedSpecUnreadable, Detail: err.Error()}, nil
		}
		criteria = &planAcceptanceCriteria{Path: *ch.SpecPath, Items: items}
	}

	prepared := &j2Prepared{HasSource: sb != nil, HasDoneTasks: len(doneTasks) > 0, KeptBudgets: keptBudgets}
	seenDoneRepo := map[string]bool{}
	for _, t := range doneTasks {
		if !seenDoneRepo[t.Repo] {
			seenDoneRepo[t.Repo] = true
			prepared.DoneRepos = append(prepared.DoneRepos, t.Repo)
		}
	}

	var upstream *j2UpstreamInput
	if sb != nil {
		repo, number, ok := parseExternalKey(sb.ExternalKey)
		if !ok {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed,
				Detail: fmt.Sprintf("malformed external_key %q", sb.ExternalKey)}, nil
		}
		uc, err := FetchUpstreamContext(ctx, in.Upstream, repo, number)
		if err != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}, nil
		}
		built := buildUpstreamInput(uc)
		upstream = &built
		if built.fullyRead() {
			prepared.Read = &readValues{CommentsCount: built.ObservedCommentsCount, UpstreamUpdatedAt: built.ObservedUpdatedAt}
		}
	}

	urgency := ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	priority := ""
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	issueBody := fmt.Sprintf(
		"タイトル: %s\n\n説明:\n%s\n\n達成条件:\n%s\n\n緊急度: %s\n優先度: %s\n起票者: %s",
		ch.Title, ch.Description, ch.DoneCriteria, urgency, priority, ch.Reporter,
	)

	sections := []JudgmentDataSection{
		{Label: labelChallenge, Content: issueBody},
		{Label: "ポジション定義", Content: string(posContent)},
	}
	if holdsText := formatHoldsSection(holds); holdsText != "" {
		sections = append(sections, JudgmentDataSection{Label: labelHoldRecord, Content: holdsText})
	}
	if t := formatPlanRejectionsSection(planRejections); t != "" {
		sections = append(sections, JudgmentDataSection{Label: PlanRejectionsSectionLabel, Content: t})
	}
	if t := formatCompletionRejectionsForReplanSection(completionRejections); t != "" {
		sections = append(sections, JudgmentDataSection{Label: CompletionRejectionsForReplanSectionLabel, Content: t})
	}
	if planAnswersText != "" {
		sections = append(sections, JudgmentDataSection{Label: PlanAnswersSectionLabel, Content: planAnswersText})
	}
	if replan != nil {
		sections = append(sections, JudgmentDataSection{Label: ReplanSectionLabel, Content: formatReplanSection(replan)})
	}
	if replan != nil && replan.Reason == string(holdReasonChallengeCheckNotMet) {
		sections = append(sections, JudgmentDataSection{Label: ChallengeCheckFindingsSectionLabel, Content: formatChallengeCheckFindingsSection(replan)})
	}
	if t := formatDoneTasksSection(doneTasks); t != "" {
		sections = append(sections, JudgmentDataSection{Label: DoneTasksSectionLabel, Content: t})
	}
	if criteria != nil {
		// 済みの印は、区画「済みのタスク」に出るタスクの受け持ちに付ける。
		sections = append(sections, JudgmentDataSection{Label: AcceptanceCriteriaSectionLabel, Content: formatAcceptanceCriteriaSection(*criteria, doneTasks)})
	}
	if stagesText := formatJ2StagesSection(ch); stagesText != "" {
		sections = append(sections, JudgmentDataSection{Label: "いまの予想・やらないこと・図", Content: stagesText})
	}
	if sourceText := formatSourceBindingSection(sb); sourceText != "" {
		sections = append(sections, JudgmentDataSection{Label: "取り込み元の対応", Content: sourceText})
	}
	if upstream != nil {
		sections = append(sections, upstream.Sections...)
	}
	if len(children) > 0 {
		sections = append(sections, JudgmentDataSection{Label: SourceChildrenSectionLabel, Content: formatSourceChildrenSection(children)})
		for _, c := range children {
			prepared.OpenChildKeys = append(prepared.OpenChildKeys, c.ExternalKey)
		}
	}
	sections = append(sections,
		JudgmentDataSection{Label: "対象リポジトリと操作の宣言", Content: formatJ2DeclarationSection(in.ConnDecl, prepared.HasSource, in.AgentDecl)},
		JudgmentDataSection{Label: "サイズごとの予算の既定", Content: formatJ2BudgetDefaultsSection(in.AgentDecl)},
	)
	sections = append(sections, memoryIndexSections(s.workspace, in.AgentDecl)...)
	prepared.Sections = sections
	return prepared, nil, nil
}

// formatJ2StagesSection は課題のいまの予想・やらないこと・図を整形する（どれも無ければ空）。
func formatJ2StagesSection(ch Challenge) string {
	var b strings.Builder
	if len(ch.Expected) > 0 {
		b.WriteString("予想:\n")
		for i, e := range ch.Expected {
			fmt.Fprintf(&b, "  %d) 種別: %s｜場面: %s｜いま: %s｜完了後: %s\n", i+1, e.Kind, e.Scene, e.Before, e.After)
		}
	}
	if len(ch.NonGoals) > 0 {
		b.WriteString("やらないこと:\n")
		for _, g := range ch.NonGoals {
			b.WriteString("  - " + g + "\n")
		}
	}
	if ch.Figure != nil {
		b.WriteString("図（Mermaid）:\n" + *ch.Figure + "\n")
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "はい"
	}
	return "いいえ"
}

// formatJ2DeclarationSection は J2 が選べる対象リポジトリと操作の一覧
// （§J2「宣言の対象リポジトリと操作の一覧（id・形態・対話前提か・成果物の種類）」）。
// J2 が選んでも不正になる操作には注記を付ける（取り込み元の対応の無い課題では、
// 取り込み元の差し込みを持つ操作、および commands.start がそれを要する cli 形態の操作は選べない）。
func formatJ2DeclarationSection(conn *ConnectorsDeclaration, hasSource bool, agent *AgentDeclaration) string {
	var b strings.Builder
	if hasSource {
		b.WriteString("この課題は取り込み元の GitHub Issue と対応している。\n")
	} else {
		b.WriteString("この課題は取り込み元の GitHub Issue と対応していない。\n")
	}
	fmt.Fprintf(&b, "1 回の実装の枠として選べる額の上限: %s USD\n", formatUSDAmount(agent.MaxRunBudgetUSD))
	for _, r := range conn.Repos {
		b.WriteString("\nリポジトリ: " + r.Name + "（" + r.Remote + "・既定ブランチ: " + r.DefaultBranch + "）\n")
		_, connector := conn.findConnectorRepo(r.Name)
		if connector == nil {
			continue
		}
		b.WriteString("  接続ツール: " + connector.ID + "（形態: " + string(connector.Form) + "）\n")
		b.WriteString("  操作:\n")
		for _, op := range connector.Operations {
			artifacts := op.Artifacts
			if artifacts == "" {
				artifacts = "指定なし"
			}
			line := "    - id: " + op.ID + "｜対話前提: " + yesNo(op.Interactive) + "｜成果物: " + artifacts
			switch {
			case !hasSource && (invocationNeedsSource(op.Invocation) || (!startTakesTaskFile(connector) && cliCommandsNeedSource(connector))):
				line += "｜注記: 取り込み元の Issue の情報を必要とするため、この課題では選べない"
			}
			if connector.Form == ConnectorFormFlow && op.Flow != "" {
				line += "｜フロー: " + flowLabel(op.Flow)
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// formatJ2BudgetDefaultsSection はサイズごとの予算の既定（§J2 の入力）。
func formatJ2BudgetDefaultsSection(agent *AgentDeclaration) string {
	var b strings.Builder
	b.WriteString("サイズごとの既定の額（実装の枠・レビュー対応の枠。USD）。出力で額を null にすると、選んだサイズのこの額になる:\n")
	for _, sz := range JudgmentSizeValues {
		pair, _ := agent.SizeBudgetFor(sz)
		fmt.Fprintf(&b, "  %s: 実装 %s・レビュー対応 %s\n", sz, formatUSDAmount(pair.Impl), formatUSDAmount(pair.Review))
	}
	return b.String()
}

// --- J2 の出力の写像 ---

// errJ2MappingNotEligible は、写す直前に読み直した課題が J2 の遷移元（分類済）で
// なくなっていたことを表す内部の sentinel（§アーキテクチャ決定③「読み直した状態が
// 遷移元でなくなっていれば写さない」）。ErrInvalidTransition・ErrTerminalState と
// 同じく mapped=false（エラーではない）として扱う。
var errJ2MappingNotEligible = errors.New("core: j2 mapping is no longer eligible (challenge is not classified anymore)")

// j2PlanKind は plan の出力を課題のいまの状態に当てた結果の種類。
type j2PlanKind int

const (
	// j2PlanNotEligible は課題が分類済でなくなっている（写さない）。
	j2PlanNotEligible j2PlanKind = iota
	// j2PlanInvalidDependsOn は depends_on に存在しない ID・自己参照・循環がある
	// （計画も依存も書かず、呼び出し元が invalid_output にする）。
	j2PlanInvalidDependsOn
	// j2PlanExpectedEmpty はサイズ M・L なのに予想が空のまま（計画を登録せず保留）。
	j2PlanExpectedEmpty
	// j2PlanRegister は計画を登録する。
	j2PlanRegister
)

// errJ2PlanStale は、写す直前に読み直した課題が事前の読み取りと違う判定になったことを表す。
var errJ2PlanStale = errors.New("core: challenge changed while mapping J2 plan")

// j2PlanMapping は applyJ2Plan の結果。
type j2PlanMapping struct {
	// Mapped は計画の登録か保留のどちらかを書いたとき true。
	Mapped bool
	// Kind が j2PlanInvalidDependsOn なら何も書いていない（呼び出し元が invalid_output にする）。
	Kind j2PlanKind
}

// j2PlanDecide は検査済みの plan の出力を課題 ch の現在の値に当てて、写し方を決める。
// 予想の規則（M4P38）だけを当てる（論点の記録の検査は無い。論点は計画の中に持つ）。
func j2PlanDecide(ch *Challenge, v *j2ValidatedOutput) j2PlanKind {
	if ch.Status != StatusClassified {
		return j2PlanNotEligible
	}
	big := v.Size == JudgmentSizeM || v.Size == JudgmentSizeL
	if big && len(v.Expected) == 0 && len(ch.Expected) == 0 {
		return j2PlanExpectedEmpty
	}
	return j2PlanRegister
}

// j2ExpectedEmptyQuestion は予想を書けなかったときの保留の問い（固定の文＋出力の question）。
func j2ExpectedEmptyQuestion(question *string) string {
	if question != nil && strings.TrimSpace(*question) != "" {
		return ExpectedEmptyHoldPreamble + " " + *question
	}
	return ExpectedEmptyHoldPreamble
}

// applyJ2Plan は判定 plan の出力を課題へ写す（M1 T3。PlanChallenge と同じ遷移を、
// 経路 invoker・本人確認 none・原因の run の ID つきで実行し、計画の本文と spec を
// 1 版として登録する。予想・やらないこと・図・実物も同じトランザクションで書く）。
// T3 と T4 は同じ操作 plan なので、遷移表の照合だけでは「計画承認待ちの計画の改訂〈T4〉」を
// 弾けない。J2 の対象は分類済の課題だけなので、写す直前に読み直した状態を照合する。
// 予想が空のままのサイズ M・L は計画を登録せず保留（T11）にする。操作は事前の読み取りで
// 選び、書き込みの中で読み直した判定がずれたら（間に edit が入った）書かずに選び直す。
func (s *Store) applyJ2Plan(ctx context.Context, runIDDisplay, challengeIDDisplay, body string, v *j2ValidatedOutput) (j2PlanMapping, error) {
	for attempt := 0; attempt < 3; attempt++ {
		m, err := s.applyJ2PlanOnce(ctx, runIDDisplay, challengeIDDisplay, body, v)
		if !errors.Is(err, errJ2PlanStale) {
			return m, err
		}
	}
	return j2PlanMapping{}, ErrConflict
}

func (s *Store) applyJ2PlanOnce(ctx context.Context, runIDDisplay, challengeIDDisplay, body string, v *j2ValidatedOutput) (j2PlanMapping, error) {
	cid, ok := parseChallengeID(challengeIDDisplay)
	if !ok {
		return j2PlanMapping{}, ErrNotFound
	}
	var pre *Challenge
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		pre = ch
		return err
	}))
	if err != nil {
		return j2PlanMapping{}, err
	}
	kind := j2PlanDecide(pre, v)
	if kind == j2PlanNotEligible {
		return j2PlanMapping{Kind: kind}, nil
	}

	op := OpPlan
	if kind == j2PlanExpectedEmpty {
		op = OpHold
	}
	specText := string(v.Spec)
	m := j2PlanMapping{Kind: kind}
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, op, func(ctx context.Context, tc *transitionCtx) error {
		if j2PlanDecide(tc.current, v) != kind {
			return errJ2PlanStale
		}
		if kind == j2PlanExpectedEmpty {
			if err := insertHold(ctx, tc, j2ExpectedEmptyQuestion(v.Question)); err != nil {
				return err
			}
			m.Mapped = true
			return nil
		}
		if _, err := insertTaskPlanVersion(ctx, tc, body, &specText); err != nil {
			return err
		}
		if err := addJ2Dependencies(ctx, tc, v.DependsOn); err != nil {
			return err
		}
		patch := StagePatch{
			NonGoals: StageSet(v.NonGoals),
			Figure:   StageSet(v.Figure),
			Actual:   StageSet[[]StageActual](nil),
		}
		if len(v.Expected) > 0 {
			patch.Expected = StageSet(v.Expected)
		}
		tc.patchStages(patch)
		m.Mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errJ2InvalidDependsOn) {
			return j2PlanMapping{Kind: j2PlanInvalidDependsOn}, nil
		}
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
			return j2PlanMapping{Kind: j2PlanNotEligible}, nil
		}
		return j2PlanMapping{}, err
	}
	return m, nil
}

// errJ2InvalidDependsOn は depends_on の検査に落ちたことを表す内部の sentinel
// （書き込みのトランザクションを巻き戻し、run を invalid_output にする）。
var errJ2InvalidDependsOn = errors.New("core: invalid depends_on in J2 output")

// addJ2Dependencies は計画を登録する遷移のトランザクションの中で、depends_on を
// AddDependencies と同じ規則（自己参照→存在→循環。循環は課題の状態によらずすべての辺）で
// 検査し、通れば依存を足して作業ログに entity=challenge・action=dep_add を残す。
// 課題の版は遷移の 1 回だけ上がる（ここでは上げない）。既にある依存は足さない。
func addJ2Dependencies(ctx context.Context, tc *transitionCtx, dependsOn []string) error {
	if len(dependsOn) == 0 {
		return nil
	}
	_, targets, err := parseDependencyTargets(formatChallengeID(tc.id), dependsOn)
	if err != nil {
		return errJ2InvalidDependsOn
	}
	if err := dependencyTargetsExist(ctx, tc.tx, targets); err != nil {
		return j2DependsOnErr(err)
	}
	changed, err := planDependencyChange(ctx, tc.tx, tc.id, targets, true)
	if err != nil {
		return j2DependsOnErr(err)
	}
	if len(changed) == 0 {
		return nil
	}
	ids, err := writeDependencyEdges(ctx, tc.tx, tc.id, changed, true, tc.nowStr)
	if err != nil {
		return err
	}
	return tc.rec.record("challenge", tc.id, ActivityActionDepAdd, nil, map[string]any{"depends_on": ids, "version": tc.current.Version + 1})
}

// j2DependsOnErr は検査の拒否（存在しない・循環）を errJ2InvalidDependsOn に写し、それ以外はそのまま返す。
func j2DependsOnErr(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDependencyCycle) {
		return errJ2InvalidDependsOn
	}
	return err
}

// applyJ2Uncertain は判定 uncertain の出力を課題へ写す（M1 T11）。J2 の対象は
// 分類済の課題だけなので、写す直前に読み直した状態を分類済と照合する（J1 の
// applyJ1Uncertain と同じ理由）。
func (s *Store) applyJ2Uncertain(ctx context.Context, runIDDisplay, challengeIDDisplay, question string) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusClassified {
			return errJ2MappingNotEligible
		}
		if err := insertHold(ctx, tc, question); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errJ2MappingNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// applyJ2ParentDirect は判定 parent_direct の出力を課題へ写す（T11）。種類が親が直接行う
// （parent_direct）の保留を、問いを理由として立て、課題の達成条件の後ろへ見出しを付けて親の
// 達成条件を書き足す（元の達成条件は残す。appendParentDirectCriteria。保留・達成条件・遷移・
// 作業ログを同じトランザクションで書く）。写す直前に読み直した状態が
// 分類済でなければ写さない。保留を解くと保留の前の状態（分類済）へ戻り、次の周で J2 が計画し直す。
func (s *Store) applyJ2ParentDirect(ctx context.Context, runIDDisplay, challengeIDDisplay, reason, doneCriteria string) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusClassified {
			return errJ2MappingNotEligible
		}
		if _, err := insertHoldKind(ctx, tc, reason, HoldKindParentDirect); err != nil {
			return err
		}
		if appended := appendParentDirectCriteria(tc.current.DoneCriteria, doneCriteria); appended != tc.current.DoneCriteria {
			if _, err := tc.tx.ExecContext(ctx, `UPDATE challenge SET done_criteria = ? WHERE id = ?`, appended, tc.id); err != nil {
				return err
			}
			tc.before["done_criteria"] = tc.current.DoneCriteria
			tc.after["done_criteria"] = appended
		}
		mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errJ2MappingNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// ParentDirectCriteriaHeading は、J2 が parent_direct を返したときに課題の達成条件の
// 後ろへ親の達成条件を書き足す区画の見出し。
const ParentDirectCriteriaHeading = "## 親が直接行うときの達成条件"

// appendParentDirectCriteria は、元の達成条件 original の後ろへ見出しを付けて親の達成条件
// parent を書き足した全文を返す（元の達成条件は残す）。original が空なら見出しと parent だけに
// する。同じ見出しと同じ本文がすでに末尾にあれば二重に書き足さず original をそのまま返す。
func appendParentDirectCriteria(original, parent string) string {
	section := ParentDirectCriteriaHeading + "\n\n" + strings.TrimSpace(parent)
	trimmed := strings.TrimRight(original, " \t\r\n")
	if trimmed == "" {
		return section
	}
	if strings.HasSuffix(trimmed, section) {
		return original
	}
	return trimmed + "\n\n" + section
}

// j2Finalize は finalizeJ2Output が使う、起動の前に決まっていた事実。
type j2Finalize struct {
	Validation j2ValidationContext
	// Read は j2Prepared.Read（nil なら読んだ記録を付けない）。
	Read *readValues
}

// j2Finalized は finalizeJ2Output の結果。Outcome は判定の文字列（invalid_output に落ちた場合は
// 空）、Status は写した後の課題の状態コード（写さなかった場合は nil）、Result は最終的な run の
// 結果、Note は人が読む補足（読んだ記録を付けられなかったときの理由。無ければ空）。
type j2Finalized struct {
	Outcome string
	Status  *string
	Result  RunResult
	Note    string
}

// finalizeJ2Output は run が succeeded の場合に、core の再検査（閉集合・必須の組・
// 宣言との突き合わせ）→写像（plan/uncertain/parent_direct）→読んだ記録を行う。再検査に落ちたら
// run の結果を invalid_output へ更新し、課題は変えない。
//
// 問いを持つ plan も、問いが 0 件の plan も、保留を作らずに計画を登録する（課題は計画の承認待ちになる）。
//
// 読んだ記録（§J2・決定 M3P14・QH14）は、計画を実際に登録できたときだけ、J2 の
// 直前の取得の値で付ける。J2 の後に取り込みで観測値が進んでいても、その値は
// 使わない（読んでいないコメントを既読にしない）。計画の登録と読んだ記録は別の
// 書き込みトランザクションなので、読んだ記録の書き込みに失敗しても、登録済みの
// 計画の結果は失わない（読んだ記録が付かないだけ＝未読の更新が残る安全な側）。
// 失敗は Note に残す。
func (s *Store) finalizeJ2Output(ctx context.Context, runIDDisplay, challengeIDDisplay string, structuredOutput []byte, fin j2Finalize) (j2Finalized, error) {
	invalid := func(reason string) (j2Finalized, error) {
		if err := s.invalidateRunOutputWithReason(ctx, runIDDisplay, "invalid J2 output: "+reason); err != nil {
			return j2Finalized{}, err
		}
		return j2Finalized{Result: RunResultInvalidOutput}, nil
	}
	succeeded := func(outcome J2Verdict, status Status) j2Finalized {
		st := string(status)
		return j2Finalized{Outcome: string(outcome), Status: &st, Result: RunResultSucceeded}
	}

	validated, reason := validateJ2OutputReason(structuredOutput, fin.Validation)
	if reason != "" {
		return invalid(reason)
	}

	switch validated.Verdict {
	case J2VerdictPlan:
		pm, err := s.applyJ2Plan(ctx, runIDDisplay, challengeIDDisplay, formatJ2PlanBody(validated), validated)
		if err != nil {
			return j2Finalized{}, err
		}
		if pm.Kind == j2PlanInvalidDependsOn {
			return invalid("depends_on names a challenge that does not exist, itself, or forms a cycle")
		}
		if !pm.Mapped {
			return j2Finalized{Outcome: string(J2VerdictPlan), Result: RunResultSucceeded}, nil
		}
		if pm.Kind == j2PlanExpectedEmpty {
			return succeeded(J2VerdictUncertain, StatusAwaitingHuman), nil
		}
		out := succeeded(J2VerdictPlan, StatusAwaitingPlanApproval)
		if fin.Read != nil {
			_, err := s.markReadWithValues(ctx, ChannelInvoker, challengeIDDisplay, markReadOptions{RunID: runIDDisplay, Values: fin.Read})
			// 課題がその間に完了した（ErrTerminalState）場合は付けない。
			if err != nil && !errors.Is(err, ErrTerminalState) {
				out.Note = "計画は登録したが、上流を読んだ記録を付けられなかった: " + err.Error()
			}
		}
		return out, nil
	case J2VerdictUncertain:
		mapped, err := s.applyJ2Uncertain(ctx, runIDDisplay, challengeIDDisplay, *validated.Question)
		if err != nil {
			return j2Finalized{}, err
		}
		if mapped {
			return succeeded(J2VerdictUncertain, StatusAwaitingHuman), nil
		}
		return j2Finalized{Outcome: string(J2VerdictUncertain), Result: RunResultSucceeded}, nil
	case J2VerdictParentDirect:
		mapped, err := s.applyJ2ParentDirect(ctx, runIDDisplay, challengeIDDisplay, *validated.Question, validated.ParentDirectCriteria)
		if err != nil {
			return j2Finalized{}, err
		}
		if mapped {
			return succeeded(J2VerdictParentDirect, StatusAwaitingHuman), nil
		}
		return j2Finalized{Outcome: string(J2VerdictParentDirect), Result: RunResultSucceeded}, nil
	default:
		return j2Finalized{Result: RunResultSucceeded}, nil
	}
}

// --- `plan --auto` の本体 ---

// J2AutoInput は Store.PlanAutoJ2 の入力。
type J2AutoInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（§判断点の共通の
	// 規則「課題の ID を指定した個別の操作は、上の除外を適用しない」）。nil なら、
	// selectJ2AutoTargets が選ぶ対象すべてを処理する。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	// ConnDecl は宣言の接続ツール・リポジトリ（LoadConnectorsDeclaration の結果）。
	ConnDecl *ConnectorsDeclaration
	Invoker  JudgmentInvoker
	// Upstream は J2 の起動の直前の上流の取得（core の IF。実装は
	// internal/adapters/github）。取り込み元の対応の無い課題では呼ばれない。
	Upstream UpstreamThreadSource
	// CycleID はこの操作が属する周（BeginCycle が返した ID）。空は ErrValidation。
	CycleID string
	// Cycle が非 nil なら、ID を省略した対象の処理の枠超過の状態をこの
	// JudgmentCycle と共有する（J1AutoInput.Cycle と同じ規則。`cycle` の段が
	// 分類の段と同じものを渡す）。nil ならこの呼び出しの中だけのものを作る。
	Cycle *JudgmentCycle
}

// J2AutoItem は `plan --auto` が処理した課題 1 件の結果（judgment_batch.go の
// JudgmentAutoItem。`classify --auto` の J1AutoItem と同じ形）。J2 の Outcome は
// plan/uncertain/parent_direct。
type J2AutoItem = JudgmentAutoItem

// J2AutoResult は Store.PlanAutoJ2 の出力（JudgmentAutoResult）。
type J2AutoResult = JudgmentAutoResult

// PlanAutoJ2 は `flywheel plan --auto [<C-ID>]` の本体。対象の選び方・上流の取得・
// 起動・出力の写像・読んだ記録は core が持ち、呼び出し元（internal/cli）は宣言の
// 読み込み・invoker と上流の取得の組み立て・周の開始と終了だけを行う（P2）。
//
//   - in.ChallengeID が非 nil（ID を指定した個別の操作）: その課題が分類済でなければ
//     ErrInvalidTransition、終了していない run があれば ErrRunInProgress、周の上限で
//     起動できなければ ErrBudgetExceeded をそのまま返す。上流の取得に失敗した
//     ときは NotStarted（upstream_fetch_failed）を結果に返す（終了コードを持つ
//     エラーではない）。
//   - in.ChallengeID が nil（自動の対象）: selectJ2AutoTargets が選んだ対象を順に
//     処理する。枠超過・周の上限・上流の取得の失敗で起動しなかった課題は
//     NotStarted へ、並行して他から起動された課題（ErrRunInProgress）は静かに
//     スキップする。
func (s *Store) PlanAutoJ2(ctx context.Context, in J2AutoInput) (*J2AutoResult, error) {
	if in.AgentDecl == nil || in.ConnDecl == nil || in.Invoker == nil || in.Upstream == nil || in.CycleID == "" {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		// run に紐づける周の ID の出所が 2 つある（CycleID と Cycle）ので、食い違いは拒否する。
		return nil, ErrValidation
	}

	result := &J2AutoResult{}

	if in.ChallengeID != nil {
		ch, err := s.loadChallengeForAuto(ctx, *in.ChallengeID)
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusClassified {
			return nil, ErrInvalidTransition
		}
		// 上流の取得（gh の呼び出し）を無駄にしないため、終了していない run が
		// あれば取得の前に ErrRunInProgress にする（RunJudgment の①が最終防衛）。
		cid, _ := parseChallengeID(ch.ID)
		var active bool
		err = s.db.Read(ctx, func(tx *sql.Tx) error {
			a, err := challengeHasActiveRun(ctx, tx, cid)
			active = a
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
		if active {
			return nil, ErrRunInProgress
		}
		items, notStarted, err := s.planOneJ2(ctx, in, nil, *ch)
		if err != nil {
			return nil, err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
		}
		result.Items = append(result.Items, items...)
		return result, nil
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, ex, err := selectJ2AutoTargets(ctx, tx)
		targetIDs = ids
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
	for _, cid := range targetIDs {
		ch, ns, err := s.j2AutoPreLaunch(ctx, in, cid)
		if err != nil {
			return nil, err
		}
		if ns != nil {
			result.NotStarted = append(result.NotStarted, *ns)
			continue
		}
		if ch == nil {
			continue
		}
		items, notStarted, err := s.planOneJ2(ctx, in, jc, *ch)
		if err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue
			}
			return nil, err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
		}
		result.Items = append(result.Items, items...)
	}
	return result, nil
}

// j2AutoPreLaunch は ID を省略した自動の対象で J2 を起動する直前の検査（PlanAutoJ2）。課題を読み直し、分類済でなければ (nil, nil, nil)
// （並行して状態が変わった。例: 人間が先に計画を登録した＝起動しない）。取り込み元の状態で外れれば
// その理由、連続失敗の上限に達していれば課題を保留に置いて failure_limit を NotStarted に返す。
// 保留に置く前に課題が対象でなくなっていれば (nil, nil, nil)。起動してよければ読み直した課題を返す。
func (s *Store) j2AutoPreLaunch(ctx context.Context, in J2AutoInput, cid int64) (*Challenge, *NotStarted, error) {
	ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
	if err != nil {
		return nil, nil, err
	}
	if ch.Status != StatusClassified {
		return nil, nil, nil
	}
	excluded, err := s.j2StillEligibleForAuto(ctx, cid)
	if err != nil || excluded != nil {
		return nil, excluded, err
	}
	held, skip, err := s.holdForJudgmentFailureLimit(ctx, *ch, JudgmentJ2, StatusClassified, in.AgentDecl, in.CycleID)
	if err != nil || held != nil || skip {
		return nil, held, err
	}
	return ch, nil, nil
}

// planOneJ2 は課題 1 件の J2 を実行する。jc が非 nil なら周の枠超過・周の上限の
// 扱いを JudgmentCycle に任せ（NotStarted を返す）、nil（ID を指定した個別の操作）
// なら周の上限で起動できないとき ErrBudgetExceeded を返す。
// 返り値の items は run ごとに 1 件（J2 の呼び出しは 1 回）。
func (s *Store) planOneJ2(ctx context.Context, in J2AutoInput, jc *JudgmentCycle, ch Challenge) ([]J2AutoItem, *NotStarted, error) {
	if jc != nil && jc.RateLimited() {
		// 枠超過を記録した周では、上流の取得（gh の呼び出し）もしない。
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}

	// 周の上限を使い切っているのに上流の取得（gh の GET を最大 7 回）だけ行って
	// cycle_budget で終わる無駄を避けるため、取得の前に周の残額を読むだけで確かめる
	// （最終の判定は RunJudgment の①のトランザクションが行う）。
	maxBudget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ2)
	if err := s.checkCycleBudget(ctx, in.CycleID, maxBudget); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}

	prepared, notStarted, err := s.prepareJ2(ctx, ch, in)
	if err != nil || notStarted != nil {
		return nil, notStarted, err
	}

	item, notStarted, err := s.runJ2Once(ctx, in, jc, ch, prepared)
	if err != nil || notStarted != nil {
		return nil, notStarted, err
	}
	return []J2AutoItem{*item}, nil, nil
}

// runJ2Once は J2 を 1 回起動して結果を写す。返り値の item と notStarted は
// どちらか一方だけが非 nil（エラー時は両方 nil）。
func (s *Store) runJ2Once(ctx context.Context, in J2AutoInput, jc *JudgmentCycle, ch Challenge, prepared *j2Prepared) (*J2AutoItem, *NotStarted, error) {
	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ2,
		MaxBudgetUSD: in.AgentDecl.JudgmentBudgetFor(JudgmentJ2),
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     prepared.Sections,
		OutputSchema: j2OutputSchema(),
		Invoker:      in.Invoker,
	}
	var res *RunJudgmentResult
	var notStarted *NotStarted
	var err error
	if jc != nil {
		res, notStarted, err = jc.RunJudgment(ctx, s, runIn)
		if err != nil || notStarted != nil {
			return nil, notStarted, err
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		res, err = s.RunJudgment(ctx, runIn)
		if err != nil {
			return nil, nil, err
		}
	}

	item := J2AutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		fin, err := s.finalizeJ2Output(ctx, res.RunID, res.ChallengeID, res.StructuredOutput, j2Finalize{
			Validation: j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: prepared.HasSource, CheckSource: true, CheckLimit: true, OpenChildKeys: prepared.OpenChildKeys, HasDoneTasks: prepared.HasDoneTasks, DoneRepos: prepared.DoneRepos, KeptBudgets: prepared.KeptBudgets},
			Read:       prepared.Read,
		})
		if err != nil {
			return nil, nil, err
		}
		item.Outcome = fin.Outcome
		item.Status = fin.Status
		item.Result = fin.Result
		item.Note = fin.Note
	}
	return &item, nil, nil
}
