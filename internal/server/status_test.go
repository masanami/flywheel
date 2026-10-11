package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは GET /api/v1/workspaces/{name}/status（AC-33・34・38〜44）を、実ストア・
// 偽の gh・実際の HTTP の要求で検証する。`flywheel status --json` との一致は、
// cmd/flywheel をビルドして同じストアに対して実行して比べる（server は cli を import しない）。

var (
	cliBuildOnce sync.Once
	cliBinary    string
	cliBuildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if cliBinary != "" {
		_ = os.RemoveAll(filepath.Dir(cliBinary))
	}
	os.Exit(code)
}

func flywheelBinary(t *testing.T) string {
	t.Helper()
	cliBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "flywheel-server-test-bin")
		if err != nil {
			cliBuildErr = err
			return
		}
		cliBinary = filepath.Join(dir, "flywheel")
		cmd := exec.Command("go", "build", "-o", cliBinary, "github.com/masanami/flywheel/cmd/flywheel")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			cliBuildErr = fmt.Errorf("go build: %w: %s", err, out)
		}
	})
	if cliBuildErr != nil {
		t.Fatal(cliBuildErr)
	}
	return cliBinary
}

func cliStatusJSON(t *testing.T, ws string) string {
	t.Helper()
	cmd := exec.Command(flywheelBinary(t), "status", "--json", "--workspace", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("flywheel status --json: %v (%s)", err, stderr.String())
	}
	return stdout.String()
}

func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("api body is not JSON: %v (%q)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("cli output is not JSON: %v (%q)", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("api body != cli output\napi: %s\ncli: %s", got, want)
	}
}

func statusRequest(t *testing.T, port int, name string) (int, string) {
	t.Helper()
	return getPath(t, port, "/api/v1/workspaces/"+name+"/status", "127.0.0.1:"+strconv.Itoa(port), "")
}

// assertStatusError は GET …/status が wantStatus・wantCode の失敗を返すことを確かめる。
func assertStatusError(t *testing.T, port int, name string, wantStatus int, wantCode string) {
	t.Helper()
	code, body := statusRequest(t, port, name)
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, body)
	}
	if code != wantStatus || doc.Error.Code != wantCode || doc.Error.Message == "" {
		t.Errorf("GET %s/status = %d %s, want %d %s (with a message)", name, code, body, wantStatus, wantCode)
	}
}

// writeFakeGH は route（引数列の末尾一致）に従って応答し、呼ばれるたびに引数列を記録する
// 偽の gh を dir に置く。どれにも合わない呼び出しは終了コード 1。
func writeFakeGH(t *testing.T, routes map[string]string) (dir string, calls func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir = t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	var b strings.Builder
	b.WriteString("#!/bin/sh\nprintf 'CALL %s\\n' \"$*\" >> '" + logPath + "'\ncase \"$*\" in\n")
	i := 0
	for match, out := range routes {
		fmt.Fprintf(&b, "*'%s')\ncat <<'EOF_%d'\n%s\nEOF_%d\nexit 0\n;;\n", match, i, out, i)
		i++
	}
	b.WriteString("*)\necho 'fake gh: no route' >&2\nexit 1\n;;\nesac\n")
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, func() int {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "CALL ")
	}
}

func ghRoutesPending() map[string]string { return ghRoutesPendingFor(1) }

func ghRoutesPendingFor(n int) map[string]string {
	sha := fmt.Sprintf("abc%d", n)
	return map[string]string{
		fmt.Sprintf("repos/o/r/pulls/%d", n):                                    fmt.Sprintf(`{"html_url":"https://github.com/o/r/pull/%d","title":"PR","state":"open","merged_at":null,"base":{"ref":"develop"},"head":{"sha":"%s"}}`, n, sha),
		fmt.Sprintf("repos/o/r/commits/%s/check-runs?page=1&per_page=100", sha): `{"total_count":2,"check_runs":[{"name":"build","status":"completed","conclusion":"success"},{"name":"lint","status":"in_progress","conclusion":null}]}`,
		fmt.Sprintf("repos/o/r/commits/%s/status?page=1&per_page=100", sha):     `{"state":"success","total_count":0,"statuses":[]}`,
	}
}

