package github

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRun_CapturesStdoutStderrAndExitCode は run() が終了コード非 0 のとき
// でも stdout・stderr をそのまま返し、err を非 nil にすることを確認する
// （GetIssue が 404 のときの終了コード 1 と同じ形。run 自体はここでは
// エラー分類をしない）。
func TestRun_CapturesStdoutStderrAndExitCode(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-gh")
	writeExecutableScript(t, scriptPath, "#!/bin/sh\necho out-line\necho err-line 1>&2\nexit 3\n")

	c := &Client{ghPath: scriptPath, timeout: 5 * time.Second}
	res := c.run(context.Background())

	if string(res.stdout) != "out-line\n" {
		t.Errorf("stdout = %q, want %q", res.stdout, "out-line\n")
	}
	if string(res.stderr) != "err-line\n" {
		t.Errorf("stderr = %q, want %q", res.stderr, "err-line\n")
	}
	if res.err == nil {
		t.Error("want non-nil err for exit code 3")
	}
	if res.timedOut {
		t.Error("want timedOut = false")
	}
}

// TestRun_TimesOutAndKillsProcessGroup は c.timeout を超えたら run() が
// 速やかに timedOut=true を返し、かつ子プロセス（とそのプロセスグループ）が
// 残らないことを確認する（完了条件「プロセスを残さない」）。
//
// 偽 gh は newFakeGHDir（テストバイナリ自身を再 exec するだけの薄い
// スクリプト）を使う。素朴な「新規に書いた眠りスクリプトを直接 exec する」
// 形（過去の実装）は、初めて実行されるまっさらな実行ファイルに対する
// macOS 側の初回スキャン等の揺らぎで、ごく稀に 200ms の締切より遅れて
// プロセスが起動し pid ファイルの生成が間に合わないことがあった
// （同じ揺らぎは newFakeGHDir 経由〈既にこのテストバイナリ自体は起動済み〉
// では観測されない）。
func TestRun_TimesOutAndKillsProcessGroup(t *testing.T) {
	dir := newFakeGHDir(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv(envScenario, "sleep")
	t.Setenv(envPidFile, pidFile)
	t.Setenv(envSleepSeconds, "30")

	c := &Client{ghPath: filepath.Join(dir, "gh"), timeout: 200 * time.Millisecond}

	start := time.Now()
	res := c.run(context.Background())
	elapsed := time.Since(start)

	if !res.timedOut {
		t.Fatal("want timedOut = true")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("run took too long to return after timeout: %s", elapsed)
	}

	pidBytes, err := waitForFile(t, pidFile, 3*time.Second)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}

	if !waitForProcessGone(pid, 3*time.Second) {
		t.Fatalf("process %d (and its group) still alive after timeout kill", pid)
	}
}

// TestRun_TimeoutKillsGrandchildrenToo は、gh が孫プロセスを作っていても
// （gh は拡張などで子プロセスを起動しうる）、時間の上限でプロセスグループ
// ごと終わらせ、孫を残さず速やかに返ることを確かめる（完了条件「プロセスを
// 残さない」）。gh 本体の pid だけに SIGKILL を送る実装では、孫が生き残り、
// 孫が出力のパイプを握ったままなので WaitDelay（5 秒）まで返らない。
func TestRun_TimeoutKillsGrandchildrenToo(t *testing.T) {
	dir := newFakeGHDir(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	grandchildPidFile := filepath.Join(t.TempDir(), "grandchild-pid")
	t.Setenv(envScenario, "sleep_with_grandchild")
	t.Setenv(envPidFile, pidFile)
	t.Setenv(envGrandchildPid, grandchildPidFile)
	t.Setenv(envSleepSeconds, "30")

	c := &Client{ghPath: filepath.Join(dir, "gh"), timeout: 500 * time.Millisecond}

	start := time.Now()
	res := c.run(context.Background())
	elapsed := time.Since(start)

	pidBytes, err := waitForFile(t, grandchildPidFile, 3*time.Second)
	if err != nil {
		t.Fatalf("read grandchild pid file (the grandchild may not have started before the timeout): %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse grandchild pid: %v", err)
	}
	// 検査が落ちても孫を残さない。
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	if !res.timedOut {
		t.Fatal("want timedOut = true")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("run took %s to return after timeout; a grandchild may still hold the output pipes", elapsed)
	}
	// プロセスグループではなく孫の pid そのものを確かめる（Setpgid を外した
	// 実装では、負の pid での存在確認はグループが無いため空振りで通る）。
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(grandchild, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild process %d still alive after timeout kill", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) ([]byte, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			return b, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForProcessGone は pid（＝プロセスグループID。Setpgid: true）が
// timeout 内に消えることを確認する。
func waitForProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		// シグナル 0 は送信せず存在確認だけを行う。プロセスグループ全体
		// （負の pid）に対して確認する。
		err := syscall.Kill(-pid, 0)
		if err == syscall.ESRCH {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
