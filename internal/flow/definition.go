package flow

// ワークスペースの定義（<ws>/.flywheel/flows/<名前>.yaml）の探し方・検査・実行のフォルダへの写し。
//
// ワークスペースの定義は、定義の YAML と、定義が指す指示文（<ws>/.flywheel/flows/prompts/ に置いたものを先に、
// 無ければ組み込みのもの）と、組み込みの出力の型（schemas/）・つなぎのスクリプト（scripts/）を 1 つのディレクトリ
// （写し）に並べ、組み込みと同じ読み込みと検査（workflow.LoadWithOverrides。上書きは重ねない）にかける。
// 検査を通った写しは、そのまま実行のフォルダの definition/ へ移し、実行はその写しを定義の場所として進む。
// 組み込みの定義は、組み込みの指示文だけを使う（ワークスペースの prompts/ を見ない）。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// 定義の種別（check の kind）。
const (
	kindReplace = "replace" // 組み込みと同じ名前のワークスペースの定義（組み込みを置き換える）
	kindNew     = "new"     // 組み込みに無い名前のワークスペースの定義
	kindBuiltin = "builtin" // 組み込みの定義
)

// ワークスペースの置き場の中の、置けないディレクトリ（出力の型とつなぎのスクリプトは組み込みだけ）。
var forbiddenWorkspaceDirs = []string{"schemas", "scripts"}

// workspacePromptsDir は、ワークスペースの定義が指す指示文のうち、ワークスペースに置けるものの置き場（定義のディレクトリからの相対）。
const workspacePromptsDir = "prompts"

// FlowsDir はワークスペースの定義の置き場（<ws>/.flywheel/flows）。
func FlowsDir(workspace string) string {
	return filepath.Join(workspace, ".flywheel", "flows")
}

// definition は検査した定義 1 つ。
type definition struct {
	Name   string
	Source string // runstate.DefinitionWorkspace | runstate.DefinitionBuiltin
	Kind   string // kindReplace | kindNew | kindBuiltin
	// Path はワークスペースの定義の元のファイル（組み込みは空）。
	Path string
	// SHA256 は定義のハッシュ（検査を通らなければ空）。
	SHA256 string
	// WorkspacePrompts はワークスペースから写した指示文（定義のディレクトリからの相対。昇順）。
	WorkspacePrompts []string
	Problems         workflow.Problems
	// WF は検査を通った定義（ワークスペースの定義では写しから読んだもの）。
	WF *workflow.Workflow
	// stage はワークスペースの定義の写しのディレクトリ（組み込みは空）。
	stage string
}

func (d *definition) ok() bool { return len(d.Problems) == 0 && d.WF != nil }

// cleanup は写しを消す（実行のフォルダへ移した後は空振りする）。
func (d *definition) cleanup() {
	if d.stage != "" {
		_ = os.RemoveAll(d.stage)
	}
}

// record は run_started に残す定義の出どころとハッシュ。
func (d *definition) record() *runstate.DefinitionRecord {
	r := &runstate.DefinitionRecord{Source: d.Source, SHA256: d.SHA256}
	if d.Source == runstate.DefinitionWorkspace {
		r.Path, r.WorkspacePrompts = d.Path, d.WorkspacePrompts
	}
	return r
}

// relocate は写しを実行のフォルダの definition/ へ移し、そこから読み直して検査した定義を返す（engine.StartParams.Relocate）。
func (d *definition) relocate(scriptsDir string) func(runDir string) (*workflow.Workflow, error) {
	return func(runDir string) (*workflow.Workflow, error) {
		dst := filepath.Join(runDir, runstate.DefinitionDir)
		if err := os.Rename(d.stage, dst); err != nil {
			return nil, err
		}
		d.stage = ""
		return workflow.LoadWithOverrides(filepath.Join(dst, d.Name+".yaml"), workflow.Options{ScriptsDir: scriptsDir}, nil)
	}
}

// errNoDefinition は、ワークスペースにも組み込みにも、その名前の定義が無いこと。
var errNoDefinition = errors.New("no such flow")

// hasBuiltin は組み込みにその名前の定義があるか。
func hasBuiltin(builtinDir, name string) bool {
	fi, err := os.Stat(filepath.Join(builtinDir, name+".yaml"))
	return err == nil && fi.Mode().IsRegular()
}

