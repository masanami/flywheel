package flow

// 写す口（flywheel flow export）と確かめる口（flywheel flow check）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/runstate"
)

// Export は組み込みのフローの定義の YAML だけを <ws>/.flywheel/flows/<名前>.yaml へ書き出す（指示文は書き出さない。
// 置いていない指示文は組み込みの最新を使う）。書き出す先に既にあれば上書きせず、組み込みに無い名前と合わせて 0 でない終了コード。
// 置き場が無ければ作る。書き出したら、その絶対パスを標準出力へ 1 行出す。
func Export(env Env, name string) int {
	env = env.withDefaults()
	if !flowName.MatchString(name) {
		logf(env.Stderr, "flywheel flow: フローの名前 %q は使えない（組み込みのフローの名前を渡す。例: ticket）\n", name)
		return ExitFailed
	}
	data, err := fs.ReadFile(assets.FS, "workflows/"+name+".yaml")
	if err != nil {
		logf(env.Stderr, "flywheel flow: 組み込みのフローに %q は無い（組み込み: %s）\n", name, strings.Join(embeddedNames(), "・"))
		return ExitFailed
	}
	dir := FlowsDir(env.Workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf(env.Stderr, "flywheel flow: %s を作れない: %v\n", dir, err)
		return ExitFailed
	}
	dst := filepath.Join(dir, name+".yaml")
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		logf(env.Stderr, "flywheel flow: %s は既にある。上書きしない（書き出し直すなら、先に消すか名前を変える）\n", dst)
		return ExitFailed
	}
	if err != nil {
		logf(env.Stderr, "flywheel flow: %s を作れない: %v\n", dst, err)
		return ExitFailed
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		logf(env.Stderr, "flywheel flow: %s へ書けない: %v\n", dst, err)
		return ExitFailed
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		logf(env.Stderr, "flywheel flow: %s へ書けない: %v\n", dst, err)
		return ExitFailed
	}
	logf(env.Stdout, "%s\n", dst)
	return ExitOK
}

