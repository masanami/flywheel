package core

// このファイルは J8（改修の提案）の出力スキーマと、出力そのものだけで決まる形・閉集合の検査を持つ。
//
// ここで見るのは出力の形だけである。入力の明細に載せた発生か・並行する J8 の再確認は
// 呼び出し側の検査で、ここでは行わない。違反が 1 つでもあれば出力全体を不正とする
// （提案を部分的に写さない）。performance の「待つ」は core が写像で付けるので、出力は waits を持たない。

import (
	"encoding/json"
	"slices"
	"strings"
)

// J8Metric は J8 の提案が改善の効果を測る指標の閉集合。
type J8Metric string

// J8Metric の4値。
const (
	J8MetricRejectionRate    J8Metric = "rejection_rate"
	J8MetricResumeCount      J8Metric = "resume_count"
	J8MetricCostPerChallenge J8Metric = "cost_per_challenge"
	J8MetricApprovalCount    J8Metric = "approval_count"
)

// J8MetricValues は J8Metric の全値。
var J8MetricValues = []J8Metric{
	J8MetricRejectionRate, J8MetricResumeCount, J8MetricCostPerChallenge, J8MetricApprovalCount,
}

// J8Action は J8 の提案の種類（足す・減らす）の閉集合。
type J8Action string

// J8Action の2値。
const (
	J8ActionAdd    J8Action = "add"
	J8ActionReduce J8Action = "reduce"
)

// J8ActionValues は J8Action の全値。
var J8ActionValues = []J8Action{J8ActionAdd, J8ActionReduce}

// J8LayerValues は J8 の提案の層の閉集合（none を含まない）。
var J8LayerValues = []OccurrenceLayer{
	OccurrenceLayerPrompt, OccurrenceLayerConnectorSkill, OccurrenceLayerEntrySkill, OccurrenceLayerKnowledge,
}

// J8CauseValues は J8 の提案の原因の閉集合（performance・none を含まない）。
var J8CauseValues = []OccurrenceCause{OccurrenceCauseContext, OccurrenceCausePermission}

// J8MaxProposals は 1 回の出力で許す提案の件数の上限。
const J8MaxProposals = 3

// J8Proposal は検査を通った提案 1 件。
type J8Proposal struct {
	Layer         OccurrenceLayer
	Target        string
	Cause         OccurrenceCause
	OccurrenceIDs []string
	Metric        J8Metric
	Expected      string
	Action        J8Action
	Title         string
	DoneCriteria  string
}

// Key は提案の改修の鍵（層と、前後の空白を除いた対象の完全一致）。
func (p J8Proposal) Key() string { return string(p.Layer) + "\x00" + p.Target }

type j8RawProposal struct {
	Layer         *string   `json:"layer"`
	Target        *string   `json:"target"`
	Cause         *string   `json:"cause"`
	OccurrenceIDs *[]string `json:"occurrence_ids"`
	Metric        *string   `json:"metric"`
	Expected      *string   `json:"expected"`
	Action        *string   `json:"action"`
	Title         *string   `json:"title"`
	DoneCriteria  *string   `json:"done_criteria"`
}

type j8RawOutput struct {
	Proposals *[]j8RawProposal `json:"proposals"`
}

// j8OutputSchema は J8 の `--json-schema` に渡すスキーマ。閉集合の値は各閉集合の定義から作る。
func j8OutputSchema() []byte {
	str := map[string]any{"type": "string"}
	nonEmpty := map[string]any{"type": "string", "minLength": 1}
	enum := func(vals []string) map[string]any { return map[string]any{"type": "string", "enum": vals} }
	props := map[string]any{
		"layer":          enum(j6StringEnum(J8LayerValues)),
		"target":         nonEmpty,
		"cause":          enum(j6StringEnum(J8CauseValues)),
		"occurrence_ids": map[string]any{"type": "array", "items": str, "minItems": 1, "uniqueItems": true},
		"metric":         enum(j6StringEnum(J8MetricValues)),
		"expected":       nonEmpty,
		"action":         enum(j6StringEnum(J8ActionValues)),
		"title":          nonEmpty,
		"done_criteria":  nonEmpty,
	}
	required := []string{"layer", "target", "cause", "occurrence_ids", "metric", "expected", "action", "title", "done_criteria"}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"proposals"},
		"properties": map[string]any{
			"proposals": map[string]any{"type": "array", "maxItems": J8MaxProposals, "items": map[string]any{
				"type": "object", "properties": props, "required": required, "additionalProperties": false,
			}},
		},
	}
	b, _ := json.Marshal(schema)
	return b
}

// validateJ8Output は J8 の出力を検査する。項目の欠落・閉集合の外の値・layer=none・cause が
// performance か none・対象・期待・題名・完了条件が空白だけ・同じ発生の ID が 2 つの提案に
// 現れる・同じ改修の鍵の提案が 2 つ・提案が 4 件以上のいずれかなら ok=false（出力全体を不正とする）。提案が 3 件までは可。
func validateJ8Output(data []byte) ([]J8Proposal, bool) {
	var raw j8RawOutput
	if err := json.Unmarshal(data, &raw); err != nil || raw.Proposals == nil {
		return nil, false
	}
	if len(*raw.Proposals) > J8MaxProposals {
		return nil, false
	}
	out := make([]J8Proposal, 0, len(*raw.Proposals))
	seenIDs := map[string]bool{}
	seenKeys := map[string]bool{}
	for _, p := range *raw.Proposals {
		if p.Layer == nil || p.Target == nil || p.Cause == nil || p.OccurrenceIDs == nil || p.Metric == nil ||
			p.Expected == nil || p.Action == nil || p.Title == nil || p.DoneCriteria == nil {
			return nil, false
		}
		layer := OccurrenceLayer(*p.Layer)
		if !slices.Contains(J8LayerValues, layer) {
			return nil, false
		}
		cause := OccurrenceCause(*p.Cause)
		if !slices.Contains(J8CauseValues, cause) {
			return nil, false
		}
		metric := J8Metric(*p.Metric)
		if !slices.Contains(J8MetricValues, metric) {
			return nil, false
		}
		action := J8Action(*p.Action)
		if !slices.Contains(J8ActionValues, action) {
			return nil, false
		}
		target := strings.TrimSpace(*p.Target)
		if target == "" || strings.TrimSpace(*p.Expected) == "" || strings.TrimSpace(*p.Title) == "" || strings.TrimSpace(*p.DoneCriteria) == "" || len(*p.OccurrenceIDs) == 0 {
			return nil, false
		}
		ids := make([]string, 0, len(*p.OccurrenceIDs))
		inThis := map[string]bool{}
		for _, id := range *p.OccurrenceIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, false
			}
			// 同じ発生は 2 つの提案に現れない（同じ提案の中の重複も不正）。
			if seenIDs[id] || inThis[id] {
				return nil, false
			}
			inThis[id] = true
			ids = append(ids, id)
		}
		for id := range inThis {
			seenIDs[id] = true
		}
		prop := J8Proposal{
			Layer: layer, Target: target, Cause: cause, OccurrenceIDs: ids, Metric: metric,
			Expected: *p.Expected, Action: action, Title: *p.Title, DoneCriteria: *p.DoneCriteria,
		}
		if seenKeys[prop.Key()] {
			return nil, false
		}
		seenKeys[prop.Key()] = true
		out = append(out, prop)
	}
	return out, true
}
