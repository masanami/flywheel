package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
)

// Observer はストアの変化を観測するための専用の接続を 1 本だけ持つ。
// DB（書き込み・閲覧）とは別に開き、PRAGMA data_version を読むためだけに使う。
//
// data_version は「他の接続の commit」でだけ変わる（この接続自身の書き込みでは
// 変わらない）。そのためこの接続では書かない。接続プールの別接続へ振り分けられると
// 値が比較できなくなるので、*sql.DB ではなく固定した *sql.Conn を使う。
type Observer struct {
	db   *sql.DB
	conn *sql.Conn
}

// OpenObserver は path の既存のストアに観測用の接続を開く。ファイルが無ければ
// 作らずに失敗する。PRAGMA の設定・マイグレーションはしない（データは書かない。\n// 他の接続がすべて閉じたあとの最後の接続になった場合は、SQLite の規則で WAL の\n// チェックポイントが走りうる）。
func OpenObserver(ctx context.Context, path string) (*Observer, error) {
	q := url.Values{}
	q.Set("mode", "rw") // 無ければ作らない。観測の接続では書かないが、WAL の共有メモリを開くために rw で開く。
	q.Set("_busy_timeout", fmt.Sprintf("%d", busyTimeoutMillis))
	u := &url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("store: open observer %s: %w", path, classifyErr(err))
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open observer %s: %w", path, classifyErr(err))
	}
	o := &Observer{db: db, conn: conn}
	// 開けていること（ファイルが SQLite のストアであること）をここで確かめる。
	if _, err := o.DataVersion(ctx); err != nil {
		_ = o.Close()
		return nil, err
	}
	return o, nil
}

// DataVersion は PRAGMA data_version の現在の値を返す。
func (o *Observer) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := o.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read data_version: %w", classifyErr(err))
	}
	return v, nil
}

// Close は観測の接続を閉じる。
func (o *Observer) Close() error {
	if o == nil {
		return nil
	}
	_ = o.conn.Close()
	return o.db.Close()
}