// workspaceDefinitionPath はワークスペースにその名前の定義があれば、そのパスを返す（無ければ空）。
func workspaceDefinitionPath(workspace, name string) string {
	p := filepath.Join(FlowsDir(workspace), name+".yaml")
	if _, err := os.Lstat(p); err != nil {
		return ""
	}
	return p
}

// resolveDefinition は名前の定義を探して検査する。ワークスペースに置いてあればそれを、無ければ組み込みを使う。
// どちらにも無ければ errNoDefinition。検査を通らなければ Problems を持つ definition を返す（誤りは返さない）。
// stageParent はワークスペースの定義の写しを作る場所。
func resolveDefinition(workspace, name, builtinDir, scriptsDir, stageParent string) (*definition, error) {
	if p := workspaceDefinitionPath(workspace, name); p != "" {
		return loadWorkspaceDefinition(workspace, name, builtinDir, scriptsDir, stageParent)
	}
	if hasBuiltin(builtinDir, name) {
		return loadBuiltinDefinition(name, builtinDir, scriptsDir), nil
	}
	return nil, errNoDefinition
}

// loadBuiltinDefinition は組み込みの定義を読んで検査する（いまの start と同じ読み方。上書きは読まない）。
func loadBuiltinDefinition(name, builtinDir, scriptsDir string) *definition {
	d := &definition{Name: name, Source: runstate.DefinitionBuiltin, Kind: kindBuiltin}
	wf, err := workflow.LoadWithOverrides(filepath.Join(builtinDir, name+".yaml"), workflow.Options{ScriptsDir: scriptsDir}, nil)
	if err != nil {
		d.Problems = asProblems(filepath.Join(builtinDir, name+".yaml"), err)
		return d
	}
	d.WF, d.SHA256 = wf, wf.Hash
	return d
}

// msgSymlink は、置き場の中のシンボリックリンクの誤り（置き場の外のファイルを指せ、Git の履歴に残らない中身で動いてしまう）。
const msgSymlink = "シンボリックリンクは置けない（置き場の外のファイルを指せる）。実体のファイル・ディレクトリに置き換える"

// forbiddenDirProblems は、ワークスペースの置き場（.flywheel/flows）や指示文の置き場（prompts/）がシンボリックリンクか
// ディレクトリでなければ、また置き場に schemas/・scripts/ があれば、その誤りを返す。
func forbiddenDirProblems(workspace string) workflow.Problems {
	var ps workflow.Problems
	for _, p := range []string{FlowsDir(workspace), filepath.Join(FlowsDir(workspace), workspacePromptsDir)} {
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
		case fi.Mode()&os.ModeSymlink != 0:
			ps = append(ps, workflow.Problem{File: p, Msg: msgSymlink})
		case !fi.IsDir():
			ps = append(ps, workflow.Problem{File: p, Msg: "ディレクトリでない"})
		}
	}
	for _, n := range forbiddenWorkspaceDirs {
		p := filepath.Join(FlowsDir(workspace), n)
		if _, err := os.Lstat(p); err == nil {
			ps = append(ps, workflow.Problem{File: p, Msg: fmt.Sprintf("%s/ はワークスペースに置けない（出力の型とつなぎのスクリプトは組み込みのものを名前で指す）。このディレクトリを消す", n)})
		}
	}
	return ps
}

