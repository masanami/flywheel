package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeSourcesJSON はワークスペース dir/.flywheel/sources.json に content を書き、
// dir を返す（呼び出し側が渡した t.TempDir() 配下）。
func writeSourcesJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	fwDir := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fwDir, err)
	}
	if err := os.WriteFile(filepath.Join(fwDir, "sources.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write sources.json: %v", err)
	}
	return dir
}

// --- AC-3〜AC-11: 宣言の検証 ---

func TestLoadSourcesDeclaration_FileNotFound(t *testing.T) {
	dir := t.TempDir() // .flywheel/sources.json を作らない
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigNotFound", err)
	}
}

func TestLoadSourcesDeclaration_UnparseableJSON(t *testing.T) {
	dir := writeSourcesJSON(t, `{ this is not json `)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_TrailingContentAfterJSONValue(t *testing.T) {
	dir := writeSourcesJSON(t, `{"version":1,"sources":[]}garbage`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_UnknownTopLevelKey(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [],
		"unknown_top_level_key": true
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_UnknownSourceElementKey(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"], "unknown_field": 1}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_TypeMustBeGitHubIssue(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "notion", "repos": ["o/r"]}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_AssigneePolicyMustBeInClosedSet(t *testing.T) {
	cases := []string{"", "SELF-ONLY", "everyone"}
	for _, policy := range cases {
		t.Run(policy, func(t *testing.T) {
			dir := writeSourcesJSON(t, fmt.Sprintf(`{
				"version": 1,
				"sources": [
					{"id": "a", "type": "github-issue", "repos": ["o/r"], "assignee_policy": %q}
				]
			}`, policy))
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadSourcesDeclaration_IDMustMatchPattern(t *testing.T) {
	cases := []string{"Foo", "-foo", "", "foo_bar", "foo bar"}
	for _, id := range cases {
		t.Run(fmt.Sprintf("id=%q", id), func(t *testing.T) {
			dir := writeSourcesJSON(t, fmt.Sprintf(`{
				"version": 1,
				"sources": [
					{"id": %q, "type": "github-issue", "repos": ["o/r"]}
				]
			}`, id))
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadSourcesDeclaration_DuplicateID(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r1"]},
			{"id": "a", "type": "github-issue", "repos": ["o/r2"]}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_ReposMustBeNonEmpty(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": []}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_ReposMustMatchOwnerSlashName(t *testing.T) {
	cases := []string{"no-slash", "o/n/extra", "/name", "owner/", "o r/name"}
	for _, repo := range cases {
		t.Run(fmt.Sprintf("repo=%q", repo), func(t *testing.T) {
			dir := writeSourcesJSON(t, fmt.Sprintf(`{
				"version": 1,
				"sources": [
					{"id": "a", "type": "github-issue", "repos": [%q]}
				]
			}`, repo))
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadSourcesDeclaration_SameRepoInTwoSources(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"]},
			{"id": "b", "type": "github-issue", "repos": ["o/r"]}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_UrgencyLabelsMustBeInClosedSet(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"], "urgency_labels": {"priority:high": "urgent"}}
		]
	}`)
	_, err := LoadSourcesDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadSourcesDeclaration_VersionMustBeOne(t *testing.T) {
	cases := []string{
		`{"version": 2, "sources": []}`,
		`{"sources": []}`,                 // version 省略 = 0
		`{"version": "1", "sources": []}`, // 型違い
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeSourcesJSON(t, doc)
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// AC-11: assignee_policy を省略した取り込み元は exclude-others として振る舞う。
func TestLoadSourcesDeclaration_AssigneePolicyDefaultsToExcludeOthers(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"]}
		]
	}`)
	decl, err := LoadSourcesDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want nil", err)
	}
	if got := decl.Sources[0].Policy(); got != AssigneePolicyExcludeOthers {
		t.Fatalf("Policy() = %q, want %q", got, AssigneePolicyExcludeOthers)
	}
}

// 仕様（§IF / API）に載っている例をそのまま解釈できることを確認する。
func TestLoadSourcesDeclaration_ValidFullExample(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{
				"id": "harness-repo-issues",
				"type": "github-issue",
				"repos": ["masanami/claude-harness", "masanami/flywheel"],
				"assignee_policy": "self-only",
				"self_assignees": ["masanami"],
				"urgency_labels": {"priority:high": "高", "priority:medium": "中", "priority:low": "低"}
			}
		]
	}`)
	decl, err := LoadSourcesDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want nil", err)
	}
	if decl.Version != 1 {
		t.Errorf("Version = %d, want 1", decl.Version)
	}
	if len(decl.Sources) != 1 {
		t.Fatalf("len(Sources) = %d, want 1", len(decl.Sources))
	}
	s := decl.Sources[0]
	if s.ID != "harness-repo-issues" {
		t.Errorf("ID = %q", s.ID)
	}
	if s.Type != SourceTypeGitHubIssue {
		t.Errorf("Type = %q", s.Type)
	}
	if len(s.Repos) != 2 || s.Repos[0] != "masanami/claude-harness" || s.Repos[1] != "masanami/flywheel" {
		t.Errorf("Repos = %v", s.Repos)
	}
	if s.Policy() != AssigneePolicySelfOnly {
		t.Errorf("Policy() = %q, want %q", s.Policy(), AssigneePolicySelfOnly)
	}
	if len(s.SelfAssignees) != 1 || s.SelfAssignees[0] != "masanami" {
		t.Errorf("SelfAssignees = %v", s.SelfAssignees)
	}
	if s.UrgencyLabels["priority:high"] != "高" {
		t.Errorf(`UrgencyLabels["priority:high"] = %q, want "高"`, s.UrgencyLabels["priority:high"])
	}
}

// --- self-review 指摘の修正確認 ---

// code-reviewer 指摘: encoding/json はキーを大文字小文字を無視して照合するため、
// DisallowUnknownFields だけでは大文字小文字違いの重複キー（例: "id" と "ID"）を
// 拒否できない（後勝ちで静かに上書きされる）。宣言は完全一致のキーだけを
// 受け付けるべき（fail-closed）。
func TestLoadSourcesDeclaration_RejectsCaseVariantDuplicateKey(t *testing.T) {
	cases := []string{
		`{"version": 1, "sources": [{"id": "a", "ID": "b", "type": "github-issue", "repos": ["o/r"]}]}`,
		`{"Version": 1, "version": 1, "sources": []}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeSourcesJSON(t, doc)
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// code-reviewer/design-reviewer 指摘: dec.More() は次のトークンが '}' や ']' の
// ときは false を返すため、"{...}}" のような末尾のゴミを見逃す。
func TestLoadSourcesDeclaration_RejectsTrailingBraceOrBracket(t *testing.T) {
	cases := []string{
		`{"version": 1, "sources": []}}`,
		`{"version": 1, "sources": []}]`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeSourcesJSON(t, doc)
			_, err := LoadSourcesDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// round2 self-review 指摘（code-reviewer）: 拒否側（'}'・']'・"garbage"）は
// 固定したが、受理側（末尾の改行・空白は末尾の JSON トークンとして扱われず、
// 引き続き受理される）を固定するテストが無かった。実運用の宣言ファイルは
// ほぼ必ず末尾に改行を持つため、ここが壊れると全ての正常な宣言が拒否される。
func TestLoadSourcesDeclaration_AcceptsTrailingNewlineAndWhitespace(t *testing.T) {
	cases := []string{
		"{\"version\": 1, \"sources\": []}\n",
		"{\"version\": 1, \"sources\": []}\r\n  \t\n",
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeSourcesJSON(t, doc)
			if _, err := LoadSourcesDeclaration(dir); err != nil {
				t.Fatalf("LoadSourcesDeclaration() error = %v, want nil", err)
			}
		})
	}
}

// code-reviewer/design-reviewer 指摘: assignee_policy・urgency_labels の値は
// enums.go の Parse*（ParseUrgency 等）が前後の空白を trim してから照合する
// ため検証を通ってしまうが、格納される値は生のまま（前後に空白付き）で、
// Policy() や UrgencyLabels の消費者はどの閉集合の値とも一致しなくなる。
// 宣言の値は完全一致だけを受け付けるべき。
func TestLoadSourcesDeclaration_AssigneePolicyAndUrgencyLabelsRejectPaddedValues(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"], "assignee_policy": " self-only "}
		]
	}`)
	if _, err := LoadSourcesDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("assignee_policy with padding: error = %v, want ErrConfigInvalid", err)
	}

	dir2 := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["o/r"], "urgency_labels": {"p": " 高 "}}
		]
	}`)
	if _, err := LoadSourcesDeclaration(dir2); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("urgency_labels with padding: error = %v, want ErrConfigInvalid", err)
	}
}

// design-reviewer 指摘: GitHub の owner/name は大文字小文字を区別しないため、
// "O/R" と "o/r" は同じリポジトリを指す。これを2つの取り込み元に書くのは
// 「同じリポジトリは宣言全体で1つの取り込み元にだけ書ける」という規則に反する。
func TestLoadSourcesDeclaration_RepoDuplicateCheckIsCaseInsensitive(t *testing.T) {
	dir := writeSourcesJSON(t, `{
		"version": 1,
		"sources": [
			{"id": "a", "type": "github-issue", "repos": ["Masanami/Flywheel"]},
			{"id": "b", "type": "github-issue", "repos": ["masanami/flywheel"]}
		]
	}`)
	if _, err := LoadSourcesDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// design-reviewer 指摘: "sources" キーの省略・null は「取り込み元 0 件」として
// 静かに受理されてしまう(fail-open)。宣言は必須キーとして扱うべき
// （空配列 `[]` は明示された「0件」として引き続き許容する。
// TestLoadSourcesDeclaration_EmptySourcesIsAllowed と対）。
func TestLoadSourcesDeclaration_SourcesKeyIsRequired(t *testing.T) {
	cases := []string{
		`{"version": 1}`,
		`{"version": 1, "sources": null}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeSourcesJSON(t, doc)
			if _, err := LoadSourcesDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// 空の sources 配列は許容する（宣言だけ用意し、取り込み元をまだ書いていない状態。
// AC には無いが規則に反しないので拒否しない）。
func TestLoadSourcesDeclaration_EmptySourcesIsAllowed(t *testing.T) {
	dir := writeSourcesJSON(t, `{"version": 1, "sources": []}`)
	decl, err := LoadSourcesDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadSourcesDeclaration() error = %v, want nil", err)
	}
	if len(decl.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(decl.Sources))
	}
}

// --- login 正規化（大文字小文字の無視・先頭 @ の除去。判定そのものは #56） ---

func TestNormalizeLogin_LowercasesAndStripsLeadingAt(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"masanami", "masanami"},
		{"MasaNami", "masanami"},
		{"@masanami", "masanami"},
		{"@MasaNami", "masanami"},
		{"@FOO", "foo"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeLogin(c.in); got != c.want {
			t.Errorf("NormalizeLogin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
