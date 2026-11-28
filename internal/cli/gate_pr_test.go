package cli_test

// このファイルは、タスクの PR の品質ゲート（make gate-pr の本体 scripts/gate-pr.sh）の、宛先の判定と試す範囲の決め方を固定する。
// 一時の git のリポジトリと小さな Go のモジュールで、本物のスクリプトを偽の make・gh・go（list だけ本物へ渡す）で流す。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type gatePRRepo struct {
	dir, bin, log string
	env           []string
}

func writeGatePRFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (r *gatePRRepo) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = r.dir, r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newGatePRRepo は、a、a を import する b、b を import する c、テストだけが b を import する d、どれにも依存しない e を持つ
// モジュールを作り、origin/main を指したブランチ feature/x（上流は origin/integration/c-1）にする。
func newGatePRRepo(t *testing.T) *gatePRRepo {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	r := &gatePRRepo{dir: t.TempDir(), bin: t.TempDir()}
	r.log = filepath.Join(r.bin, "calls")
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "MAKE") || name == "PATH" || name == "GOFLAGS" || name == "GOWORK" {
			continue
		}
		r.env = append(r.env, kv)
	}
	r.env = append(r.env, "PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GOWORK=off", "MAKE=fakemake",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	writeGatePRFile(t, filepath.Join(r.bin, "fakemake"), "#!/bin/sh\necho \"make $*\" >>'"+r.log+"'\n", 0o755)
	writeGatePRFile(t, filepath.Join(r.bin, "go"), "#!/bin/sh\nif [ \"$1\" = test ]; then echo \"go $*\" >>'"+r.log+"'; exit 0; fi\nexec '"+realGo+"' \"$@\"\n", 0o755)
	r.setGH(t, "exit 0")

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "gate-pr.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"scripts/gate-pr.sh":                string(script),
		"go.mod":                            "module example.com/m\n\ngo 1.22\n",
		"Makefile":                          "all:\n",
		"a/a.go":                            "package a\n",
		"a/data.txt":                        "x\n",
		"b/b.go":                            "package b\n\nimport _ \"example.com/m/a\"\n",
		"c/c.go":                            "package c\n\nimport _ \"example.com/m/b\"\n",
		"d/d.go":                            "package d\n",
		"d/d_test.go":                       "package d_test\n\nimport _ \"example.com/m/b\"\n",
		"e/e.go":                            "package e\n",
		".claude-harness/quality-gate.json": "{}\n",
		"docs/x.md":                         "x\n",
		"web/x.ts":                          "x\n",
	} {
		writeGatePRFile(t, filepath.Join(r.dir, path), content, 0o644)
	}
	r.git(t, "init", "-q", "-b", "main")
	r.git(t, "add", ".")
	r.git(t, "commit", "-qm", "init")
	r.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	r.git(t, "update-ref", "refs/remotes/origin/integration/c-1", "HEAD")
	r.git(t, "checkout", "-q", "-b", "feature/x")
	r.upstream(t, "origin", "integration/c-1")
	return r
}

func (r *gatePRRepo) upstream(t *testing.T, remote, branch string) {
	t.Helper()
	r.git(t, "config", "branch.feature/x.remote", remote)
	r.git(t, "config", "branch.feature/x.merge", "refs/heads/"+branch)
}

func (r *gatePRRepo) setGH(t *testing.T, body string) {
	t.Helper()
	writeGatePRFile(t, filepath.Join(r.bin, "gh"), "#!/bin/sh\n"+body+"\n", 0o755)
}

