package server

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
	db := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}

	port, cmd := startBinary(t, bin, ws, 0)
	token, _ := fetchToken(t, port) // AC-147: cookie と meta で配られる
	for name, tc := range map[string][2]string{
		"AC-148 cookie 無し": {"", token},
		"AC-148 ヘッダ無し":     {token, ""},
		"AC-149 不一致":       {token, token + "x"},
	} {
		if code, body := post(t, port, "/api/v1/workspaces/w/challenges/C-1/approve", tc[0], tc[1]); code != 403 || !strings.Contains(body, "forbidden_origin") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _ := post(t, port, "/api/v1/unknown", token, token); code != 404 {
		t.Errorf("正しい値はルーティングへ進む: %d", code)
	}

	// AC-150: 起動し直す（同じポート）と、前回の起動の値は拒否される。
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	port2, _ := startBinary(t, bin, ws, port)
	if port2 != port {
		t.Skipf("同じポートを再利用できなかった: %d → %d", port, port2)
	}
	token2, _ := fetchToken(t, port2)
	if token2 == token {
		t.Fatal("再起動で値が変わらなかった")
	}
	if code, body := post(t, port2, "/api/v1/workspaces/w/challenges/C-1/approve", token, token); code != 403 || !strings.Contains(body, "forbidden_origin") {
		t.Errorf("AC-150: %d %s", code, body)
	}

	// AC-151: 拒否した要求でストアのファイルが変わらない。
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("AC-151: ストアのファイルが変わった")
	}
}
