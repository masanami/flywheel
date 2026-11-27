package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/runstate"
)

// placeFlows は testdata/workspace-flows の定義と指示文を、ワークスペースの .flywheel/flows/ へ写す（name の YAML は as の名前で置く）。
func (f *fixture) placeFlow(name, as string) string {
	f.t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "workspace-flows", name+".yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	data := string(src)
	if as != name {
		data = strings.Replace(data, "id: "+name+"\n", "id: "+as+"\n", 1)
	}
	dst := filepath.Join(FlowsDir(f.ws), as+".yaml")
	writeFile(f.t, dst, data, 0o644)
	prompt, err := os.ReadFile(filepath.Join("testdata", "workspace-flows", "prompts", "hello.md"))
	if err != nil {
		f.t.Fatal(err)
	}
	writeFile(f.t, filepath.Join(FlowsDir(f.ws), "prompts", "hello.md"), string(prompt), 0o644)
	return dst
}

func syncResolved(cost float64) string {
	return claudeStructured(map[string]any{"outcome": "resolved", "summary": "ok"}, cost)
}

func (f *fixture) startFlow(name string) (contractDoc, string, int) {
	f.t.Helper()
	env, out, errb := f.env()
	code := Start(env, StartOptions{Name: name, PermissionMode: "auto", DelegatedRun: "R-ws"})
	return decodeDoc(f.t, out.String()), errb.String(), code
}

func (f *fixture) resumeGo(id string) contractDoc {
	f.t.Helper()
	env, out, errb := f.env()
	if code := Resume(env, ResumeOptions{ID: id, Input: "go", HasInput: true, PermissionMode: "auto", DelegatedRun: "R-ws"}); code != ExitOK {
		f.t.Fatalf("resume exit %d\n%s", code, errb.String())
	}
	return decodeDoc(f.t, out.String())
}

func (f *fixture) callStdin(n int) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.fdir, "calls", fmt.Sprint(n)+".stdin"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// builtinPromptHead は組み込みの指示文の 1 行目。
func builtinPromptHead(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(assets.FS, "workflows/prompts/"+name)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(b), "\n")
	return head
}

// 組み込みに無い名前のワークスペースの定義で始められる。指示文はワークスペースのものを先に使い、無ければ組み込みへ落ちる。
// 実行は実行のフォルダの写しで動き、途中でワークスペースの定義と指示文を書き換え（壊し）ても、再開は写しで最後まで進む。
// 契約に definition（workspace と定義のハッシュ）が載る。
func TestWorkspaceDefinitionNewNameRunsFromTheCopy(t *testing.T) {
	f := newFixture(t, "5.3.0")
	src := f.placeFlow("hello", "hello")
	f.respond(1, syncResolved(0.2))
	f.respond(2, syncResolved(0.3))

	d, errOut, code := f.startFlow("hello")
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d (%+v)\n%s", code, d, errOut)
	}
	id := *d.RunID
	yaml, _ := os.ReadFile(src)
	prompt, _ := os.ReadFile(filepath.Join(FlowsDir(f.ws), "prompts", "hello.md"))
	wantHash := definitionHash(yaml, map[string][]byte{"prompts/hello.md": prompt})
	if d.Definition == nil || d.Definition.Source != "workspace" || d.Definition.SHA256 != wantHash {
		t.Fatalf("start: definition = %+v, want workspace %s", d.Definition, wantHash)
	}
	w := f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	if w.Definition == nil || w.Definition.Source != "workspace" || w.Definition.SHA256 != wantHash {
		t.Errorf("status: definition = %+v", w.Definition)
	}
	// 1 つ目のステップは、ワークスペースに置いていない指示文 → 組み込みのもの。
	if got := f.callStdin(1); !strings.Contains(got, builtinPromptHead(t, "sync-resolve.md")) {
		t.Errorf("step greet did not use the built-in prompt:\n%s", got)
	}
	st := f.load(id)
	copyDir := filepath.Join(runsDir(f.ws), id, runstate.DefinitionDir)
	if st.Workflow.Path != filepath.Join(copyDir, "hello.yaml") || st.WorkflowDir != copyDir || st.Embedded {
		t.Errorf("run_started: path %s dir %s embedded %v; want the copy under %s", st.Workflow.Path, st.WorkflowDir, st.Embedded, copyDir)
	}
	if st.Definition == nil || st.Definition.Path != src || strings.Join(st.Definition.WorkspacePrompts, ",") != "prompts/hello.md" {
		t.Errorf("run_started definition = %+v", st.Definition)
	}
	for _, p := range []string{"schemas/sync-resolve-result.json", "prompts/sync-resolve.md", "prompts/hello.md"} {
		if _, err := os.Stat(filepath.Join(copyDir, p)); err != nil {
			t.Errorf("the copy lacks %s: %v", p, err)
		}
	}
	// 写しの元（.definition-*）が残っていない。
	if m, _ := filepath.Glob(filepath.Join(f.ws, ".flywheel", "flow", ".definition-*")); len(m) != 0 {
		t.Errorf("staging left behind: %v", m)
	}

	// 途中でワークスペースの定義を壊し、指示文を書き換える。
	writeFile(t, src, "not: [valid\n", 0o644)
	writeFile(t, filepath.Join(FlowsDir(f.ws), "prompts", "hello.md"), "CHANGED-PROMPT\n", 0o644)
	r := f.resumeGo(id)
	if r.Definition == nil || r.Definition.SHA256 != wantHash {
		t.Errorf("resume: definition = %+v", r.Definition)
	}
	f.waitState(id, "succeeded")
	f.waitRunnerExit(id)
	got := f.callStdin(2)
	if !strings.Contains(got, "WORKSPACE-HELLO-PROMPT") || strings.Contains(got, "CHANGED-PROMPT") {
		t.Errorf("step again did not use the copy of the workspace prompt:\n%s", got)
	}
}