func (r *gatePRRepo) change(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(r.dir, path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
}

// calls はスクリプトを流し、偽の make と go test の呼び出しを返す。
func (r *gatePRRepo) calls(t *testing.T) []string {
	t.Helper()
	_ = os.Remove(r.log)
	cmd := exec.Command("/bin/sh", "scripts/gate-pr.sh")
	cmd.Dir, cmd.Env = r.dir, r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gate-pr.sh: %v\n%s", err, out)
	}
	b, err := os.ReadFile(r.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

const gatePRTest = "go test -race -count=1 -timeout 20m "

func TestGatePR_RunsTheFullGateUnlessTheDestinationIsAnIntegrationBranch(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(t *testing.T, r *gatePRRepo){
		"upstream is main":         func(t *testing.T, r *gatePRRepo) { r.upstream(t, "origin", "main") },
		"upstream is its own name": func(t *testing.T, r *gatePRRepo) { r.upstream(t, "origin", "feature/x") },
		"upstream is its own name on an integration branch": func(t *testing.T, r *gatePRRepo) {
			r.git(t, "checkout", "-q", "-b", "integration/c-1")
			r.git(t, "config", "branch.integration/c-1.remote", "origin")
			r.git(t, "config", "branch.integration/c-1.merge", "refs/heads/integration/c-1")
		},
		"upstream is not origin": func(t *testing.T, r *gatePRRepo) { r.upstream(t, ".", "integration/c-1") },
		"no upstream": func(t *testing.T, r *gatePRRepo) {
			r.git(t, "config", "--unset", "branch.feature/x.merge")
		},
		"detached HEAD": func(t *testing.T, r *gatePRRepo) { r.git(t, "checkout", "-q", "--detach") },
		"an open pull request to main wins over the upstream": func(t *testing.T, r *gatePRRepo) { r.setGH(t, "echo main") },
		"one of the open pull requests goes to main": func(t *testing.T, r *gatePRRepo) {
			r.setGH(t, "echo integration/c-1; echo main")
		},
		"gh fails and the upstream is main": func(t *testing.T, r *gatePRRepo) {
			r.setGH(t, "exit 1")
			r.upstream(t, "origin", "main")
		},
		"no merge base with the destination": func(t *testing.T, r *gatePRRepo) {
			r.git(t, "update-ref", "-d", "refs/remotes/origin/integration/c-1")
		},
		"no merge base with origin/main when there are two destinations": func(t *testing.T, r *gatePRRepo) {
			r.setGH(t, "echo integration/c-1; echo integration/c-2")
			// 2 つの宛先はどちらも在る。比べる相手が origin/main であることだけを、その有無で確かめる。
			r.git(t, "update-ref", "refs/remotes/origin/integration/c-2", "HEAD")
			r.git(t, "update-ref", "-d", "refs/remotes/origin/main")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newGatePRRepo(t)
			setup(t, r)
			wantCalls(t, r.calls(t), "make gate")
		})
	}
}

func TestGatePR_AnOpenPullRequestToAnIntegrationBranchIsLight(t *testing.T) {
	t.Parallel()
	r := newGatePRRepo(t)
	r.upstream(t, "origin", "feature/x")
	r.setGH(t, `[ "$1 $2 $3 $4" = "pr list --head feature/x" ] && echo integration/c-1`)
	wantCalls(t, r.calls(t), "make fmt-check vet lint", "make build")
}

func TestGatePR_TheLightGateTestsTheChangedPackagesAndTheirDependents(t *testing.T) {
	t.Parallel()
	const m = "example.com/m/"
	for name, c := range map[string]struct {
		changed []string
		test    string
	}{
		"dependents include test imports": {[]string{"a/a.go"}, m + "a " + m + "b " + m + "c " + m + "d"},
		"a leaf":                          {[]string{"c/c.go"}, m + "c"},
		"an untracked file":               {[]string{"e/new.go"}, m + "e"},
		"a non-Go file next to a Go change belongs to its package": {[]string{"a/data.txt", "e/e.go"},
			m + "a " + m + "b " + m + "c " + m + "d " + m + "e"},
		"no Go file":      {[]string{"a/data.txt", "docs/x.md"}, ""},
		"go.mod":          {[]string{"go.mod"}, "./..."},
		"go.sum":          {[]string{"go.sum"}, "./..."},
		"Makefile":        {[]string{"Makefile"}, "./..."},
		".golangci.yml":   {[]string{".golangci.yml"}, "./..."},
		"the gate script": {[]string{"scripts/gate-pr.sh"}, "./..."},
		"a declaration":   {[]string{".claude-harness/quality-gate.json"}, "./..."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newGatePRRepo(t)
			for _, p := range c.changed {
				if p == "go.mod" {
					r.git(t, "commit", "-q", "--allow-empty", "-m", "x")
				}
				if p == "e/new.go" { // 追跡外の新しい Go のファイル
					writeGatePRFile(t, filepath.Join(r.dir, p), "package e\n", 0o644)
					continue
				}
				r.change(t, p)
			}
			want := []string{"make fmt-check vet lint", "make build"}
			if c.test != "" {
				want = []string{want[0], gatePRTest + c.test, want[1]}
			}
			wantCalls(t, r.calls(t), want...)
		})
	}
}

