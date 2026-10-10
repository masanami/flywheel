package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

type fleetEntryJSON struct {
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	State string  `json:"state"`
	Error *string `json:"error"`
}

func fetchWorkspaces(t *testing.T, port int) []fleetEntryJSON {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces: status %d", resp.StatusCode)
	}
	var doc struct {
		Workspaces []fleetEntryJSON `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc.Workspaces
}

func writeFleet(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "fleet.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fleetJSON(entries ...[2]string) string {
	var parts []string
	for _, e := range entries {
		n, _ := json.Marshal(e[0])
		p, _ := json.Marshal(e[1])
		parts = append(parts, fmt.Sprintf(`{"name":%s,"path":%s}`, n, p))
	}
	return `{"version":1,"workspaces":[` + strings.Join(parts, ",") + `]}`
}

func terminate(t *testing.T, cmdProc *os.Process, wait func() int) {
	t.Helper()
	_ = cmdProc.Signal(syscall.SIGTERM)
	if code := wait(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestServeFleet_FleetFlagListsWorkspacesInOrder(t *testing.T) {
	a, b := initializedWorkspace(t), initializedWorkspace(t)
	f := writeFleet(t, t.TempDir(), fleetJSON([2]string{"zeta", b}, [2]string{"alpha", a}))
	cmd, port, _ := startServeChild(t, "--fleet", f)
	got := fetchWorkspaces(t, port)
	if len(got) != 2 || got[0].Name != "zeta" || got[0].Path != b || got[1].Name != "alpha" || got[1].Path != a {
		t.Errorf("workspaces = %+v", got)
	}
	for _, w := range got {
		if w.State != "ok" || w.Error != nil {
			t.Errorf("%s: state=%q error=%v, want ok/null", w.Name, w.State, w.Error)
		}
	}
	terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
}

func TestServeFleet_DefaultLocationIsUsedWhenNoFlag(t *testing.T) {
	a := initializedWorkspace(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", cfgHome)
	def, err := core.DefaultFleetPath()
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := os.UserConfigDir(); def != filepath.Join(want, "flywheel", "fleet.json") {
		t.Fatalf("default path = %q", def)
	}
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte(fleetJSON([2]string{"from-default", a})), 0o600); err != nil {
		t.Fatal(err)
	}
	// カレントディレクトリは別のワークスペース（宣言が優先されることを確かめる）。
	other := initializedWorkspace(t)
	cmd, port, _ := startServeChildIn(t, other, isolatedConfigEnv(cfgHome))
	got := fetchWorkspaces(t, port)
	if len(got) != 1 || got[0].Name != "from-default" || got[0].Path != a {
		t.Errorf("workspaces = %+v", got)
	}
	terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
}

func TestServeFleet_InvalidDefaultFileIsConfigInvalid(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", cfgHome)
	def, _ := core.DefaultFleetPath()
	_ = os.MkdirAll(filepath.Dir(def), 0o755)
	_ = os.WriteFile(def, []byte("{"), 0o600)
	assertServeFails(t, "config_invalid", "serve", "--port", "0")
}

func TestServeFleet_NoDeclarationBundlesOneWorkspaceFromCwd(t *testing.T) {
	ws := initializedWorkspace(t)
	sub := filepath.Join(ws, "deep", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, port, _ := startServeChildIn(t, sub, append(isolatedConfigEnv(t.TempDir()), "FLYWHEEL_WORKSPACE="))
	got := fetchWorkspaces(t, port)
	// macOS の一時ディレクトリは /var → /private/var の symlink を通る。子の cwd は
	// 解決済みでありうるので、末尾（basename）と名前で確かめる。
	if len(got) != 1 || filepath.Base(got[0].Path) != filepath.Base(ws) || got[0].Name != core.FleetNameFromPath(ws) || got[0].State != "ok" {
		t.Errorf("workspaces = %+v, want one ok workspace named from %s", got, ws)
	}
	terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
}

func TestServeFleet_WorkspaceFlagNameDerivation(t *testing.T) {
	cases := map[string]string{
		"My_Agent.1": "my-agent-1",
		"_a":         "a",
		"__a":        "a",
		"--x":        "x",
		".hidden":    "hidden",
		"日本語-app":    "app",
		"日本語":        "workspace",
	}
	for base, want := range cases {
		t.Run(base, func(t *testing.T) {
			ws := filepath.Join(t.TempDir(), base)
			if err := os.Mkdir(ws, 0o755); err != nil {
				t.Skipf("cannot create directory %q on this filesystem: %v", base, err)
			}
			if _, err := core.Init(ws); err != nil {
				t.Fatal(err)
			}
			cmd, port, _ := startServeChild(t, "--workspace", ws)
			got := fetchWorkspaces(t, port)
			if len(got) != 1 || got[0].Name != want || got[0].Path != ws {
				t.Errorf("workspaces = %+v, want name %q path %q", got, want, ws)
			}
			terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
		})
	}
}

// 宣言を使わない 1 つのワークスペースは、既定の場所に fleet.json があっても読まない
// （--workspace は 1 つだけを束ねる指定）。
func TestServeFleet_WorkspaceFlagIgnoresDefaultFleetFile(t *testing.T) {
	ws, other := initializedWorkspace(t), initializedWorkspace(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", cfgHome)
	def, err := core.DefaultFleetPath() // OS ごとの既定の場所（macOS は HOME 配下の Library）
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(def), 0o755)
	_ = os.WriteFile(def, []byte(fleetJSON([2]string{"declared", other})), 0o600)
	cmd, port, _ := startServeChildIn(t, "", isolatedConfigEnv(cfgHome), "--workspace", ws)
	got := fetchWorkspaces(t, port)
	if len(got) != 1 || got[0].Path != ws {
		t.Errorf("workspaces = %+v, want only %s", got, ws)
	}
	terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
}

func assertServeFails(t *testing.T, wantCode string, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append(args, "--json"), strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 2 {
		t.Errorf("%v: exit code = %d, want 2 (stderr=%s)", args, code, stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != wantCode {
		t.Errorf("%v: error code = %q, want %q", args, got, wantCode)
	}
	if strings.Contains(stderr.String(), "listening on") || stdout.Len() != 0 {
		t.Errorf("%v: served despite the error: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
	}
}

func isolateUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestServeFleet_FleetAndWorkspaceAreExclusive(t *testing.T) {
	isolateUserConfig(t)
	ws := initializedWorkspace(t)
	f := writeFleet(t, t.TempDir(), fleetJSON([2]string{"a", ws}))
	assertServeFails(t, "usage_error", "serve", "--port", "0", "--fleet", f, "--workspace", ws)
}

func TestServeFleet_MissingFleetFileIsConfigNotFound(t *testing.T) {
	isolateUserConfig(t)
	assertServeFails(t, "config_not_found", "serve", "--port", "0", "--fleet", filepath.Join(t.TempDir(), "nope.json"))
}

func TestServeFleet_InvalidFleetFileIsConfigInvalid(t *testing.T) {
	isolateUserConfig(t)
	ws, ws2 := initializedWorkspace(t), initializedWorkspace(t)
	one := func(name, path string) string { return fleetJSON([2]string{name, path}) }
	cases := map[string]string{
		"not json":          `{"version":1,`,
		"unknown key":       `{"version":1,"workspaces":[],"extra":true}`,
		"unknown entry key": `{"version":1,"workspaces":[{"name":"a","path":"` + ws + `","x":1}]}`,
		"duplicate name":    fleetJSON([2]string{"a", ws}, [2]string{"a", ws2}),
		"duplicate path":    fleetJSON([2]string{"a", ws}, [2]string{"b", ws}),
		"relative path":     one("a", "relative/dir"),
		"uppercase name":    one("Abc", ws),
		"leading hyphen":    one("-a", ws),
		"empty name":        one("", ws),
		"space in name":     one("a b", ws),
		"type mismatch":     `{"version":1,"workspaces":[{"name":1,"path":"` + ws + `"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			f := writeFleet(t, t.TempDir(), content)
			assertServeFails(t, "config_invalid", "serve", "--port", "0", "--fleet", f)
		})
	}
}

func TestServeFleet_UnopenableWorkspaceDoesNotStopOthers(t *testing.T) {
	good, empty, tooNew := initializedWorkspace(t), t.TempDir(), initializedWorkspace(t)
	latest, err := core.LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	coretest.SetStoreVersion(t, tooNew, latest+1)
	f := writeFleet(t, t.TempDir(), fleetJSON([2]string{"empty", empty}, [2]string{"good", good}, [2]string{"new", tooNew}))
	cmd, port, _ := startServeChild(t, "--fleet", f)
	got := fetchWorkspaces(t, port)
	want := []string{"store_not_found", "ok", "store_too_new"}
	if len(got) != 3 {
		t.Fatalf("workspaces = %+v", got)
	}
	for i, w := range got {
		if w.State != want[i] || (w.State == "ok") != (w.Error == nil) {
			t.Errorf("%s: state=%q error=%v, want %q", w.Name, w.State, w.Error, want[i])
		}
	}
	terminate(t, cmd.Process, func() int { return waitChild(t, cmd, 10*time.Second) })
}
