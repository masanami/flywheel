package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// このファイルは仕様の文書の読み取りと、受入基準の抜き出し・識別を持つ。

// maxSpecDocumentBytes は読む仕様の文書の大きさの上限。
const maxSpecDocumentBytes = 2 << 20

// acceptanceHeading は受入基準の節の見出し。行末の空白のほかは、ちょうどこの行だけを節の始まりにする。
const acceptanceHeading = "## 受入基準"

var (
	// errSpecUnreadable は仕様の文書を読めないことを表す。下の理由の誤りも、readSpecCriteria が返すときはこれを包む。
	errSpecUnreadable  = errors.New("the spec document cannot be read")
	errSpecTooLarge    = errors.New("the spec document is larger than 2 MiB")
	errSpecNoCriteria  = errors.New("the spec document has no acceptance criteria")
	errSpecDuplicateID = errors.New("different acceptance criteria in the spec document have the same ID")
)

// acceptanceCriterion は仕様の文書の受入基準 1 件。Text は空白を正規化した文。
type acceptanceCriterion struct {
	ID   string
	Text string
}

// acceptanceCriterionID は、空白を正規化した文から基準の識別を作る。
func acceptanceCriterionID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "AC-" + hex.EncodeToString(sum[:4])
}

// extractAcceptanceCriteria は文書の `## 受入基準` の節（次の `## ` の見出しの前まで。同じ見出しが複数あれば全部）から、
// 行頭がチェックの行の基準を、文書に出てくる順に返す。文が同じ行は 1 件にする。文が違うのに識別が同じ基準があれば
// errSpecDuplicateID。基準が無ければ空。
func extractAcceptanceCriteria(doc string) ([]acceptanceCriterion, error) {
	out := []acceptanceCriterion{}
	textByID := map[string]string{}
	inSection := false
	for _, line := range strings.Split(strings.ReplaceAll(doc, "\r", ""), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.TrimRight(line, " \t") == acceptanceHeading
			continue
		}
		if !inSection {
			continue
		}
		text, ok := checklistText(line)
		if !ok {
			continue
		}
		id := acceptanceCriterionID(text)
		if seen, dup := textByID[id]; dup {
			if seen != text {
				return nil, fmt.Errorf("%w: %s", errSpecDuplicateID, id)
			}
			continue
		}
		textByID[id] = text
		out = append(out, acceptanceCriterion{ID: id, Text: text})
	}
	return out, nil
}

// checklistText は、行頭がチェックの印の行の、正規化した文を返す。印の後ろには空白と文が要る。
func checklistText(line string) (string, bool) {
	for _, mark := range []string{"- [ ]", "- [x]", "- [X]"} {
		rest, ok := strings.CutPrefix(line, mark)
		if !ok {
			continue
		}
		if !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
			return "", false
		}
		text := strings.Join(strings.Fields(rest), " ")
		return text, text != ""
	}
	return "", false
}

// readSpecCriteria はワークスペースの作業ツリーの仕様の文書（場所 path）を読み、受入基準を返す。
// 場所は読むときにも検査する。読めない・大きすぎる・基準が無い・識別が重なる文書は errSpecUnreadable
// （理由の誤りも errors.Is で見分けられる）。
func readSpecCriteria(workspace, path string) ([]acceptanceCriterion, error) {
	criteria, err := loadSpecCriteria(workspace, path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errSpecUnreadable, path, err)
	}
	return criteria, nil
}

func loadSpecCriteria(workspace, path string) ([]acceptanceCriterion, error) {
	resolved, err := resolveSpecPath(workspace, path)
	if err != nil {
		return nil, err
	}
	// 通常のファイルでないもの（FIFO など）は、開くと止まりうるので先に断る。
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSpecDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSpecDocumentBytes {
		return nil, errSpecTooLarge
	}
	criteria, err := extractAcceptanceCriteria(string(data))
	if err != nil {
		return nil, err
	}
	if len(criteria) == 0 {
		return nil, errSpecNoCriteria
	}
	return criteria, nil
}
