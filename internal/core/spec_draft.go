package core

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// このファイルは、仕様の下書きの見分けと、置き換える元の文書の場所（challenge.spec_base_path）の検査を持つ。
// 下書きかどうかは仕様の文書の場所から導き、ストアには持たない。

// specDraftDir は仕様の下書きの置き場（ワークスペースから見た相対のパス）。
const specDraftDir = flywheelDirName + "/specs"

// maxSpecBasePathRunes は置き換える元の文書の場所の長さの上限（Unicode のコードポイントの数）。
const maxSpecBasePathRunes = 500

// isSpecDraftPath は仕様の文書の場所 p が下書きの置き場の下を指すかを返す。正規化した形で見分ける
// （置き場そのものは下書きでない）。
func isSpecDraftPath(p string) bool {
	return strings.HasPrefix(path.Clean(p), specDraftDir+"/")
}

// SpecIsDraft は課題の仕様の文書が下書きかを返す。仕様の文書の場所が無ければ偽。
func (c Challenge) SpecIsDraft() bool {
	return c.SpecPath != nil && isSpecDraftPath(*c.SpecPath)
}

// specBasePathValue は create・edit が受けた置き換える元の文書の場所（リポジトリの中の相対のパス）を検査し、
// 記録する値を返す。空は「場所なし」（nil）。どのリポジトリの文書かはここでは決まらないので、ファイルの有無は見ない。
// 検査の誤りは ErrValidation。記録するのは受けた文字列そのもの。
func specBasePathValue(p string) (*string, error) {
	if p == "" {
		return nil, nil
	}
	reject := func(reason string) (*string, error) {
		return nil, fmt.Errorf("%w: spec base: %w", ErrValidation, errors.New(reason))
	}
	// UTF-8 でない TEXT は書き出し（Export）が扱えない。
	if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return reject("the path is not a usable file path")
	}
	if utf8.RuneCountInString(p) > maxSpecBasePathRunes {
		return reject(fmt.Sprintf("the path is longer than %d characters", maxSpecBasePathRunes))
	}
	if path.IsAbs(p) {
		return reject("the path must be relative to the repository")
	}
	for _, elem := range strings.Split(p, "/") {
		if elem == ".." {
			return reject(`the path must not have a ".." element`)
		}
	}
	return &p, nil
}
