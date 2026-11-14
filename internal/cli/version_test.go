package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

func TestVersionText_Fixed(t *testing.T) {
	cases := []struct {
		name string
		v    core.VersionInfo
		want string
	}{
		{"ビルド情報なし", core.VersionInfo{Commit: core.UnknownBuildValue, Time: core.UnknownBuildValue, SchemaVersion: 7}, "commit: unknown\ntime: unknown\nschema_version: 7\n"},
		{"クリーン", core.VersionInfo{Commit: "abc", Time: "2026-11-14T00:00:00Z", SchemaVersion: 7}, "commit: abc\ntime: 2026-11-14T00:00:00Z\nschema_version: 7\n"},
		{"未コミットの変更つき", core.VersionInfo{Commit: "abc", Time: "t", Modified: true, SchemaVersion: 7}, "commit: abc+dirty\ntime: t\nschema_version: 7\n"},
	}
	for _, c := range cases {
		if got := versionText(c.v); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestVersionAndStatusShowBuildCommit(t *testing.T) {
	ws := t.TempDir()
	var o, e bytes.Buffer
	if code := run([]string{"init", "--workspace", ws}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
		t.Fatalf("init: %s", e.String())
	}
	v, err := core.ReadVersion()
	if err != nil {
		t.Fatal(err)
	}
	o.Reset()
	if code := run([]string{"version", "--workspace", ws}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
		t.Fatalf("version: %s", e.String())
	}
	if o.String() != versionText(v) {
		t.Errorf("version output %q, want %q", o.String(), versionText(v))
	}
	o.Reset()
	if code := run([]string{"status", "--workspace", ws}, strings.NewReader(""), &o, &e, defaultCommands()); code != 0 {
		t.Fatalf("status: %s", e.String())
	}
	if !strings.HasPrefix(o.String(), "バイナリのコミット: "+commitText(v)+"\n") {
		t.Errorf("status output lacks leading commit line: %q", o.String())
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
