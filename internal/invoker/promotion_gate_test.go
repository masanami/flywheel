package invoker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

func runGate(ctx context.Context, t *testing.T, script string, timeout time.Duration) (core.PromotionGateOutput, string, error) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "gate.log")
	out, err := NewLauncher().RunPromotionGate(ctx, core.PromotionGateInput{
		Dir: dir, Command: []string{"/bin/sh", "-c", script}, LogPath: log, Timeout: timeout,
	})
	b, _ := os.ReadFile(log)
	return out, string(b), err
}

func TestRunPromotionGate_ReturnsTheExitCodeAndKeepsTheOutput(t *testing.T) {
	t.Parallel()
	out, log, err := runGate(context.Background(), t, "echo out; echo err >&2; exit 3", time.Minute)
	if err != nil || out.ExitCode != 3 || out.TimedOut {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	for _, s := range []string{out.OutputTail, log} {
		if !strings.Contains(s, "out") || !strings.Contains(s, "err") {
			t.Fatalf("output = %q", s)
		}
	}
}

func TestRunPromotionGate_KillsTheCommandAtTheTimeLimit(t *testing.T) {
	t.Parallel()
	out, _, err := runGate(context.Background(), t, "/bin/sleep 30", 200*time.Millisecond)
	if err != nil || !out.TimedOut {
		t.Fatalf("out = %+v, err = %v, want timed out", out, err)
	}
}

func TestRunPromotionGate_ACancelledContextAndAMissingCommandAreErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runGate(ctx, t, "exit 0", time.Minute); err == nil {
		t.Fatal("cancelled context: err = nil")
	}
	_, err := NewLauncher().RunPromotionGate(context.Background(), core.PromotionGateInput{
		Dir: t.TempDir(), Command: []string{"flywheel-no-such-command"}, LogPath: filepath.Join(t.TempDir(), "l"), Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("missing command: err = nil")
	}
}

func TestPromotionGateEnv_DropsFlywheelAndGitDirectoryVariables(t *testing.T) {
	t.Parallel()
	got := promotionGateEnv([]string{"PATH=/bin", "FLYWHEEL_WORKSPACE=/w", "FLYWHEEL_DELEGATED_RUN=1", "GIT_DIR=/g", "GIT_WORK_TREE=/t", "HOME=/h"})
	if strings.Join(got, ",") != "PATH=/bin,HOME=/h" {
		t.Fatalf("env = %v", got)
	}
}
