package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// --- 記憶の書き込み（J6 の出力の memory の要素を置き場へ書く） ---
//
// docs/features/m4.5-learning-and-self-improvement.md §クリティカル設計決定 3・
// M45H10・M45H22・M45P20。書くのは作成と追記だけで、上書き・削除・改名は
// 持たない。書き込みは memory_dir の中に限り、標準ライブラリの os だけで行う
// （git・internal/adapters・internal/invoker を使わない）。

// ErrMemoryWriteRejected は記憶の書き込みの出力が規則に反することを表す
// （閉集合の外の値・必須項目の欠け・同名の作成・無いファイルへの追記・
// 置き場の外へ出るパス・上限の超過）。何も書かない。J6 の写像はこれを
// invalid_output に写す。
var ErrMemoryWriteRejected = errors.New("core: memory write rejected")

// 記憶の書き込みの mode。
const (
	MemoryModeCreate = "create"
	MemoryModeAppend = "append"
)

// 記憶の種類（旧 agent-memory と同じ閉集合）。
const (
	MemoryTypeMap        = "map"
	MemoryTypeTacit      = "tacit"
	MemoryTypeExperience = "experience"
	MemoryTypeReference  = "reference"
)

// memoryItem は J6 の出力の memory の要素。
// Outcome・Target・Signal は Type が experience のとき必須。
type memoryItem struct {
	Mode        string
	Type        string
	Slug        string
	Description string
	Confidence  string
	Sources     []string
	Outcome     string
	Target      string
	Signal      string
	Body        string
}

// memoryWritten は書いたファイル 1 件の結果。Path は置き場からの相対パス、
// SHA256 は書いた本文の SHA-256（16 進。作成はファイル全体・追記は足した分）。
type memoryWritten struct {
	Path   string
	Mode   string
	SHA256 string
}

// memoryWriteFault はテストが書き込みの途中の失敗を注入するフック
// （n 件目〔0 始まり〕のファイルを書く直前と、n = 要素数で索引を書く直前に呼ぶ）。本番では nil。
var memoryWriteFault func(n int) error

var memorySlugRe = regexp.MustCompile(`^[^\s/\\]+$`)

func memoryReject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMemoryWriteRejected, fmt.Sprintf(format, args...))
}

func validMemoryType(t string) bool {
	switch t {
	case MemoryTypeMap, MemoryTypeTacit, MemoryTypeExperience, MemoryTypeReference:
		return true
	}
	return false
}

// validateMemorySlug は slug が置き場の直下の 1 つのファイル名の一部になることを
// 確かめる（絶対パス・".."・"/"・空白や制御文字を含むものを拒否する）。
func validateMemorySlug(slug string) error {
	if slug == "" {
		return memoryReject("slug is empty")
	}
	if filepath.IsAbs(slug) || strings.Contains(slug, "..") || !memorySlugRe.MatchString(slug) {
		return memoryReject("slug %q must not be an absolute path or contain '..', '/' or whitespace", slug)
	}
	for _, r := range slug {
		if unicode.IsControl(r) {
			return memoryReject("slug %q contains a control character", slug)
		}
	}
	return nil
}

