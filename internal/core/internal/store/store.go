// Package store は flywheel のストア（SQLite）の接続・PRAGMA・前方のみの
// マイグレーション実行器を持つ。Go の internal 規則により、この配下は
// internal/core からしか import できない（P2）。
//
// クリティカル設計決定 2（親 #4・PD1・PD2）に従う:
//   - ドライバは modernc.org/sqlite（cgo 不要）
//   - 開くたびに WAL・busy_timeout（5000ms）・foreign_keys=ON を設定する
//   - 書き込みトランザクションはすべて BEGIN IMMEDIATE で始める
//   - プロセス内の書き込み接続は 1 本に絞る
//   - マイグレーションは PRAGMA user_version と embed.FS の SQL による自前の
//     実行器で、開くときに自動で適用する（1 版 = 1 トランザクション）
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"modernc.org/sqlite" // import 自体が database/sql ドライバを登録する。Error 型の判定にも使う
)

// busyTimeoutMillis は §クリティカル設計決定 2 の仮定（5000ms）。
const busyTimeoutMillis = 5000

// sqliteBusyPrimaryResultCode は SQLite の主要結果コード SQLITE_BUSY（5）。
// sqlite.org の C API が安定して公開している値であり、modernc.org/sqlite の
// 内部生成パッケージ（modernc.org/sqlite/lib）に依存せずハードコードする。
// 拡張結果コード（例: SQLITE_BUSY_TIMEOUT = 5 | (3 << 8)）を下位 8 ビットで
// マスクして判定する。
const sqliteBusyPrimaryResultCode = 5

var (
	// ErrTooNew はストアのスキーマ版がこのバイナリの知る最新版より新しいことを表す。
	// 開く操作（読み取りを含む）はファイルを一切変更せずに失敗する。
	ErrTooNew = errors.New("store: schema version is newer than this binary supports")

	// ErrBusy は他のプロセスが書き込みトランザクションを保持し続け、
	// busy_timeout の上限を超えても取得できなかったことを表す。
	ErrBusy = errors.New("store: timed out waiting for a write lock held by another process")
)

// DB は開いたストアへのハンドル。Close で必ず閉じる。
//
// 生の *sql.DB は公開しない（P2 と同じ理由で、呼び出し側が PRAGMA・トランザクション
// モードの前提を壊せないようにする）。すべての読み書きは Read / Write を経由する。
type DB struct {
	sqlDB          *sql.DB
	path           string
	initialVersion int
}

// Path はこの DB が開いているファイルのパスを返す。
func (d *DB) Path() string { return d.path }

// WasCreated は、この Open/OpenExisting 呼び出しの時点でスキーマが
// まだ無かったか（PRAGMA user_version が 0 だったか）を返す。core.Init は
// これを使って「この呼び出しで新規作成したか」を判定する（呼び出し前の
// os.Stat は、ストアを開く前後でファイルの状態が変わりうるため使わない）。
func (d *DB) WasCreated() bool { return d.initialVersion == 0 }

// Stats は基礎の *sql.DB の接続統計を返す（テスト専用。MaxOpenConnections の
// 検証などに使う）。
func (d *DB) Stats() sql.DBStats { return d.sqlDB.Stats() }

// Close はストアへの接続を閉じる。
func (d *DB) Close() error {
	if d == nil || d.sqlDB == nil {
		return nil
	}
	return d.sqlDB.Close()
}

// Write は fn を 1 つのトランザクションの中で実行する。DSN の _txlock=immediate
// により、このトランザクションは常に BEGIN IMMEDIATE で始まる（クリティカル
// 設計決定 2: 「書き込みトランザクションはすべて BEGIN IMMEDIATE で始める」）。
// fn がエラーを返せばロールバックし、そうでなければコミットする。SQLITE_BUSY は
// ErrBusy に写像して返す。
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.execTx(ctx, nil, fn)
}

// Read は fn を 1 つの読み取り専用トランザクションの中で実行する。ReadOnly の
// トランザクションではドライバが _txlock を無視して素の BEGIN（deferred）を
// 発行するため、書き込みロックを要求しない（WAL では、別のプロセスが書き込み
// トランザクションを保持していても読み取りは塞がれない）。SQLITE_BUSY は
// ErrBusy に写像して返す。
//
// fn の中で書き込みをしてはならない（書くなら Write を使う）。deferred の
// トランザクションからの書き込みは BEGIN IMMEDIATE の規約を迂回し、ロック昇格時に
// busy_timeout で待たない SQLITE_BUSY_SNAPSHOT になりうる。
func (d *DB) Read(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.execTx(ctx, &sql.TxOptions{ReadOnly: true}, fn)
}

