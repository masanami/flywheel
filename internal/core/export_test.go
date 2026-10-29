package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// docs/features/m5-tom-migration-and-switchover.md S1（AC-1〜AC-16）。実ストアで書き出しと再構築を検証する。

var allTablesForExportTest = []string{"activity", "approval", "challenge", "challenge_dependency", "cycle", "hold", "lock",
	"occurrence", "operation", "operation_execution", "run", "run_artifact", "slot", "source_binding", "task_plan"}

func newWorkspaceStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func filledStore(t *testing.T) (*Store, string) {
	t.Helper()
	_, dir := newWorkspaceStore(t)
	coretest.FillAllTables(t, dir)
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func mustExport(t *testing.T, s *Store) []byte {
	t.Helper()
	data, _, err := s.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return data
}

func dumpAll(t *testing.T, dir string) string {
	t.Helper()
	return coretest.DumpTables(t, dir, append(append([]string{}, allTablesForExportTest...), "sqlite_sequence")...)
}

// assertKeysSorted は JSON のすべてのオブジェクトのキーが辞書順（バイト列の順）に並ぶことを検査する。
func assertKeysSorted(t *testing.T, data []byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	type frame struct {
		isObject bool
		last     string
		wantKey  bool
	}
	var stack []frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, frame{isObject: true, wantKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].isObject {
					stack[n-1].wantKey = true
				}
			}
			continue
		}
		n := len(stack)
		if n > 0 && stack[n-1].isObject {
			f := &stack[n-1]
			if f.wantKey {
				key := tok.(string)
				if key < f.last {
					t.Errorf("object keys are not in lexicographic order: %q after %q", key, f.last)
				}
				f.last = key
				f.wantKey = false
			} else {
				f.wantKey = true
			}
		}
	}
}

// AC-1・AC-2・AC-5: 最上位のキー・行の無い表も含めたすべての表・sqlite_sequence の行。
func TestExport_TopLevelShapeAllTablesAndSequences(t *testing.T) {
	s, _ := newWorkspaceStore(t) // 行の無い表だけのストア
	var doc struct {
		SchemaVersion int                         `json:"schema_version"`
		Sequences     map[string]int64            `json:"sequences"`
		Tables        map[string][]map[string]any `json:"tables"`
	}
	raw := mustExport(t, s)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, 3)
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"schema_version", "sequences", "tables"}) {
		t.Fatalf("top-level keys = %v", keys)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	latest, _ := LatestSchemaVersion()
	if doc.SchemaVersion != latest {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, latest)
	}
	var got []string
	for name, rows := range doc.Tables {
		got = append(got, name)
		if rows == nil || len(rows) != 0 {
			t.Errorf("empty store: tables[%q] = %v, want []", name, rows)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, allTablesForExportTest) {
		t.Errorf("tables = %v, want %v", got, allTablesForExportTest)
	}
	// init した直後の空のストアにも sqlite_sequence に run|0 の行がある。
	if !reflect.DeepEqual(doc.Sequences, map[string]int64{"run": 0}) {
		t.Errorf("sequences = %v, want map[run:0]", doc.Sequences)
	}
}

