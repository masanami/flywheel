package invoker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

func TestRunFullCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := func(timeout time.Duration, command ...string) core.FullCheckOutcome {
		return (&Launcher{}).RunFullCheck(context.Background(), core.FullCheckRequest{Dir: dir, Command: command, Timeout: timeout})
	}
	if out := run(time.Minute, "/bin/sh", "-c", "exit 0"); !out.Passed {
		t.Fatalf("exit 0: %+v", out)
	}
	if out := run(time.Minute, "/bin/sh", "-c", "pwd; echo boom >&2; exit 3"); out.Passed ||
		!strings.Contains(out.Detail, "exit status 3") || !strings.Contains(out.Detail, "boom") || !strings.Contains(out.Detail, dir) {
		t.Fatalf("exit 3: %+v", out)
	}
	if out := run(100*time.Millisecond, "/bin/sh", "-c", "/bin/sleep 30"); out.Passed || !strings.Contains(out.Detail, "timed out") {
		t.Fatalf("timeout: %+v", out)
	}
	if out := run(time.Minute, "no-such-command-flywheel"); out.Passed || out.Detail == "" {
		t.Fatalf("missing command: %+v", out)
	}
}
