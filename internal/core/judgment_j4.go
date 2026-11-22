package core

// このファイルは J4（子の問いへの応答。docs/features/m3-invoker-delegation.md §J4・#630 の Issue 本文の決定）を持つ。
// 旧来の委譲の問い（結末 questions）と契約の判断待ち（decider が parent・kind が answer）の両方が、
// 同じ指示文・出力の型・再開の上限（runJ4）を使う。J4 の回答は保留に入れず、run の出力として残る。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// j4MaxAnswerResumes は、1 つの課題の委譲の連なり（最後に人間が回答した保留より後）で、J4 の答えによる
// 再開ができる回数の上限。
const j4MaxAnswerResumes = 3

// j4Question は J4 へ渡す問い 1 件。契約の判断待ちは Kind が空。
type j4Question struct {
	Kind           string
	Text           string
	Options        []string
	Recommendation *string
}

// j4Decision は J4 の出力の、問いごとの判定。QuestionIndex は 1 始まり（入力の問いの番号）。
type j4Decision struct {
	QuestionIndex int
	Decision      J4Decision
	// Option は選択肢のある問いで選んだ選択肢（選択肢の文字列と同じ）。
	Option *string
	// Answer は選択肢の無い問いの回答。選択肢のある問いでは添え書き、escalate では人へ上げる理由（どちらも任意）。
	Answer *string
}

type j4RawDecision struct {
	QuestionIndex *int    `json:"question_index"`
	Decision      string  `json:"decision"`
	Option        *string `json:"option"`
	Answer        *string `json:"answer"`
}

type j4RawOutput struct {
	Decisions []j4RawDecision `json:"decisions"`
}

