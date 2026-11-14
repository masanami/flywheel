package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `flywheel version` と status のバイナリのコミットの行（Issue #593）を検証する。

// 固定のビルド情報（テストごとに Args.ReadVersion へ注入する。パッケージ変数は差し替えない）。
var (
	versionClean   = core.VersionInfo{Commit: "abc123", Time: "2026-11-14T00:00:00Z", SchemaVersion: 7}
	versionDirty   = core.VersionInfo{Commit: "abc123", Time: "2026-11-14T00:00:00Z", Modified: true, SchemaVersion: 7}
	versionUnknown = core.VersionInfo{Commit: core.UnknownBuildValue, Time: core.UnknownBuildValue, SchemaVersion: 7}
)

// commandsWithVersion は defaultCommands の各コマンドへ、ビルド情報の読み取り元として v を注入した表を返す。
func commandsWithVersion(v core.VersionInfo) []Command {
	cmds := defaultCommands()
	for i := range cmds {
		orig := cmds[i].Run
		cmds[i].Run = func(a Args) (any, error) {
			a.ReadVersion = func() (core.VersionInfo, error) { return v, nil }
			return orig(a)
		}
	}
	return cmds
}

func TestVersionText_Fixed(t *testing.T) {
	cases := []struct {
		name string
		v    core.VersionInfo
		want string
	}{
		{"ビルド情報なし", versionUnknown, "commit: unknown\ntime: unknown\nmodified: false\nschema_version: 7\n"},
		{"クリーン（modified=false）", versionClean, "commit: abc123\ntime: 2026-11-14T00:00:00Z\nmodified: false\nschema_version: 7\n"},
		{"未コミットの変更つき（modified=true）", versionDirty, "commit: abc123+dirty\ntime: 2026-11-14T00:00:00Z\nmodified: true\nschema_version: 7\n"},
	}
	for _, c := range cases {
		var o, e bytes.Buffer
		if code := run([]string{"version", "--workspace", t.TempDir()}, strings.NewReader(""), &o, &e, commandsWithVersion(c.v)); code != 0 {
			t.Fatalf("%s: version: %s", c.name, e.String())
		}
		if o.String() != c.want {
			t.Errorf("%s: got %q want %q", c.name, o.String(), c.want)
		}
	}
}

func TestVersionJSON_Fixed(t *testing.T) {
	cases := []struct {
		name string
		v    core.VersionInfo
		want string
	}{
		{"ビルド情報あり・modified=false", versionClean, `{"commit":"abc123","modified":false,"schema_version":7,"time":"2026-11-14T00:00:00Z"}` + "\n"},
		{"ビルド情報あり・modified=true", versionDirty, `{"commit":"abc123","modified":true,"schema_version":7,"time":"2026-11-14T00:00:00Z"}` + "\n"},
		{"ビルド情報なし", versionUnknown, `{"commit":"unknown","modified":false,"schema_version":7,"time":"unknown"}` + "\n"},
	}
	for _, c := range cases {
		var o, e bytes.Buffer
		if code := run([]string{"version", "--workspace", t.TempDir(), "--json"}, strings.NewReader(""), &o, &e, commandsWithVersion(c.v)); code != 0 {
			t.Fatalf("%s: version --json: %s", c.name, e.String())
		}
		if o.String() != c.want {
			t.Errorf("%s: got %q want %q", c.name, o.String(), c.want)
		}
	}
}

func TestStatusShowsBinaryCommit(t *testing.T) {
	ws := t.TempDir()
	var o, e bytes.Buffer
	if code := run([]string{"init", "--workspace", ws}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
		t.Fatalf("init: %s", e.String())
	}
	cases := []struct {
		name string
		v    core.VersionInfo
		want string
	}{
		{"ビルド情報あり・modified=false", versionClean, "abc123"},
		{"ビルド情報あり・modified=true", versionDirty, "abc123+dirty"},
		{"ビルド情報なし", versionUnknown, "unknown"},
	}
	for _, c := range cases {
		o.Reset()
		e.Reset()
		if code := run([]string{"status", "--workspace", ws}, strings.NewReader(""), &o, &e, commandsWithVersion(c.v)); code != 0 {
			t.Fatalf("%s: status: %s", c.name, e.String())
		}
		if !strings.HasPrefix(o.String(), "バイナリのコミット: "+c.want+"\n") {
			t.Errorf("%s: status text lacks leading commit line %q: %q", c.name, c.want, o.String())
		}
		o.Reset()
		if code := run([]string{"status", "--workspace", ws, "--json"}, strings.NewReader(""), &o, &e, commandsWithVersion(c.v)); code != 0 {
			t.Fatalf("%s: status --json: %s", c.name, e.String())
		}
		var got struct {
			BinaryCommit *string `json:"binary_commit"`
		}
		if err := json.Unmarshal(o.Bytes(), &got); err != nil {
			t.Fatalf("%s: status --json: %v: %s", c.name, err, o.String())
		}
		if got.BinaryCommit == nil || *got.BinaryCommit != c.want {
			t.Errorf("%s: status --json binary_commit = %v, want %q", c.name, got.BinaryCommit, c.want)
		}
	}
}

func TestVersion_DefaultReadsRunningBinary(t *testing.T) {
	v, err := core.ReadVersion()
	if err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	if code := run([]string{"version", "--workspace", t.TempDir()}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
		t.Fatalf("version: %s", e.String())
	}
	if o.String() != versionText(v) {
		t.Errorf("version output %q, want %q", o.String(), versionText(v))
	}
}

func TestVersion_WorksOnTooNewStoreAndOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatal(err)
	}
	coretest.SetStoreVersion(t, ws, 999999)
	for _, dir := range []string{ws, t.TempDir()} {
		var o, e bytes.Buffer
		if code := run([]string{"version", "--workspace", dir, "--json"}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
			t.Errorf("version in %s: exit=%d stderr=%s", dir, code, e.String())
		}
	}
}