// embeddedNames は埋め込んだフローの名前（昇順）。
func embeddedNames() []string {
	entries, err := fs.ReadDir(assets.FS, "workflows")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok && e.Type().IsRegular() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// CheckOptions は Check に渡すもの。
type CheckOptions struct {
	// Names は検査する定義の名前（空ならワークスペースの .flywheel/flows/*.yaml をすべて）。
	Names []string
	// JSON は標準出力を JSON にする。
	JSON bool
}

// checkDoc は check --json の出力。
type checkDoc struct {
	Definitions []checkDefinition `json:"definitions"`
}

type checkDefinition struct {
	Name     string         `json:"name"`
	Source   string         `json:"source"`
	Kind     string         `json:"kind"`
	Path     string         `json:"path"`
	SHA256   string         `json:"sha256"`
	OK       bool           `json:"ok"`
	Problems []checkProblem `json:"problems"`
}

type checkProblem struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Check は定義を組み込みと同じ検査にかけ、定義ごとの結果を出す。名前を渡さなければワークスペースの .flywheel/flows/*.yaml を
// すべて、渡せばその名前だけを検査する（ワークスペースに置いていない名前は組み込みとして検査し、組み込みにも無ければ通らない）。
// すべて通れば 0、1 つでも通らなければ（検査を行えなかったときも）0 でない終了コード。
func Check(env Env, o CheckOptions) int {
	env = env.withDefaults()
	plugin, err := ResolvePlugin(env.Getenv, env.LookPath)
	if err != nil {
		logf(env.Stderr, "flywheel flow: 定義を検査できない（検査はプラグインのスクリプトとエージェントの実在も確かめる）: %v\n", err)
		return ExitFailed
	}
	layout, err := ensureAssets(env)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	names := o.Names
	if len(names) == 0 {
		if names, err = workspaceNames(env.Workspace); err != nil {
			logf(env.Stderr, "flywheel flow: %v\n", err)
			return ExitFailed
		}
	}
	stageParent, err := os.MkdirTemp("", "flywheel-flow-check-")
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	defer func() { _ = os.RemoveAll(stageParent) }()
	doc := checkDoc{Definitions: []checkDefinition{}}
	allOK := true
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		cd := checkOne(env.Workspace, name, layout.WorkflowDir, plugin.ScriptsDir, stageParent)
		allOK = allOK && cd.OK
		doc.Definitions = append(doc.Definitions, cd)
	}
	// 定義が 1 つも無くても、置けないディレクトリがあれば通らない。
	if len(o.Names) == 0 && len(names) == 0 {
		if ps := forbiddenDirProblems(env.Workspace); len(ps) > 0 {
			allOK = false
			for _, p := range ps {
				logf(env.Stderr, "flywheel flow: %s\n", p.String())
			}
		}
	}
	if o.JSON {
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			logf(env.Stderr, "flywheel flow: %v\n", err)
			return ExitFailed
		}
		logf(env.Stdout, "%s\n", b)
	} else {
		writeCheckText(env, doc, len(names) == 0)
	}
	if !allOK {
		return ExitFailed
	}
	return ExitOK
}

// checkOne は名前 1 つを検査する。
func checkOne(workspace, name, builtinDir, scriptsDir, stageParent string) checkDefinition {
	cd := checkDefinition{Name: name, Source: runstate.DefinitionBuiltin, Kind: kindBuiltin, Problems: []checkProblem{}}
	if !flowName.MatchString(name) {
		cd.Source, cd.Kind = runstate.DefinitionWorkspace, kindNew
		cd.Path = filepath.Join(FlowsDir(workspace), name+".yaml")
		cd.Problems = append(cd.Problems, checkProblem{File: cd.Path, Message: fmt.Sprintf("フローの名前 %q は使えない（英小文字で始まり、英小文字・数字・- だけ）", name)})
		return cd
	}
	def, err := resolveDefinition(workspace, name, builtinDir, scriptsDir, stageParent)
	if errors.Is(err, errNoDefinition) {
		cd.Problems = append(cd.Problems, checkProblem{Message: fmt.Sprintf("フロー %q はワークスペース（%s）にも組み込みにも無い", name, FlowsDir(workspace))})
		return cd
	}
	if err != nil {
		cd.Problems = append(cd.Problems, checkProblem{Message: fmt.Sprintf("検査できない: %v", err)})
		return cd
	}
	defer def.cleanup()
	cd.Source, cd.Kind, cd.Path = def.Source, def.Kind, def.Path
	for _, p := range def.Problems {
		cd.Problems = append(cd.Problems, checkProblem{File: p.File, Line: p.Line, Message: p.Msg})
	}
	if def.ok() {
		cd.OK, cd.SHA256 = true, def.SHA256
	}
	return cd
}

// workspaceNames はワークスペースの .flywheel/flows/*.yaml の名前（昇順）。置き場が無ければ空。
// 置き場がシンボリックリンクのときも中を読み、定義ごとの検査（forbiddenDirProblems）で誤りにする。
func workspaceNames(workspace string) ([]string, error) {
	entries, err := os.ReadDir(FlowsDir(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		// 通常のファイルでないもの（シンボリックリンク・ディレクトリ）も名前に入れ、検査で誤りにする。
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

var kindLabels = map[string]string{
	kindReplace: "ワークスペース（組み込みの置き換え）",
	kindNew:     "ワークスペース（新しい名前）",
	kindBuiltin: "組み込み",
}

// writeCheckText は check のテキストの出力: 定義ごとに 1 行（名前・種別・結果・ハッシュの先頭 12 文字）と、通らない所を
// 「ファイル:行: 理由」で並べる。
func writeCheckText(env Env, doc checkDoc, none bool) {
	if none {
		logf(env.Stdout, "ワークスペースに定義は無い（%s/*.yaml）\n", FlowsDir(env.Workspace))
		return
	}
	for _, d := range doc.Definitions {
		result, hash := "通らなかった", "-"
		if d.OK {
			result = "通った"
			hash = d.SHA256
			if len(hash) > 12 {
				hash = hash[:12]
			}
		}
		logf(env.Stdout, "%s\t%s\t%s\t%s\n", d.Name, kindLabels[d.Kind], result, hash)
		for _, p := range d.Problems {
			switch {
			case p.File == "":
				logf(env.Stdout, "  %s\n", p.Message)
			case p.Line > 0:
				logf(env.Stdout, "  %s:%d: %s\n", p.File, p.Line, p.Message)
			default:
				logf(env.Stdout, "  %s: %s\n", p.File, p.Message)
			}
		}
	}
}
