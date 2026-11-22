package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

var allTablesForExportTest = []string{"activity", "approval", "challenge", "challenge_dependency", "challenge_parent", "challenge_reopen", "chat_message", "chat_session", "contract_run", "cycle", "direct_completion", "direct_completion_artifact", "followup", "hold", "lock",
	"occurrence", "operation", "operation_execution", "plan_answer", "run", "run_artifact", "slot", "source_binding", "source_child", "supervisor_action", "task", "task_addition", "task_dependency", "task_plan"}

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	src, _ := filledStore(t)
	exported := mustExport(t, src)
	dst, dir := newWorkspaceStore(t)
	// 1 つの表（slot）だけに行を持つストア。
	coretest.InsertSlot(t, dir, "o/r", "local", "/tmp/s", "free", "")
	assertImportRejected(t, dst, dir, exported)
}

// AC-14: 主キーがぶつからない形（空のストアの書き出しを、slot だけに行を持つストアへ）でも、
// 行数の検査で何も変えず validation_failed。
func TestImport_EmptyExportIntoStoreWithRows_IsRejected(t *testing.T) {
	t.Parallel()
	empty, _ := newWorkspaceStore(t)
	exported := mustExport(t, empty)
	dst, dir := newWorkspaceStore(t)
	coretest.InsertSlot(t, dir, "o/r", "local", "/tmp/s", "free", "")
	assertImportRejected(t, dst, dir, exported)
}