// 組み込みと同じ名前の定義をワークスペースに置くと、その名前のフローはワークスペースの定義で始まる。
func TestWorkspaceDefinitionReplacesTheBuiltin(t *testing.T) {
	f := newFixture(t, "5.3.0")
	f.placeFlow("hello", "ticket")
	f.respond(1, syncResolved(0.2))
	d, errOut, code := f.startFlow("ticket")
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d (%+v)\n%s", code, d, errOut)
	}
	f.waitState(*d.RunID, "waiting")
	f.waitRunnerExit(*d.RunID)
	if d.Definition == nil || d.Definition.Source != "workspace" {
		t.Errorf("definition = %+v", d.Definition)
	}
	if st := f.load(*d.RunID); st.Unit("main").Gate == nil || st.Unit("main").Gate.Gate != "wait" {
		t.Errorf("the run is not the workspace flow: %+v", st.Unit("main"))
	}
}

// 検査を通らないワークスペースの定義では始めない（組み込みへも落ちない）。契約の failed の理由にファイルと行が出る。
func TestBrokenWorkspaceDefinitionDoesNotStart(t *testing.T) {
	f := newFixture(t, "5.3.0")
	src := f.placeFlow("hello", "ticket")
	data, _ := os.ReadFile(src)
	writeFile(t, src, strings.Replace(string(data), "{ goto: again }", "{ goto: nowhere }", 1), 0o644)
	d, _, _ := f.startFlow("ticket")
	if d.State != "failed" || d.RunID != nil || !strings.Contains(d.Summary, src+":") || !strings.Contains(d.Summary, "nowhere") {
		t.Fatalf("contract = %+v; want failed naming %s and the line", d, src)
	}
	if ids, _ := runstate.List(runsDir(f.ws)); len(ids) != 0 {
		t.Errorf("a run was created: %v", ids)
	}
	if m, _ := filepath.Glob(filepath.Join(f.ws, ".flywheel", "flow", ".definition-*")); len(m) != 0 {
		t.Errorf("staging left behind: %v", m)
	}
	// 指示文が無い定義も通らない。
	f2 := newFixture(t, "5.3.0")
	src2 := f2.placeFlow("hello", "hello")
	if err := os.Remove(filepath.Join(FlowsDir(f2.ws), "prompts", "hello.md")); err != nil {
		t.Fatal(err)
	}
	d2, _, _ := f2.startFlow("hello")
	if d2.State != "failed" || !strings.Contains(d2.Summary, src2) || !strings.Contains(d2.Summary, "prompts/hello.md") {
		t.Errorf("missing prompt: contract = %+v", d2)
	}
}

