package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// markerCheckChildEnvVar が "1" のとき、TestMain_UnsetsTheDelegatedMarker は子プロセスとして
// 目印が外れているかだけを検査する（親が自分自身を再実行するときの合図）。
const markerCheckChildEnvVar = "FLYWHEEL_CLI_TEST_MARKER_CHECK_CHILD"

// 目印を付けて起動されたテストバイナリでも、TestMain が目印を外すこと（AC-219d。外れていなければ、
// 他のテストが目印のせいで全滅する）。通常の環境には目印が無く、TestMain の Unsetenv を消しても
// 通ってしまうため、目印を付けた子プロセスとして自分自身を起動して確かめる（値が空文字でも設定ありとなる）。
func TestMain_UnsetsTheDelegatedMarker(t *testing.T) {
	if os.Getenv(markerCheckChildEnvVar) == "1" {
		if v, set := os.LookupEnv(core.DelegatedRunEnvVar); set {
			t.Fatalf("the marker must be unset by TestMain, got %q", v)
		}
		return
	}
	for _, value := range []string{"R-7", ""} {
		t.Run("value="+value, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMain_UnsetsTheDelegatedMarker$", "-test.v")
			cmd.Env = append(os.Environ(), markerCheckChildEnvVar+"=1", core.DelegatedRunEnvVar+"="+value)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child with the marker set failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "--- PASS: TestMain_UnsetsTheDelegatedMarker") {
				t.Fatalf("child did not run the check:\n%s", out)
			}
		})
	}
}

// E2E（AC-219a）: `flywheel run` の一周で、委譲の子（偽の claude）の環境には目印が run の ID で
// 渡り、判断 J3 の子の環境には渡らない。
func TestRun_E2E_DelegationChildGetsTheMarkerAndJ3DoesNot(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	dir := t.TempDir()
	delegEnv := filepath.Join(dir, "deleg.env")
	j3Env := filepath.Join(dir, "j3.env")
	probe := func(path string) string {
		return `printf '%s' "${` + core.DelegatedRunEnvVar + `-UNSET}" > ` + shellSingleQuote(path)
	}
	child := delegateRoute("completed")
	child.ShellBefore = probe(delegEnv)
	j3 := j3Route("BRIEF-E2E")
	j3.ShellBefore = probe(j3Env)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{child, j3}, "")
	withFakeGHRoutesOnPATH(t, nil)

	runJSON(t, ws, "run")

	runs := delegateRunsOf(t, ws)
	if len(runs) != 1 || runs[0]["challenge_id"] != id {
		t.Fatalf("delegate runs = %v", runs)
	}
	if got, _ := os.ReadFile(delegEnv); string(got) != runs[0]["id"].(string) {
		t.Errorf("delegation child marker = %q, want the run ID %q", got, runs[0]["id"])
	}
	if got, _ := os.ReadFile(j3Env); string(got) != "UNSET" {
		t.Errorf("J3 child marker = %q, want UNSET", got)
	}
}
