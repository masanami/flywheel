package core

import (
	"fmt"
	"strings"
)

// EscapeControl は外部・LLM が書きうる値を人が読む 1 行へ埋め込めるように、改行・復帰・タブ・ESC などの制御文字と Unicode の行・段落の区切り・
// 双方向制御文字・ゼロ幅文字を見える形（\n・\x1b・\u2028 など）へ置き換える。keepLines が真なら改行とタブは残す。
// CLI の表示（escapeControl・escapeControlKeepLines）と、core が返す誤りの文が同じ直し方になるよう、規則はここに 1 つだけ置く。
func EscapeControl(s string, keepLines bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case keepLines && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == 0x2028 || r == 0x2029 || isBidiOrZeroWidth(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isBidiOrZeroWidth は、表示の並びを入れ替える双方向制御文字（U+202A〜202E・U+2066〜2069、
// U+200E・U+200F・U+061C）とゼロ幅文字（U+200B〜200D・U+2060・U+FEFF）かを返す。
// 見えないまま承認の根拠の読みを変えさせないため、見える形にする。
func isBidiOrZeroWidth(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200b && r <= 0x200f, r == 0x061c, r == 0x2060, r == 0xfeff:
		return true
	}
	return false
}
