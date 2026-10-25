package core

// このファイルは J6（学習）の出力スキーマと、その形・閉集合の検査を持つ。
//
// ここで見るのは出力そのものの形だけである。未分類の発生か・抜けや重複が無いか・evidence が
// その課題の ID か（ストアを読む呼び出し側）、記憶の規則・フォローアップの件数
// （記憶の書き込み器と呼び出し側）はここで検査しない。閉集合の外の値・必須項目の欠け・
// 同じ occurrence_id の重複は、出力全体を不正とする（分類を部分的に写さない）。
//
// J6 の記憶の出力は作成（create）と追記（append）だけで、上書き・削除・改名は持たない。

import (
	"encoding/json"
	"slices"
	"strings"
)

// J6Mode は J6 の記憶の書き込みの mode の閉集合。
type J6Mode string

// J6Mode の2値。
const (
	J6ModeCreate J6Mode = J6Mode(MemoryModeCreate)
	J6ModeAppend J6Mode = J6Mode(MemoryModeAppend)
)

// J6ModeValues は J6Mode の全値。
var J6ModeValues = []J6Mode{J6ModeCreate, J6ModeAppend}

// J6MemoryTypeValues は J6 の記憶の種類の閉集合。
var J6MemoryTypeValues = []string{MemoryTypeMap, MemoryTypeTacit, MemoryTypeExperience, MemoryTypeReference}

// OccurrenceCauseValues は発生の原因の閉集合。
var OccurrenceCauseValues = []OccurrenceCause{
	OccurrenceCauseContext, OccurrenceCausePermission, OccurrenceCausePerformance, OccurrenceCauseNone,
}

// OccurrenceLayerValues は改修の対象の層の閉集合。
var OccurrenceLayerValues = []OccurrenceLayer{
	OccurrenceLayerPrompt, OccurrenceLayerConnectorSkill, OccurrenceLayerEntrySkill,
	OccurrenceLayerKnowledge, OccurrenceLayerNone,
}

type j6RawClassification struct {
	OccurrenceID *string `json:"occurrence_id"`
	Cause        *string `json:"cause"`
	Layer        *string `json:"layer"`
	Target       *string `json:"target"`
	Signal       *string `json:"signal"`
}

type j6RawAdded struct {
	Outcome  *string `json:"outcome"`
	Kind     *string `json:"kind"`
	Evidence *string `json:"evidence"`
	Cause    *string `json:"cause"`
	Layer    *string `json:"layer"`
	Target   *string `json:"target"`
	Signal   *string `json:"signal"`
}

type j6RawMemory struct {
	Mode        *string   `json:"mode"`
	Type        *string   `json:"type"`
	Slug        *string   `json:"slug"`
	Description *string   `json:"description"`
	Confidence  *string   `json:"confidence"`
	Sources     *[]string `json:"sources"`
	Outcome     *string   `json:"outcome"`
	Target      *string   `json:"target"`
	Signal      *string   `json:"signal"`
	Body        *string   `json:"body"`
}

type j6RawFollowup struct {
	Title        *string `json:"title"`
	Description  *string `json:"description"`
	DoneCriteria *string `json:"done_criteria"`
}

type j6RawOutput struct {
	Classifications *[]j6RawClassification `json:"classifications"`
	Added           *[]j6RawAdded          `json:"added"`
	Memory          *[]j6RawMemory         `json:"memory"`
	Followups       *[]j6RawFollowup       `json:"followups"`
}

// J6Classification は未分類の発生 1 件への分類。
type J6Classification struct {
	OccurrenceID string
	Cause        OccurrenceCause
	Layer        OccurrenceLayer
	Target       string
	Signal       string
}

// J6Added は J6 が足す発生 1 件。
type J6Added struct {
	Outcome  OccurrenceOutcome
	Kind     string
	Evidence string
	Cause    OccurrenceCause
	Layer    OccurrenceLayer
	Target   string
	Signal   string
}

// J6Followup は J6 が出力するフォローアップの課題 1 件。
type J6Followup struct {
	Title        string
	Description  string
	DoneCriteria string
}

// j6ValidatedOutput は検査を通った J6 の出力。
type j6ValidatedOutput struct {
	Classifications []J6Classification
	Added           []J6Added
	Memory          []memoryItem
	Followups       []J6Followup
}

func j6StringEnum[T ~string](vals []T) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, string(v))
	}
	return out
}

