package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 登録表の宣言が、m5 の仕様の §IF / API「CLI に足すもの」の表の行（引数）と一致する。
func TestExportImportCommands_MatchM5IFAPITable(t *testing.T) {
	for name, flag := range map[string]string{"export": "--out <path>", "import": "--in <path>"} {
		m5SpecLine(t, "| `"+name+" "+flag+"` |", "新しいコマンド")
		cmd, ok := registeredCommands()[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		flagName := strings.Fields(flag)[0][2:]
		if cmd.MinPositional != 0 || cmd.MaxPositional != 0 || len(cmd.Flags) != 1 || cmd.Flags[0].Name != flagName || !cmd.Flags[0].Required || !cmd.Flags[0].HasValue {
			t.Errorf("%s: registration %+v does not match the table row (one required --%s <path>, no positional)", name, cmd, flagName)
		}
		if !cmd.RequiresStore {
			t.Errorf("%s must open the store", name)
		}
	}
}

func runOK(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("args=%v exit=%d stderr=%s", args, code, stderr.String())
	}
	return stdout.String(), stderr.String()
}

// AC-12・AC-13: 往復の後のストアの show <C-ID> --json と log --json は、元のストアの出力と一致する。
func TestExportImport_RoundTripShowAndLogMatch(t *testing.T) {
	src := t.TempDir()
	if _, err := core.Init(src); err != nil {
		t.Fatal(err)
	}
	coretest.FillAllTables(t, src)
	id := createForCase(t, src)
	path := filepath.Join(t.TempDir(), "export.json")
	runJSON(t, src, "export", "--out", path)

	dst := t.TempDir()
	if _, err := core.Init(dst); err != nil {
		t.Fatal(err)
	}
	imported := runJSON(t, dst, "import", "--in", path)
	if imported["import"].(map[string]any)["path"] != path {
		t.Errorf("import path = %v, want %s", imported["import"], path)
	}
	for _, id := range []string{"C-1", "C-2", id} {
		want, _ := runOK(t, "show", id, "--workspace", src, "--json")
		got, _ := runOK(t, "show", id, "--workspace", dst, "--json")
		if got != want {
			t.Errorf("show %s differs after the round trip:\n%s\n---\n%s", id, want, got)
		}
	}
	want, _ := runOK(t, "log", "--workspace", src, "--json")
	got, _ := runOK(t, "log", "--workspace", dst, "--json")
	if got != want {
		t.Errorf("log differs after the round trip:\n%s\n---\n%s", want, got)
	}
}

// 行のあるストアへの import は validation_failed（終了コードは他のコマンドの validation_failed と同じ）。
func TestImport_IntoStoreWithRowsIsValidationFailed(t *testing.T) {
	src := t.TempDir()
	if _, err := core.Init(src); err != nil {
		t.Fatal(err)
	}
	createForCase(t, src)
	path := filepath.Join(t.TempDir(), "export.json")
	runJSON(t, src, "export", "--out", path)
	before := coretest.DumpTables(t, src, "challenge", "activity")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--in", path, "--workspace", src, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code == 0 || !strings.Contains(stderr.String(), `"validation_failed"`) {
		t.Errorf("exit=%d stderr=%s, want validation_failed", code, stderr.String())
	}
	if after := coretest.DumpTables(t, src, "challenge", "activity"); after != before {
		t.Error("a rejected import changed the store")
	}
}

// 主キーがぶつからない形（空のストアの書き出しを、行のあるストアへ）でも validation_failed。
func TestImport_EmptyExportIntoStoreWithRowsIsValidationFailed(t *testing.T) {
	empty := t.TempDir()
	if _, err := core.Init(empty); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	runJSON(t, empty, "export", "--out", path)
	dst := t.TempDir()
	if _, err := core.Init(dst); err != nil {
		t.Fatal(err)
	}
	coretest.InsertSlot(t, dst, "o/r", "local", "/tmp/s", "free", "")
	tables := []string{"challenge", "activity", "slot", "sqlite_sequence"}
	before := coretest.DumpTables(t, dst, tables...)

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--in", path, "--workspace", dst, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code == 0 || !strings.Contains(stderr.String(), `"validation_failed"`) {
		t.Errorf("exit=%d stderr=%s, want validation_failed", code, stderr.String())
	}
	if after := coretest.DumpTables(t, dst, tables...); after != before {
		t.Error("a rejected import changed the store")
	}
}

// export の書き込みに失敗したとき（out の場所にディレクトリ）は store_error で、ストアを変えない。
func TestExport_WriteFailureIsStoreError(t *testing.T) {
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatal(err)
	}
	before := coretest.DumpTables(t, ws, "challenge", "activity", "sqlite_sequence")
	var stdout, stderr bytes.Buffer
	code := run([]string{"export", "--out", ws, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code == 0 || !strings.Contains(stderr.String(), `"store_error"`) {
		t.Errorf("exit=%d stderr=%s, want store_error", code, stderr.String())
	}
	if after := coretest.DumpTables(t, ws, "challenge", "activity", "sqlite_sequence"); after != before {
		t.Error("a failed export changed the store")
	}
}
