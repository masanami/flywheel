package flow

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/runstate"
)

// run を始められない start は、終了コード 0 と契約の failed の JSON を変えず、理由を標準エラーにも 1 行で出す。
func TestStartFailureKeepsTheContractAndWritesOneReasonLineToStderr(t *testing.T) {
	cases := []struct {
		name string
		// arrange はワークスペースを整え、start に渡すフローの名前と入力、期待する契約の summary を返す。
		// 期待する summary は検査対象の出力から取らずに組み立てる。
		arrange func(t *testing.T, f *fixture) (string, []string, string)
		// reasons は標準エラーの 1 行に含まれるはずの文。
		reasons []string
	}{
		{"broken-definition", func(t *testing.T, f *fixture) (string, []string, string) {
			// 誤りが 2 件（行き先が無い・指示文が無い）ある定義。契約の summary は誤りの一覧の改行を残す。
			src := f.placeFlow("hello", "hello")
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, src, strings.Replace(string(data), "{ goto: again }", "{ goto: nowhere }", 1), 0o644)
			if err := os.Remove(filepath.Join(FlowsDir(f.ws), "prompts", "hello.md")); err != nil {
				t.Fatal(err)
			}
			return "hello", nil, "no run was started: workflow is not valid: " +
				src + `:25: steps.again: prompt "prompts/hello.md" does not exist` + "\n" +
				src + `:24: steps.wait.on.go: step "nowhere" does not exist`
		}, []string{"workflow is not valid", "nowhere", "prompts/hello.md"}},
		{"unparsable-definition", func(t *testing.T, f *fixture) (string, []string, string) {
			src := filepath.Join(FlowsDir(f.ws), "hello.yaml")
			writeFile(t, src, "not: [valid\n", 0o644)
			return "hello", nil, "no run was started: workflow is not valid: " +
				src + ": invalid YAML: yaml: line 1: did not find expected ',' or ']'"
		}, []string{"workflow is not valid", "hello.yaml"}},
		{"no-such-flow", func(_ *testing.T, f *fixture) (string, []string, string) {
			return "nosuch", nil, `no run was started: flow "nosuch" is neither in the workspace (` + FlowsDir(f.ws) + `) nor embedded`
		}, []string{`flow "nosuch" is neither in the workspace`}},
		{"bad-input", func(*testing.T, *fixture) (string, []string, string) {
			return "ticket", []string{"nosuch=1"}, `no run was started: input "nosuch" is not declared by workflow ticket`
		}, []string{`input "nosuch" is not declared`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "5.3.0")
			name, inputs, summary := c.arrange(t, f)
			env, out, errb := f.env()
			code := Start(env, StartOptions{Name: name, Inputs: inputs, PermissionMode: "auto", DelegatedRun: "R-fail"})
			if code != ExitOK {
				t.Fatalf("exit %d, want %d\n%s", code, ExitOK, errb)
			}
			quoted, err := json.Marshal(summary)
			if err != nil {
				t.Fatal(err)
			}
			want := `{
  "contract_version": 1,
  "run_id": null,
  "state": "failed",
  "summary": ` + string(quoted) + `,
  "requested_action": null,
  "artifacts": [],
  "cost_usd": null
}
`
			if out.String() != want {
				t.Errorf("stdout = %s\nwant %s", out, want)
			}
			line, ok := strings.CutSuffix(errb.String(), "\n")
			if !ok || strings.ContainsAny(line, "\r\n") {
				t.Fatalf("stderr is not exactly one line: %q", errb.String())
			}
			for _, r := range c.reasons {
				if !strings.Contains(line, r) {
					t.Errorf("stderr lacks %q: %q", r, line)
				}
			}
			if ids, _ := runstate.List(runsDir(f.ws)); len(ids) != 0 {
				t.Errorf("a run was created: %v", ids)
			}
		})
	}
}

// 実行のフォルダに写した指示文は所有者が書ける。ほかの写し（定義の YAML・schemas/・scripts/）と展開した元は書き込みの権限を持たないままにする。
func TestRunFolderPromptCopiesAreOwnerWritable(t *testing.T) {
	f := newFixture(t, "5.3.0")
	f.placeFlow("hello", "hello")
	f.respond(1, syncResolved(0.2))
	d, errOut, code := f.startFlow("hello")
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d (%+v)\n%s", code, d, errOut)
	}
	f.waitState(*d.RunID, "waiting")
	f.waitRunnerExit(*d.RunID)

	modes := func(root string) map[string]os.FileMode {
		got := map[string]os.FileMode{}
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() {
				return err
			}
			fi, err := e.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			got[filepath.ToSlash(rel)] = fi.Mode().Perm()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	copied := modes(filepath.Join(runsDir(f.ws), *d.RunID, runstate.DefinitionDir))
	// ワークスペースの指示文・組み込みから写した指示文と、指示文でない写し。
	for _, p := range []string{"prompts/hello.md", "prompts/sync-resolve.md", "hello.yaml", "schemas/sync-resolve-result.json", "scripts/quality-gate.sh"} {
		if _, ok := copied[p]; !ok {
			t.Errorf("the copy lacks %s", p)
		}
	}
	for p, m := range copied {
		if strings.HasPrefix(p, "prompts/") {
			if m&0o200 == 0 {
				t.Errorf("prompt copy %s mode %v is not owner writable", p, m)
			}
			if m&0o022 != 0 {
				t.Errorf("prompt copy %s mode %v is writable by others", p, m)
			}
		} else if m&0o222 != 0 {
			t.Errorf("copy %s mode %v became writable", p, m)
		}
		if strings.HasSuffix(p, ".sh") && m&0o100 == 0 {
			t.Errorf("copy %s mode %v is not executable", p, m)
		}
	}

	digest, err := assets.Digest(assets.FS)
	if err != nil {
		t.Fatal(err)
	}
	extracted := modes(assets.LayoutOf(assets.RuntimeRoot(f.ws, versionDir("test-version", digest))).WorkflowDir)
	if len(extracted) == 0 {
		t.Fatal("no extracted files")
	}
	for p, m := range extracted {
		if m&0o222 != 0 {
			t.Errorf("extracted %s mode %v became writable", p, m)
		}
	}
}