func TestGatePR_CommittedChangesSinceTheMergeBaseCount(t *testing.T) {
	t.Parallel()
	r := newGatePRRepo(t)
	r.change(t, "c/c.go")
	r.git(t, "commit", "-qam", "change c")
	wantCalls(t, r.calls(t), "make fmt-check vet lint", gatePRTest+"example.com/m/c", "make build")
}

// 統合ブランチに先に入った変更（別のタスクの PR で試し済み）は、軽い検査の差分に数えない。
func TestGatePR_ChangesAlreadyOnTheIntegrationBranchDoNotCount(t *testing.T) {
	t.Parallel()
	r := newGatePRRepo(t)
	r.git(t, "checkout", "-q", "-b", "integ", "main")
	r.change(t, "a/a.go")
	r.git(t, "commit", "-qam", "an earlier task")
	r.git(t, "update-ref", "refs/remotes/origin/integration/c-1", "HEAD")
	r.git(t, "checkout", "-q", "-B", "feature/x", "integ")
	r.upstream(t, "origin", "integration/c-1")
	r.change(t, "c/c.go")
	wantCalls(t, r.calls(t), "make fmt-check vet lint", gatePRTest+"example.com/m/c", "make build")
}

// 宛先が 2 つ以上なら origin/main と比べる（統合ブランチに先に入った変更も数える側に倒す）。
func TestGatePR_TwoDestinationsCompareWithMain(t *testing.T) {
	t.Parallel()
	r := newGatePRRepo(t)
	r.git(t, "checkout", "-q", "-b", "integ", "main")
	r.change(t, "a/a.go")
	r.git(t, "commit", "-qam", "an earlier task")
	r.git(t, "update-ref", "refs/remotes/origin/integration/c-1", "HEAD")
	r.git(t, "update-ref", "refs/remotes/origin/integration/c-2", "HEAD")
	r.git(t, "checkout", "-q", "-B", "feature/x", "integ")
	r.setGH(t, "echo integration/c-1; echo integration/c-2")
	r.change(t, "c/c.go")
	const m = "example.com/m/"
	wantCalls(t, r.calls(t), "make fmt-check vet lint", gatePRTest+m+"a "+m+"b "+m+"c "+m+"d", "make build")
}

func TestGatePR_TheUIGateAndE2ERunOnlyWhenWebChanges(t *testing.T) {
	t.Parallel()
	r := newGatePRRepo(t)
	r.change(t, "web/x.ts")
	wantCalls(t, r.calls(t), "make ui-check", "make fmt-check vet lint", "make build", "make e2e")
}

// 品質ゲートの宣言は軽い検査の入口を、昇格の前の検査の宣言はフルのゲートを呼ぶ。
func TestGateDeclarationsCallTheLightAndTheFullGate(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"quality-gate.json":   `{"test": "make gate-pr"}`,
		"promotion-gate.json": `{"command": ["make", "gate"]}`,
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", ".claude-harness", path))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(b)); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
}
