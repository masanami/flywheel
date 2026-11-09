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

// invalidReportErrorPrefix は、子の報告が検査を通らず invalid_output になった run の error の
// 先頭（理由の前）。再開で子へ渡すときは外す。
const invalidReportErrorPrefix = "invalid report: "

// branchFieldPattern は報告の branch 欄の pattern（JSON Schema は ECMA-262 の正規表現）。
// 空白・制御文字・`~^:?*[\` を含まない（空文字は core が「ブランチなし」と読む）。
const branchFieldPattern = `^[^\s\x00-\x1f\x7f~^:?*\[\\]*$`

// delegationReportSchema は委譲の `--json-schema` に渡すスキーマを返す。閉集合の値は
// DelegationOutcomeValues と宣言から機械的に組み立てる（手で書き写さない）。
func delegationReportSchema(declared []HumanQuestionKind) []byte {
	outcomes := make([]string, 0, len(DelegationOutcomeValues))
	for _, o := range DelegationOutcomeValues {
		outcomes = append(outcomes, string(o))
	}
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	nullableString := map[string]any{"type": []string{"string", "null"}}
	// branch はブランチ名だけを書く欄。空白・制御文字・git のブランチ名に使えない文字を
	// 含む値（注記つきの値）をスキーマの段階で拒む。core の再検査（plausibleBranchName）が正。
	branchField := map[string]any{"type": []string{"string", "null"}, "pattern": branchFieldPattern}
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
			"branch":       branchField,
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
// キーの欠落・未知のキー・型の違い・問いの種類が閉集合の外・ブランチの欄がブランチ名で
// ないもののいずれかで ok=false（run の結果は invalid_output になる）。
func validateDelegationReport(data []byte, declared []HumanQuestionKind) (*DelegationReport, bool) {
	r, reason := checkDelegationReport(data, declared)
	return r, reason == ""
}

// checkDelegationReport は validateDelegationReport と同じ検査をして、不備があれば報告のどこが
// 不備かを短い 1 行（reason）で返す。不備が無ければ reason は空。reason は子へ再開で渡すので、
// 子の報告の値そのものは含めない。
func checkDelegationReport(data []byte, declared []HumanQuestionKind) (*DelegationReport, string) {
	m, ok := parseStrictObject(data, delegationReportKeys)
	if !ok {
		return nil, "the report is not one JSON object with exactly the required keys"
	}
	var r DelegationReport
	var outcome string
	if json.Unmarshal(m["outcome"], &outcome) != nil {
		return nil, "the outcome field is not a string"
	}
	r.Outcome = DelegationOutcome(outcome)
	known := false
	for _, o := range DelegationOutcomeValues {
		if o == r.Outcome {
			known = true
		}
	}
	if !known {
		return nil, "the outcome field is not one of the allowed values"
	}
	if json.Unmarshal(m["summary"], &r.Summary) != nil || strings.TrimSpace(string(m["summary"])) == "null" {
		return nil, "the summary field is not a string"
	}
	var ok1, ok2, ok3, ok4, ok5 bool
	r.Branch, ok1 = decodeNullableString(m["branch"])
	r.QualityGate, ok2 = decodeNullableString(m["quality_gate"])
	r.PRURLs, ok3 = decodeStringArray(m["pr_urls"])
	r.Commits, ok4 = decodeStringArray(m["commits"])
	switch {
	case !ok1:
		return nil, "the branch field is neither a string nor null"
	case !ok2:
		return nil, "the quality_gate field is neither a string nor null"
	case !ok3:
		return nil, "the pr_urls field is not an array of strings"
	case !ok4:
		return nil, "the commits field is not an array of strings"
	}
	if r.Branch != nil {
		if b := strings.TrimSpace(*r.Branch); b != "" && !plausibleBranchName(b) {
			return nil, "the branch field is not a git branch name; write only the branch name there and put any note (repository, state) in the summary or assumptions"
		}
	}
	if r.Assumptions, ok5 = decodeStringArray(m["assumptions"]); !ok5 {
		return nil, "the assumptions field is not an array of strings"
	}
	if r.Unverified, ok5 = decodeStringArray(m["unverified"]); !ok5 {
		return nil, "the unverified field is not an array of strings"
	}

	var rawQuestions []json.RawMessage
	if strings.TrimSpace(string(m["questions"])) == "null" || json.Unmarshal(m["questions"], &rawQuestions) != nil {
		return nil, "the questions field is not an array"
	}
	kinds := map[string]bool{}
	for _, k := range allowedQuestionKinds(declared) {
		kinds[k] = true
	}
	for _, rq := range rawQuestions {
		qm, ok := parseStrictObject(rq, delegationQuestionKeys)
		if !ok {
			return nil, "a question is not an object with exactly the required keys"
		}
		var q DelegationQuestion
		if json.Unmarshal(qm["kind"], &q.Kind) != nil || !kinds[q.Kind] {
			return nil, "a question's kind is not one of the allowed values"
		}
		if json.Unmarshal(qm["text"], &q.Text) != nil || strings.TrimSpace(string(qm["text"])) == "null" {
			return nil, "a question's text is not a string"
		}
		if q.Options, ok = decodeStringArray(qm["options"]); !ok {
			return nil, "a question's options is not an array of strings"
		}
		if q.Recommendation, ok = decodeNullableString(qm["recommendation"]); !ok {
			return nil, "a question's recommendation is neither a string nor null"
		}
		r.Questions = append(r.Questions, q)
	}
	if r.Outcome == DelegationOutcomeQuestions && len(r.Questions) == 0 {
		return nil, "the outcome is questions but the questions field is empty"
	}
	return &r, ""
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