func (d *DB) execTx(ctx context.Context, txOpts *sql.TxOptions, fn func(*sql.Tx) error) (err error) {
	tx, err := d.sqlDB.BeginTx(ctx, txOpts)
	if err != nil {
		return classifyErr(err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = fn(tx); err != nil {
		return classifyErr(err)
	}
	if err = tx.Commit(); err != nil {
		return classifyErr(err)
	}
	return nil
}

// openMode は Open がファイルの不在をどう扱うかを選ぶ。
type openMode int

const (
	// modeCreateIfMissing はファイルが無ければ作る（init が使う）。
	modeCreateIfMissing openMode = iota
	// modeExistingOnly はファイルが無ければ作らずに失敗する（fail-closed。
	// OpenWorkspace が使う）。
	modeExistingOnly
)

// openOptions は Open の内部オプション。busyTimeoutMillis はテストだけが
// 本番の DSN 組み立て（dsnFor）を通したまま busy_timeout を短縮するために使う
// （openWithOptions 経由）。
type openOptions struct {
	mode              openMode
	busyTimeoutMillis int
}

// Open は path の SQLite ファイルを開き（無ければ作る）、次を行う:
//  1. PRAGMA user_version を読み、migrations の最大版より新しければ ErrTooNew を返す
//     （ファイルは一切変更しない）。
//  2. WAL・busy_timeout・foreign_keys を設定し、それぞれ読み戻して検証する。
//  3. 現在の版が最新版より古ければ、前方のマイグレーションを 1 版 1 トランザクションで
//     順に適用する。
//
// migrations は呼び出し側が組み立てた集合をそのまま使う（本番は Migrations() の値、
// テストは架空の追加版を継ぎ足した値を渡してよい）。
func Open(path string, migrations []Migration) (*DB, error) {
	return openWithOptions(path, migrations, openOptions{
		mode:              modeCreateIfMissing,
		busyTimeoutMillis: busyTimeoutMillis,
	})
}

// OpenExisting は Open と同じだが、path が既に存在するときだけ開く
// （fail-closed。ファイルが無ければ作らずに失敗する）。init 以外のコマンドが
// ワークスペースを決定する core.OpenWorkspace が使う。
func OpenExisting(path string, migrations []Migration) (*DB, error) {
	return openWithOptions(path, migrations, openOptions{
		mode:              modeExistingOnly,
		busyTimeoutMillis: busyTimeoutMillis,
	})
}

// openWithOptions は Open / OpenExisting の共通実装。非公開のテスト専用フックで、
// busy_timeout を短縮したテスト（internal/core/internal/store/busy_test.go）が
// 本番の DSN 組み立て（dsnFor）をそのまま通すために使う。
func openWithOptions(path string, migrations []Migration, opts openOptions) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsnFor(path, opts))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, classifyErr(err))
	}
	// プロセス内の書き込み接続は 1 本に絞る（クリティカル設計決定 2）。
	// 読み取りも同じ 1 本を共有する（M1 は CLI が 1 コマンド 1 プロセスで
	// 完結するため、プロセス内の読み書きの直列化は許容できる簡素化）。
	sqlDB.SetMaxOpenConns(1)

	version, err := readUserVersion(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: read schema version of %s: %w", path, classifyErr(err))
	}

	latest := latestVersion(migrations)
	if version > latest {
		// ファイルを変更する前に判定して閉じる（store_too_new はファイルを変えない）。
		_ = sqlDB.Close()
		return nil, ErrTooNew
	}

	if err := setJournalModeWAL(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: set WAL mode on %s: %w", path, classifyErr(err))
	}

	if version < latest {
		if err := applyMigrations(sqlDB, migrations, version); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}

	if err := verifyPragmas(sqlDB, opts.busyTimeoutMillis); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: verify pragmas on %s: %w", path, err)
	}

	return &DB{sqlDB: sqlDB, path: path, initialVersion: version}, nil
}

func readUserVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setJournalModeWAL は PRAGMA journal_mode=WAL を実行し、返ってきた結果行を
// 読み戻して "wal" であることを確認する（PRAGMA journal_mode は結果セットを
// 返す問い合わせであり、実行しただけでは要求した値になったかを検証できない）。
func setJournalModeWAL(db *sql.DB) error {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("journal_mode = %q after requesting WAL, want wal", mode)
	}
	return nil
}

// verifyPragmas は Open が要求した PRAGMA（foreign_keys・busy_timeout）が
// 実際に効いていることを読み戻して確認する。journal_mode は setJournalModeWAL が
// 別途、判定より前のタイミングで検証する。
func verifyPragmas(db *sql.DB, wantBusyTimeoutMillis int) error {
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if busyTimeout != wantBusyTimeoutMillis {
		return fmt.Errorf("busy_timeout = %d, want %d", busyTimeout, wantBusyTimeoutMillis)
	}
	return nil
}

// dsnFor は modernc.org/sqlite の DSN を file: URI として組み立てる。
//
// 生の "path?query" 連結（旧実装）は、path 自身がリテラルの '?' を含むと
// 最初の '?' で DSN が切り詰められ、意図しないファイルを開いたうえ
// _busy_timeout 等のクエリパラメータも失われる（実機で再現済み）。file: URI
// なら path 部分は必ず percent-encode され、'?' '#' '%' 空白を含むパスでも
// 正しく1つのファイルとして解釈される。
//
//   - _busy_timeout: sqlite3_busy_timeout に相当。上限を超えると SQLITE_BUSY。
//   - _foreign_keys: 外部キー制約を有効にする。
//   - _txlock=immediate: db.Begin()/BeginTx() が発行する BEGIN を常に
//     BEGIN IMMEDIATE にする（読みから書きへの昇格時の SQLITE_BUSY を避ける）。
//   - mode=rw: modeExistingOnly のときだけ付け、ファイルが無ければ作らずに
//     失敗させる（SQLite 自身の URI 解釈がこのモードを尊重する。実機で確認済み:
//     modernc.org/sqlite は常に SQLITE_OPEN_CREATE を要求するが、file: URI の
//     mode パラメータがそれより制限的であれば、より制限的な方が優先される）。
//
// _journal_mode はここに含めない。version too new の判定より前に PRAGMA を
// 実行してしまうと、判定前にファイルが変わりうるため（Open 内で判定後に
// 明示的に PRAGMA journal_mode=WAL を実行する）。
func dsnFor(path string, opts openOptions) string {
	q := url.Values{}
	q.Set("_busy_timeout", fmt.Sprintf("%d", opts.busyTimeoutMillis))
	q.Set("_foreign_keys", "1")
	q.Set("_txlock", "immediate")
	if opts.mode == modeExistingOnly {
		q.Set("mode", "rw")
	}
	// OmitHost: 相対パスの先頭セグメントが URI の authority として出力されるのを防ぐ
	// （"file://rel/x.db" は SQLite が invalid uri authority で拒否する）。
	u := &url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: q.Encode()}
	return u.String()
}

// classifyErr は SQLITE_BUSY を ErrBusy に写像し、それ以外はそのまま返す
// （呼び出し側で fmt.Errorf の %w として包む）。
func classifyErr(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteBusyPrimaryResultCode {
		return ErrBusy
	}
	return err
}

// sqliteConstraintUniqueExtendedResultCode は SQLite の拡張結果コード
// SQLITE_CONSTRAINT_UNIQUE（2067）。主要結果コード SQLITE_CONSTRAINT（19）は
// 外部キー（787）・主キー（1555）・NOT NULL・CHECK の違反とも共有されるため、
// UNIQUE 制約の違反だけを判別するには拡張コードの完全一致で見る。
const sqliteConstraintUniqueExtendedResultCode = 2067

// IsUniqueViolation は err が UNIQUE 制約の違反（SQLITE_CONSTRAINT_UNIQUE）かを
// 返す。Write の fn の中（コミット前）で、文の実行が返したエラーに対して使う
// （呼び出し側が同じトランザクションの中でドメインのエラーへ翻訳するため）。
// 主キー・外部キーの違反には true を返さない。
func IsUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintUniqueExtendedResultCode
}