// AC-3・AC-4・AC-5: 行は主キーの昇順、キーは辞書順、sequences は sqlite_sequence の行。
func TestExport_RowsInPrimaryKeyOrderKeysSortedSequencesMatch(t *testing.T) {
	s, dir := filledStore(t)
	// 主キーの順（挿入順と逆）を確かめるため、後から小さい主キーの行は作れないので、複合主キーの表へ足す。
	coretest.Exec(t, dir, `INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (1, 0, 'older', '2026-10-29T00:00:00.000Z')`)
	coretest.Exec(t, dir, `INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (1, 2, '2026-10-29T00:00:00.000Z')`)
	raw := mustExport(t, s)
	assertKeysSorted(t, raw)

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc struct {
		Sequences map[string]json.Number      `json:"sequences"`
		Tables    map[string][]map[string]any `json:"tables"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	plans := doc.Tables["task_plan"]
	if len(plans) != 2 || plans[0]["version"].(json.Number).String() != "0" || plans[1]["version"].(json.Number).String() != "1" {
		t.Errorf("task_plan rows are not in (challenge_id, version) order: %v", plans)
	}
	deps := doc.Tables["challenge_dependency"]
	if len(deps) != 2 || deps[0]["challenge_id"].(json.Number).String() != "1" || deps[1]["challenge_id"].(json.Number).String() != "2" {
		t.Errorf("challenge_dependency rows are not in primary key order: %v", deps)
	}
	for _, tbl := range []string{"challenge", "run", "activity"} {
		prev := int64(0)
		for _, row := range doc.Tables[tbl] {
			id, err := row["id"].(json.Number).Int64()
			if err != nil || id <= prev {
				t.Errorf("%s ids are not ascending: %d after %d", tbl, id, prev)
			}
			prev = id
		}
	}
	// sequences は sqlite_sequence の行と一致する。
	want := map[string]string{}
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT name, seq FROM sqlite_sequence`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			var seq int64
			if err := rows.Scan(&n, &seq); err != nil {
				return err
			}
			want[n] = strconv.FormatInt(seq, 10)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for k, v := range doc.Sequences {
		got[k] = v.String()
	}
	if !reflect.DeepEqual(got, want) || len(got) == 0 {
		t.Errorf("sequences = %v, sqlite_sequence = %v", got, want)
	}
}

// AC-6・AC-8: 2 回の書き出しはバイト単位で一致し、書き出しはストアを変えない。
func TestExport_IsDeterministicAndDoesNotChangeTheStore(t *testing.T) {
	s, dir := filledStore(t)
	before := dumpAll(t, dir)
	first := mustExport(t, s)
	second := mustExport(t, s)
	if !bytes.Equal(first, second) {
		t.Error("two exports of the same state differ")
	}
	if after := dumpAll(t, dir); after != before {
		t.Errorf("export changed the store:\nbefore=%s\nafter=%s", before, after)
	}
}

// AC-7: run のディレクトリのファイルの中身は書き出しに現れない。
func TestExport_DoesNotIncludeRunDirectoryFiles(t *testing.T) {
	s, dir := filledStore(t)
	runDir := filepath.Join(dir, ".flywheel", "runs", "R-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "RUN-DIR-MARKER-8f3a"
	if err := os.WriteFile(filepath.Join(runDir, "output.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mustExport(t, s), []byte(marker)) {
		t.Error("the export contains a run directory file's content")
	}
}

// ExportToFile は絶対パスを返し、既存のファイルを置き換え、ディレクトリが居る場所へは書かずに store_error 相当で終わる。
func TestExportToFile_WritesReplacesAndFailsOnDirectory(t *testing.T) {
	s, dir := filledStore(t)
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.ExportToFile(context.Background(), out)
	if err != nil {
		t.Fatalf("ExportToFile: %v", err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, mustExport(t, s)) || res.Path != out || res.SchemaVersion == 0 {
		t.Errorf("file/result mismatch: result=%+v", res)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, ".export-*")); len(entries) != 0 {
		t.Errorf("temporary files left: %v", entries)
	}
	asDir := filepath.Join(dir, "adir")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportToFile(context.Background(), asDir); !errors.Is(err, ErrStoreError) {
		t.Errorf("ExportToFile onto a directory = %v, want ErrStoreError", err)
	}
}

// AC-9・AC-10: 往復で書き出しがバイト単位で一致し、次の ID も同じ。import は作業ログを足さない。
func TestImport_RoundTripIsByteIdenticalAndKeepsNumbering(t *testing.T) {
	src, srcDir := filledStore(t)
	exported := mustExport(t, src)
	dst, dstDir := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), exported); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if again := mustExport(t, dst); !bytes.Equal(exported, again) {
		t.Errorf("export after import differs:\n%s\n---\n%s", exported, again)
	}
	tablesOnly := func(dir string) string { return coretest.DumpTables(t, dir, allTablesForExportTest...) }
	if tablesOnly(srcDir) != tablesOnly(dstDir) {
		t.Error("tables differ after the round trip")
	}
	a, err := src.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "next"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := dst.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Errorf("next challenge ID = %s after the round trip, original store gives %s", b.ID, a.ID)
	}
}

// 整数を float64 で丸めない（2^53 + 1 の費用の列が往復で変わらない）。
func TestImport_KeepsLargeIntegersExact(t *testing.T) {
	src, _ := filledStore(t)
	exported := mustExport(t, src)
	if !bytes.Contains(exported, []byte("9007199254740993")) {
		t.Fatal("fixture lacks the large integer")
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), exported); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mustExport(t, dst), []byte("9007199254740993")) {
		t.Error("the large integer was rounded")
	}
}

func replaceJSON(t *testing.T, data []byte, mutate func(doc map[string]any)) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-11: 空のストアの書き出しの sequences に challenge: 372 を足して import すると、最初の課題は C-373。
func TestImport_EmptyStoreWithChallengeSequence_FirstCreateIsC373(t *testing.T) {
	empty, _ := newWorkspaceStore(t)
	doc := replaceJSON(t, mustExport(t, empty), func(d map[string]any) {
		d["sequences"].(map[string]any)["challenge"] = json.Number("372")
	})
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), doc); err != nil {
		t.Fatalf("Import: %v", err)
	}
	c, err := dst.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "C-373" {
		t.Errorf("first challenge ID = %s, want C-373", c.ID)
	}
}

