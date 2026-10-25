package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// memoryIndexFileName は記憶の置き場の索引のファイル名。
const memoryIndexFileName = "INDEX.md"

// memoryRoot は記憶の置き場の解決結果。
type memoryRoot struct {
	abs    string // base と memory_dir を結合したパス（シンボリックリンクを辿らない）
	real   string // 置き場の実体のパス（置き場が無いときは ""）
	exists bool
	domain string // 置き場のディレクトリの名前
}

// resolveMemoryRoot は base の下の置き場 dir を解決する。置き場の実体（置き場が
// 無いときは存在する最も近い祖先の実体）がワークスペースの実体の外にあるとき、
// すなわち置き場そのものが（途中の経路を含めて）外を指すシンボリックリンクの
// ときは、ErrMemoryWriteRejected を返す。何も作らない。
func resolveMemoryRoot(base, dir string) (memoryRoot, error) {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return memoryRoot{}, fmt.Errorf("resolve workspace %s: %w", base, err)
	}
	baseReal, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return memoryRoot{}, fmt.Errorf("resolve workspace %s: %w", base, err)
	}
	abs := filepath.Join(baseAbs, dir)
	root := memoryRoot{abs: abs, domain: filepath.Base(abs)}

	probe := abs
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !pathInside(baseReal, resolved) {
				return memoryRoot{}, fmt.Errorf("%w: memory directory resolves outside the workspace", ErrMemoryWriteRejected)
			}
			if probe == abs {
				fi, err := os.Stat(resolved)
				if err != nil {
					return memoryRoot{}, fmt.Errorf("stat memory directory: %w", err)
				}
				if !fi.IsDir() {
					return memoryRoot{}, fmt.Errorf("%w: memory directory is not a directory", ErrMemoryWriteRejected)
				}
				root.real = resolved
				root.exists = true
			}
			return root, nil
		}
		if _, lerr := os.Lstat(probe); lerr == nil {
			// 存在するのに辿れない（壊れたリンク・輪）。安全のため拒否する。
			return memoryRoot{}, fmt.Errorf("%w: memory directory cannot be resolved: %w", ErrMemoryWriteRejected, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return memoryRoot{}, fmt.Errorf("%w: memory directory cannot be resolved", ErrMemoryWriteRejected)
		}
		probe = parent
	}
}

// pathInside は p が dir 自身か dir の配下であることを返す（どちらも実体のパス）。
func pathInside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// memoryIndexPath は記憶の置き場の索引（INDEX.md）のパスを返す。置き場に
// INDEX.md があればそのパス（base が絶対パスなら絶対パス）、無ければ ""。
// 置き場が外を指すシンボリックリンクのとき、INDEX.md が置き場の外へ出る
// シンボリックリンクのとき、通常のファイルでないときも ""（判断点に置き場の
// 外のパスを渡さない）。何も作らない。判断点の入力へ渡す側（J2・J3・J5・J6）が
// 使う。
func memoryIndexPath(base string, decl *AgentDeclaration) string {
	dir := defaultMemoryDir
	if decl != nil && decl.MemoryDir != "" {
		dir = decl.MemoryDir
	}
	root, err := resolveMemoryRoot(base, dir)
	if err != nil || !root.exists {
		return ""
	}
	p := filepath.Join(root.abs, memoryIndexFileName)
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || !pathInside(root.real, resolved) {
		return ""
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return filepath.Join(filepath.Join(base, dir), memoryIndexFileName)
}

// memoryIndexLine は作成した記憶の索引の 1 行（改行を含まない）。
func memoryIndexLine(slug, typ, confidence, description string) string {
	return fmt.Sprintf("- [%s] %s/%s — %s", slug, typ, confidence, description)
}

// newMemoryIndexHeader は INDEX.md が無いときに作る索引の先頭（置き場の
// ワークスペースからの相対パスを見出しにする）。
func newMemoryIndexHeader(dir string) string {
	return fmt.Sprintf("# %s INDEX\n\n", filepath.ToSlash(dir))
}

// memoryIndexSectionLabel は J2・J3・J5 の入力に足す、記憶の索引の節のラベル。
const memoryIndexSectionLabel = "記憶の索引"

// memoryIndexSections は J2・J3・J5 の入力に足す記憶の索引の節（0 個か 1 個）を返す。
// 置き場に INDEX.md があるときだけ、索引のパスと「関係する記憶を読んでよい」という
// 指示の節を返し、無いときは nil（判断点はそのまま起動する）。判断点は読み取りの道具
// だけを持つ（書くのは core だけ）。「読んでよい」という指示は、データの区画の
// 中の指示には従わないという指示文の規則に打ち消されないよう、指示文
// （prompts/j2.md・j3.md・j5.md）に置き、節にはパスだけを入れる。
func memoryIndexSections(base string, decl *AgentDeclaration) []JudgmentDataSection {
	p := memoryIndexPath(base, decl)
	if p == "" {
		return nil
	}
	return []JudgmentDataSection{{
		Label:   memoryIndexSectionLabel,
		Content: "索引のパス: " + p,
	}}
}
