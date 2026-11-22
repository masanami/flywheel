package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// 対話の欄の返事から起動したサイクルの子は、欄の印を引き継がない（引き継ぐと、子の cycle が欄の一覧の検査で断られて始まらない）。
func TestStartCycle_ChildDoesNotInheritTheChatMark(t *testing.T) {
	t.Setenv(core.ChatEnvSession, "S")
	t.Setenv(core.ChatEnvMessage, "1")
	t.Setenv(core.ChatEnvTools, "restricted")
	dir := t.TempDir()
	rec := filepath.Join(dir, "env")
	exe := filepath.Join(dir, "fake-flywheel")
	script := "#!/bin/sh\nenv > '" + rec + "'\necho '周 Y-1 を開始した'\nexit 0\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()

	l, err := NewCycleStarterWait(func() (string, error) { return exe, nil }, 5*time.Second)(ws, "approve")
	if err != nil || !l.Started {
		t.Fatalf("launch = %+v, err = %v, want started", l, err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{core.ChatEnvSession, core.ChatEnvMessage, core.ChatEnvTools} {
		if strings.Contains(string(b), key+"=") {
			t.Errorf("child environment carries %s", key)
		}
	}
	if !strings.Contains(string(b), "PATH=") {
		t.Error("other environment variables must be kept")
	}
}
