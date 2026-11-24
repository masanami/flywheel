package flow

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 偽の claude が n 回目に受けた argv（1 行 1 引数）・環境の目印の値を読む。
func (f *fixture) claudeArgv(n int) []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.fdir, "calls", itoa(n)+".argv"))
	if err != nil {
		f.t.Fatalf("claude call %d: %v", n, err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func (f *fixture) claudeMarker(n int) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.fdir, "calls", itoa(n)+".marker"))
	if err != nil {
		f.t.Fatalf("claude call %d marker: %v", n, err)
	}
	return string(b)
}

func itoa(n int) string { return strconv.Itoa(n) }

func hasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

// AC-49・AC-51・AC-52・AC-57: claude の子の環境の FLYWHEEL_DELEGATED_RUN は --delegated-run の値、つなぎのスクリプトの環境には無い。
// 進める側（runner）自身の環境にも無い（置くと runner が自分の起動で拒否される）。--permission-mode auto は claude の引数に入る。
func TestStartPassesTheMarkerToClaudeOnly(t *testing.T) {
	f := newFixture(t, "5.3.0")
	// 親の環境に目印があっても、進める側・つなぎのスクリプトへは渡さない。
	t.Setenv("FLYWHEEL_DELEGATED_RUN", "R-parent")
	d, errOut, code := f.startWith(StartOptions{PermissionMode: "auto", DelegatedRun: "R-77"})
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	for n := 1; n <= 3; n++ {
		if got := f.claudeMarker(n); got != "R-77" {
			t.Errorf("claude call %d: FLYWHEEL_DELEGATED_RUN = %q, want R-77 (AC-49)", n, got)
		}
		if !hasPair(f.claudeArgv(n), "--permission-mode", "auto") {
			t.Errorf("claude call %d argv lacks --permission-mode auto (AC-57): %v", n, f.claudeArgv(n))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.ghDir, "script-marker")); string(b) != "UNSET" {
		t.Errorf("FLYWHEEL_DELEGATED_RUN in a glue script = %q, want unset (AC-51)", b)
	}
	// runner 自身の環境（runner.log の隣の記録ではなく、起動時の組み立て）に目印が無い。
	if got := runnerEnv(Env{Getenv: os.Getenv}, map[string]string{DelegatedRunPassEnv: "R-77"}); containsKey(got, "FLYWHEEL_DELEGATED_RUN") {
		t.Errorf("the runner's environment has FLYWHEEL_DELEGATED_RUN (AC-52): %v", got)
	}
	if got := runnerEnv(Env{Getenv: os.Getenv}, map[string]string{DelegatedRunPassEnv: "R-77"}); !containsKV(got, DelegatedRunPassEnv+"=R-77") {
		t.Errorf("the runner's environment lacks %s=R-77 (AC-50 の引き継ぎ)", DelegatedRunPassEnv)
	}
}

func containsKey(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

func containsKV(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// AC-58: default で始めた実行の claude の引数は --permission-mode default を含む。
func TestStartPassesPermissionModeDefault(t *testing.T) {
	f := newFixture(t, "5.3.0")
	d, errOut, code := f.startWith(StartOptions{PermissionMode: "default", DelegatedRun: "R-1"})
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	if !hasPair(f.claudeArgv(1), "--permission-mode", "default") {
		t.Errorf("argv lacks --permission-mode default: %v", f.claudeArgv(1))
	}
}

// AC-50・AC-59: resume の後に起動する claude の環境の目印と --permission-mode は、その resume の値（start のときの値ではない）。
func TestResumeUsesItsOwnMarkerAndPermissionMode(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview() // start は auto・R-start
	f.respond(4, claudeStructured(map[string]any{"outcome": "no_change", "summary": "nothing", "human_question": "", "self_review": "not_run"}, 1.0))
	_, errOut, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "respond", HasInput: true, PermissionMode: "acceptEdits", DelegatedRun: "R-resume"})
	if code != ExitOK {
		t.Fatalf("resume exit %d\n%s", code, errOut)
	}
	f.waitRunnerExit(id)
	f.waitState(id, "waiting")
	if got := f.claudeMarker(4); got != "R-resume" {
		t.Errorf("FLYWHEEL_DELEGATED_RUN after resume = %q, want R-resume (AC-50)", got)
	}
	if !hasPair(f.claudeArgv(4), "--permission-mode", "acceptEdits") {
		t.Errorf("argv after resume lacks --permission-mode acceptEdits (AC-59): %v", f.claudeArgv(4))
	}
}