// yamlScalar は frontmatter の 1 行の値を返す。素のままで安全なら素で、
// そうでなければ JSON の文字列（YAML の二重引用符の文字列として有効）にする。
func yamlScalar(s string) string {
	if s != "" && s == strings.TrimSpace(s) && !strings.ContainsAny(s, "\n\r\t") &&
		!strings.Contains(s, ": ") && !strings.Contains(s, " #") &&
		!strings.HasSuffix(s, ":") && !strings.ContainsAny(s[:1], "[]{}&*!|>'\"%@`-?:,#") {
		switch strings.ToLower(s) {
		case "true", "false", "null", "yes", "no", "on", "off", "~":
		default:
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return s
			}
		}
	}
	return jsonString(s)
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// memoryFileContent は作成するファイル全体（frontmatter と本文）を返す。
func memoryFileContent(it memoryItem, domain string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", yamlScalar(it.Slug))
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(it.Description))
	fmt.Fprintf(&b, "domain: %s\n", yamlScalar(domain))
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  type: %s\n", it.Type)
	if it.Type == MemoryTypeExperience {
		fmt.Fprintf(&b, "  outcome: %s\n", yamlScalar(it.Outcome))
		fmt.Fprintf(&b, "  target: %s\n", yamlScalar(it.Target))
		fmt.Fprintf(&b, "  signal: %s\n", yamlScalar(it.Signal))
	}
	fmt.Fprintf(&b, "  confidence: %s\n", yamlScalar(it.Confidence))
	srcs := make([]string, 0, len(it.Sources))
	for _, s := range it.Sources {
		srcs = append(srcs, jsonString(s))
	}
	fmt.Fprintf(&b, "  sources: [%s]\n", strings.Join(srcs, ", "))
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(it.Body, "\n"))
	b.WriteString("\n")
	return b.String()
}

// memoryAppendSection は追記する節（日付つきの見出しと本文）を返す。
// 先頭は空行で始め、既存の内容が改行で終わっていないときは改行を足す。
func memoryAppendSection(it memoryItem, date string, needNewline bool) string {
	s := "\n## " + date + "\n\n" + strings.TrimRight(it.Body, "\n") + "\n"
	if needNewline {
		s = "\n" + s
	}
	return s
}

// plannedMemoryWrite は検査を終えた書き込み 1 件。
type plannedMemoryWrite struct {
	item     memoryItem
	rel      string // 置き場からの相対パス（<type>-<slug>.md）
	path     string // 書くパス（append はシンボリックリンクを辿った実体）
	content  string // 書く内容（作成はファイル全体・追記は足す節）
	indexRow string // 作成のとき索引に足す 1 行
}

// writeMemory は J6 の出力の memory の要素 items を、decl の置き場（base の下の
// memory_dir）へ書く。すべての要素を検査してから書き、規則に反する出力は
// ErrMemoryWriteRejected で何も書かずに拒否する。書き込みの途中で失敗したら、
// それより前に書いたファイル（作成は削除・追記は元の長さへ戻す・索引も同様）を
// 元に戻して、その失敗を返す。now は追記の節の日付に使う。core は git を起動せず、
// コミットもしない。
func writeMemory(base string, decl *AgentDeclaration, items []memoryItem, now time.Time) ([]memoryWritten, error) {
	plan, err := prepareMemoryWrite(base, decl, items, now)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, nil
	}
	if _, err := plan.apply(); err != nil {
		return nil, err
	}
	return plan.written(), nil
}

// memoryWritePlan は検査を終えた書き込みの全体（何も書いていない）。J6 の写像は、検査
// （prepareMemoryWrite）と書き込み（apply。元に戻す関数を返す）を分けて呼び、ストアの
// トランザクションが失敗したときに書いたファイルを戻す。
type memoryWritePlan struct {
	root        memoryRoot
	dir         string
	plans       []plannedMemoryWrite
	indexPath   string
	indexOrig   []byte
	indexExists bool
	hasCreate   bool
}

