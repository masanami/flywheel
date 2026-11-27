package core

// このファイルは J4（子の問いへの応答）の定数・出力の型・過去の run の出力の読み取りを持つ。J4 の新しい呼び出しは無い
// （子の問いはスーパーバイザーが受ける。supervisor_child_question.go）。過去の J4 の run の記録は読める。

import (
	"encoding/json"
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

// j4StoredAllAnswered は、保存した run の出力が、全問に答えた出力か（問いの数は保存時に検査済み）。J4 の出力（全問 answer）と、
// スーパーバイザーの出力（手が answer。区切り S4b-3）を読む。全問に答えていれば判定を返す。answers に null の要素（答えない問い）があれば、全問に答えていない。
func j4StoredAllAnswered(output string) ([]j4Decision, bool) {
	var raw struct {
		j4RawOutput
		Hand    string `json:"hand"`
		Answers []*struct {
			Answer string `json:"answer"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil, false
	}
	if SupervisorHand(raw.Hand) == HandAnswer && len(raw.Answers) > 0 {
		out := make([]j4Decision, len(raw.Answers))
		for i, a := range raw.Answers {
			if a == nil {
				return nil, false
			}
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
