package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// HoldOption は保留の選択肢 1 つ。ID は保留の中で重ならない識別、Label は見出し、Description は説明（無くてもよい）。
// JSON のキーの並びはストアの options 列にそのまま入る。
type HoldOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// HoldOptionsInput は保留を作る入口が受ける、選択肢と止まり方の指定。Options は 0 件以上（件数の決まりは当てない）。
// ID が空の選択肢には、core が並びの順に o1・o2… を振る。Required は「識別の無い回答を断る」印（S4b の作り手は使わない）。
// StopKind は止まり方（スーパーバイザーを呼ぶきっかけ。保留を作る呼び出し元が渡す。空は渡さない）、SupervisorEnabled は
// 宣言の supervisor の値（呼び出し元が渡す）。StopKind があり SupervisorEnabled が真のときだけ、保留はスーパーバイザー待ち（pending）になる。
//
// 止まり方が child_question のとき、保留はスーパーバイザー待ちにならない（SupervisorEnabled を見ない。子の問いは保留を作る前に判断点を呼ぶ）。
// Escalation があれば、スーパーバイザーが上げた保留（supervisor_state が escalated）として作る（子の問いだけ。判断点を呼ばずに上げるときは付けない）。
// 答えによる再開の上限に達しているときは上限が優先で、Escalation は使わない（印は NULL・対処の記録は指さない。呼び出し元は上限を確かめてから判断点を呼ぶ）。
type HoldOptionsInput struct {
	Options           []HoldOption
	Required          bool
	StopKind          StopKind
	SupervisorEnabled bool
	Escalation        *HoldEscalation
}

// HoldEscalation は、子の問いをスーパーバイザーが上げた保留に残す値（調べたこと・推奨・対処の記録の ID）。
// FailedQuestion が真なら、問いの先頭へ「判断できなかった」の固定の 1 行を付ける（対処の記録の結果が failed のとき）。
type HoldEscalation struct {
	Findings       string
	Recommendation string
	ActionID       int64
	FailedQuestion bool
}

// ErrHoldOptionInvalid は、保留を作る入口が受けた選択肢が誤っている（識別の重なり・形の不正・見出しが空か複数行）ことを示す。
// core の中の作り手の誤りであり、利用者の入力の誤りではない。保留は作られない。
var ErrHoldOptionInvalid = errors.New("core: invalid hold option")

var holdOptionIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// holdOptionsFromLabels は委譲先が返した選択肢の文を、保留の選択肢にする（見出しは文そのまま、識別は core が振る。件数は絞らない）。
// 見出しにできない文（空・複数行）が 1 つでもあれば選択肢を持たせない（問いの文には残る。保留が作れなくなるよりよい）。
func holdOptionsFromLabels(labels []string) HoldOptionsInput {
	var in HoldOptionsInput
	for _, o := range labels {
		if strings.TrimSpace(o) == "" || strings.ContainsAny(o, "\r\n") {
			return HoldOptionsInput{}
		}
		in.Options = append(in.Options, HoldOption{Label: o})
	}
	return in
}

// resolveHoldOptions は選択肢の識別を確定し（空の識別へ並びの順に o<位置> を振る）、検査して、options 列へ入れる JSON を返す。
// 選択肢が 0 件なら nil（NULL）。
func resolveHoldOptions(in []HoldOption) ([]HoldOption, *string, error) {
	if len(in) == 0 {
		return nil, nil, nil
	}
	out := make([]HoldOption, len(in))
	seen := make(map[string]bool, len(in))
	for i, o := range in {
		if o.ID == "" {
			o.ID = fmt.Sprintf("o%d", i+1)
		}
		if !holdOptionIDPattern.MatchString(o.ID) {
			return nil, nil, fmt.Errorf("%w: id %q (options[%d]) must be 1-32 chars of a-z, 0-9, _ or -", ErrHoldOptionInvalid, o.ID, i)
		}
		if seen[o.ID] {
			return nil, nil, fmt.Errorf("%w: duplicate id %q", ErrHoldOptionInvalid, o.ID)
		}
		seen[o.ID] = true
		if strings.TrimSpace(o.Label) == "" || strings.ContainsAny(o.Label, "\r\n") {
			return nil, nil, fmt.Errorf("%w: label of %q must be a non-empty single line", ErrHoldOptionInvalid, o.ID)
		}
		out[i] = o
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	s := string(b)
	return out, &s, nil
}

// parseHoldOptions は options 列の JSON を読む。NULL（空）なら nil。
func parseHoldOptions(raw *string) ([]HoldOption, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var opts []HoldOption
	if err := json.Unmarshal([]byte(*raw), &opts); err != nil {
		return nil, fmt.Errorf("hold.options is not valid JSON: %w", err)
	}
	return opts, nil
}

// jsonMarshalOptions は選択肢の並びを options 列へ入れる JSON にする。
func jsonMarshalOptions(opts []HoldOption) (string, error) {
	b, err := json.Marshal(opts)
	return string(b), err
}