// ワークスペースの置き場に schemas/ か scripts/ があれば、start も check も誤りにする。
func TestWorkspaceSchemasOrScriptsAreRejected(t *testing.T) {
	for _, dir := range []string{"schemas", "scripts"} {
		t.Run(dir, func(t *testing.T) {
			f := newFixture(t, "5.3.0")
			f.placeFlow("hello", "hello")
			writeFile(t, filepath.Join(FlowsDir(f.ws), dir, "x.json"), "{}", 0o644)
			d, _, _ := f.startFlow("hello")
			if d.State != "failed" || d.RunID != nil || !strings.Contains(d.Summary, filepath.Join(FlowsDir(f.ws), dir)) {
				t.Errorf("start: contract = %+v", d)
			}
			env, out, _ := f.env()
			if code := Check(env, CheckOptions{JSON: true}); code != ExitFailed {
				t.Errorf("check exit %d, want 1\n%s", code, out)
			}
		})
	}
}

// 組み込みの定義は組み込みの指示文だけを使い（ワークスペースの prompts/ を見ない）、契約の definition は builtin と YAML の sha256。
func TestBuiltinDefinitionIgnoresWorkspacePrompts(t *testing.T) {
	f := newFixture(t, "5.3.0")
	writeFile(t, filepath.Join(FlowsDir(f.ws), "prompts", "ticket-analyze.md"), "WORKSPACE-ANALYZE\n", 0o644)
	id := f.runToReview()
	if got := f.callStdin(1); strings.Contains(got, "WORKSPACE-ANALYZE") || !strings.Contains(got, builtinPromptHead(t, "ticket-analyze.md")) {
		t.Errorf("the built-in ticket used a workspace prompt:\n%s", got)
	}
	st := f.load(id)
	embedded, _ := fs.ReadFile(assets.FS, "workflows/ticket.yaml")
	sum := sha256.Sum256(embedded)
	d := f.status(id)
	if d.Definition == nil || d.Definition.Source != "builtin" || d.Definition.SHA256 != hex.EncodeToString(sum[:]) || st.Workflow.Hash != d.Definition.SHA256 {
		t.Errorf("definition = %+v (workflow hash %s)", d.Definition, st.Workflow.Hash)
	}
}

// 写しが始めたときから書き換えられていれば、開き直さない。
func TestReopenRefusesATamperedCopy(t *testing.T) {
	f := newFixture(t, "5.3.0")
	f.placeFlow("hello", "hello")
	f.respond(1, syncResolved(0.2))
	d, errOut, code := f.startFlow("hello")
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	p := filepath.Join(runsDir(f.ws), id, runstate.DefinitionDir, "prompts", "hello.md")
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "TAMPERED\n", 0o644)
	if err := verifyDefinitionCopy(f.load(id)); err == nil || !strings.Contains(err.Error(), "changed since the run started") {
		t.Errorf("verifyDefinitionCopy = %v", err)
	}
	env, out, _ := f.env()
	Resume(env, ResumeOptions{ID: id, Input: "go", HasInput: true, PermissionMode: "auto", DelegatedRun: "R-ws"})
	if r := decodeDoc(t, out.String()); r.State != "waiting" || !strings.Contains(r.Summary, "changed since the run started") {
		t.Errorf("resume with a tampered copy: %+v", r)
	}
}

// export は組み込みの定義の YAML だけを書き出す。既にあれば上書きせず、組み込みに無い名前と合わせて誤り。
func TestExport(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".flywheel", "keep"), "", 0o644)
	run := func(name string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Export(Env{Workspace: ws, Stdout: &out, Stderr: &errb}, name)
		return code, out.String(), errb.String()
	}
	code, out, errOut := run("ticket")
	dst := filepath.Join(ws, ".flywheel", "flows", "ticket.yaml")
	if code != ExitOK || strings.TrimSpace(out) != dst {
		t.Fatalf("export exit %d out %q err %q", code, out, errOut)
	}
	want, _ := fs.ReadFile(assets.FS, "workflows/ticket.yaml")
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Errorf("the exported file differs from the embedded definition")
	}
	if entries, _ := os.ReadDir(filepath.Join(ws, ".flywheel", "flows")); len(entries) != 1 {
		t.Errorf("export wrote more than the definition: %v", entries)
	}
	writeFile(t, dst, "edited\n", 0o644)
	if code, _, errOut := run("ticket"); code == ExitOK || !strings.Contains(errOut, "上書きしない") {
		t.Errorf("export over an existing file: exit %d err %q", code, errOut)
	}
	if got, _ := os.ReadFile(dst); string(got) != "edited\n" {
		t.Errorf("the existing file was overwritten")
	}
	if code, _, _ := run("no-such-flow"); code == ExitOK {
		t.Errorf("export of an unknown flow exited 0")
	}
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flows", "no-such-flow.yaml")); err == nil {
		t.Errorf("a file was written for an unknown flow")
	}
}

