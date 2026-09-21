package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// Migration は 1 版分の前方マイグレーション。Version は PRAGMA user_version に
// 書き込む版番号、SQL は適用するスキーマ変更（複数の DDL 文を ; で連結してよい）。
type Migration struct {
	Version int
	SQL     string
}

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migrations は本番のマイグレーション集合を、埋め込んだ SQL ファイルから
// 版番号の昇順で返す。ファイル名は "NNNN_説明.sql" 形式（NNNN が版番号）でなければ
// ならない。
//
// テスト（AC-10・マイグレーション途中失敗のテストなど）は、この関数を経由せず、
// 独自に組み立てた []Migration（本番の版に架空の追加版を継ぎ足したもの等）を
// Open / Runner へ直接渡してよい（実行器へマイグレーションの集合を差し込める形、
// という完了条件のための設計）。
func Migrations() ([]Migration, error) {
	return parseMigrations(embeddedMigrations, "migrations")
}

func parseMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}

	seen := map[int]string{}
	migrations := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", e.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: duplicate migration version %d (%s and %s)", version, other, e.Name())
		}
		seen[version] = e.Name()

		content, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		migrations = append(migrations, Migration{Version: version, SQL: string(content)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// migrationVersion はファイル名の先頭の数字列を版番号として解釈する。
func migrationVersion(name string) (int, error) {
	idx := strings.IndexByte(name, '_')
	if idx <= 0 {
		return 0, fmt.Errorf("filename must start with NNNN_ (a numeric version prefix followed by an underscore)")
	}
	n, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, fmt.Errorf("invalid numeric version prefix: %w", err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("version must be a positive integer, got %d", n)
	}
	return n, nil
}

// latestVersion は migrations の中の最大の版番号を返す（0 件なら 0）。
func latestVersion(migrations []Migration) int {
	latest := 0
	for _, m := range migrations {
		if m.Version > latest {
			latest = m.Version
		}
	}
	return latest
}

// applyMigrations は from より新しい版を昇順に 1 つずつ、版ごとに 1 トランザクションで
// 適用する（前方のみ）。適用順は版番号の連番（from+1, from+2, …）でなければならず、
// 欠番があれば適用せずエラーにする。欠番の検査は、どの版も適用する前の事前パスで
// 集合全体に対して行う（例: {1,2,4} を版 0 から適用しようとすると、1・2 も含めて
// 何も適用されずにエラーになる。版 1・2 を適用してから 4 の欠番に気付く、という
// 部分適用は起きない）。1 つの版の適用が失敗したら、その版はロールバックし、
// それ以前に適用済みの版はそのまま残す（クリティカル設計決定 2: 「1 つの版の適用は
// 1 トランザクションで行う。途中で失敗したら版も内容も元のまま」）。
func applyMigrations(db *sql.DB, migrations []Migration, from int) error {
	sorted := append([]Migration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	var pending []Migration
	next := from
	for _, m := range sorted {
		if m.Version <= from {
			continue
		}
		if m.Version != next+1 {
			return fmt.Errorf("store: migration sequence has a gap: expected version %d, next available is %d", next+1, m.Version)
		}
		next = m.Version
		pending = append(pending, m)
	}

	for _, m := range pending {
		if err := applyOne(db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(db *sql.DB, m Migration) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return classifyErr(err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(m.SQL); err != nil {
		return fmt.Errorf("store: apply migration %d: %w", m.Version, classifyErr(err))
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return fmt.Errorf("store: set user_version to %d: %w", m.Version, classifyErr(err))
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", m.Version, classifyErr(err))
	}
	return nil
}