// prepareMemoryWrite は writeMemory の検査の部分。items のすべてを検査して計画を返す
// （何も書かず、置き場も作らない）。規則に反する出力は ErrMemoryWriteRejected。items が空なら
// (nil, nil)。
func prepareMemoryWrite(base string, decl *AgentDeclaration, items []memoryItem, now time.Time) (*memoryWritePlan, error) {
	dir, limit := defaultMemoryDir, MemoryWriteLimit{Files: defaultMemoryWriteFiles, Bytes: defaultMemoryWriteBytes}
	if decl != nil {
		if decl.MemoryDir != "" {
			dir = decl.MemoryDir
		}
		if decl.MemoryWriteLimit.Files > 0 && decl.MemoryWriteLimit.Bytes > 0 {
			limit = decl.MemoryWriteLimit
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > limit.Files {
		return nil, memoryReject("%d memory items exceed memory_write_limit.files (%d)", len(items), limit.Files)
	}

	root, err := resolveMemoryRoot(base, dir)
	if err != nil {
		return nil, err
	}
	plans, hasCreate, err := planMemoryWrites(root, items, limit, now.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	p := &memoryWritePlan{root: root, dir: dir, plans: plans, hasCreate: hasCreate}
	if hasCreate {
		p.indexPath, p.indexOrig, p.indexExists, err = planMemoryIndex(root)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

// written は書き込みの一覧（置き場からの相対パス・mode・書く本文の SHA-256）を返す。
// 本文は計画の時点で決まっているので、書く前に作業ログの内容を組み立てられる。
func (p *memoryWritePlan) written() []memoryWritten {
	out := make([]memoryWritten, 0, len(p.plans))
	for _, pl := range p.plans {
		sum := sha256.Sum256([]byte(pl.content))
		out = append(out, memoryWritten{Path: pl.rel, Mode: pl.item.Mode, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

// apply は計画を書く。失敗したらそれまでに書いた分を元に戻してエラーを返す。成功したら、
// 書いた分をすべて元に戻す関数を返す（呼び出し側が後続の失敗で呼ぶ）。
func (p *memoryWritePlan) apply() (undo func(), err error) {
	return applyMemoryWrites(p.root, p.dir, p.plans, p.indexPath, p.indexOrig, p.indexExists)
}

// planMemoryWrites はすべての要素を検査し、書き込みの計画を返す（何も書かない）。
func planMemoryWrites(root memoryRoot, items []memoryItem, limit MemoryWriteLimit, date string) ([]plannedMemoryWrite, bool, error) {
	plans := make([]plannedMemoryWrite, 0, len(items))
	created := map[string]bool{} // この出力で作るファイル
	touched := map[string]bool{} // この出力で既に書く対象にしたファイル（改行の判定用）
	hasCreate := false
	for i, it := range items {
		if it.Mode != MemoryModeCreate && it.Mode != MemoryModeAppend {
			return nil, false, memoryReject("memory[%d]: mode %q must be create or append", i, it.Mode)
		}
		if !validMemoryType(it.Type) {
			return nil, false, memoryReject("memory[%d]: type %q must be map, tacit, experience or reference", i, it.Type)
		}
		if err := validateMemorySlug(it.Slug); err != nil {
			return nil, false, fmt.Errorf("memory[%d]: %w", i, err)
		}
		if strings.TrimSpace(it.Body) == "" {
			return nil, false, memoryReject("memory[%d]: body is empty", i)
		}
		rel := it.Type + "-" + it.Slug + ".md"
		path := filepath.Join(root.abs, rel)
		p := plannedMemoryWrite{item: it, rel: rel, path: path}

		switch it.Mode {
		case MemoryModeCreate:
			if strings.TrimSpace(it.Description) == "" || strings.ContainsAny(it.Description, "\r\n") {
				return nil, false, memoryReject("memory[%d]: description must be a non-empty single line", i)
			}
			if strings.TrimSpace(it.Confidence) == "" || strings.ContainsAny(it.Confidence, " \t\r\n") {
				return nil, false, memoryReject("memory[%d]: confidence must be a non-empty single word", i)
			}
			if it.Type == MemoryTypeExperience &&
				(strings.TrimSpace(it.Outcome) == "" || strings.TrimSpace(it.Target) == "" || strings.TrimSpace(it.Signal) == "") {
				return nil, false, memoryReject("memory[%d]: experience requires outcome, target and signal", i)
			}
			if created[rel] {
				return nil, false, memoryReject("memory[%d]: %s is created twice", i, rel)
			}
			if _, err := os.Lstat(path); err == nil {
				return nil, false, memoryReject("memory[%d]: %s already exists", i, rel)
			} else if !os.IsNotExist(err) {
				return nil, false, memoryReject("memory[%d]: %s cannot be inspected: %v", i, rel, err)
			}
			p.content = memoryFileContent(it, root.domain)
			p.indexRow = memoryIndexLine(it.Slug, it.Type, it.Confidence, it.Description)
			if len(p.content) > limit.Bytes {
				return nil, false, memoryReject("memory[%d]: %s is %d bytes, over memory_write_limit.bytes (%d)", i, rel, len(p.content), limit.Bytes)
			}
			created[rel], touched[rel] = true, true
			hasCreate = true
		case MemoryModeAppend:
			needNL := false
			if !created[rel] { // この出力で作るファイルの本文は改行で終わる

				resolved, err := filepath.EvalSymlinks(path)
				if err != nil {
					if os.IsNotExist(err) {
						return nil, false, memoryReject("memory[%d]: %s does not exist", i, rel)
					}
					return nil, false, memoryReject("memory[%d]: %s cannot be resolved: %v", i, rel, err)
				}
				if !root.exists || !pathInside(root.real, resolved) {
					return nil, false, memoryReject("memory[%d]: %s resolves outside the memory directory", i, rel)
				}
				fi, err := os.Stat(resolved)
				if err != nil || !fi.Mode().IsRegular() {
					return nil, false, memoryReject("memory[%d]: %s is not a regular file", i, rel)
				}
				p.path = resolved
				if !touched[rel] {
					needNL, err = lacksTrailingNewline(resolved)
					if err != nil {
						return nil, false, fmt.Errorf("inspect %s: %w", rel, err)
					}
				}
			}
			p.content = memoryAppendSection(it, date, needNL)
			if len(p.content) > limit.Bytes {
				return nil, false, memoryReject("memory[%d]: append to %s is %d bytes, over memory_write_limit.bytes (%d)", i, rel, len(p.content), limit.Bytes)
			}
			touched[rel] = true
		}
		plans = append(plans, p)
	}
	return plans, hasCreate, nil
}

func lacksTrailingNewline(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if fi.Size() == 0 {
		return false, nil
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, fi.Size()-1); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return buf[0] != '\n', nil
}

// planMemoryIndex は索引（INDEX.md）の書き込み先を検査する。索引が置き場の外へ
// 出るシンボリックリンクなら拒否する。無ければ exists=false で、書くときに作る。
func planMemoryIndex(root memoryRoot) (path string, orig []byte, exists bool, err error) {
	p := filepath.Join(root.abs, memoryIndexFileName)
	if _, lerr := os.Lstat(p); lerr != nil {
		if os.IsNotExist(lerr) {
			return p, nil, false, nil
		}
		return "", nil, false, memoryReject("index cannot be inspected: %v", lerr)
	}
	resolved, rerr := filepath.EvalSymlinks(p)
	if rerr != nil {
		return "", nil, false, memoryReject("index cannot be resolved: %v", rerr)
	}
	if !root.exists || !pathInside(root.real, resolved) {
		return "", nil, false, memoryReject("%s resolves outside the memory directory", memoryIndexFileName)
	}
	fi, serr := os.Stat(resolved)
	if serr != nil || !fi.Mode().IsRegular() {
		return "", nil, false, memoryReject("%s is not a regular file", memoryIndexFileName)
	}
	data, rerr := os.ReadFile(resolved)
	if rerr != nil {
		return "", nil, false, fmt.Errorf("read %s: %w", memoryIndexFileName, rerr)
	}
	return resolved, data, true, nil
}

// applyMemoryWrites は plans を順に書く。失敗したら書いた分を元に戻す。
func applyMemoryWrites(root memoryRoot, dir string, plans []plannedMemoryWrite, indexPath string, indexOrig []byte, indexExists bool) (_ func(), err error) {
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	defer func() {
		if err != nil {
			rollback()
		}
	}()

	if !root.exists {
		made, mkerr := mkdirAllTracked(root.abs)
		for _, d := range made {
			d := d
			undo = append(undo, func() { _ = os.Remove(d) })
		}
		if mkerr != nil {
			return nil, fmt.Errorf("create memory directory: %w", mkerr)
		}
	}

	var rows []string
	for n, p := range plans {
		if memoryWriteFault != nil {
			if ferr := memoryWriteFault(n); ferr != nil {
				return nil, fmt.Errorf("write %s: %w", p.rel, ferr)
			}
		}
		switch p.item.Mode {
		case MemoryModeCreate:
			f, oerr := os.OpenFile(p.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if oerr != nil {
				return nil, fmt.Errorf("create %s: %w", p.rel, oerr)
			}
			path := p.path
			undo = append(undo, func() { _ = os.Remove(path) })
			_, werr := f.WriteString(p.content)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return nil, fmt.Errorf("write %s: %w", p.rel, errors.Join(werr, cerr))
			}
			rows = append(rows, p.indexRow)
		case MemoryModeAppend:
			fi, serr := os.Stat(p.path)
			if serr != nil {
				return nil, fmt.Errorf("stat %s: %w", p.rel, serr)
			}
			f, oerr := os.OpenFile(p.path, os.O_WRONLY|os.O_APPEND, 0)
			if oerr != nil {
				return nil, fmt.Errorf("append %s: %w", p.rel, oerr)
			}
			path, size := p.path, fi.Size()
			undo = append(undo, func() { _ = os.Truncate(path, size) })
			_, werr := f.WriteString(p.content)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return nil, fmt.Errorf("write %s: %w", p.rel, errors.Join(werr, cerr))
			}
		}
	}

	if len(rows) > 0 {
		if memoryWriteFault != nil { // 索引の書き込みも失敗注入の対象（n = 要素数）
			if ferr := memoryWriteFault(len(plans)); ferr != nil {
				return nil, fmt.Errorf("write %s: %w", memoryIndexFileName, ferr)
			}
		}
		var add strings.Builder
		if indexExists {
			if len(indexOrig) > 0 && indexOrig[len(indexOrig)-1] != '\n' {
				add.WriteString("\n")
			}
		} else {
			add.WriteString(newMemoryIndexHeader(dir))
		}
		for _, r := range rows {
			add.WriteString(r + "\n")
		}
		if indexExists {
			size := int64(len(indexOrig))
			undo = append(undo, func() { _ = os.Truncate(indexPath, size) })
			f, oerr := os.OpenFile(indexPath, os.O_WRONLY|os.O_APPEND, 0)
			if oerr != nil {
				return nil, fmt.Errorf("append %s: %w", memoryIndexFileName, oerr)
			}
			_, werr := f.WriteString(add.String())
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return nil, fmt.Errorf("write %s: %w", memoryIndexFileName, errors.Join(werr, cerr))
			}
		} else {
			f, oerr := os.OpenFile(indexPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if oerr != nil {
				return nil, fmt.Errorf("create %s: %w", memoryIndexFileName, oerr)
			}
			undo = append(undo, func() { _ = os.Remove(indexPath) })
			_, werr := f.WriteString(add.String())
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return nil, fmt.Errorf("write %s: %w", memoryIndexFileName, errors.Join(werr, cerr))
			}
		}
	}
	return rollback, nil
}

// mkdirAllTracked は os.MkdirAll と同じく dir を作り、新しく作ったディレクトリを
// 浅い順に返す（失敗時に深い順に消すため）。
func mkdirAllTracked(dir string) ([]string, error) {
	var missing []string
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append([]string{p}, missing...)
		if filepath.Dir(p) == p {
			break
		}
	}
	var made []string
	for _, p := range missing {
		if err := os.Mkdir(p, 0o755); err != nil && !os.IsExist(err) {
			return made, err
		}
		made = append(made, p)
	}
	return made, nil
}
