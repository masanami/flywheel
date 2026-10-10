package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFleetFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFleetDeclaration_ReadsWorkspacesInOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	p := writeFleetFile(t, `{"version":1,"workspaces":[{"name":"zeta","path":"`+b+`"},{"name":"alpha-1","path":"`+a+`"}]}`)
	got, err := LoadFleetDeclaration(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []FleetWorkspace{{"zeta", b}, {"alpha-1", a}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLoadFleetDeclaration_MissingFileIsConfigNotFound(t *testing.T) {
	_, err := LoadFleetDeclaration(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("err = %v, want ErrConfigNotFound", err)
	}
}

func TestLoadFleetDeclaration_RejectsInvalid(t *testing.T) {
	abs := t.TempDir()
	other := t.TempDir()
	ws := func(entries string) string { return `{"version":1,"workspaces":[` + entries + `]}` }
	e := func(name, path string) string { return `{"name":"` + name + `","path":"` + path + `"}` }
	cases := map[string]string{
		"not json":            `{`,
		"trailing garbage":    `{"version":1,"workspaces":[]}x`,
		"null":                `null`,
		"unknown top key":     `{"version":1,"workspaces":[],"extra":1}`,
		"unknown entry key":   ws(`{"name":"a","path":"` + abs + `","x":1}`),
		"case-variant key":    ws(`{"Name":"a","path":"` + abs + `"}`),
		"missing version":     `{"workspaces":[]}`,
		"wrong version":       `{"version":2,"workspaces":[]}`,
		"version string":      `{"version":"1","workspaces":[]}`,
		"missing workspaces":  `{"version":1}`,
		"workspaces null":     `{"version":1,"workspaces":null}`,
		"entry not object":    ws(`1`),
		"name type":           ws(`{"name":1,"path":"` + abs + `"}`),
		"path type":           ws(`{"name":"a","path":1}`),
		"missing name":        ws(`{"path":"` + abs + `"}`),
		"missing path":        ws(`{"name":"a"}`),
		"duplicate name":      ws(e("a", abs) + "," + e("a", other)),
		"duplicate path":      ws(e("a", abs) + "," + e("b", abs)),
		"duplicate path norm": ws(e("a", abs) + "," + e("b", abs+"/.")),
		"relative path":       ws(e("a", "rel/dir")),
		"empty path":          ws(e("a", "")),
		"uppercase name":      ws(e("Abc", abs)),
		"leading hyphen":      ws(e("-a", abs)),
		"empty name":          ws(e("", abs)),
		"space in name":       ws(e("a b", abs)),
		"trailing newline":    ws(`{"name":"a\n","path":"` + abs + `"}`),
		"underscore name":     ws(e("a_b", abs)),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFleetDeclaration(writeFleetFile(t, content))
			if !errors.Is(err, ErrConfigInvalid) {
				t.Errorf("err = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadFleetDeclaration_EmptyListIsAllowed(t *testing.T) {
	got, err := LoadFleetDeclaration(writeFleetFile(t, `{"version":1,"workspaces":[]}`))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestFleetNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/x/My_Agent.1": "my-agent-1",
		"/x/_a":         "a",
		"/x/__a":        "a",
		"/x/--x":        "x",
		"/x/.hidden":    "hidden",
		"/x/日本語-app":    "app",
		"/x/日本語":        "workspace",
		"/x/a--b":       "a--b",
		"/x/ABC":        "abc",
		"/":             "workspace",
	}
	for path, want := range cases {
		if got := FleetNameFromPath(path); got != want {
			t.Errorf("FleetNameFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDefaultFleetPath_UsesUserConfigDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg) // macOS は HOME から導く
	got, err := DefaultFleetPath()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "flywheel", "fleet.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
