package core

// このファイルは判断点 J9（2 つの課題の計画を同時に進めてよいかの判断）の出力の形と検査を持つ。
// 委譲の段は J9 を呼ばない（過去の run の読み取りと宣言の検査のために残す）。

import (
	"encoding/json"
	"strings"
)

// J9Verdict は J9 の判定の閉集合。
type J9Verdict string

// J9Verdict の 3 値。
const (
	J9VerdictStrong    J9Verdict = "strong"
	J9VerdictWeak      J9Verdict = "weak"
	J9VerdictUncertain J9Verdict = "uncertain"
)

// J9VerdictValues は J9Verdict の全値。
var J9VerdictValues = []J9Verdict{J9VerdictStrong, J9VerdictWeak, J9VerdictUncertain}

// j9MaxReasonChars は記録する判断の理由の文字数の上限。
const j9MaxReasonChars = 1000

type j9RawOutput struct {
	Verdict *string `json:"verdict"`
	Reason  *string `json:"reason"`
}

// j9OutputSchema は J9 の `--json-schema` に渡すスキーマ。
func j9OutputSchema() []byte {
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"verdict", "reason"},
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": j6StringEnum(J9VerdictValues)},
			"reason":  map[string]any{"type": "string", "minLength": 1},
		},
	}
	b, _ := json.Marshal(schema)
	return b
}

// validateJ9Output は J9 の出力を検査する。項目の欠落・閉集合の外の値・理由が空白だけなら ok=false。
func validateJ9Output(data []byte) (J9Verdict, string, bool) {
	var raw j9RawOutput
	if err := json.Unmarshal(data, &raw); err != nil || raw.Verdict == nil || raw.Reason == nil {
		return "", "", false
	}
	v := J9Verdict(*raw.Verdict)
	valid := false
	for _, x := range J9VerdictValues {
		if v == x {
			valid = true
		}
	}
	reason := strings.TrimSpace(*raw.Reason)
	if !valid || reason == "" {
		return "", "", false
	}
	reason, _ = truncateRunes(reason, j9MaxReasonChars)
	return v, reason, true
}