// ghCallsPerFetch は 1 つの PR のチェックの取得で gh が起動される回数（PR・check-runs・status）。
const ghCallsPerFetch = 3

// verifyingWorkspace は、検証中で o/r の PR #1 を成果物に持つ課題を含む、agent.json つきの
// ワークスペースを作る。
func verifyingWorkspace(t *testing.T) string { return verifyingWorkspaceWithPR(t, 1) }

func verifyingWorkspaceWithPR(t *testing.T, pr int) string {
	t.Helper()
	ws := initWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(`{"position_file": "position.md"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "t", DoneCriteria: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	cid, err := strconv.Atoi(strings.TrimPrefix(ch.ID, "C-"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(map[string]any{
		"verdict": "plan", "summary": "S", "steps": []string{"a", "b"}, "repo": "direct-repo", "operation": "brief-op",
		"size": "M", "done_criteria": "D", "budget_impl_usd": nil, "budget_review_usd": nil, "cross_repo": false,
		"related_repos": []string{}, "question": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	coretest.InsertApprovedPlan(t, ws, cid, "PLAN", string(spec))
	coretest.SetChallengeStatus(t, ws, cid, "verifying")
	runID := coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: cid, Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
		EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 5_000_000, BudgetBucket: "impl",
	})
	coretest.InsertRunArtifact(t, ws, runID, "pr", fmt.Sprintf("https://github.com/o/r/pull/%d", pr), "open", "develop")
	return ws
}

func withGHOnPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func waitingExternalCount(t *testing.T, body string) int {
	t.Helper()
	var doc struct {
		WaitingExternal struct {
			Challenges []json.RawMessage `json:"challenges"`
		} `json:"waiting_external"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return len(doc.WaitingExternal.Challenges)
}

// AC-38: 本文は同じストアでの flywheel status --json の標準出力と JSON として等しい。
func TestStatusAPI_EqualsCLIStatusJSON(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := statusRequest(t, port, "w")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if n := waitingExternalCount(t, body); n != 1 {
		t.Errorf("waiting_external.challenges = %d, want 1 (the fixture must exercise gh): %s", n, body)
	}
	sameJSON(t, body, cliStatusJSON(t, ws))
}

// AC-38: agent.json が読めないときも CLI と同じ（budget_exhausted の判定の対象外になるだけ）。
func TestStatusAPI_UnreadableAgentJSONEqualsCLI(t *testing.T) {
	ws := initWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := statusRequest(t, port, "w")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	sameJSON(t, body, cliStatusJSON(t, ws))
}

// AC-39: PATH に gh が無くても 200 で、本文は同じ環境の flywheel status --json と等しい。
func TestStatusAPI_WithoutGHIs200AndEqualsCLI(t *testing.T) {
	ws := verifyingWorkspace(t)
	flywheelBinary(t)             // PATH を差し替える前にビルドしておく
	t.Setenv("PATH", t.TempDir()) // gh も何も無い
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := statusRequest(t, port, "w")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if n := waitingExternalCount(t, body); n != 0 {
		t.Errorf("waiting_external.challenges = %d, want 0 without gh", n)
	}
	sameJSON(t, body, cliStatusJSON(t, ws))
}

// AC-40: 保持の時間内の 2 回目の要求は gh を起動しない。
func TestStatusAPI_CachesChecksWithinTTL(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	s.checks.now = func() time.Time { return time.Unix(1000, 0) }

	for i := 0; i < 3; i++ {
		if code, body := statusRequest(t, port, "w"); code != http.StatusOK || waitingExternalCount(t, body) != 1 {
			t.Fatalf("request %d: %d %s", i, code, body)
		}
	}
	if got := calls(); got != ghCallsPerFetch {
		t.Errorf("gh calls = %d, want %d (one fetch for three requests)", got, ghCallsPerFetch)
	}
}

// AC-41: 検証中の PR を持つ 2 つのワークスペースを交互に 2 回ずつ呼ぶと、gh の起動は
// ワークスペースごとに 1 回ずつ（PR ごとの保持）。
func TestStatusAPI_TwoWorkspacesAlternatingFetchOncePerWorkspace(t *testing.T) {
	a, b := verifyingWorkspaceWithPR(t, 1), verifyingWorkspaceWithPR(t, 2)
	routes := ghRoutesPendingFor(1)
	for k, v := range ghRoutesPendingFor(2) {
		routes[k] = v
	}
	dir, calls := writeFakeGH(t, routes)
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: a}, core.FleetWorkspace{Name: "b", Path: b})
	s.checks.now = func() time.Time { return time.Unix(1000, 0) }

	for i := 0; i < 2; i++ {
		for _, name := range []string{"a", "b"} {
			if code, body := statusRequest(t, port, name); code != http.StatusOK || waitingExternalCount(t, body) != 1 {
				t.Fatalf("round %d %s: %d %s", i, name, code, body)
			}
		}
	}
	if got := calls(); got != 2*ghCallsPerFetch {
		t.Errorf("gh calls = %d, want %d (one fetch per workspace)", got, 2*ghCallsPerFetch)
	}
}

