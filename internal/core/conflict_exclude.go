package core

// このファイルは repos[].conflict_exclude_paths（衝突の判定から外すパス）の形の検査・照合と、予測の出力から
// 外すパスのファイルを除く処理を持つ。どのパスを外すかはリポジトリの事情なので宣言が持ち、ここに特定のパスを
// 書かない。

import (
	"fmt"
	"path"
	"strings"
)

// validateExcludePattern は 1 つのパターンの形を検査する。形: `/` 区切りでリポジトリの根からの相対パス。
// 各要素は path.Match の形（`*`・`?`・`[...]`。`/` をまたがない）か、要素全体が `**`（0 個以上の要素に当たる）。
// 空・先頭が `/`・`\` を含む・空の要素・`.`／`..` の要素・`**` を他の文字と混ぜた要素・不正な文字クラスは拒否する。
func validateExcludePattern(p string) error {
	if p == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("must be relative to the repository root (no leading /)")
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf(`must not contain \`)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return fmt.Errorf("must not contain an empty path element")
		case seg == "." || seg == "..":
			return fmt.Errorf("must not contain %q", seg)
		case seg == "**":
		case strings.Contains(seg, "**"):
			return fmt.Errorf("** must be a whole path element, got %q", seg)
		default:
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("invalid pattern element %q: %w", seg, err)
			}
		}
	}
	return nil
}

// matchExcludePattern は検査済みのパターン pat が file（`/` 区切りの相対パス）に当たるかを返す。
func matchExcludePattern(pat, file string) bool {
	return matchSegments(strings.Split(pat, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, file []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return len(file) > 0 // `docs/**` は docs の下のファイルに当たる
			}
			for i := 0; i <= len(file); i++ {
				if matchSegments(rest, file[i:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], file[0]); err != nil || !ok {
			return false
		}
		pat, file = pat[1:], file[1:]
	}
	return len(file) == 0
}

// excludedByAny は file がいずれかのパターンに当たるか。
func excludedByAny(patterns []string, file string) bool {
	file = strings.TrimPrefix(file, "./")
	for _, p := range patterns {
		if matchExcludePattern(p, file) {
			return true
		}
	}
	return false
}

// excludeSharedFiles は予測の出力の pairs[].shared_files から、patterns に当たるファイルを除いた写しを返す
// （元の出力は変えない）。patterns が空なら out をそのまま返す。status・dependency は変えない。
func excludeSharedFiles(out *ConflictPredictionOutput, patterns []string) *ConflictPredictionOutput {
	if out == nil || len(patterns) == 0 {
		return out
	}
	cp := *out
	cp.Pairs = make([]PredictedPair, len(out.Pairs))
	for i, p := range out.Pairs {
		np := p
		np.SharedFiles = nil
		for _, f := range p.SharedFiles {
			if !excludedByAny(patterns, f.Path) {
				np.SharedFiles = append(np.SharedFiles, f)
			}
		}
		cp.Pairs[i] = np
	}
	return &cp
}
