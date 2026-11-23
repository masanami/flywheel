package core

import (
	"os"
	"path/filepath"
	"strings"
)

// このファイルは、子の問いへの答えの根拠（basis の path）の検査を持つ（区切り S4b-3）。LLM を使わず、場所だけを確かめる。
// 根拠として数えるのは、リンクを解決した後の場所が、ワークスペースの根の下の通常のファイルであるもの。
// 数えないもの: ワークスペースの外・リンクの先が外・ディレクトリ・ストアの置き場（.flywheel/）の下・存在しない場所。
// 中身が合っているかは確かめない。

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
