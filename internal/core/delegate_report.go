package core

// このファイルは委譲の報告（`--json-schema` に渡す形と、core が再検査する閉集合・
// 必須のキー）を持つ（親要件チケット #98 §IF / API「判断点の出力」の委譲の報告の行。
// 決定 M3H4）。

import (
	"encoding/json"
	"strings"
)

// DelegationQuestion は委譲の報告の問い 1 件。
type DelegationQuestion struct {
	Kind           string
	Text           string
	Options        []string
	Recommendation *string
}

// DelegationReport は検査を通った委譲の報告。
type DelegationReport struct {
	Outcome     DelegationOutcome
	Summary     string
	Branch      *string
	PRURLs      []string
	Commits     []string
	QualityGate *string
	Assumptions []string
	Unverified  []string
	Questions   []DelegationQuestion
}

// delegationReportKeys は委譲の報告の必須のキー（宣言の順）。
var delegationReportKeys = []string{
	"outcome", "summary", "branch", "pr_urls", "commits", "quality_gate", "assumptions", "unverified", "questions",
}

// delegationQuestionKeys は問いの必須のキー。
var delegationQuestionKeys = []string{"kind", "text", "options", "recommendation"}

// allowedQuestionKinds は問いの種類の閉集合（基本の 8 値と、宣言の人間へ上げる問いの
// 種類の id の和集合。順序は基本→宣言）。
func allowedQuestionKinds(declared []HumanQuestionKind) []string {
	out := make([]string, 0, len(QuestionKindValues)+len(declared))
	seen := map[string]bool{}
	for _, k := range QuestionKindValues {
		out = append(out, string(k))
		seen[string(k)] = true
	}
	for _, k := range declared {
		if !seen[k.ID] {
			out = append(out, k.ID)
			seen[k.ID] = true
		}
	}
	return out
}

// delegationReportSchema は委譲の `--json-schema` に渡すスキーマを返す。閉集合の値は
// DelegationOutcomeValues と宣言から機械的に組み立てる（手で書き写さない）。
func delegationReportSchema(declared []HumanQuestionKind) []byte {
	outcomes := make([]string, 0, len(DelegationOutcomeValues))
	for _, o := range DelegationOutcomeValues {
		outcomes = append(outcomes, string(o))
	}
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	nullableString := map[string]any{"type": []string{"string", "null"}}
	question := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":           map[string]any{"type": "string", "enum": allowedQuestionKinds(declared)},
			"text":           map[string]any{"type": "string"},
			"options":        stringArray,
			"recommendation": nullableString,
		},
		"required":             delegationQuestionKeys,
		"additionalProperties": false,
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome":      map[string]any{"type": "string", "enum": outcomes},
			"summary":      map[string]any{"type": "string"},
			"branch":       nullableString,
			"pr_urls":      stringArray,
			"commits":      stringArray,
			"quality_gate": nullableString,
			"assumptions":  stringArray,
			"unverified":   stringArray,
			"questions":    map[string]any{"type": "array", "items": question},
		},
		"required":             delegationReportKeys,
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return b
}

// parseStrictObject は data を JSON のオブジェクト 1 つとして読み、キーの集合が
// required と一致すること（欠落も未知のキーも不正）を確かめる。
func parseStrictObject(data []byte, required []string) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return nil, false
	}
	if len(m) != len(required) {
		return nil, false
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	return m, true
}

func decodeStringArray(raw json.RawMessage) ([]string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, false
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

func decodeNullableString(raw json.RawMessage) (*string, bool) {
	var out *string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// validateDelegationReport は data（run の StructuredOutput）を検査する
// （§結果の判別「core が閉集合と必須の組…を検査し直す」）。結末が閉集合の外・必須の
// キーの欠落・未知のキー・型の違い・問いの種類が閉集合の外のいずれかで ok=false
// （run の結果は invalid_output になる）。
func validateDelegationReport(data []byte, declared []HumanQuestionKind) (*DelegationReport, bool) {
	m, ok := parseStrictObject(data, delegationReportKeys)
	if !ok {
		return nil, false
	}
	var r DelegationReport
	var outcome string
	if json.Unmarshal(m["outcome"], &outcome) != nil {
		return nil, false
	}
	r.Outcome = DelegationOutcome(outcome)
	known := false
	for _, o := range DelegationOutcomeValues {
		if o == r.Outcome {
			known = true
		}
	}
	if !known {
		return nil, false
	}
	if json.Unmarshal(m["summary"], &r.Summary) != nil || strings.TrimSpace(string(m["summary"])) == "null" {
		return nil, false
	}
	var ok1, ok2, ok3, ok4, ok5 bool
	r.Branch, ok1 = decodeNullableString(m["branch"])
	r.QualityGate, ok2 = decodeNullableString(m["quality_gate"])
	r.PRURLs, ok3 = decodeStringArray(m["pr_urls"])
	r.Commits, ok4 = decodeStringArray(m["commits"])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, false
	}
	r.Assumptions, ok5 = decodeStringArray(m["assumptions"])
	if !ok5 {
		return nil, false
	}
	if r.Unverified, ok5 = decodeStringArray(m["unverified"]); !ok5 {
		return nil, false
	}

	var rawQuestions []json.RawMessage
	if strings.TrimSpace(string(m["questions"])) == "null" || json.Unmarshal(m["questions"], &rawQuestions) != nil {
		return nil, false
	}
	kinds := map[string]bool{}
	for _, k := range allowedQuestionKinds(declared) {
		kinds[k] = true
	}
	for _, rq := range rawQuestions {
		qm, ok := parseStrictObject(rq, delegationQuestionKeys)
		if !ok {
			return nil, false
		}
		var q DelegationQuestion
		if json.Unmarshal(qm["kind"], &q.Kind) != nil || !kinds[q.Kind] {
			return nil, false
		}
		if json.Unmarshal(qm["text"], &q.Text) != nil || strings.TrimSpace(string(qm["text"])) == "null" {
			return nil, false
		}
		if q.Options, ok = decodeStringArray(qm["options"]); !ok {
			return nil, false
		}
		if q.Recommendation, ok = decodeNullableString(qm["recommendation"]); !ok {
			return nil, false
		}
		r.Questions = append(r.Questions, q)
	}
	if r.Outcome == DelegationOutcomeQuestions && len(r.Questions) == 0 {
		return nil, false
	}
	return &r, true
}

// j3OutputSchema は J3 の `--json-schema` に渡すスキーマ（brief の文字列だけ）。
func j3OutputSchema() []byte {
	b, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"brief": map[string]any{"type": "string"}},
		"required":             []string{"brief"},
		"additionalProperties": false,
	})
	return b
}

// validateJ3Output は J3 の出力を検査する。brief 以外のキーを含む・brief が文字列でない・
// 空は不正。
func validateJ3Output(data []byte) (string, bool) {
	m, ok := parseStrictObject(data, []string{"brief"})
	if !ok {
		return "", false
	}
	var brief string
	if json.Unmarshal(m["brief"], &brief) != nil || strings.TrimSpace(brief) == "" {
		return "", false
	}
	return brief, true
}
