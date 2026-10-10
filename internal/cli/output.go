package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/masanami/flywheel/internal/view"
)

// textOutput は、--json 無しのときに人間向けのテキストを持つ成功時のデータ。
// JSON には json を、テキストには text（末尾に改行を含む）をそのまま使う。
// テキストの形式は安定を保証しない（docs/features/m1-core.md「--json なしの
// 出力は人間向けで、形式の安定を保証しない」）。
type textOutput struct {
	json any
	text string
}

// errorEnvelope は --json 失敗時に標準エラーへ出す固定の形。
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// writeJSONSuccess は v を JSON エンコードして w へ書く。
//
// docs/features/m1-core.md 追記節「成功時の JSON 出力の規約」に従い:
//   - UTF-8・HTML エスケープなし・末尾に改行 1 つ
//   - 最上位は必ずオブジェクト（配列・スカラーを最上位にしない）
//
// v がオブジェクトへマーシャルされない場合はプログラマの誤りとみなし、
// 何も書き込まずにエラーを返す（呼び出し側はこれを internal_error として扱う）。
func writeJSONSuccess(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(buf.Bytes())
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("cli: success payload must marshal to a top-level JSON object, got %T", v)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// writeJSONError は {"error":{"code":"…","message":"…"}} を w（標準エラー）へ書く。
func writeJSONError(w io.Writer, code ErrorCode, message string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(errorEnvelope{Error: errorBody{Code: code, Message: message}}); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// writeTextError は --json 無しの失敗時に標準エラーへ出す人間向けの 1 行を書く。
func writeTextError(w io.Writer, code ErrorCode, message string) {
	_, _ = fmt.Fprintf(w, "flywheel: %s (%s)\n", message, code)
}

// FormatTimestamp は t を UTC・ミリ秒固定の RFC 3339 文字列にする
// （docs/features/m1-core.md 追記節「成功時の JSON 出力の規約」§日時）。
func FormatTimestamp(t time.Time) string {
	return view.FormatTimestamp(t)
}