// AC-10: import は sequences の値を採番の状態にする（最大の ID より大きい値でも保つ）。sqlite_sequence に
// 同じ名前の行が重複しない。
func TestImport_KeepsSequencesAboveTheLargestID(t *testing.T) {
	t.Parallel()
	src, srcDir := filledStore(t)
	coretest.Exec(t, srcDir, "UPDATE sqlite_sequence SET seq = 10 WHERE name = 'challenge'")
	coretest.Exec(t, srcDir, "UPDATE sqlite_sequence SET seq = 10 WHERE name = 'run'")
	exported := mustExport(t, src)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), exported); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if again := mustExport(t, dst); !bytes.Equal(exported, again) {
		t.Errorf("export after import differs:\n%s\n---\n%s", exported, again)
	}
	var dups int
	if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM (SELECT name FROM sqlite_sequence GROUP BY name HAVING COUNT(*) > 1)`).Scan(&dups)
	}); err != nil {
		t.Fatal(err)
	}
	if dups != 0 {
		t.Errorf("sqlite_sequence has %d duplicated names", dups)
	}
	for _, s := range []*Store{src, dst} {
		c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "next"})
		if err != nil {
			t.Fatal(err)
		}
		if c.ID != "C-11" {
			t.Errorf("next challenge ID = %s, want C-11", c.ID)
		}
	}
}

// rowid が ID でない表（TEXT の主キーの lock）では、sequences の値を最大の ID と比べない。
func TestImport_SequenceOfTableWithoutIntegerKeyIsNotComparedWithRowid(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	doc := replaceJSON(t, mustExport(t, src), func(d map[string]any) {
		if len(d["tables"].(map[string]any)["lock"].([]any)) == 0 {
			t.Fatal("fixture lacks a lock row")
		}
		d["sequences"].(map[string]any)["lock"] = json.Number("0")
	})
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), doc); err != nil {
		t.Errorf("Import: %v, want success", err)
	}
}

// UTF-8 として不正な TEXT は書き出さず store_error（encoding/json の置き換えで往復が崩れるため）。
func TestExport_InvalidUTF8TextIsStoreError(t *testing.T) {
	t.Parallel()
	s, dir := filledStore(t)
	coretest.Exec(t, dir, "UPDATE challenge SET title = CAST(x'ff' AS TEXT) WHERE id = 1")
	if _, _, err := s.Export(context.Background()); !errors.Is(err, ErrStoreError) {
		t.Errorf("Export = %v, want ErrStoreError", err)
	}
}

// AC-15: スキーマの版が違う書き出しは、何も変えず validation_failed。
func TestImport_DifferentSchemaVersion_IsRejected(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	doc := replaceJSON(t, mustExport(t, src), func(d map[string]any) { d["schema_version"] = json.Number("11") })
	dst, dir := newWorkspaceStore(t)
	assertImportRejected(t, dst, dir, doc)
}

// AC-16: 外部キーの違反（存在しない課題を参照する行）は validation_failed で、すべての表が 0 行のまま。
func TestImport_ForeignKeyViolation_IsRejectedAndLeavesNoRows(t *testing.T) {
	t.Parallel()
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
	t.Parallel() // 共有の状態（t.Setenv・t.Chdir・パッケージ変数の書き換え）が無いことを確かめた（flywheel#567）
	src, _ := filledStore(t)
	exported := mustExport(t, src)
	cases := map[string]func(d map[string]any){
		"sequences key not a table": func(d map[string]any) { d["sequences"].(map[string]any)["nope"] = json.Number("1") },
		"sequence not an integer":   func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = json.Number("2.5") },
		"sequence is a string":      func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = "9" },
		"sequence below max id":     func(d map[string]any) { d["sequences"].(map[string]any)["challenge"] = json.Number("1") },
		"unknown table":             func(d map[string]any) { d["tables"].(map[string]any)["ghost"] = []any{} },
		// lock はどの外部キーからも参照されないので、欠けの検査より先に外部キーの違反で落ちない。
		"missing table":         func(d map[string]any) { delete(d["tables"].(map[string]any), "lock") },
		"unknown top-level key": func(d map[string]any) { d["extra"] = json.Number("1") },
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
	// 行の無い表の負の値（最大の ID の検査に先に当たらない形）。
	t.Run("negative sequence", func(t *testing.T) {
		empty, _ := newWorkspaceStore(t)
		doc := replaceJSON(t, mustExport(t, empty), func(d map[string]any) {
			d["sequences"].(map[string]any)["challenge"] = json.Number("-1")
		})
		dst, dir := newWorkspaceStore(t)
		assertImportRejected(t, dst, dir, doc)
	})
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
	t.Parallel()
	s, dir := newWorkspaceStore(t)
	if _, err := s.ImportFromFile(context.Background(), filepath.Join(dir, "missing.json")); !errors.Is(err, ErrValidation) {
		t.Errorf("ImportFromFile(missing) = %v, want ErrValidation", err)
	}
}

// タスクの表の往復（core の見直しの区切り S2a・masanami/flywheel#750 の AC-10〜AC-12）。
// 書き出して別のストアへ再構築し、再構築したストアの表に、書き出した行の全列が同じ値である。
func TestImport_TaskTablesRowsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ac, table string
		minRows   int
	}{
		{"AC-10", "task", 2},
		{"AC-11", "task_dependency", 1},
		{"AC-12", "plan_answer", 2},
		{"S2b followup", "followup", 2},
		{"S2b source_child", "source_child", 1},
		{"AC-223", "task_addition", 1},
	} {
		t.Run(tc.ac+"_"+tc.table, func(t *testing.T) {
			t.Parallel()
			src, srcDir := filledStore(t)
			dst, dstDir := newWorkspaceStore(t)
			if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
				t.Fatalf("Import: %v", err)
			}
			want := coretest.DumpTables(t, srcDir, tc.table)
			got := coretest.DumpTables(t, dstDir, tc.table)
			// 先頭の "[table]" の行を除いた行数が、書き出した行数。
			if rows := strings.Count(want, "\n") - 1; rows < tc.minRows {
				t.Fatalf("the source store has %d %s rows, want >= %d", rows, tc.table, tc.minRows)
			}
			if got != want {
				t.Errorf("%s rows after the round trip differ:\nwant:\n%s\ngot:\n%s", tc.table, want, got)
			}
		})
	}
	// 追加した列（task_id・uses_integration）の値も往復で残る。
	t.Run("task_id_and_uses_integration_columns", func(t *testing.T) {
		t.Parallel()
		src, _ := filledStore(t)
		dst, _ := newWorkspaceStore(t)
		if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			"SELECT task_id FROM run WHERE id = 1", "SELECT task_id FROM contract_run WHERE id = 1",
			"SELECT task_id FROM hold WHERE id = 1", "SELECT uses_integration FROM challenge WHERE id = 1",
		} {
			var v sql.NullInt64
			if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&v) }); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if !v.Valid {
				t.Errorf("%s = NULL after the round trip, want a value", q)
			}
		}
	})
}

// AC-224・版 34 で足した列: 課題の上限と理由、タスクの addition_id・expand_after は、書き出し→空のストアへの再構築で同じになる。
func TestImport_S4c1ColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, want string }{
		{`SELECT budget_limit_micros || '|' || budget_limit_reason FROM challenge WHERE id = 1`, `40000000|続きを足す分を見込む "quoted"`},
		{`SELECT COALESCE(budget_limit_micros, 'NULL') || '|' || COALESCE(budget_limit_reason, 'NULL') FROM challenge WHERE id = 2`, `NULL|NULL`},
		{`SELECT addition_id || '|' || expand_after FROM task WHERE id = 2`, `1|1`},
		{`SELECT origin || '|' || origin_task_id || '|' || state || '|' || spec || '|' || total_after_micros || '|' || limit_micros || '|' || hold_id FROM task_addition WHERE id = 1`,
			`expand|1|applied|{"tasks":[]}|9000000|40000000|1`},
	} {
		var got string
		if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(tc.query).Scan(&got) }); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// 版 28 で足した列（run_artifact.role・contract_run の 8 列）の値は、書き出し→空のストアへの再構築で同じになる。
func TestImport_S3aColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	q := []string{
		`SELECT kind || '|' || COALESCE(operation_id, 'NULL') || '|' || requested_options || '|' || content_ref || '|' || facts || '|' ||
			facts_unmet_count || '|' || land_ref || '|' || land_delivered FROM contract_run WHERE id = 1`,
		`SELECT role FROM run_artifact WHERE id = 1`,
		`SELECT COALESCE(operation_id, 'NULL') || '|' || (SELECT COALESCE(operation_id, 'NULL') FROM run WHERE id = 1) FROM run WHERE id = 2`,
	}
	want := []string{`reflect|1|["ready","respond"]|abc123|{"k":"v"}|2|o/r#3|1`, `integration`, `1|NULL`}
	for i, query := range q {
		var got string
		if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query).Scan(&got) }); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want[i] {
			t.Errorf("%s = %q, want %q", query, got, want[i])
		}
	}
}