// check: 置いた定義をすべて検査し、1 つでも通らなければ 1。JSON は約束の形。名前を渡せば、置いていない名前は組み込みを検査する。
func TestCheck(t *testing.T) {
	f := newFixture(t, "5.3.0")
	f.placeFlow("hello", "hello")
	f.placeFlow("hello", "ticket")
	bad := f.placeFlow("hello", "broken")
	data, _ := os.ReadFile(bad)
	writeFile(t, bad, strings.Replace(string(data), "{ goto: again }", "{ goto: nowhere }", 1), 0o644)

	check := func(o CheckOptions) (int, string) {
		env, out, _ := f.env()
		return Check(env, o), out.String()
	}
	code, out := check(CheckOptions{JSON: true})
	if code != ExitFailed {
		t.Fatalf("check exit %d, want 1\n%s", code, out)
	}
	var doc struct {
		Definitions []map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(doc.Definitions) != 3 {
		t.Fatalf("definitions = %d\n%s", len(doc.Definitions), out)
	}
	keys := []string{"kind", "name", "ok", "path", "problems", "sha256", "source"}
	byName := map[string]checkDefinition{}
	for _, raw := range doc.Definitions {
		if len(raw) != len(keys) {
			t.Errorf("keys = %v, want %v", raw, keys)
		}
		for _, k := range keys {
			if _, ok := raw[k]; !ok {
				t.Errorf("key %s missing: %v", k, raw)
			}
		}
		b, _ := json.Marshal(raw)
		var cd checkDefinition
		_ = json.Unmarshal(b, &cd)
		byName[cd.Name] = cd
	}
	if c := byName["hello"]; !c.OK || c.Kind != "new" || c.Source != "workspace" || len(c.SHA256) != 64 || c.Path != filepath.Join(FlowsDir(f.ws), "hello.yaml") {
		t.Errorf("hello = %+v", c)
	}
	if c := byName["ticket"]; !c.OK || c.Kind != "replace" || c.Source != "workspace" {
		t.Errorf("ticket = %+v", c)
	}
	c := byName["broken"]
	if c.OK || c.SHA256 != "" || len(c.Problems) == 0 || c.Problems[0].File != bad || c.Problems[0].Line == 0 {
		t.Errorf("broken = %+v", c)
	}

	// テキスト: 定義ごとの 1 行と「ファイル:行: 理由」。
	code, out = check(CheckOptions{})
	if code != ExitFailed || !strings.Contains(out, "hello\tワークスペース（新しい名前）\t通った\t"+byName["hello"].SHA256[:12]) ||
		!strings.Contains(out, "ticket\tワークスペース（組み込みの置き換え）\t通った") || !strings.Contains(out, "broken\tワークスペース（新しい名前）\t通らなかった\t-") ||
		!strings.Contains(out, bad+":") {
		t.Errorf("text output:\n%s", out)
	}

	// 名前を渡す: 通るものだけなら 0。置いていない名前は組み込み。
	if code, out := check(CheckOptions{Names: []string{"hello", "reflect"}, JSON: true}); code != ExitOK || !strings.Contains(out, `"kind": "builtin"`) {
		t.Errorf("check hello reflect: exit %d\n%s", code, out)
	}
	if code, _ := check(CheckOptions{Names: []string{"broken"}}); code != ExitFailed {
		t.Errorf("check broken exit %d", code)
	}
	if code, _ := check(CheckOptions{Names: []string{"no-such-flow"}}); code != ExitFailed {
		t.Errorf("check of an unknown name exit %d", code)
	}
	// 書き出した組み込みの定義はそのまま通り、ハッシュは置き換えとして出る。
	if err := os.Remove(filepath.Join(FlowsDir(f.ws), "ticket.yaml")); err != nil {
		t.Fatal(err)
	}
	env, _, _ := f.env()
	if code := Export(env, "ticket"); code != ExitOK {
		t.Fatal("export failed")
	}
	if code, out := check(CheckOptions{Names: []string{"ticket"}}); code != ExitOK || !strings.Contains(out, "組み込みの置き換え）\t通った") {
		t.Errorf("exported ticket: exit %d\n%s", code, out)
	}
}
