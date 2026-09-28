package invoker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/masanami/flywheel/internal/core"
)

// claudeResponse は `claude -p --output-format json` の応答の envelope から、
// 判別・費用の抽出に使うフィールドだけを取り出す。判別には子が生成しない
// フィールド（subtype・is_error・structured_output の有無）と時間の上限・
// 起動の事実だけを使い、result（自由記述）は枠超過の判定にだけ使う
// （§結果の判別「子の自由記述〈result〉を判別に使わない」）。
type claudeResponse struct {
	SessionID        string          `json:"session_id"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// subtypeBudgetExhausted は §結果の判別の表の4行目が見る subtype の値。
const subtypeBudgetExhausted = "error_max_budget_usd"

// parseSingleJSONObject は data を JSON オブジェクト1つとして解釈する
// （§結果の判別「標準出力がJSONのオブジェクト1つとして解釈できない」の
// 判定本体）。null リテラル・末尾のゴミ（2つ目の値）・オブジェクト以外の
// 最上位の値（配列・スカラー）はすべて「解釈できない」として拒否する。
func parseSingleJSONObject(data []byte) (claudeResponse, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return claudeResponse{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var resp claudeResponse
	if err := dec.Decode(&resp); err != nil {
		return claudeResponse{}, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return claudeResponse{}, false // 末尾にゴミが残っている（2つ目のJSON値等）
	}
	return resp, true
}

// classifyOutcome は§結果の判別の表を上から評価する。launchErr・timedOut は
// 呼び出し（cmd.Run）の事実、stdout は子の標準出力。戻り値の *claudeResponse
// は、標準出力をJSONオブジェクト1つとして解釈できた場合だけ非nil
// （malformed・launch_failed・timed_outでは常にnil）。
func classifyOutcome(launchErr error, timedOut bool, stdout []byte) (core.RunResult, *claudeResponse) {
	if launchErr != nil {
		return core.RunResultLaunchFailed, nil
	}
	if timedOut {
		return core.RunResultTimedOut, nil
	}
	resp, ok := parseSingleJSONObject(stdout)
	if !ok {
		return core.RunResultMalformed, nil
	}
	if resp.Subtype == subtypeBudgetExhausted {
		return core.RunResultBudgetExhausted, &resp
	}
	if resp.IsError {
		return core.RunResultErrored, &resp
	}
	if !hasStructuredOutput(resp.StructuredOutput) {
		return core.RunResultInvalidOutput, &resp
	}
	return core.RunResultSucceeded, &resp
}

// hasStructuredOutput は structured_output が有効な値（欠落でも JSON null
// でもない）を持つかを返す（§結果の判別「structured_outputが無い…成功の
// 結果はinvalid_outputになる」。子の result 本文に verdict らしき値が
// 含まれていても、structured_output が無ければこの判定に影響しない＝
// 判別に子の自由記述を使わないことの担保）。
func hasStructuredOutput(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null"
}
