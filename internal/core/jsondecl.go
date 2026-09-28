package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// このファイルは .flywheel/*.json の宣言ファイル（sources.json・agent.json・
// connectors.json）が共有する、JSON の厳格な読み方の部品を持つ。宣言ごとの
// キーの閉集合・既定値・意味的な検証（重複・閉集合外の値など）は各宣言の
// ファイル（sources.go・agent_declaration.go・connectors_declaration.go）に残す。

// decodeStrictJSONObject は data を単一の JSON オブジェクトとして解釈し、
// 最上位のキーを生の json.RawMessage のまま返す。末尾にゴミが残っている場合
// （例: "{}garbage"・"{}}"・2つ目の JSON 値）も「解釈できない JSON」として
// 拒否する。dec.More() は次のトークンが '}'/']' のとき false を返してしまう
// ため使わず、次のトークンを読んで io.EOF になることを確認する
// （sources.go の self-review 指摘と同じ規則。3つの宣言ファイルの読み込みが
// この関数を共有することで、規則の重複管理を避ける）。
func decodeStrictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	// 最上位が JSON の null（ファイルの中身がそのまま "null" 等）だと、
	// map への Decode はエラーにならず raw=nil を返してしまう
	// （self-review 指摘: agent.json 全体が "null" でもエラーにならず、
	// 全既定値で静かに読めてしまう）。JSON オブジェクトでない最上位の値
	// （null を含む）は解釈できない JSON として拒否する。
	if isJSONNull(data) {
		return nil, fmt.Errorf("must be a JSON object, got null")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing content after the JSON value")
	}
	return raw, nil
}

// isJSONNull は raw（前後の空白を除く）が JSON のリテラル null と完全一致
// することを返す。encoding/json の Unmarshal は null を「対象を変えない」と
// 扱う（非ポインタのスカラー値はそのまま・map/slice は nil のまま）ため、
// 「キーの省略」と「明示的な null」を区別したい呼び出し側（宣言ファイルの
// 型違いを fail-closed に拒否したい箇所）は、Unmarshal の前にこの関数で
// 明示的な null を検査する（self-review 指摘: size_budgets_usd・
// judgment_budget_usd・timeout_sec・position_file・connectors.json の
// connectors/repos・操作の interactive/child_may_decide/artifacts に
// null を与えると、省略と同じ既定値へ静かにフォールバックしていた）。
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// rejectUnknownKeys は raw のキーがすべて known（大文字小文字の完全一致）に
// 含まれることを確認する。含まれないキーがあれば、そのキーを含むエラーを
// context を付けて返す（fail-closed。encoding/json の DisallowUnknownFields は
// フィールド名の大文字小文字を無視して照合するため、大文字違いの重複キーを
// 素通りさせてしまう。sources.go の self-review 指摘と同じ理由でここでも
// 生のキー集合を完全一致で検査する）。
func rejectUnknownKeys(raw map[string]json.RawMessage, known map[string]bool, context string) error {
	for k := range raw {
		if !known[k] {
			return fmt.Errorf("%s: unknown key %q", context, k)
		}
	}
	return nil
}

// decodeJSONNumber は raw を JSON の数値リテラルとして解釈する。JSON の文字列
// リテラル（例: `"30"`）は、json.Number へのデコードがそのまま成功してしまう
// ため（encoding/json の実装確認済み）、ここで先頭バイトが `"` の場合を明示的に
// 拒否する（型違いの金額・整数値を fail-closed に拒否するための前提）。
func decodeJSONNumber(raw json.RawMessage) (json.Number, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '"' {
		return "", fmt.Errorf("must be a JSON number, got %s", trimmed)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	return n, nil
}