// j4OutputSchema は J4 の `--json-schema` に渡すスキーマ。閉集合の値は J4DecisionValues から作る。
func j4OutputSchema() []byte {
	decisions := make([]string, 0, len(J4DecisionValues))
	for _, v := range J4DecisionValues {
		decisions = append(decisions, string(v))
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decisions": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question_index": map[string]any{"type": "integer", "minimum": 1},
						"decision":       map[string]any{"type": "string", "enum": decisions},
						"option":         map[string]any{"type": []string{"string", "null"}},
						"answer":         map[string]any{"type": []string{"string", "null"}},
					},
					"required":             []string{"question_index", "decision", "option", "answer"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"decisions"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return b
}

func blankStr(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }

// validateJ4Output は J4 の出力を検査する。判定は問いごとに 1 つ（番号は 1〜問いの数を 1 回ずつ）。
// decision が answer のとき、選択肢のある問いは option が選択肢のどれかと一致すること、選択肢の無い問いは
// answer が空でないこと。閉集合の外の値・番号の欠落や重複・answer の条件を満たさないものは ok=false。
func validateJ4Output(data []byte, questions []j4Question) ([]j4Decision, bool) {
	var raw j4RawOutput
	if err := json.Unmarshal(data, &raw); err != nil || len(raw.Decisions) != len(questions) {
		return nil, false
	}
	out := make([]j4Decision, len(questions))
	seen := make([]bool, len(questions))
	for _, r := range raw.Decisions {
		if r.QuestionIndex == nil || *r.QuestionIndex < 1 || *r.QuestionIndex > len(questions) || seen[*r.QuestionIndex-1] {
			return nil, false
		}
		i := *r.QuestionIndex - 1
		seen[i] = true
		d := J4Decision(r.Decision)
		q := questions[i]
		switch d {
		case J4DecisionAnswer:
			if len(q.Options) > 0 {
				if r.Option == nil || !j4Contains(q.Options, *r.Option) {
					return nil, false
				}
			} else if blankStr(r.Answer) {
				return nil, false
			}
		case J4DecisionEscalate:
		default:
			return nil, false
		}
		out[i] = j4Decision{QuestionIndex: i + 1, Decision: d, Option: r.Option, Answer: r.Answer}
	}
	return out, true
}

func j4Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// j4AllAnswered は全問が answer か。
func j4AllAnswered(ds []j4Decision) bool {
	for _, d := range ds {
		if d.Decision != J4DecisionAnswer {
			return false
		}
	}
	return len(ds) > 0
}

// j4StoredAllAnswered は、保存した run の出力が、全問に答えた出力か（問いの数は保存時に検査済み）。J4 の出力（全問 answer）と、
// スーパーバイザーの出力（手が answer。区切り S4b-3）を読む。全問に答えていれば判定を返す。
func j4StoredAllAnswered(output string) ([]j4Decision, bool) {
	var raw struct {
		j4RawOutput
		Hand    string `json:"hand"`
		Answers []struct {
			Answer string `json:"answer"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, false
	}
	if SupervisorHand(raw.Hand) == HandAnswer && len(raw.Answers) > 0 {
		out := make([]j4Decision, len(raw.Answers))
		for i, a := range raw.Answers {
			answer := a.Answer
			out[i] = j4Decision{QuestionIndex: i + 1, Decision: J4DecisionAnswer, Answer: &answer}
		}
		return out, true
	}
	if len(raw.Decisions) == 0 {
		return nil, false
	}
	out := make([]j4Decision, 0, len(raw.Decisions))
	for _, r := range raw.Decisions {
		if r.QuestionIndex == nil || J4Decision(r.Decision) != J4DecisionAnswer {
			return nil, false
		}
		out = append(out, j4Decision{QuestionIndex: *r.QuestionIndex, Decision: J4DecisionAnswer, Option: r.Option, Answer: r.Answer})
	}
	return out, true
}

// j4AnswerText は 1 問の回答の文。選択肢を選んだなら選択肢、添え書きがあれば続ける。
func j4AnswerText(d j4Decision) string {
	var parts []string
	if d.Option != nil && strings.TrimSpace(*d.Option) != "" {
		parts = append(parts, strings.TrimSpace(*d.Option))
	}
	if !blankStr(d.Answer) {
		parts = append(parts, strings.TrimSpace(*d.Answer))
	}
	return strings.Join(parts, "\n")
}

// formatJ4Answers は子へ渡す回答の文（問いの番号ごと）。
func formatJ4Answers(ds []j4Decision) string {
	var b strings.Builder
	b.WriteString("親（flywheel）が、子の問いに次のとおり答えた。\n")
	for _, d := range ds {
		fmt.Fprintf(&b, "\n問い %d の回答:\n%s\n", d.QuestionIndex, j4AnswerText(d))
	}
	return strings.TrimRight(b.String(), "\n")
}

// j4RuleEscalates は、J4 を呼ばずに人間対応待ちにする問いの種類を含むか（親も代わりに決めない問い）。
func j4RuleEscalates(qs []DelegationQuestion, declared []HumanQuestionKind) bool {
	for _, q := range qs {
		switch QuestionKind(q.Kind) {
		case QuestionKindProductPolicy, QuestionKindScope, QuestionKindSafetyTradeoff:
			return true
		}
		for _, k := range declared {
			if k.ID == q.Kind {
				return true
			}
		}
	}
	return false
}

// j4Request は runJ4 の入力。
type j4Request struct {
	ChallengeID string
	Questions   []j4Question
	// Extra は問いの背景の区画（子の報告の要約・接続ツールの状況）。
	Extra     []JudgmentDataSection
	AgentDecl *AgentDeclaration
	CycleID   string
	Invoker   JudgmentInvoker
	Cycle     *JudgmentCycle
}

// j4Result は runJ4 の結果。Answered が真なら全問に答えた（Decisions が回答）。偽なら人間対応待ちにし、
// Reason を保留の問いの冒頭へ書く。
type j4Result struct {
	Answered  bool
	Decisions []j4Decision
	RunID     string
	Reason    string
}

// countJ4Answered は課題の、最後に人間が回答した保留より後の、全問 answer の J4 の run の数（答えによる再開の数）。
func countJ4Answered(ctx context.Context, tx *sql.Tx, cid int64) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT output FROM run WHERE challenge_id = ? AND kind = 'judgment' AND judgment = 'J4' AND result = 'succeeded'
		 AND started_at > COALESCE((SELECT MAX(answered_at) FROM hold WHERE challenge_id = ? AND answered_at IS NOT NULL), '')`, cid, cid)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var out sql.NullString
		if err := rows.Scan(&out); err != nil {
			return 0, err
		}
		if _, ok := j4StoredAllAnswered(out.String); ok {
			n++
		}
	}
	return n, rows.Err()
}

// j4MaterialSections は課題・承認済みの計画・保留の記録の区画。
func (s *Store) j4MaterialSections(ctx context.Context, cid int64) ([]JudgmentDataSection, error) {
	var ch *Challenge
	var plan approvedPlan
	var havePlan bool
	var holds []Hold
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		ch = c
		if plan, havePlan, err = loadApprovedPlanAnySpec(ctx, tx, cid); err != nil {
			return err
		}
		holds, err = loadHolds(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	sections := []JudgmentDataSection{{Label: "課題", Content: fmt.Sprintf(
		"タイトル: %s\n\n説明:\n%s\n\n達成条件:\n%s", ch.Title, ch.Description, ch.DoneCriteria)}}
	if havePlan {
		sections = append(sections, JudgmentDataSection{Label: "承認済みの計画の本文", Content: plan.Body})
	}
	if t := formatHoldsSection(holds); t != "" {
		sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: t})
	}
	return sections, nil
}

