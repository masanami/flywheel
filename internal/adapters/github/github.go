// Package github は core.UpstreamIssueSource（internal/core/upstream.go）を、
// PATH 上の gh を子プロセスとして起動する gh api で実装する。
//
// docs/features/m2-github-issue-ingest.md §機能全体の設計「規則は core、
// 入出力は adapter」に従い、このパッケージは gh の起動と応答の core の型
// （core.UpstreamIssue）への正規化だけを行い、取り込みの規則（ポリシー・
// fingerprint・3 分岐・close の確かめ・食い違い）は一切持たない。依存は
// internal/core（取得 IF・型・ErrUpstreamIssueNotFound）と標準ライブラリだけで、
// ストア（internal/core/internal/store）を import しない
// （AC-105。CLAUDE.md「モジュール構成の規約」）。
package github

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ErrGHNotFound は PATH 上に gh が見つからなかったことを表す sentinel
// error。errors.Is で判定できる（CLI〈#59〉がこれを upstream_unavailable へ
// 写せるようにするための契約。docs/features/m2-github-issue-ingest.md
// §上流からの取得「gh が PATH に無いときは、何も変更せずに
// upstream_unavailable で終わる」）。
var ErrGHNotFound = errors.New("adapters/github: gh executable not found in PATH")

// defaultTimeout は gh の 1 回の呼び出しに置く時間の上限の既定値
// （【仮定】。Issue #55 本文に「既定 60 秒」と明記されている）。
const defaultTimeout = 60 * time.Second

// Options は Client の挙動を調整する（テストから短い Timeout に差し替える
// ため）。
type Options struct {
	// Timeout は gh の 1 回の呼び出しに置く時間の上限。ゼロ値なら
	// defaultTimeout を使う。
	Timeout time.Duration
}

// Client は core.UpstreamIssueSource の実装。gh の絶対パスを保持し、以後の
// 呼び出しに使う（コードに絶対パスを埋め込まない。AC-106）。
type Client struct {
	ghPath  string
	timeout time.Duration
}

// New は PATH 上の gh を探し、見つかれば Client を返す。見つからなければ
// errors.Is(err, ErrGHNotFound) が true になるエラーを返す。
func New(opts Options) (*Client, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrGHNotFound, err.Error())
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	return &Client{ghPath: path, timeout: timeout}, nil
}
