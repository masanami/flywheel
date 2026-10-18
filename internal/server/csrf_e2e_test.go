package server

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// startBinary は実バイナリの `flywheel serve` を dir で起動し、ポートを返す。
func startBinary(t *testing.T, bin, dir string, port int) (int, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(bin, "serve", "--port", strconv.Itoa(port))
	cmd.Dir = dir
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
	p, err := strconv.Atoi(strings.TrimPrefix(sc.Text(), prefix))
	if err != nil || !strings.HasPrefix(sc.Text(), prefix) {
		t.Fatalf("stderr = %q", sc.Text())
	}
	go func() {
		for sc.Scan() {
		}
	}()
	return p, cmd
}

// E2E: 実バイナリで AC-147〜151（CSRF の cookie・ヘッダ・起動ごとの値・ストア不変）を確かめる。
func TestE2ECSRFWithRealBinary(t *testing.T) {
	root := repoRoot(t)
	readInputs(t, root)
	bin := filepath.Join(t.TempDir(), "flywheel")
	if out, err := run(t, root, append(os.Environ(), "CGO_ENABLED=0"), "go", "build", "-o", bin, "./cmd/flywheel"); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	// 拒否されなければ実在の課題を承認できる経路（ワークスペース名は fleet 名 = 作業ディレクトリ名）。
	approvePath := "/api/v1/workspaces/" + core.FleetNameFromPath(mustAbs(t, ws)) + "/challenges/C-1/approve"

	port, cmd := startBinary(t, bin, ws, 0)
	token, _ := fetchToken(t, port) // AC-147: cookie と meta で配られる
	before := snapshotTree(t, filepath.Join(ws, ".flywheel"))
	for name, tc := range map[string][2]string{
		"AC-148 cookie 無し": {"", token},
		"AC-148 ヘッダ無し":     {token, ""},
		"AC-149 不一致":       {token, token + "x"},
	} {
		if code, body := post(t, port, approvePath, tc[0], tc[1]); code != 403 || !strings.Contains(body, "forbidden_origin") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _ := post(t, port, "/api/v1/unknown", token, token); code != 404 {
		t.Errorf("正しい値はルーティングへ進む: %d", code)
	}
	// AC-151: 拒否した要求で .flywheel/ 全体（-wal・-shm を含む）が変わらない。
	assertDirUnchanged(t, "AC-151", before, snapshotTree(t, filepath.Join(ws, ".flywheel")))
	// 対照: approvePath の fleet 名と課題 C-1 は実在する（同じ課題の要約は 200 で読め、正しい CSRF の承認は
	// 拒否されない）。拒否の応答が「対象が無いから何も書かない」ためではないことを示す。
	if code, body := getPath(t, port, strings.TrimSuffix(approvePath, "/approve")+"/approval", "", ""); code != 200 {
		t.Errorf("課題が引けない: %d %s", code, body)
	}

	// AC-150: 起動し直す（同じポート）と、前回の起動の値は拒否される。
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	port2, _ := startBinary(t, bin, ws, port)
	if port2 != port {
		t.Skipf("同じポートを再利用できなかった: %d → %d", port, port2)
	}
	token2, _ := fetchToken(t, port2)
	before2 := snapshotTree(t, filepath.Join(ws, ".flywheel"))
	if token2 == token {
		t.Fatal("再起動で値が変わらなかった")
	}
	if code, body := post(t, port2, approvePath, token, token); code != 403 || !strings.Contains(body, "forbidden_origin") {
		t.Errorf("AC-150: %d %s", code, body)
	}

	assertDirUnchanged(t, "AC-151（再起動後）", before2, snapshotTree(t, filepath.Join(ws, ".flywheel")))
}

// snapshotTree は dir 配下の全ファイルの相対パスと内容を返す（-wal・-shm を含む）。
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		snap[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func assertDirUnchanged(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Errorf("%s: %s が消えた", label, name)
		} else if a != b {
			t.Errorf("%s: %s の内容が変わった", label, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s: %s が増えた", label, name)
		}
	}
}
