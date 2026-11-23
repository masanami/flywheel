package core

// このファイルはスーパーバイザーの判断点（SV。区切り S4b-2）の出力の型・出力スキーマ・検査を持つ。
// 判断点は型つきの値を返すだけで、ストアを書かない。手の実行・段・入力の組み立ては後のタスク。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// SVOutput はスーパーバイザーの判断点の出力。
// Hand は選んだ手、Findings は調べたこと（必須）、Note は手に添える文、Basis は根拠の場所、Recommendation はオーナーへ上げるときの推奨
// （Hand が escalate のとき必須）、Options はオーナーへ上げるときに付ける選択肢（escalate のときだけ。無くてもよい）、Conditions は
// フォローアップへ回す条件の番号（入力の「回せる条件」の番号。split_followup のとき 1 件以上）。
// Answers は子の問いへの答え（Hand が answer のとき 1 件以上。問いの並びの順で、問いごとに 1 件）。
type SVOutput struct {
	Hand           SupervisorHand    `json:"hand"`
	Findings       string            `json:"findings"`
	Note           string            `json:"note"`
	Basis          []SupervisorBasis `json:"basis"`
	Recommendation string            `json:"recommendation"`
	Options        []SVOption        `json:"options"`
	Conditions     []int             `json:"conditions"`
	Answers        []SVAnswer        `json:"answers"`
}

// SVAnswer は子の問い 1 件への答え。Answer は、選択肢がある問いでは選んだ選択肢の見出し、無い問いでは回答の文。
// Basis は根拠の場所（core が、ワークスペースの根の下の通常のファイルであることを確かめる）。
type SVAnswer struct {
	Answer string            `json:"answer"`
	Basis  []SupervisorBasis `json:"basis"`
}

// SVOption はスーパーバイザーが付ける選択肢 1 つ（見出しと説明だけ。識別は core が並びの順に o1・o2… と振る）。
type SVOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// HoldOptions は選択肢を保留の選択肢へ写す（識別は空のまま。保留を作る入口が振る）。
func (o SVOutput) HoldOptions() []HoldOption {
	out := make([]HoldOption, 0, len(o.Options))
	for _, v := range o.Options {
		out = append(out, HoldOption{Label: v.Label, Description: v.Description})
	}
	return out
}

// svOptionsMin・svOptionsMax はスーパーバイザーが付ける選択肢の件数の範囲（スーパーバイザーの選択肢だけに当てる）。
const (
	svOptionsMin = 2
	svOptionsMax = 6
)

// ErrSVOutputInvalid は、スーパーバイザーの出力が形・必須の項目・閉集合の条件を満たさないことを表す。
var ErrSVOutputInvalid = fmt.Errorf("core: invalid supervisor judgment output")

// svOutputSchema は SV の `--json-schema` に渡すスキーマ。手の値は SupervisorHandValues から作る。
func svOutputSchema() []byte {
	hands := make([]string, 0, len(supervisorHandValues))
	for _, h := range supervisorHandValues {
		hands = append(hands, string(h))
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"hand":           map[string]any{"type": "string", "enum": hands},
			"findings":       map[string]any{"type": "string"},
			"note":           map[string]any{"type": "string"},
			"recommendation": map[string]any{"type": "string"},
			"basis": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"locator": map[string]any{"type": "string"},
					},
					"required":             []string{"path", "locator"},
					"additionalProperties": false,
				},
			},
			"options": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label":       map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
					},
					"required":             []string{"label", "description"},
					"additionalProperties": false,
				},
			},
			"conditions": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}},
			"answers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"answer": map[string]any{"type": "string"},
						"basis": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path":    map[string]any{"type": "string"},
									"locator": map[string]any{"type": "string"},
								},
								"required":             []string{"path", "locator"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"answer", "basis"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"hand", "findings", "note", "basis", "recommendation", "options", "conditions", "answers"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return b
}

// SVOutputSchema は SV の出力スキーマ（JSON）を返す。
func SVOutputSchema() []byte { return svOutputSchema() }

// ParseSVOutput はスーパーバイザーの出力の JSON を読んで検査する。allowed は入力に渡した選べる手の一覧で、hand がその外なら誤り。
// 検査: 未知のキー・手の閉集合の外・空の findings・手ごとの必須（reverify・send_back・replan・defer は note、escalate は recommendation、
// split_followup は conditions が 1 件以上で重ならない正の整数）・options は escalate のときだけ（選択肢の形は保留の選択肢と同じ決まり）。
// 誤りは ErrSVOutputInvalid（%w）。
func ParseSVOutput(data []byte, allowed []SupervisorHand) (*SVOutput, error) {
	var out SVOutput
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSVOutputInvalid, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrSVOutputInvalid)
	}
	if _, ok := ParseSupervisorHand(string(out.Hand)); !ok {
		return nil, fmt.Errorf("%w: unknown hand %q", ErrSVOutputInvalid, out.Hand)
	}
	permitted := false
	for _, h := range allowed {
		if h == out.Hand {
			permitted = true
		}
	}
	if !permitted {
		return nil, fmt.Errorf("%w: hand %q is not among the offered hands", ErrSVOutputInvalid, out.Hand)
	}
	if strings.TrimSpace(out.Findings) == "" {
		return nil, fmt.Errorf("%w: findings is required", ErrSVOutputInvalid)
	}
	switch out.Hand {
	case HandReverify, HandSendBack, HandReplan, HandDefer:
		if strings.TrimSpace(out.Note) == "" {
			return nil, fmt.Errorf("%w: note is required for %s", ErrSVOutputInvalid, out.Hand)
		}
	case HandEscalate:
		if strings.TrimSpace(out.Recommendation) == "" {
			return nil, fmt.Errorf("%w: recommendation is required for %s", ErrSVOutputInvalid, out.Hand)
		}
	case HandSplitFollowup:
		if strings.TrimSpace(out.Note) == "" || len(out.Conditions) == 0 {
			return nil, fmt.Errorf("%w: note and at least one condition are required for %s", ErrSVOutputInvalid, out.Hand)
		}
		seen := map[int]bool{}
		for _, n := range out.Conditions {
			if n < 1 || seen[n] {
				return nil, fmt.Errorf("%w: condition numbers must be distinct integers from 1", ErrSVOutputInvalid)
			}
			seen[n] = true
		}
	}
	switch {
	case out.Hand == HandAnswer && len(out.Answers) == 0:
		return nil, fmt.Errorf("%w: at least one answer is required for %s", ErrSVOutputInvalid, out.Hand)
	case out.Hand != HandAnswer && len(out.Answers) > 0:
		return nil, fmt.Errorf("%w: answers are only for %s", ErrSVOutputInvalid, HandAnswer)
	}
	for i, a := range out.Answers {
		if strings.TrimSpace(a.Answer) == "" {
			return nil, fmt.Errorf("%w: answer %d is empty", ErrSVOutputInvalid, i+1)
		}
	}
	if len(out.Options) > 0 {
		if out.Hand != HandEscalate {
			return nil, fmt.Errorf("%w: options are only for escalate", ErrSVOutputInvalid)
		}
		if n := len(out.Options); n < svOptionsMin || n > svOptionsMax {
			return nil, fmt.Errorf("%w: options must be %d to %d, got %d", ErrSVOutputInvalid, svOptionsMin, svOptionsMax, n)
		}
		if _, _, err := resolveHoldOptions(out.HoldOptions()); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSVOutputInvalid, err)
		}
	}
	return &out, nil
}
