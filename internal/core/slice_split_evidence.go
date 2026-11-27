package core

// このファイルは、分ける案の証跡のファイルの形と、書く先を確かめて書く処理を持つ。

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	errRequirementEvidenceNotDirectory = errors.New("the requirement evidence directory is not a directory")
	errRequirementEvidenceTargetLink   = errors.New("the evidence target is a symbolic link or not a regular file")
)

// sliceSplitEvidencePath は課題 cid を分ける案の証跡の場所（ワークスペースから見た相対のパス）。
func sliceSplitEvidencePath(cid int64) string {
	return requirementEvidenceDir + "/split-" + formatChallengeID(cid) + ".md"
}

// sliceSplitEvidenceContent は分ける案の証跡のファイルの中身。title・url は元の課題の題名と元の Issue の URL。
// 判断点が書いた値は 1 行にする（見出しと項目の行を増やさせない）。
func sliceSplitEvidenceContent(title, url, createdAt string, p sliceSplitProposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- 元の Issue: %s\n- 作った日時: %s\n\n## 分ける理由\n%s\n\n## 区切りの案\n",
		oneLine(title), url, createdAt, oneLine(p.Reason))
	for i, sl := range p.Slices {
		deps := sliceNumbers(sl.DependsOn, ", ")
		if deps == "" {
			deps = j10NoValue
		}
		fmt.Fprintf(&b, "### %d. %s\n- 一言で: %s\n- 位置づけ: %s\n- 要旨: %s\n- 範囲: %s\n- リポジトリ: %s\n- 前に済ませる区切り: %s\n\n",
			i+1, oneLine(sl.Title), oneLine(sl.Headline), oneLine(sl.Context), oneLine(sl.Summary), oneLine(sl.Scope), sl.Repo, deps)
	}
	b.WriteString("## 調べた根拠\n")
	for _, e := range p.Evidence {
		fmt.Fprintf(&b, "- %s（%s）\n", oneLine(e.Path), oneLine(e.Note))
	}
	return b.String()
}

// sliceNumbers は区切りの番号を sep でつなぐ。
func sliceNumbers(numbers []int, sep string) string {
	out := make([]string, len(numbers))
	for i, n := range numbers {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, sep)
}

// writeRequirementEvidence は証跡の置き場の直下の場所 evidencePath に content を書く（.gitignore に requirements/ の行が
// 無ければ先に足す）。書くのは、置き場の実体がワークスペースの実体の .flywheel/ の下のディレクトリで（無ければ作る）、
// 書く先のリンクをたどった先が置き場の実体の中にあり、書く先が無いか通常のファイルのときだけ。
// 別名のファイルへ書いてから置き換えるので、確かめた後に書く先がリンクに変わっても、リンクの先は書き換わらない。
func (s *Store) writeRequirementEvidence(evidencePath, content string) (err error) {
	base, err := flywheelPlacement(s.workspace, requirementEvidenceDir, errRequirementEvidenceOutside)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(base); err != nil {
		return err
	} else if !info.IsDir() {
		return errRequirementEvidenceNotDirectory
	}
	name := path.Base(evidencePath)
	resolved, err := followPath(base, name)
	if err != nil {
		return err
	}
	if resolved == base || !pathInside(base, resolved) {
		return errRequirementEvidenceOutside
	}
	dest := filepath.Join(base, name)
	switch info, err := os.Lstat(dest); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return errRequirementEvidenceTargetLink
	}
	if err := ensureGitignoreDir(s.workspace, path.Base(requirementEvidenceDir)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(base, ".evidence-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	_, werr := tmp.WriteString(content)
	if err := errors.Join(werr, tmp.Chmod(0o644), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