// 版 29 で足した列（task の予算・challenge.integration_ref・contract_run.verified_ref・approval.task_id・run.reflect）の値は、
// 書き出し→空のストアへの再構築で同じになる。
func TestImport_S3bColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{
		`SELECT budget_impl_micros || '|' || budget_review_micros FROM task WHERE id = 1`: `3000000|1500000`,
		`SELECT integration_ref FROM challenge WHERE id = 1`:                              `feat/issue-1`,
		`SELECT verified_ref FROM contract_run WHERE id = 1`:                              `deadbeef`,
		`SELECT CAST(task_id AS TEXT) FROM approval WHERE id = 1`:                         `1`,
		`SELECT CAST(reflect AS TEXT) FROM run WHERE id = 2`:                              `1`,
		`SELECT CAST(reflect AS TEXT) FROM run WHERE id = 1`:                              `0`,
	} {
		var got string
		if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query).Scan(&got) }); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
}

// 版 30 で足した列（contract_run の確かめ待ち・通した・通らなかった中身）の値は、書き出し→空のストアへの再構築で同じになる。
func TestImport_S3cColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	for col, want := range map[string]string{"fix_pending_ref": "pending01", "fix_verified_ref": "verified02", "fix_rejected_ref": "rejected03"} {
		var got string
		if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT ` + col + ` FROM contract_run WHERE id = 1`).Scan(&got)
		}); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
}

// 版 31 で足した列（cycle の end_reason・rounds・stop_requested_at）の値は、書き出し→空のストアへの再構築で同じになる（AC-188・AC-189）。
func TestImport_S4aCycleColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	if err := src.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE cycle SET end_reason = 'settled', rounds = 3, stop_requested_at = '2026-11-02T03:04:05.000Z' WHERE id = (SELECT MIN(id) FROM cycle)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(mustExport(t, src), &doc); err != nil {
		t.Fatal(err)
	}
	rows, _ := doc["tables"].(map[string]any)["cycle"].([]any)
	if len(rows) == 0 {
		t.Fatal("export has no cycle rows")
	}
	for _, col := range []string{"end_reason", "rounds", "stop_requested_at"} {
		if _, ok := rows[0].(map[string]any)[col]; !ok {
			t.Errorf("exported cycle row lacks %q", col)
		}
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	var reason, stop string
	var rounds int
	if err := dst.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT end_reason, rounds, stop_requested_at FROM cycle WHERE id = (SELECT MIN(id) FROM cycle)`).Scan(&reason, &rounds, &stop)
	}); err != nil {
		t.Fatal(err)
	}
	if reason != "settled" || rounds != 3 || stop != "2026-11-02T03:04:05.000Z" {
		t.Errorf("imported cycle = (%q, %d, %q), want (settled, 3, 2026-11-02T03:04:05.000Z)", reason, rounds, stop)
	}
}