func assertImportRejected(t *testing.T, s *Store, dir string, data []byte) {
	t.Helper()
	before := dumpAll(t, dir)
	_, err := s.Import(context.Background(), data)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Import error = %v, want ErrValidation", err)
	}
	if after := dumpAll(t, dir); after != before {
		t.Errorf("a rejected import changed the store:\nbefore=%s\nafter=%s", before, after)
	}
}

// AC-14: 1 つ以上の表に行を持つストアへの import は、何も変えず validation_failed。
func TestImport_IntoStoreWithRows_IsRejected(t *testing.T) {
	src, _ := filledStore(t)
	exported := mustExport(t, src)
	dst, dir := newWorkspaceStore(t)
	// 1 つの表（slot）だけに行を持つストア。
	coretest.InsertSlot(t, dir, "o/r", "local", "/tmp/s", "free", "")
	assertImportRejected(t, dst, dir, exported)
}

// AC-15: スキーマの版が違う書き出しは、何も変えず validation_failed。
func TestImport_DifferentSchemaVersion_IsRejected(t *testing.T) {
	src, _ := filledStore(t)
	doc := replaceJSON(t, mustExport(t, src), func(d map[string]any) { d["schema_version"] = json.Number("11") })
	dst, dir := newWorkspaceStore(t)
	assertImportRejected(t, dst, dir, doc)
}

// AC-16: 外部キーの違反（存在しない課題を参照する行）は validation_failed で、すべての表が 0 行のまま。
func TestImport_ForeignKeyViolation_IsRejectedAndLeavesNoRows(t *testing.T) {
	src, _ := filledStore(t)
	doc := replaceJSON(t, mustExport(t, src), func(d map[string]any) {
		d["tables"].(map[string]any)["challenge"] = []any{}
	})
	dst, dir := newWorkspaceStore(t)
	assertImportRejected(t, dst, dir, doc)
	for _, tbl := range allTablesForExportTest {
		if strings.Contains(coretest.DumpTables(t, dir, tbl), "\n[") {
			t.Errorf("table %s has rows after the rejected import", tbl)
		}
	}
	if n := coretest.CountChallenges(t, dir); n != 0 {
		t.Errorf("challenges = %d, want 0", n)
	}
}

// 書き出しの形の誤りは何も書かず validation_failed（sequences のキーが tables に無い・値が整数でない・
// 最大の ID より小さい・列の過不足・未知のキー・JSON でない）。
func TestImport_MalformedDocuments_AreRejected(t *testing.T) {
	src, _ := filledStore(t)
	exported := mustExport(t, src)
	cases := map[string]func(d map[string]any){
		"sequences key not a table": func(d map[string]any) { d["sequences"].(map[string]any)["nope"] = json.Number("1") },
		"sequence not an integer":   func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = json.Number("2.5") },
		"sequence is a string":      func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = "9" },
		"sequence below max id":     func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = json.Number("1") },
		"negative sequence":         func(d map[string]any) { d["sequences"].(map[string]any)["slot"] = json.Number("-1") },
		"unknown table":             func(d map[string]any) { d["tables"].(map[string]any)["ghost"] = []any{} },
		"missing table":             func(d map[string]any) { delete(d["tables"].(map[string]any), "slot") },
		"unknown top-level key":     func(d map[string]any) { d["extra"] = json.Number("1") },
		"missing column": func(d map[string]any) {
			delete(d["tables"].(map[string]any)["slot"].([]any)[0].(map[string]any), "path")
		},
		"extra column": func(d map[string]any) {
			d["tables"].(map[string]any)["slot"].([]any)[0].(map[string]any)["zzz"] = json.Number("1")
		},
		"non-integer id": func(d map[string]any) {
			d["tables"].(map[string]any)["slot"].([]any)[0].(map[string]any)["id"] = "abc"
		},
		"boolean value": func(d map[string]any) {
			d["tables"].(map[string]any)["slot"].([]any)[0].(map[string]any)["path"] = true
		},
		"null in NOT NULL column": func(d map[string]any) {
			d["tables"].(map[string]any)["slot"].([]any)[0].(map[string]any)["path"] = nil
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dst, dir := newWorkspaceStore(t)
			assertImportRejected(t, dst, dir, replaceJSON(t, exported, cases[name]))
		})
	}
	t.Run("not json", func(t *testing.T) {
		dst, dir := newWorkspaceStore(t)
		assertImportRejected(t, dst, dir, []byte("not json"))
	})
	t.Run("trailing data", func(t *testing.T) {
		dst, dir := newWorkspaceStore(t)
		assertImportRejected(t, dst, dir, append(append([]byte{}, exported...), []byte(" {}")...))
	})
}

func TestImportFromFile_UnreadableFileIsValidationFailed(t *testing.T) {
	s, dir := newWorkspaceStore(t)
	if _, err := s.ImportFromFile(context.Background(), filepath.Join(dir, "missing.json")); !errors.Is(err, ErrValidation) {
		t.Errorf("ImportFromFile(missing) = %v, want ErrValidation", err)
	}
}
