package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// FingerprintVersion は fingerprint の算式の版（現行 ingest-fp.sh と同じ値を
// 出す算式が版2。docs/features/m2-github-issue-ingest.md §fingerprint）。
const FingerprintVersion = 2

// asciiWhitespaceCutset は正規化の 3 番目の規則（全体の先頭・末尾の ASCII 空白の
// 削除）が対象にする文字集合（スペース・タブ・LF・VT・FF）。
//
// strings.TrimSpace は使わない: unicode.IsSpace に基づくため、Unicode の空白
// （例: U+3000 全角空白・U+00A0）まで削ってしまい、AC-65（末尾の全角空白の
// 有無で fingerprint が変わること）を満たせなくなる
// （§クリティカル設計決定 2「実装上の落とし穴」）。
const asciiWhitespaceCutset = " \t\n\v\f"

// normalizeFingerprintInput は本文を fingerprint 用に正規化する。
// 1. CR（U+000D）をすべて削除する（CRLF・孤立 CR のどちらも対象）
// 2. 各行の行末のスペース・タブを削除する
// 3. 全体の先頭・末尾の ASCII 空白を削除する
// それ以外（行中の空白・インデント・空行・引用符・大文字小文字・Unicode の空白）は
// 一切変更しない。
func normalizeFingerprintInput(body string) string {
	noCR := strings.ReplaceAll(body, "\r", "")

	lines := strings.Split(noCR, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	joined := strings.Join(lines, "\n")

	return strings.Trim(joined, asciiWhitespaceCutset)
}

// fingerprintVersionPrefix は既知の版（FingerprintVersion）のプレフィックス
// （"2:"）。hasKnownFingerprintVersion が使う。
var fingerprintVersionPrefix = fmt.Sprintf("%d:", FingerprintVersion)

// hasKnownFingerprintVersion は fp（source_binding.fingerprint に記録された
// "<版>:<値>"）の版が FingerprintVersion と一致するかを判定する
// （docs/features/m2-github-issue-ingest.md §冪等な作成と更新「記録された
// fingerprint の版が2以外なら…fail-closed」）。版が違う・区切りが無い・空文字列は
// いずれも false（更新しない側＝fail-closed に倒す）。
func hasKnownFingerprintVersion(fp string) bool {
	return strings.HasPrefix(fp, fingerprintVersionPrefix)
}

// Fingerprint は Issue の本文（それだけを入力に。タイトル・ラベル・課題に書いた
// 値は入力にしない）から fingerprint を算出し、"<版>:<12桁小文字hex>" の形で
// 返す（docs/features/m2-github-issue-ingest.md §fingerprint。現行 ingest-fp.sh
// と同じ値。QH3）。
func Fingerprint(body string) string {
	normalized := normalizeFingerprintInput(body)
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%d:%s", FingerprintVersion, hex.EncodeToString(sum[:])[:12])
}
