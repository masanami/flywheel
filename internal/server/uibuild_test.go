package server

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// repoRoot は go.mod のあるディレクトリ（テストの作業ディレクトリから遡る）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// nestedEnv は make check を入れ子に起動したことを子へ伝える。make check が失敗せず
// go test まで進んだ場合に、同じテストが再び make check を起動して無限に入れ子になるのを防ぐ。
const nestedEnv = "FLYWHEEL_UIBUILD_TEST_NESTED"

// readInputs は Makefile と web/package.json を読む。make は子プロセスで読むため、
// go test のキャッシュはこれらの変更を知らない。読んでおけば変更でキャッシュが無効になる。
func readInputs(t *testing.T, root string) {
	t.Helper()
	for _, f := range []string{"Makefile", "web/package.json", "web/package-lock.json", ".github/workflows/ci.yml"} {
		if _, err := os.ReadFile(filepath.Join(root, f)); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func indexOf(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q not found in:\n%s", sub, s)
	}
	return i
}

// AC-128: make check は UI の lint・テスト・ビルドを go build の前に実行する。
func TestMakeCheckRunsUIGatesBeforeGoBuild(t *testing.T) {
	root := repoRoot(t)
	readInputs(t, root)
	out, err := run(t, root, os.Environ(), "make", "-n", "check")
	if err != nil {
		t.Fatalf("make -n check: %v\n%s", err, out)
	}
	lint := indexOf(t, out, "npm run lint")
	unit := indexOf(t, out, "npm run test")
	build := indexOf(t, out, "npm run build")
	goBuild := indexOf(t, out, "go build")
	if lint >= goBuild || unit >= goBuild || build >= goBuild {
		t.Fatalf("UI gates must precede go build:\n%s", out)
	}
}

// AC-129: UI のゲートが 1 つ失敗すると make check は非 0 で終わり、後続は実行されない。
func TestMakeCheckFailsWhenAUIGateFails(t *testing.T) {
	if os.Getenv(nestedEnv) != "" {
		t.Skip("nested run: make check reached go test after the UI gate")
	}
	root := repoRoot(t)
	readInputs(t, root)
	for _, failing := range []string{"lint", "test", "build"} {
		t.Run(failing, func(t *testing.T) {
			tmp := t.TempDir()
			web := filepath.Join(tmp, "web")
			if err := os.MkdirAll(filepath.Join(web, "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(web, "package-lock.json"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			// 印をロックファイルより新しくして npm ci を走らせない。
			stamp := filepath.Join(web, "node_modules", ".install-stamp")
			if err := os.WriteFile(stamp, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			now := time.Now().Add(time.Minute)
			if err := os.Chtimes(stamp, now, now); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(tmp, "calls.log")
			stub := filepath.Join(tmp, "npm")
			script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n[ \"$*\" = 'run " + failing + "' ] && exit 1\nexit 0\n"
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), nestedEnv+"=1", "MAKEFLAGS=", "MAKELEVEL=")
			out, err := run(t, root, env, "make", "check", "NPM="+stub, "WEB_DIR="+web)
			if err == nil {
				t.Fatalf("make check succeeded despite failing npm run %s:\n%s", failing, out)
			}
			if strings.Contains(out, "==> gofmt") || strings.Contains(out, "go build") || strings.Contains(out, "go test") {
				t.Fatalf("later gates ran after the UI failure:\n%s", out)
			}
			b, _ := os.ReadFile(log)
			if !strings.Contains(string(b), "run "+failing) {
				t.Fatalf("npm run %s was not invoked; calls:\n%s", failing, b)
			}
		})
	}
}

// AC-126: PATH に node も npm も無い環境で go build が通る。
func TestGoBuildWithoutNodeOrNpm(t *testing.T) {
	root := repoRoot(t)
	readInputs(t, root)
	goroot, err := run(t, root, os.Environ(), "go", "env", "GOROOT")
	if err != nil {
		t.Fatalf("go env GOROOT: %v\n%s", err, goroot)
	}
	goBin := filepath.Join(strings.TrimSpace(goroot), "bin", "go")
	bin := t.TempDir()
	if err := os.Symlink(goBin, filepath.Join(bin, "go")); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+bin, "CGO_ENABLED=0")
	for _, tool := range []string{"node", "npm"} {
		cmd := exec.Command("sh", "-c", "command -v "+tool)
		cmd.Env = env
		if err := cmd.Run(); err == nil {
			t.Fatalf("%s is still on PATH", tool)
		}
	}
	out, err := run(t, root, env, "go", "build", "-o", filepath.Join(t.TempDir(), "flywheel"), "./cmd/flywheel")
	if err != nil {
		t.Fatalf("go build without node/npm: %v\n%s", err, out)
	}
}

// AC-127: UI をビルドして埋め込んだバイナリの serve は GET / でビルドした index.html を返す。
// 成果物は make check の ui-build が先に作る（テストの中で作り直すと、同時に走る go build の
// 埋め込み元を壊しうる）。成果物が無い環境ではスキップする。
func TestServeReturnsBuiltUIIndex(t *testing.T) {
	root := repoRoot(t)
	readInputs(t, root)
	built, err := os.ReadFile(filepath.Join(root, "internal", "server", "dist", "ui", "index.html"))
	if os.IsNotExist(err) {
		t.Skip("internal/server/dist/ui/index.html is missing; run `make ui-build` (make check does) to produce the built UI")
	}
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "flywheel")
	if out, err := run(t, root, append(os.Environ(), "CGO_ENABLED=0"), "go", "build", "-o", bin, "./cmd/flywheel"); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	work := t.TempDir()
	cmd := exec.Command(bin, "serve", "--port", "0")
	cmd.Dir = work
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(stderr)
	if !sc.Scan() {
		t.Fatal("serve printed nothing")
	}
	const prefix = "listening on 127.0.0.1:"
	line := sc.Text()
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("stderr = %q", line)
	}
	port, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	for path, want := range map[string][]byte{"/": built} {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Equal(got, want) {
			t.Fatalf("GET %s = %d %q, want built index.html", path, resp.StatusCode, got)
		}
		if !bytes.Contains(got, []byte("manifest.webmanifest")) {
			t.Fatalf("built index.html does not reference the manifest: %s", got)
		}
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(mb, []byte(`"standalone"`)) {
		t.Fatalf("manifest = %d %s", resp.StatusCode, mb)
	}
}
