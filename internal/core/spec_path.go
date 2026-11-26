package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// このファイルは、課題の仕様の文書の場所（challenge.spec_path）の検査を持つ。場所はワークスペースから見た相対のパスで、
// 書き込むときと文書を読むときの両方でこの検査を通す。ファイルの有無は問わない。

var (
	errSpecPathAbsolute = errors.New("the spec path must be relative to the workspace")
	errSpecPathOutside  = errors.New("the spec path leads outside the workspace")
	errSpecPathInvalid  = errors.New("the spec path is not a usable file path")
)

// maxSymlinkHops は、場所の解決でたどるシンボリックリンクの数の上限（超えたら輪とみなす）。
const maxSymlinkHops = 40

// resolveSpecPath は場所 p を検査し、実体のパス（実在しない部分は字面のまま）を返す。
// 字面で `..` を畳んだ形と、書かれたとおりにリンクをたどってから `..` を数える形の、どちらもワークスペースの中でなければ
// 断る（読み手がどちらの数え方をしても外へ出ないように）。返すのは畳んだ形の解決の結果。
func resolveSpecPath(workspace, p string) (string, error) {
	if strings.TrimSpace(p) == "" || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return "", errSpecPathInvalid
	}
	if filepath.IsAbs(p) {
		return "", errSpecPathAbsolute
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errSpecPathOutside
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errSpecPathInvalid, err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return "", fmt.Errorf("%w: %w", errSpecPathInvalid, err)
	}
	var resolved string
	for _, form := range []string{p, clean} {
		if resolved, err = followPath(root, form); err != nil {
			return "", err
		}
		// ワークスペースの根そのものは文書を指さない。
		if !pathInside(root, resolved) || resolved == root {
			return "", errSpecPathOutside
		}
	}
	return resolved, nil
}

// followPath は実体のディレクトリ dir から rel を 1 要素ずつたどり、実在する所のシンボリックリンクをすべて解決した
// パスを返す。リンクの先がまだ無くても、先のパスまでたどる。実在しない要素は字面のまま足す。
// 確かめられない要素（検索の権限が無いなど）があれば誤りにする。
func followPath(dir, rel string) (string, error) {
	sep := string(filepath.Separator)
	cur, rest, hops := dir, strings.Split(rel, sep), 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, name)
		info, err := os.Lstat(next)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			cur = next
			continue
		case err != nil:
			return "", fmt.Errorf("%w: %w", errSpecPathInvalid, err)
		case info.Mode()&fs.ModeSymlink == 0:
			cur = next
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", fmt.Errorf("%w: too many symbolic links", errSpecPathInvalid)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errSpecPathInvalid, err)
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return cur, nil
}

// specPathValue は create・edit が受けた場所を検査し、記録する値を返す。空は「場所なし」（nil）。
// 検査の誤りは ErrValidation。記録するのは受けた文字列そのもの。
func specPathValue(workspace, p string) (*string, error) {
	if p == "" {
		return nil, nil
	}
	if _, err := resolveSpecPath(workspace, p); err != nil {
		return nil, fmt.Errorf("%w: spec: %w", ErrValidation, err)
	}
	return &p, nil
}