// AC-42: 保持の時間が過ぎると取り直す。時間は取得した時刻から数える。
func TestStatusAPI_RefetchesAfterTTL(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	s.checks.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	statusRequest(t, port, "w")
	advance(defaultCheckTTL - time.Second)
	statusRequest(t, port, "w")
	if got := calls(); got != ghCallsPerFetch {
		t.Fatalf("gh calls = %d before expiry, want %d", got, ghCallsPerFetch)
	}
	advance(2 * time.Second) // 取得から ttl を超えた
	statusRequest(t, port, "w")
	if got := calls(); got != 2*ghCallsPerFetch {
		t.Errorf("gh calls = %d after expiry, want %d", got, 2*ghCallsPerFetch)
	}
	// 取り直した時刻から数え直す（直後は保持される）。
	statusRequest(t, port, "w")
	if got := calls(); got != 2*ghCallsPerFetch {
		t.Errorf("gh calls = %d, want still %d", got, 2*ghCallsPerFetch)
	}
}

// AC-43: 取得の失敗も保持し、失敗の間は waiting_external に載せないまま 200 を返す。
func TestStatusAPI_FailedFetchIsCachedToo(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, map[string]string{}) // すべての呼び出しが失敗
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	s.checks.now = func() time.Time { return time.Unix(1000, 0) }

	for i := 0; i < 3; i++ {
		code, body := statusRequest(t, port, "w")
		if code != http.StatusOK || waitingExternalCount(t, body) != 0 {
			t.Fatalf("request %d: %d %s", i, code, body)
		}
	}
	if got := calls(); got != 1 {
		t.Errorf("gh calls = %d, want 1 (the failure is held)", got)
	}
}

// AC-42: 保持の時間は server の内部の設定で、短くすれば実時間で失効する。
func TestStatusAPI_ShortTTLExpiresInRealTime(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	s.checks.ttl = 20 * time.Millisecond

	statusRequest(t, port, "w")
	time.Sleep(60 * time.Millisecond)
	statusRequest(t, port, "w")
	if got := calls(); got != 2*ghCallsPerFetch {
		t.Errorf("gh calls = %d, want %d", got, 2*ghCallsPerFetch)
	}
}

// AC-44: 候補は要求ごとに導き直す。検証中でなくなった課題は、保持が残っていても載らない。
func TestStatusAPI_CandidatesAreRederivedPerRequest(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	s.checks.now = func() time.Time { return time.Unix(1000, 0) }

	if _, body := statusRequest(t, port, "w"); waitingExternalCount(t, body) != 1 {
		t.Fatalf("first request: %s", body)
	}
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_plan_approval")
	code, body := statusRequest(t, port, "w")
	if code != http.StatusOK || waitingExternalCount(t, body) != 0 {
		t.Errorf("after leaving verifying: %d %s, want 200 with no waiting_external", code, body)
	}
	if got := calls(); got != ghCallsPerFetch {
		t.Errorf("gh calls = %d, want %d", got, ghCallsPerFetch)
	}
}