// loadWorkspaceDefinition はワークスペースの定義を写しへ並べ、組み込みと同じ読み込みと検査にかける。
func loadWorkspaceDefinition(workspace, name, builtinDir, scriptsDir, stageParent string) (*definition, error) {
	src := filepath.Join(FlowsDir(workspace), name+".yaml")
	d := &definition{Name: name, Source: runstate.DefinitionWorkspace, Kind: kindNew, Path: src}
	if hasBuiltin(builtinDir, name) {
		d.Kind = kindReplace
	}
	if !flowName.MatchString(name) {
		d.Problems = workflow.Problems{{File: src, Msg: fmt.Sprintf("フローの名前 %q は使えない（英小文字で始まり、英小文字・数字・- だけ）", name)}}
		return d, nil
	}
	if ps := forbiddenDirProblems(workspace); len(ps) > 0 {
		d.Problems = ps
		return d, nil
	}
	fi, err := os.Lstat(src)
	if err != nil {
		d.Problems = workflow.Problems{{File: src, Msg: fmt.Sprintf("読めない: %v", err)}}
		return d, nil
	}
	if msg := irregularMsg(fi); msg != "" {
		d.Problems = workflow.Problems{{File: src, Msg: msg}}
		return d, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		d.Problems = workflow.Problems{{File: src, Msg: fmt.Sprintf("読めない: %v", err)}}
		return d, nil
	}
	// 構文だけ先に読み、指示文の参照を集める（構文の誤りは元のファイルの行で返す）。
	parsed, err := workflow.Parse(src, data)
	if err != nil {
		d.Problems = asProblems(src, err)
		return d, nil
	}
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(stageParent, ".definition-"+name+"-")
	if err != nil {
		return nil, err
	}
	d.stage = stage
	if err := os.Chmod(stage, 0o755); err != nil { // MkdirTemp は 0700 で作る
		d.cleanup()
		return nil, err
	}
	prompts, ps, err := stageDefinition(stage, name, data, parsed, FlowsDir(workspace), builtinDir)
	if err != nil {
		d.cleanup()
		return nil, err
	}
	if len(ps) > 0 {
		d.Problems = ps
		d.cleanup()
		return d, nil
	}
	stageYAML := filepath.Join(stage, name+".yaml")
	wf, err := workflow.LoadWithOverrides(stageYAML, workflow.Options{ScriptsDir: scriptsDir}, nil)
	if err != nil {
		d.Problems = remapProblems(asProblems(stageYAML, err), stage, src, FlowsDir(workspace), builtinDir, prompts)
		d.cleanup()
		return d, nil
	}
	d.WF = wf
	d.WorkspacePrompts = sortedKeys(prompts)
	d.SHA256 = definitionHash(data, prompts)
	return d, nil
}

// stageDefinition は写しのディレクトリに、定義の YAML・組み込みの schemas/ と scripts/・定義が指す指示文を並べる。
// 返すのはワークスペースから写した指示文（相対パス → 中身）。指示文はワークスペースの prompts/ の下を先に探し、
// 無ければ組み込みを使う。どちらにも無い指示文は写さない（検査が「無い」と返す）。ワークスペースの指示文の道筋に
// シンボリックリンクか通常のファイルでないものがあれば、写さずに誤り（Problems）として返す（組み込みへも落ちない）。
func stageDefinition(stage, name string, data []byte, parsed *workflow.Workflow, flowsDir, builtinDir string) (map[string][]byte, workflow.Problems, error) {
	if err := writeReadOnly(filepath.Join(stage, name+".yaml"), data, 0o444); err != nil {
		return nil, nil, err
	}
	for _, dir := range forbiddenWorkspaceDirs {
		if err := copyTree(filepath.Join(builtinDir, dir), filepath.Join(stage, dir)); err != nil {
			return nil, nil, err
		}
	}
	var problems workflow.Problems
	fromWorkspace := map[string][]byte{}
	seen := map[string]bool{}
	for _, s := range parsed.Steps {
		if s.Prompt == "" || filepath.IsAbs(s.Prompt) {
			continue
		}
		rel := filepath.Clean(s.Prompt)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || seen[rel] {
			continue // 定義のディレクトリの外を指す指示文は、検査が誤りにする
		}
		seen[rel] = true
		if strings.HasPrefix(rel, workspacePromptsDir+string(filepath.Separator)) {
			b, found, p := readWorkspaceFile(flowsDir, rel)
			if p != nil {
				problems = append(problems, *p)
				continue
			}
			if found {
				fromWorkspace[filepath.ToSlash(rel)] = b
				if err := writeReadOnly(filepath.Join(stage, rel), b, 0o444); err != nil {
					return nil, nil, err
				}
				continue
			}
		}
		if b, ok := readRegular(filepath.Join(builtinDir, rel)); ok {
			if err := writeReadOnly(filepath.Join(stage, rel), b, 0o444); err != nil {
				return nil, nil, err
			}
		}
	}
	return fromWorkspace, problems, nil
}

