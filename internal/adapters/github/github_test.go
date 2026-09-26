package github

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestNew_ReturnsErrGHNotFound_WhenGHMissingFromPATH は、PATH 上に gh が
// 無いとき New が ErrGHNotFound（errors.Is で判定可能）を返すことを確認する
// （完了条件「gh が PATH に無いことを core／CLI が upstream_unavailable に
// 写せる形で返す」）。
func TestNew_ReturnsErrGHNotFound_WhenGHMissingFromPATH(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := New(Options{})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrGHNotFound) {
		t.Fatalf("want errors.Is(err, ErrGHNotFound), got %v", err)
	}
}

// TestNew_ResolvesGHFromPATH は、PATH 上に gh があれば New が成功し、
// 解決した絶対パスを保持することを確認する（AC-107 の前提となる基本動作）。
func TestNew_ResolvesGHFromPATH(t *testing.T) {
	dir := t.TempDir()
	writeExecutableScript(t, filepath.Join(dir, "gh"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)

	c, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("want non-nil client")
	}
	if want := filepath.Join(dir, "gh"); c.ghPath != want {
		t.Fatalf("ghPath = %q, want %q", c.ghPath, want)
	}
}

// TestRequireOnlyGETCalls_DetectsJoinedWriteFlags は AC-22 の検査が、値を
// 連結した形の書き込み系フラグも検出することを確かめる（検査自体の検出力）。
func TestRequireOnlyGETCalls_DetectsJoinedWriteFlags(t *testing.T) {
	for _, a := range []string{"--field=k=v", "--raw-field=k=v", "--input=f", "-fk=v", "-Fk=v"} {
		if !isJoinedWriteFlag(a) {
			t.Errorf("isJoinedWriteFlag(%q) = false, want true", a)
		}
	}
	for _, a := range []string{"api", "-i", "user", "repos/o/n/issues?state=open&per_page=100&page=1"} {
		if isJoinedWriteFlag(a) {
			t.Errorf("isJoinedWriteFlag(%q) = true, want false", a)
		}
	}
}

// TestCurrentLogin_ColorAndTTYForcingIsNeutralized は、利用者の環境に
// CLICOLOR_FORCE・GH_FORCE_TTY があっても、gh を着色・端末扱いを打ち消した
// 環境で起動することを確かめる（着色された JSON は解析できない）。
func TestCurrentLogin_ColorAndTTYForcingIsNeutralized(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "current_login_env_check")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("GH_FORCE_TTY", "1")

	c := newTestClient(t, dir)
	login, err := c.CurrentLogin(context.Background())
	if err != nil {
		t.Fatalf("CurrentLogin: %v", err)
	}
	if login != "masanami" {
		t.Fatalf("login = %q", login)
	}
}