// 検証中の課題が無ければ、どの要求でも gh を起動しない（SSE の観測に当たる読み取りを含む）。
func TestStatusAPI_NoVerifyingChallengeNeverCallsGH(t *testing.T) {
	ws := initWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	statusRequest(t, port, "w")
	listWorkspaces(t, port)
	if got := calls(); got != 0 {
		t.Errorf("gh calls = %d, want 0", got)
	}
}

// AC-34: fleet に無い名前は 404・not_found。
func TestStatusAPI_UnknownWorkspaceIs404(t *testing.T) {
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: initWorkspace(t)})
	assertStatusError(t, port, "nope", http.StatusNotFound, CodeNotFound)
}

// AC-33: ok でないワークスペースは、その state と同じエラーコードと写像した状態コードで返る。
func TestStatusAPI_NotOKWorkspaceReturnsItsStateCode(t *testing.T) {
	tooNew := initWorkspace(t)
	coretest.SetStoreVersion(t, tooNew, latestVersion(t)+1)
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	junk := "this is not an sqlite database, just text padding it out to be long enough to look like a header"
	if err := os.WriteFile(filepath.Join(bad, ".flywheel", "flywheel.db"), []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	_, port := startFleetServer(t,
		core.FleetWorkspace{Name: "missing", Path: t.TempDir()},
		core.FleetWorkspace{Name: "new", Path: tooNew},
		core.FleetWorkspace{Name: "bad", Path: bad})
	assertStatusError(t, port, "missing", http.StatusNotFound, CodeStoreNotFound)
	assertStatusError(t, port, "new", http.StatusServiceUnavailable, CodeStoreTooNew)
	assertStatusError(t, port, "bad", http.StatusServiceUnavailable, CodeStoreError)
}

func TestStatusAPI_NonGETIs404(t *testing.T) {
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: initWorkspace(t)})
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/api/v1/workspaces/w/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404", resp.StatusCode)
	}
}

// エラーコードから HTTP の状態コードへの写像（閉集合。以降の全 API が共有する）。
func TestErrorStatusTable(t *testing.T) {
	want := map[string]int{
		CodeNotFound: 404, CodeStoreNotFound: 404,
		CodeValidationFailed: 400, CodeUsageError: 400,
		CodeInvalidTransition: 409, CodeTerminalState: 409, CodeConflict: 409, CodeLocked: 409,
		CodeVerificationRejected: 403, CodeConfirmationMismatch: 403,
		CodeStoreTooNew: 503, CodeStoreBusy: 503, CodeStoreError: 503,
		CodeInternalError: 500, "something_else": 500,
	}
	for code, status := range want {
		if got := errorStatus(code); got != status {
			t.Errorf("errorStatus(%q) = %d, want %d", code, got, status)
		}
	}
}

func TestCodeForCoreErr(t *testing.T) {
	cases := map[string]error{
		CodeStoreNotFound:        core.ErrStoreNotFound,
		CodeStoreTooNew:          core.ErrStoreTooNew,
		CodeStoreBusy:            core.ErrStoreBusy,
		CodeStoreError:           core.ErrStoreError,
		CodeNotFound:             core.ErrNotFound,
		CodeValidationFailed:     core.ErrValidation,
		CodeTerminalState:        core.ErrTerminalState,
		CodeInvalidTransition:    core.ErrInvalidTransition,
		CodeConflict:             core.ErrConflict,
		CodeConfirmationMismatch: core.ErrConfirmationMismatch,
		CodeVerificationRejected: core.ErrVerificationRejected,
		CodeLocked:               core.ErrLocked,
		CodeInternalError:        fmt.Errorf("unexpected"),
	}
	for want, err := range cases {
		if got := CodeForCoreErr(fmt.Errorf("wrapped: %w", err)); got != want {
			t.Errorf("CodeForCoreErr(%v) = %q, want %q", err, got, want)
		}
	}
}