// formatJ4Questions は J4 へ渡す問いの区画の本文（番号は 1 始まり。出力の question_index と同じ）。
func formatJ4Questions(qs []j4Question) string {
	var b strings.Builder
	for i, q := range qs {
		if i > 0 {
			b.WriteString("\n")
		}
		if q.Kind != "" {
			fmt.Fprintf(&b, "問い %d [%s]: %s\n", i+1, q.Kind, q.Text)
		} else {
			fmt.Fprintf(&b, "問い %d: %s\n", i+1, q.Text)
		}
		if len(q.Options) > 0 {
			b.WriteString("選択肢:\n")
			for _, o := range q.Options {
				fmt.Fprintf(&b, "- %s\n", o)
			}
		} else {
			b.WriteString("選択肢: なし（文章で答える）\n")
		}
		if !blankStr(q.Recommendation) {
			fmt.Fprintf(&b, "子の推奨: %s\n", strings.TrimSpace(*q.Recommendation))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// runJ4 は再開の上限を確かめ、J4 を 1 回呼んで出力を検査する。人間対応待ちにする場合は Answered が偽で、
// その理由を Reason に入れる（J4 の失敗・枠超過・周の上限・不正な出力を含む。人間対応待ちにしない失敗は無い）。
// エラーはストアの失敗だけ。
func (s *Store) runJ4(ctx context.Context, req j4Request) (*j4Result, error) {
	cid, ok := parseChallengeID(req.ChallengeID)
	if !ok {
		return nil, ErrValidation
	}
	var used int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		n, err := countJ4Answered(ctx, tx, cid)
		used = n
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if used >= j4MaxAnswerResumes {
		return &j4Result{Reason: fmt.Sprintf("親が答えて再開できる回数の上限（%d 回）に達した。", j4MaxAnswerResumes)}, nil
	}
	if req.Invoker == nil || req.AgentDecl == nil {
		return nil, ErrValidation
	}
	sections, err := s.j4MaterialSections(ctx, cid)
	if err != nil {
		return nil, err
	}
	sections = append(sections, req.Extra...)
	sections = append(sections, JudgmentDataSection{Label: "子の問い", Content: formatJ4Questions(req.Questions)})
	runIn := RunJudgmentInput{
		ChallengeID:  req.ChallengeID,
		Judgment:     JudgmentJ4,
		MaxBudgetUSD: req.AgentDecl.JudgmentBudgetFor(JudgmentJ4),
		TimeoutSec:   req.AgentDecl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: j4OutputSchema(),
		Invoker:      req.Invoker,
	}
	var res *RunJudgmentResult
	if req.Cycle != nil {
		r, ns, err := req.Cycle.RunJudgment(ctx, s, runIn)
		if err != nil {
			return nil, err
		}
		if ns != nil {
			return &j4Result{Reason: fmt.Sprintf("親が答える判断を呼べなかった（理由: %s）。", ns.Reason)}, nil
		}
		res = r
	} else {
		cyc := req.CycleID
		runIn.CycleID = &cyc
		r, err := s.RunJudgment(ctx, runIn)
		if err != nil {
			if errors.Is(err, ErrBudgetExceeded) {
				return &j4Result{Reason: "周の上限のため、親が答える判断を呼べなかった。"}, nil
			}
			return nil, err
		}
		res = r
	}
	if res.Result != RunResultSucceeded {
		return &j4Result{RunID: res.RunID, Reason: fmt.Sprintf("親が答える判断の呼び出しが成功しなかった（結果: %s）。", res.Result)}, nil
	}
	ds, valid := validateJ4Output(res.StructuredOutput, req.Questions)
	if !valid {
		if err := s.invalidateRunOutput(ctx, res.RunID); err != nil {
			return nil, err
		}
		return &j4Result{RunID: res.RunID, Reason: "親が答える判断の出力が検査を通らなかった。"}, nil
	}
	if !j4AllAnswered(ds) {
		var b strings.Builder
		b.WriteString("親が答える判断が、人へ上げると判定した問いがある。\n")
		for _, d := range ds {
			if d.Decision == J4DecisionEscalate {
				fmt.Fprintf(&b, "- 問い %d", d.QuestionIndex)
				if !blankStr(d.Answer) {
					fmt.Fprintf(&b, ": %s", strings.TrimSpace(*d.Answer))
				}
				b.WriteString("\n")
			}
		}
		return &j4Result{RunID: res.RunID, Decisions: ds, Reason: strings.TrimRight(b.String(), "\n")}, nil
	}
	return &j4Result{Answered: true, Decisions: ds, RunID: res.RunID}, nil
}

// j4StoredQuestions は、保存した委譲の報告の出力から問いを読む（検査済みの出力なので緩く読む）。
func j4StoredQuestions(output string) []DelegationQuestion {
	var raw struct {
		Summary   string `json:"summary"`
		Questions []struct {
			Kind           string   `json:"kind"`
			Text           string   `json:"text"`
			Options        []string `json:"options"`
			Recommendation *string  `json:"recommendation"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}
	out := make([]DelegationQuestion, 0, len(raw.Questions))
	for _, q := range raw.Questions {
		out = append(out, DelegationQuestion{Kind: q.Kind, Text: q.Text, Options: q.Options, Recommendation: q.Recommendation})
	}
	return out
}