// 版 33 で足した列（hold の options・answer_option・option_required）の値は、書き出し→空のストアへの再構築で同じになる。
func TestImport_HoldOptionsColumnsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	read := func(s *Store) (opts, ans string, req int) {
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT options, answer_option, option_required FROM hold WHERE id = 1`).Scan(&opts, &ans, &req)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	so, sa, sr := read(src)
	do, da, dr := read(dst)
	if so == "" || sa != "add" || sr != 1 {
		t.Fatalf("fixture did not fill the columns: %q %q %d", so, sa, sr)
	}
	if so != do || sa != da || sr != dr {
		t.Errorf("after import: %q %q %d, want %q %q %d", do, da, dr, so, sa, sr)
	}
}

// 足す依頼の作成・状態の変更の試験用の口（本番の公開 API には置かない。本体は createTaskAdditionTx・setTaskAdditionStateTx）。

// CreateTaskAddition は試験用に、課題 challengeID に、仕様待ち（pending）の足す依頼を 1 件作り、作業ログに残す。
// 課題が無ければ ErrNotFound。出どころが閉集合の外・出どころのタスクが同じ課題のタスクでない・同じ出どころの expand が既にあるときは
// ErrValidation。
func (s *Store) CreateTaskAddition(ctx context.Context, ch Channel, challengeID string, in TaskAdditionInput) (*TaskAddition, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, ErrNotFound
	}
	if in.Origin != TaskAdditionOriginExpand {
		return nil, fmt.Errorf("%w: unknown task addition origin %q", ErrValidation, in.Origin)
	}
	var out *TaskAddition
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		out, err = createTaskAdditionTx(ctx, tx, rec, cid, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetTaskAdditionState は足す依頼 id の状態を to にし、upd の値を同じ書き込みで更新して、作業ログに残す。
// 閉じる状態（applied・none・declined・superseded）へ動かしたときに decided_at を記録する。
// 足す依頼が無ければ ErrNotFound、to が閉集合の外なら ErrValidation、閉じた足す依頼の状態を変えようとすれば ErrInvalidTransition。
func (s *Store) SetTaskAdditionState(ctx context.Context, ch Channel, id string, to TaskAdditionState, upd TaskAdditionUpdate) (*TaskAddition, error) {
	aid, ok := parseTaskAdditionID(id)
	if !ok {
		return nil, ErrNotFound
	}
	if _, ok := ParseTaskAdditionState(string(to)); !ok {
		return nil, fmt.Errorf("%w: unknown task addition state %q", ErrValidation, to)
	}
	var out *TaskAddition
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		out, err = setTaskAdditionStateTx(ctx, tx, rec, aid, to, upd)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AC-255・AC-256・AC-257: 書き出しに対処の記録の表が含まれ、再構築したストアで件数と保留の supervisor_state が元と同じになる。
func TestImport_SupervisorActionAndHoldStateSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	src, _ := filledStore(t)
	data := mustExport(t, src)
	var doc struct {
		Tables map[string][]json.RawMessage `json:"tables"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Tables["supervisor_action"]); got != 3 {
		t.Errorf("export has %d supervisor_action rows, want 3", got)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	read := func(s *Store) (n int, state, kind, findings string, deferred int, streak, rounds int) {
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var q sql.NullString
			if err := tx.QueryRow(`SELECT questions FROM supervisor_action WHERE id = 2`).Scan(&q); err != nil || !strings.Contains(q.String, `"answer":"A"`) {
				t.Errorf("supervisor_action.questions of row 2 = %q, %v; want the questions JSON to survive the round trip", q.String, err)
			}
			if err := tx.QueryRow(`SELECT questions FROM supervisor_action WHERE id = 1`).Scan(&q); err != nil || q.Valid {
				t.Errorf("supervisor_action.questions of row 1 = %v, %v; want NULL", q, err)
			}
			if err := tx.QueryRow(`SELECT COUNT(*) FROM supervisor_action`).Scan(&n); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT supervisor_state, stop_kind, supervisor_findings, deferred_count FROM hold WHERE id = 1`).Scan(&state, &kind, &findings, &deferred); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT verify_not_met_streak FROM task WHERE id = 1`).Scan(&streak); err != nil {
				return err
			}
			return tx.QueryRow(`SELECT upstream_unreadable_rounds FROM contract_run WHERE id = 1`).Scan(&rounds)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	sn, ss, sk, sf, sd, st, sr := read(src)
	dn, ds, dk, df, dd, dt, dr := read(dst)
	if sn != 3 || ss != "handled" {
		t.Fatalf("fixture did not fill the columns: %d %q", sn, ss)
	}
	if sn != dn || ss != ds || sk != dk || sf != df || sd != dd || st != dt || sr != dr {
		t.Errorf("after import: (%d %q %q %q %d %d %d), want (%d %q %q %q %d %d %d)", dn, ds, dk, df, dd, dt, dr, sn, ss, sk, sf, sd, st, sr)
	}
}