// j6OutputSchema は J6 の `--json-schema` に渡すスキーマ（§IF / API「判断点の出力」の J6 の行）。
// 閉集合の値は各閉集合の定義から作る。
func j6OutputSchema() []byte {
	str := map[string]any{"type": "string"}
	obj := func(props map[string]any, required []string) map[string]any {
		return map[string]any{
			"type": "object", "properties": props, "required": required, "additionalProperties": false,
		}
	}
	enum := func(vals []string) map[string]any { return map[string]any{"type": "string", "enum": vals} }
	causes := enum(j6StringEnum(OccurrenceCauseValues))
	layers := enum(j6StringEnum(OccurrenceLayerValues))
	outcomes := enum([]string{string(OccurrenceOutcomeGood), string(OccurrenceOutcomeBad)})
	schema := obj(map[string]any{
		"classifications": map[string]any{"type": "array", "items": obj(map[string]any{
			"occurrence_id": str, "cause": causes, "layer": layers, "target": str, "signal": str,
		}, []string{"occurrence_id", "cause", "layer", "target", "signal"})},
		"added": map[string]any{"type": "array", "items": obj(map[string]any{
			"outcome": outcomes, "kind": str, "evidence": str, "cause": causes, "layer": layers, "target": str, "signal": str,
		}, []string{"outcome", "kind", "evidence", "cause", "layer", "target", "signal"})},
		"memory": map[string]any{"type": "array", "items": obj(map[string]any{
			"mode":        enum(j6StringEnum(J6ModeValues)),
			"type":        enum(J6MemoryTypeValues),
			"slug":        str,
			"description": str,
			"confidence":  str,
			"sources":     map[string]any{"type": "array", "items": str},
			"outcome":     str,
			"target":      str,
			"signal":      str,
			"body":        str,
		}, []string{"mode", "type", "slug", "body"})},
		"followups": map[string]any{"type": "array", "items": obj(map[string]any{
			"title": str, "description": str, "done_criteria": str,
		}, []string{"title", "description", "done_criteria"})},
	}, []string{"classifications", "added", "memory", "followups"})
	b, _ := json.Marshal(schema)
	return b
}

func j6Blank(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }

func j6Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// validateJ6Output は J6 の出力を検査する。閉集合の外の値・必須項目の欠け・
// 同じ occurrence_id が classifications に 2 回現れる出力は ok=false（出力全体を不正とする）。
func validateJ6Output(data []byte) (*j6ValidatedOutput, bool) {
	var raw j6RawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	if raw.Classifications == nil || raw.Added == nil || raw.Memory == nil || raw.Followups == nil {
		return nil, false
	}
	out := &j6ValidatedOutput{}
	seen := map[string]bool{}
	for _, c := range *raw.Classifications {
		if j6Blank(c.OccurrenceID) || c.Target == nil || j6Blank(c.Signal) {
			return nil, false
		}
		cause, ok := j6ParseCause(c.Cause)
		if !ok {
			return nil, false
		}
		layer, ok := j6ParseLayer(c.Layer)
		if !ok {
			return nil, false
		}
		id := strings.TrimSpace(*c.OccurrenceID)
		if seen[id] {
			return nil, false
		}
		seen[id] = true
		out.Classifications = append(out.Classifications, J6Classification{
			OccurrenceID: id, Cause: cause, Layer: layer, Target: *c.Target, Signal: *c.Signal,
		})
	}
	for _, a := range *raw.Added {
		if a.Outcome == nil || j6Blank(a.Kind) || j6Blank(a.Evidence) || a.Target == nil || j6Blank(a.Signal) {
			return nil, false
		}
		outcome, ok := ParseOccurrenceOutcome(*a.Outcome)
		if !ok {
			return nil, false
		}
		cause, ok := j6ParseCause(a.Cause)
		if !ok {
			return nil, false
		}
		layer, ok := j6ParseLayer(a.Layer)
		if !ok {
			return nil, false
		}
		out.Added = append(out.Added, J6Added{
			Outcome: outcome, Kind: *a.Kind, Evidence: strings.TrimSpace(*a.Evidence),
			Cause: cause, Layer: layer, Target: *a.Target, Signal: *a.Signal,
		})
	}
	for _, m := range *raw.Memory {
		if m.Mode == nil || m.Type == nil || m.Slug == nil || m.Body == nil {
			return nil, false
		}
		if !slices.Contains(J6ModeValues, J6Mode(*m.Mode)) {
			return nil, false
		}
		if !slices.Contains(J6MemoryTypeValues, *m.Type) {
			return nil, false
		}
		if *m.Type == MemoryTypeExperience && (j6Blank(m.Outcome) || j6Blank(m.Target) || j6Blank(m.Signal)) {
			return nil, false
		}
		item := memoryItem{
			Mode: *m.Mode, Type: *m.Type, Slug: *m.Slug, Description: j6Str(m.Description),
			Confidence: j6Str(m.Confidence), Outcome: j6Str(m.Outcome), Target: j6Str(m.Target),
			Signal: j6Str(m.Signal), Body: *m.Body,
		}
		if m.Sources != nil {
			item.Sources = *m.Sources
		}
		out.Memory = append(out.Memory, item)
	}
	for _, f := range *raw.Followups {
		if j6Blank(f.Title) || f.Description == nil || f.DoneCriteria == nil {
			return nil, false
		}
		out.Followups = append(out.Followups, J6Followup{Title: *f.Title, Description: *f.Description, DoneCriteria: *f.DoneCriteria})
	}
	return out, true
}

func j6ParseCause(p *string) (OccurrenceCause, bool) {
	if p == nil {
		return "", false
	}
	return ParseOccurrenceCause(*p)
}

func j6ParseLayer(p *string) (OccurrenceLayer, bool) {
	if p == nil {
		return "", false
	}
	return ParseOccurrenceLayer(*p)
}
