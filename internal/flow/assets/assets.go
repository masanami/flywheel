// Package assets は flow の資材（ticket・ticket-light・reflect・spec-draft の定義と、それが使う指示文・出力の型・つなぎのスクリプト）を
// バイナリへ埋め込み、版ごとのディレクトリへ展開する。
//
// 展開先は <ワークスペース>/.flywheel/flow/runtime/<版>/ で、版ごとに不変にする（長く走る run の途中で
// 定義・スクリプトだけ新しくなる事故を起こさない）。展開は一時ディレクトリへ書いてから rename で置き、
// 最後に中身の sha256 を印として書く。このパッケージは標準ライブラリだけを import する。
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed files
var embedded embed.FS

// Dirs は展開するディレクトリ（files/ の直下）。
var Dirs = []string{"workflows"}

// MarkerFile は展開したディレクトリに置く印（中身の sha256）。
const MarkerFile = ".bundle-sha256"

// ErrNotEmbedded は定義を埋め込んでいないこと。
var ErrNotEmbedded = errors.New("this binary has no embedded workflows")

// FS は埋め込んだ files/ の中身。
var FS fs.FS = mustSub(embedded, "files")

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}

// RuntimeRoot はワークスペースの中の、版ごとの展開先を返す。置き場は workspace の値だけから決め、
// 環境変数は読まない。
func RuntimeRoot(workspace, version string) string {
	return filepath.Join(workspace, ".flywheel", "flow", "runtime", version)
}

// Available は定義が埋め込まれているか（Dirs がすべて在るか）。
func Available(fsys fs.FS) bool {
	for _, d := range Dirs {
		fi, err := fs.Stat(fsys, d)
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// Files は Dirs 配下の通常ファイルのパス（fsys 内のスラッシュ区切り）を昇順で返す。
func Files(fsys fs.FS) ([]string, error) {
	var out []string
	for _, d := range Dirs {
		err := fs.WalkDir(fsys, d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type().IsRegular() {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// Digest は Dirs 配下の中身の sha256（パスと内容から作る。展開したディレクトリの同一性の確認に使う）。
func Digest(fsys fs.FS) (string, error) {
	names, err := Files(fsys)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, n := range names {
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", n, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Layout は展開したディレクトリの中の場所。
type Layout struct {
	Root        string
	WorkflowDir string
}

// LayoutOf は展開先 root の中の場所。
func LayoutOf(root string) Layout {
	return Layout{Root: root, WorkflowDir: filepath.Join(root, "workflows")}
}

// Ensure は fsys を root へ展開する（在れば印を確かめて使う）。root が在って印が中身と違えば上書きせずに止まる
// （その版の定義で動いている run がありうる。手で直されたものを黙って差し替えない）。
func Ensure(fsys fs.FS, root string) (Layout, error) {
	if !Available(fsys) {
		return Layout{}, ErrNotEmbedded
	}
	digest, err := Digest(fsys)
	if err != nil {
		return Layout{}, err
	}
	if err := verify(root, digest); err == nil {
		return LayoutOf(root), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Layout{}, err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return Layout{}, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root), ".extract-"+filepath.Base(root)+"-")
	if err != nil {
		return Layout{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // rename できたら空振りする
	if err := extract(fsys, tmp, digest); err != nil {
		return Layout{}, fmt.Errorf("cannot extract the embedded workflows: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp は 0700 で作る
		return Layout{}, err
	}
	if err := os.Rename(tmp, root); err != nil {
		// 同時に別のプロセスが展開し終えた場合は、その中身を確かめて使う。
		if verr := verify(root, digest); verr == nil {
			return LayoutOf(root), nil
		}
		return Layout{}, fmt.Errorf("cannot place the extracted directory at %s: %w", root, err)
	}
	return LayoutOf(root), nil
}

// verify は root の印が digest と一致するかを確かめる。root が無ければ fs.ErrNotExist を包んで返す。
func verify(root, digest string) error {
	if _, err := os.Stat(root); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, MarkerFile))
	if err != nil {
		return fmt.Errorf("%s exists but has no %s (not extracted by flywheel, or extraction was interrupted); remove it and retry", root, MarkerFile)
	}
	if got := strings.TrimSpace(string(b)); got != digest {
		return fmt.Errorf("%s holds different contents (sha256 %s) from this binary's embedded ones (%s); it is not overwritten. Remove it if no run uses it", root, got, digest)
	}
	return nil
}

// extract は Dirs 配下を dir へ書き出す。*.sh は実行可能にし、ファイルは読み取り専用にする（版のディレクトリは不変）。
func extract(fsys fs.FS, dir, digest string) error {
	names, err := Files(fsys)
	if err != nil {
		return err
	}
	for _, n := range names {
		dst := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if path.Ext(n) == ".sh" {
			mode = 0o555
		}
		if err := copyFile(fsys, n, dst, mode); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, MarkerFile), []byte(digest+"\n"), 0o444)
}

func copyFile(fsys fs.FS, name, dst string, mode os.FileMode) error {
	src, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

//go:embed SHA256SUMS
var sums []byte

// Sums は写した元のファイルごとの SHA-256 の一覧（`<hex>  <files/ からのパス>` を 1 行 1 件）。
func Sums() []byte { return sums }

// CheckSums は fsys の Dirs 配下のファイルが sums（Sums の形）と過不足なく一致するかを確かめる。
// 一覧に無いファイル・一覧にあって無いファイル・ハッシュの違いを、すべて 1 つの誤りにまとめて返す。
func CheckSums(fsys fs.FS, sums []byte) error {
	want := map[string]string{}
	for i, line := range strings.Split(string(sums), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != sha256.Size*2 || name == "" {
			return fmt.Errorf("SHA256SUMS line %d is malformed: %q", i+1, line)
		}
		if _, dup := want[name]; dup {
			return fmt.Errorf("SHA256SUMS lists %s twice", name)
		}
		want[name] = sum
	}
	names, err := Files(fsys)
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		switch got, ok := want[n]; {
		case !ok:
			problems = append(problems, n+" is not in SHA256SUMS")
		case got != hex.EncodeToString(h[:]):
			problems = append(problems, n+" differs from its sha256 in SHA256SUMS")
		}
	}
	for n := range want {
		if !seen[n] {
			problems = append(problems, n+" is in SHA256SUMS but not embedded")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
