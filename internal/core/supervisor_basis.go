package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// このファイルは、子の問いへの答えの根拠（basis の path）の検査を持つ（区切り S4b-3）。LLM を使わず、場所だけを確かめる。
// 根拠として数えるのは、リンクを解決した後の場所が、ワークスペースの根の下の通常のファイルであるもの。
// 数えないもの: ワークスペースの外・リンクの先が外・ディレクトリ・ストアの置き場（.flywheel/）の下・存在しない場所。
// 中身が合っているかは確かめない。
//
// 下書きの問いの答えの根拠は、規則が違う（specBasisCounts。対象のリポジトリの元のクローンは、置き場の下でも数える）。

// isStoreDirName は、パスの最初の要素 first がストアの置き場の名前かを返す。macOS の既定のファイルシステムは大文字と小文字を区別しないので、区別せずに比べる。
func isStoreDirName(first string) bool { return strings.EqualFold(first, flywheelDirName) }

// basisCounts は根拠の場所 path が、根拠として数えられるかを返す。root はワークスペースの根。
func basisCounts(root, path string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	if realRoot, err = filepath.Abs(realRoot); err != nil {
		return false
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	if isUnderStoreDir(root, target) {
		return false // 書かれた場所がストアの置き場の下（リンクの先に関わらず）
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false // 存在しない・解決できない
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false // 根の外
	}
	if first := strings.Split(rel, string(filepath.Separator))[0]; isStoreDirName(first) {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.Mode().IsRegular()
}

// countableBasis は basis のうち、根拠として数えられるものの件数を返す。
func countableBasis(root string, basis []SupervisorBasis) int {
	n := 0
	for _, b := range basis {
		if basisCounts(root, b.Path) {
			n++
		}
	}
	return n
}

// isUnderStoreDir は、書かれた場所 target（リンクを解決する前）が、root 直下のストアの置き場の下かを返す。
func isUnderStoreDir(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return isStoreDirName(strings.Split(rel, string(filepath.Separator))[0])
}

// specBasisScope は下書きの問いの根拠の場所を分ける、宣言から作る場所の一覧（どれもワークスペースから見た相対のパス）。
type specBasisScope struct {
	// TargetBase は記録の対象のリポジトリの元のクローン（slots.base）。
	TargetBase string
	// Excluded は数えない場所: 対象でないリポジトリの元のクローンと、どのリポジトリ（対象を含む）のタスクの作業場所（slots.paths）。
	Excluded []string
}

// specBasisScopeFor は、宣言 conn で対象のリポジトリが target のときの specBasisScope を返す。
func specBasisScopeFor(conn *ConnectorsDeclaration, target string) specBasisScope {
	var out specBasisScope
	if conn == nil {
		return out
	}
	for _, r := range conn.Repos {
		if r.Name == target {
			out.TargetBase = r.Slots.Base
		} else if r.Slots.Base != "" {
			out.Excluded = append(out.Excluded, r.Slots.Base)
		}
		out.Excluded = append(out.Excluded, r.Slots.Paths...)
	}
	return out
}

// specBasisCounts は、下書きの問いの答えの根拠の場所 path が、根拠として数えられるかを返す。root はワークスペースの根。
// 数えるのは、リンクを解決した後の場所が、ワークスペースの中の通常のファイルであるものだけ。次の順に決める（順を変えない）:
// ①対象のリポジトリの元のクローンの下は、.git の下でなければ数える ②scope.Excluded の下は数えない
// ③ストアの置き場の下は数えない ④そのほかは数える。
// 場所の内外は、親のディレクトリが同じファイルかで比べる（大文字と小文字を区別しないファイルシステムで、書き方を変えて抜けさせない）。
func specBasisCounts(root string, scope specBasisScope, path string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	inRoot := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	resolved, err := filepath.EvalSymlinks(inRoot(path))
	if err != nil {
		return false
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return false
	}
	if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
		return false
	}
	if _, ok := partsBelow(resolved, root); !ok {
		return false
	}
	if scope.TargetBase != "" {
		if parts, ok := partsBelow(resolved, inRoot(scope.TargetBase)); ok {
			return !slices.ContainsFunc(parts, func(p string) bool { return strings.EqualFold(p, ".git") })
		}
	}
	for _, dir := range append(slices.Clone(scope.Excluded), flywheelDirName) {
		if _, ok := partsBelow(resolved, inRoot(dir)); ok {
			return false
		}
	}
	return true
}

// partsBelow は、リンクを解決した絶対パス resolved がディレクトリ dir の下なら、dir からの要素の並びを返す。
func partsBelow(resolved, dir string) ([]string, bool) {
	want, err := os.Stat(dir)
	if err != nil || !want.IsDir() {
		return nil, false
	}
	var parts []string
	for cur := resolved; ; {
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, false
		}
		parts = append([]string{filepath.Base(cur)}, parts...)
		if info, err := os.Stat(parent); err == nil && os.SameFile(info, want) {
			return parts, true
		}
		cur = parent
	}
}
