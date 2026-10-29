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
	"sort"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// ストアの全状態の書き出しと再構築（docs/features/m5-tom-migration-and-switchover.md
// §書き出し・§再構築・§IF / API「書き出しの形」）。run のディレクトリのファイルは読み書きしない。
// LLM も呼ばない。

// ExportResult は書き出し（ExportToFile）の結果。
type ExportResult struct {
	// Path は書き出したファイルの絶対パス。
	Path string
	// SchemaVersion は書き出した時点のスキーマの版（PRAGMA user_version）。
	SchemaVersion int
}

// ImportResult は再構築（ImportFromFile）の結果。
type ImportResult struct {
	// Path は読み込んだファイルの絶対パス。
	Path string
	// SchemaVersion は読み込んだ書き出しのスキーマの版。
	SchemaVersion int
}

// isInternalTable は SQLite の内部の表（名前が sqlite_ で始まるもの）かを返す。
// LIKE の `_` は任意の 1 文字に一致するため、SQL ではなく接頭辞で判定する。
func isInternalTable(name string) bool { return strings.HasPrefix(name, "sqlite_") }

type exportColumn struct {
	name string
	pk   int // 主キーの中の位置（1 始まり）。主キーでなければ 0
}

// userTables は sqlite_master の表のうち sqlite_ で始まらないものを名前の昇順で返す。
func userTables(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if !isInternalTable(n) {
			names = append(names, n)
		}
	}
	return names, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func tableColumns(tx *sql.Tx, table string) ([]exportColumn, error) {
	rows, err := tx.Query(`SELECT name, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var cols []exportColumn
	for rows.Next() {
		var c exportColumn
		if err := rows.Scan(&c.name, &c.pk); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// orderBy は主キーの列の宣言順の ORDER BY 句（主キーが無い表は rowid）。
func orderBy(cols []exportColumn) string {
	var pks []exportColumn
	for _, c := range cols {
		if c.pk > 0 {
			pks = append(pks, c)
		}
	}
	if len(pks) == 0 {
		return "rowid"
	}
	sort.SliceStable(pks, func(i, j int) bool { return pks[i].pk < pks[j].pk })
	parts := make([]string, len(pks))
	for i, c := range pks {
		parts[i] = quoteIdent(c.name)
	}
	return strings.Join(parts, ", ")
}

// Export はストアの全状態を、1 つの読み取りのトランザクションで読み、書き出しの JSON のバイト列で返す。
// 返す整数は書き出したスキーマの版。ストアを書かない。同じ状態からはバイト単位で同じ列を返す
// （オブジェクトのキーは辞書順、各表の行は主キーの昇順）。
func (s *Store) Export(ctx context.Context) ([]byte, int, error) {
	var version int
	sequences := map[string]any{}
	tables := map[string]any{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		names, err := userTables(tx)
		if err != nil {
			return err
		}
		for _, name := range names {
			cols, err := tableColumns(tx, name)
			if err != nil {
				return err
			}
			rows, err := exportRows(ctx, tx, name, cols)
			if err != nil {
				return fmt.Errorf("table %s: %w", name, err)
			}
			tables[name] = rows
		}
		var hasSeq int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'`).Scan(&hasSeq); err != nil {
			return err
		}
		if hasSeq == 0 {
			return nil
		}
		srows, err := tx.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence ORDER BY name`)
		if err != nil {
			return err
		}
		defer func() { _ = srows.Close() }()
		for srows.Next() {
			var n string
			var seq int64
			if err := srows.Scan(&n, &seq); err != nil {
				return err
			}
			sequences[n] = seq
		}
		return srows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		if errors.Is(err, ErrStoreTooNew) || errors.Is(err, ErrStoreBusy) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("%w: export: %w", ErrStoreError, err)
	}
	doc := map[string]any{"schema_version": version, "sequences": sequences, "tables": tables}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil { // map のキーは encoding/json が辞書順に並べる
		return nil, 0, fmt.Errorf("%w: export: %w", ErrStoreError, err)
	}
	return buf.Bytes(), version, nil
}

func exportRows(ctx context.Context, tx *sql.Tx, table string, cols []exportColumn) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, "SELECT * FROM "+quoteIdent(table)+" ORDER BY "+orderBy(cols))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(names))
		for i, n := range names {
			switch v := vals[i].(type) {
			case nil, int64, float64, string:
				row[n] = v
			case []byte:
				return nil, fmt.Errorf("column %s holds a BLOB, which the export format does not define", n)
			default:
				return nil, fmt.Errorf("column %s holds an unsupported value of type %T", n, v)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ExportToFile は Export の結果を path へ書く（同じディレクトリの一時ファイルへ書いてから置き換える。
// 既存のファイルは置き換え、途中で失敗しても元のファイルを壊さない）。ストアを書かない。
func (s *Store) ExportToFile(ctx context.Context, path string) (*ExportResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: export: %w", ErrStoreError, err)
	}
	data, version, err := s.Export(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(abs, data); err != nil {
		return nil, fmt.Errorf("%w: export: write %s: %w", ErrStoreError, abs, err)
	}
	return &ExportResult{Path: abs, SchemaVersion: version}, nil
}

func writeFileAtomic(path string, data []byte) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".export-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func importInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: import: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// parsedExport は読み込んだ書き出し。数は json.Number のまま持つ（INTEGER を float64 で丸めない）。
type parsedExport struct {
	version   int
	sequences map[string]any // 値は json.Number であること（文字列などは importTx が拒否する）
	tables    map[string][]map[string]any
}

func parseExport(data []byte) (*parsedExport, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, importInvalid("not a JSON object: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, importInvalid("unexpected data after the JSON document")
	}
	for k := range raw {
		if k != "schema_version" && k != "sequences" && k != "tables" {
			return nil, importInvalid("unknown top-level key %q", k)
		}
	}
	for _, k := range []string{"schema_version", "sequences", "tables"} {
		if _, ok := raw[k]; !ok {
			return nil, importInvalid("missing top-level key %q", k)
		}
	}
	p := &parsedExport{}
	var ver json.Number
	if err := decodeUseNumber(raw["schema_version"], &ver); err != nil {
		return nil, importInvalid("schema_version is not an integer")
	}
	v, err := strconv.Atoi(ver.String())
	if err != nil {
		return nil, importInvalid("schema_version is not an integer: %s", ver)
	}
	p.version = v
	if err := decodeUseNumber(raw["sequences"], &p.sequences); err != nil || p.sequences == nil {
		return nil, importInvalid("sequences is not an object of integers")
	}
	if err := decodeUseNumber(raw["tables"], &p.tables); err != nil || p.tables == nil {
		return nil, importInvalid("tables is not an object of row arrays")
	}
	return p, nil
}

func decodeUseNumber(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(dst)
}

// sqlValue は JSON の値を SQLite の値へ写す（整数は int64、小数は float64、文字列、null）。
func sqlValue(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return x, nil
	case json.Number:
		if i, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil {
			return nil, fmt.Errorf("number %s is out of range", x)
		}
		return f, nil
	default:
		return nil, fmt.Errorf("unsupported value %v", v)
	}
}

// Import は Export が書いた JSON を、すべての表に行が無いストアへ読み込む。1 つの書き込みの
// トランザクションで全表の行と採番の状態（sqlite_sequence）を書き、途中で失敗したら何も残さない。
// 返す整数は読み込んだ書き出しのスキーマの版。次のいずれかなら何も書かず ErrValidation を返す:
// 行のある表がある／スキーマの版が違う／外部キーの違反がある／書き出しが表・列の構成と合わない／
// sequences のキーが tables に無い表の名前／値が整数でない・その表の行の最大の ID より小さい。
//
// 外部キーは循環している（challenge.issues_hold_id→hold、hold.challenge_id→challenge、
// run.resumed_from_run_id→run 自身）ので、トランザクションの中で PRAGMA defer_foreign_keys = ON にして
// 検査をコミットの時点へ遅らせ、コミットの直前に PRAGMA foreign_key_check を自分で実行して、違反は
// ErrValidation にする（コミットの失敗〔SQLite の汎用のエラー〕にしない）。
func (s *Store) Import(ctx context.Context, data []byte) (int, error) {
	p, err := parseExport(data)
	if err != nil {
		return 0, err
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error { return importTx(ctx, tx, p) })
	if err = classifyReadWriteErr(err); err != nil {
		if errors.Is(err, ErrValidation) || errors.Is(err, ErrStoreTooNew) || errors.Is(err, ErrStoreBusy) {
			return 0, err
		}
		return 0, fmt.Errorf("%w: import: %w", ErrStoreError, err)
	}
	return p.version, nil
}

// ImportFromFile は path のファイルを Import する。読めないファイルは ErrValidation。
func (s *Store) ImportFromFile(ctx context.Context, path string) (*ImportResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, importInvalid("cannot resolve %s: %v", path, err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, importInvalid("cannot read %s: %v", abs, err)
	}
	version, err := s.Import(ctx, data)
	if err != nil {
		return nil, err
	}
	return &ImportResult{Path: abs, SchemaVersion: version}, nil
}

func importTx(ctx context.Context, tx *sql.Tx, p *parsedExport) error {
	var storeVersion int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&storeVersion); err != nil {
		return err
	}
	if p.version != storeVersion {
		return importInvalid("schema version %d differs from the store's %d", p.version, storeVersion)
	}
	names, err := userTables(tx)
	if err != nil {
		return err
	}
	isTable := make(map[string]bool, len(names))
	for _, n := range names {
		isTable[n] = true
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(n)).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return importInvalid("table %s already has rows; import needs a store with no rows", n)
		}
	}
	for n := range p.tables {
		if !isTable[n] {
			return importInvalid("tables has %q, which is not a table of the store", n)
		}
	}
	for _, n := range names {
		if _, ok := p.tables[n]; !ok {
			return importInvalid("tables lacks the table %q", n)
		}
	}
	seqNames := make([]string, 0, len(p.sequences))
	seqValues := make(map[string]int64, len(p.sequences))
	for n, raw := range p.sequences {
		if !isTable[n] {
			return importInvalid("sequences has %q, which is not a table in tables", n)
		}
		num, isNumber := raw.(json.Number)
		if !isNumber {
			return importInvalid("sequences[%q] = %v is not an integer", n, raw)
		}
		v, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil || v < 0 {
			return importInvalid("sequences[%q] = %s is not a non-negative integer", n, num)
		}
		seqNames = append(seqNames, n)
		seqValues[n] = v
	}
	sort.Strings(seqNames)

	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return err
	}
	for _, n := range names {
		if err := insertRows(ctx, tx, n, p.tables[n]); err != nil {
			return err
		}
	}
	// 採番の状態は書き出しの値にする。AUTOINCREMENT の挿入が作った行（と、空のストアに既にある
	// run|0 の行）を捨てて、sequences の行だけを置く。
	if _, err := tx.ExecContext(ctx, "DELETE FROM sqlite_sequence"); err != nil {
		return err
	}
	for _, n := range seqNames {
		var maxID sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT MAX(rowid) FROM "+quoteIdent(n)).Scan(&maxID); err != nil {
			return err
		}
		if maxID.Valid && seqValues[n] < maxID.Int64 {
			return importInvalid("sequences[%q] = %d is smaller than the largest ID %d in the table", n, seqValues[n], maxID.Int64)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", n, seqValues[n]); err != nil {
			return err
		}
	}
	return checkForeignKeys(ctx, tx)
}

func insertRows(ctx context.Context, tx *sql.Tx, table string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	cols, err := tableColumns(tx, table)
	if err != nil {
		return err
	}
	colNames := make([]string, len(cols))
	quoted := make([]string, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.name
		quoted[i] = quoteIdent(c.name)
		marks[i] = "?"
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+quoteIdent(table)+" ("+strings.Join(quoted, ", ")+") VALUES ("+strings.Join(marks, ", ")+")")
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for i, row := range rows {
		if len(row) != len(cols) {
			return importInvalid("tables[%q][%d] has %d columns, the table has %d", table, i, len(row), len(cols))
		}
		args := make([]any, len(cols))
		for j, name := range colNames {
			raw, ok := row[name]
			if !ok {
				return importInvalid("tables[%q][%d] lacks the column %q", table, i, name)
			}
			v, err := sqlValue(raw)
			if err != nil {
				return importInvalid("tables[%q][%d].%s: %v", table, i, name, err)
			}
			args[j] = v
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			// NOT NULL・CHECK・主キー・UNIQUE の違反は書き出しの内容の誤り。
			if isConstraintErr(err) {
				return importInvalid("tables[%q][%d]: %v", table, i, err)
			}
			return err
		}
	}
	return nil
}

func isConstraintErr(err error) bool { return store.IsInvalidData(err) }

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		return importInvalid("foreign key violation: a row of %s refers to a missing row of %s", table, parent)
	}
	return rows.Err()
}
