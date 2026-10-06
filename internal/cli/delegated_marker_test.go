package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// M3H13: 委譲の目印（core.DelegatedRunEnvVar）のある環境では、help を除く全コマンドを
// コマンドの照合・ストアを開くより前に拒否する（値が空文字でも拒否する）。
func TestRun_DelegatedMarker_RejectsEveryCommandExceptHelp(t *testing.T) {
	for _, value := range []string{"R-7", ""} {
		for _, args := range [][]string{
			{}, {"status"}, {"status", "--json"}, {"list"}, {"show", "C-1"}, {"log"}, {"runs"},
			{"create", "--title", "x"}, {"hold", "C-1"}, {"no-such-command"}, {"approve", "C-1"},
		} {
			t.Run(value+"/"+strings.Join(args, " "), func(t *testing.T) {
				t.Setenv(core.DelegatedRunEnvVar, value)
				var stdout, stderr bytes.Buffer
				code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands())
				if code != 1 {
					t.Fatalf("exit = %d, want 1 (stderr=%s)", code, stderr.String())
				}
				if !strings.Contains(stderr.String(), core.DelegatedRunEnvVar) {
					t.Errorf("stderr = %q, want the marker named", stderr.String())
				}
				if stdout.Len() != 0 {
					t.Errorf("stdout = %q, want empty", stdout.String())
				}
				if value != "" && !strings.Contains(stderr.String(), value) {
					t.Errorf("stderr = %q, want the run ID shown", stderr.String())
				}
			})
		}
	}
}

func TestRun_DelegatedMarker_JSONErrorCodeIsVerificationRejected(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "R-7")
	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 1 || !strings.Contains(stderr.String(), `"code":"verification_rejected"`) {
		t.Errorf("exit=%d stderr=%s", code, stderr.String())
	}
}

func TestRun_DelegatedMarker_HelpStillWorks(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "R-7")
	for _, a := range []string{"help", "--help", "-h"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{a}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 || stdout.Len() == 0 {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", a, code, stdout.String(), stderr.String())
		}
	}
}

// TestMain が目印を外していること（外れていなければ、他のテストが目印のせいで全滅する）。
func TestMain_UnsetsTheDelegatedMarker(t *testing.T) {
	if _, set := os.LookupEnv(core.DelegatedRunEnvVar); set {
		t.Fatal("the marker must be unset by TestMain")
	}
}