// readWorkspaceFile は置き場（base）の下の rel を、シンボリックリンクをたどらずに読む。道筋のどこかが無ければ found は false
// （組み込みへ落ちる）。道筋にシンボリックリンクがあるか、通常のファイルでなければ、その誤りを返す。
func readWorkspaceFile(base, rel string) (data []byte, found bool, problem *workflow.Problem) {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := base
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, &workflow.Problem{File: cur, Msg: fmt.Sprintf("読めない: %v", err)}
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, false, &workflow.Problem{File: cur, Msg: msgSymlink}
		}
		if i < len(parts)-1 {
			if !fi.IsDir() {
				return nil, false, &workflow.Problem{File: cur, Msg: "ディレクトリでない"}
			}
			continue
		}
		if msg := irregularMsg(fi); msg != "" {
			return nil, false, &workflow.Problem{File: cur, Msg: msg}
		}
	}
	b, err := os.ReadFile(cur)
	if err != nil {
		return nil, false, &workflow.Problem{File: cur, Msg: fmt.Sprintf("読めない: %v", err)}
	}
	return b, true, nil
}

// irregularMsg は、Lstat の結果がシンボリックリンクか通常のファイルでなければ、その理由を返す（通常のファイルなら空）。
func irregularMsg(fi os.FileInfo) string {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return msgSymlink
	case !fi.Mode().IsRegular():
		return "通常のファイルでない"
	}
	return ""
}

// definitionHash はワークスペースの定義のハッシュ: YAML の中身と、ワークスペースから写した指示文（パスと中身。パスの昇順）を
// まとめた sha256。
func definitionHash(yaml []byte, prompts map[string][]byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "workflow\x00%d\x00", len(yaml))
	h.Write(yaml)
	for _, p := range sortedKeys(prompts) {
		b := prompts[p]
		_, _ = fmt.Fprintf(h, "\x00prompt\x00%s\x00%d\x00", p, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// verifyDefinitionCopy は、ワークスペースの定義で始めた run の写し（YAML と、ワークスペースから写した指示文）が、
// 始めたときのハッシュのままかを確かめる（手で書き換えた写しで黙って続けない）。組み込みと、この版より前の run は確かめない。
func verifyDefinitionCopy(st *runstate.State) error {
	rec := st.Definition
	if rec == nil || rec.Source != runstate.DefinitionWorkspace {
		return nil
	}
	data, err := os.ReadFile(st.Workflow.Path)
	if err != nil {
		return fmt.Errorf("cannot read the copy of the workspace workflow the run started with: %w", err)
	}
	prompts := map[string][]byte{}
	for _, p := range rec.WorkspacePrompts {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(st.Workflow.Path), filepath.FromSlash(p)))
		if err != nil {
			return fmt.Errorf("cannot read the copy of the workspace prompt %s the run started with: %w", p, err)
		}
		prompts[p] = b
	}
	if got := definitionHash(data, prompts); got != rec.SHA256 {
		return fmt.Errorf("the copy of the workspace workflow %s changed since the run started (sha256 %s, now %s); the run is not continued with a different definition",
			filepath.Dir(st.Workflow.Path), rec.SHA256, got)
	}
	return nil
}

// asProblems は読み込みの誤りを Problems にする（Problems でなければ file の 1 件にする）。
func asProblems(file string, err error) workflow.Problems {
	var ps workflow.Problems
	if errors.As(err, &ps) {
		return ps
	}
	return workflow.Problems{{File: file, Msg: err.Error()}}
}

// remapProblems は写しのパスで出た誤りを、人が直す元のファイルのパスへ置き換える（定義の YAML はワークスペースの元のファイル、
// ワークスペースから写した指示文はワークスペースのもの、ほかは組み込みのもの）。
func remapProblems(ps workflow.Problems, stage, src, flowsDir, builtinDir string, fromWorkspace map[string][]byte) workflow.Problems {
	out := make(workflow.Problems, 0, len(ps))
	for _, p := range ps {
		rel, err := filepath.Rel(stage, p.File)
		switch {
		case err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		case p.File == filepath.Join(stage, filepath.Base(src)):
			p.File = src
		case fromWorkspace[filepath.ToSlash(rel)] != nil:
			p.File = filepath.Join(flowsDir, rel)
		default:
			p.File = filepath.Join(builtinDir, rel)
		}
		p.Msg = strings.ReplaceAll(p.Msg, stage+string(filepath.Separator), "")
		out = append(out, p)
	}
	return out
}

func readRegular(p string) ([]byte, bool) {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil
}

func writeReadOnly(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// copyTree は src の下の通常ファイルを dst へ写す（*.sh は実行可能、ほかは読み取り専用）。src が無ければ何もしない。
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !e.Type().IsRegular() {
			return nil
		}
		mode := os.FileMode(0o444)
		if filepath.Ext(p) == ".sh" {
			mode = 0o555
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
