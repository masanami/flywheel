package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、タスクの PR の軽い検査（make gate-light → scripts/gate-light.sh）の判定を、
// 実際の git のリポジトリと go list で固定する。

type gateRepo struct {
	t      *testing.T
	dir    string
	script string
}

func (r gateRepo) run(env []string, name string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), append([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GATE_BASE=", "GOFLAGS=", "GOWORK=off"}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func (r gateRepo) git(args ...string) string {
	r.t.Helper()
	return r.run(nil, "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
}

func (r gateRepo) write(path, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r gateRepo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

// plan は --plan の出力の "gate-light: " を除いた行。
func (r gateRepo) plan(env ...string) []string {
	r.t.Helper()
	var out []string
	for _, l := range strings.Split(r.run(env, "sh", r.script, "--plan"), "\n") {
		if v, ok := strings.CutPrefix(l, "gate-light: "); ok {
			if strings.HasPrefix(v, "mode=light") {
				v = "mode=light"
			}
			out = append(out, v)
		}
	}
	return out
}

// newGateRepo は、b が a を、d のテストだけが b を import し、c は独立なモジュールを main に持ち、
// 統合ブランチ（origin/integration/x）の上の作業ブランチにいる状態を作る。
func newGateRepo(t *testing.T) gateRepo {
	t.Helper()
	script, err := filepath.Abs("../../scripts/gate-light.sh")
	if err != nil {
		t.Fatal(err)
	}
	r := gateRepo{t: t, dir: t.TempDir(), script: script}
	r.write("go.mod", "module example.com/m\n\ngo 1.22\n")
	r.write("a/a.go", "package a\n")
	r.write("a/prompt.md", "x\n")
	r.write("b/b.go", "package b\n\nimport _ \"example.com/m/a\"\n")
	r.write("c/c.go", "package c\n")
	r.write("d/d.go", "package d\n")
	r.write("d/d_test.go", "package d_test\n\nimport _ \"example.com/m/b\"\n")
	r.git("init", "-q", "-b", "main")
	r.commit("base")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	r.git("checkout", "-q", "-b", "work")
	r.commit("earlier task on the integration branch")
	r.git("update-ref", "refs/remotes/origin/integration/x", "HEAD")
	return r
}

func assertPlan(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestGateLightSelectsChangedPackagesAndReverseDeps(t *testing.T) {
	r := newGateRepo(t)
	r.write("a/a.go", "package a\n\nconst X = 1\n")
	r.commit("change a")
	assertPlan(t, r.plan(), "mode=light", "ui=no", "test=packages",
		"package=example.com/m/a", "package=example.com/m/b", "package=example.com/m/d")
}

func TestGateLightIncludesUncommittedAndUntrackedFiles(t *testing.T) {
	r := newGateRepo(t)
	r.write("c/new.go", "package c\n")
	assertPlan(t, r.plan(), "mode=light", "ui=no", "test=packages", "package=example.com/m/c")
}

func TestGateLightSkipsGoTestWithoutGoChanges(t *testing.T) {
	r := newGateRepo(t)
	r.write("a/prompt.md", "y\n")
	r.write("README.md", "y\n")
	assertPlan(t, r.plan(), "mode=light", "ui=no", "test=none")
}

func TestGateLightRunsUIGatesOnlyForWebChanges(t *testing.T) {
	r := newGateRepo(t)
	r.write("web/src/x.ts", "export {}\n")
	assertPlan(t, r.plan(), "mode=light", "ui=yes", "test=none")
}

func TestGateLightTestsEverything(t *testing.T) {
	for _, f := range []string{"go.mod", "go.sum", "Makefile", ".golangci.yml", "scripts/gate-light.sh"} {
		t.Run(f, func(t *testing.T) {
			r := newGateRepo(t)
			r.write(f, "module example.com/m\n\ngo 1.22\n\n")
			assertPlan(t, r.plan(), "mode=light", "ui=no", "test=all")
		})
	}
	t.Run("removed package", func(t *testing.T) {
		r := newGateRepo(t)
		r.git("rm", "-q", "c/c.go")
		assertPlan(t, r.plan(), "mode=light", "ui=no", "test=all")
	})
}

func TestGateLightBaseDetection(t *testing.T) {
	full := func(reason string) string { return "mode=full reason=" + reason }
	light := []string{"mode=light", "ui=no", "test=none"}

	t.Run("GATE_BASE main", func(t *testing.T) {
		assertPlan(t, newGateRepo(t).plan("GATE_BASE=main"), full("base-is-main"))
	})
	t.Run("GATE_BASE unknown branch", func(t *testing.T) {
		assertPlan(t, newGateRepo(t).plan("GATE_BASE=release/1"), full("base-unknown"))
	})
	t.Run("branch cut from main", func(t *testing.T) {
		r := newGateRepo(t)
		r.git("checkout", "-q", "-b", "task", "refs/remotes/origin/main")
		r.commit("task")
		assertPlan(t, r.plan(), full("base-unknown"))
		assertPlan(t, r.plan("GATE_BASE=integration/x"), light...)
	})
	t.Run("upstream is main", func(t *testing.T) {
		r := newGateRepo(t)
		r.git("remote", "add", "origin", r.dir)
		r.git("branch", "-q", "--set-upstream-to=origin/main")
		assertPlan(t, r.plan(), full("base-is-main"))
	})
	t.Run("upstream is a new integration branch with no commits", func(t *testing.T) {
		r := newGateRepo(t)
		r.git("update-ref", "refs/remotes/origin/integration/new", "refs/remotes/origin/main")
		r.git("update-ref", "-d", "refs/remotes/origin/integration/x")
		r.git("checkout", "-q", "-b", "task", "refs/remotes/origin/main")
		r.git("remote", "add", "origin", r.dir)
		assertPlan(t, r.plan(), full("base-unknown"))
		r.git("branch", "-q", "--set-upstream-to=origin/integration/new")
		assertPlan(t, r.plan(), light...)
	})
	t.Run("history contains the integration branch", func(t *testing.T) {
		assertPlan(t, newGateRepo(t).plan(), light...)
	})
	t.Run("no origin/main", func(t *testing.T) {
		r := newGateRepo(t)
		r.git("update-ref", "-d", "refs/remotes/origin/main")
		assertPlan(t, r.plan("GATE_BASE=integration/x"), full("origin/main-not-found"))
	})
}

// full と判定したときは make gate を、軽い検査のときは決めたターゲットだけを呼ぶ。
func TestGateLightInvokesMakeTargets(t *testing.T) {
	r := newGateRepo(t)
	fake := filepath.Join(t.TempDir(), "make")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"make $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := r.run([]string{"MAKE=" + fake, "GATE_BASE=main"}, "sh", r.script); !strings.Contains(out, "make gate\n") {
		t.Fatalf("full run must call make gate:\n%s", out)
	}
	r.write("web/x.ts", "export {}\n")
	out := r.run([]string{"MAKE=" + fake}, "sh", r.script)
	var calls []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "make ") {
			calls = append(calls, l)
		}
	}
	assertPlan(t, calls, "make ui-check", "make fmt-check vet lint", "make build", "make e2e")
}
