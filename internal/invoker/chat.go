package invoker

// このファイルは、対話の欄の会話の実行（chat_conversation.go）が使う共通の部品を持つ。

import (
	"bytes"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// maxChatStderr は保持する標準エラーの上限（バイト）。
const maxChatStderr = 64 << 10

// sessionLostMarker は、続きの会話が実行エンジンに無いときの出力に含まれる文言。
const sessionLostMarker = "No conversation found"

// chatEnv は base に add を重ねた環境を返す（add にある名前は base のものを置き換える）。委譲の目印は、
// 親の環境にあっても子へ渡さない。
// 対話の欄の印も親の環境のものは渡さない（子の印は add が持つものだけ）。
func chatEnv(base, add []string) []string {
	override := make(map[string]bool, len(add))
	for _, kv := range add {
		if i := strings.IndexByte(kv, '='); i > 0 {
			override[kv[:i]] = true
		}
	}
	env := make([]string, 0, len(base)+len(add))
	for _, kv := range withoutDelegatedMarker(base) {
		if i := strings.IndexByte(kv, '='); i > 0 && override[kv[:i]] {
			continue
		}
		if isChatMark(kv) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, add...)
}

// isChatMark は kv が対話の欄の印（過去の名前を含む）かを返す。
func isChatMark(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	return name == core.ChatEnvSession || name == core.ChatEnvMessage || name == core.ChatEnvTools
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

// limitedBuffer は先頭 maxChatStderr バイトだけを保持する io.Writer。
type limitedBuffer struct{ buf bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxChatStderr - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
