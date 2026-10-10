package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// ingestGHTimeout は `gh` の 1 回の呼び出しの時間の上限（Issue #59
// §時間の上限の差し替え）。ゼロ値は internal/adapters/github の既定
// （60 秒）を使う。テストだけがこのパッケージ変数を書き換える（本番の既定は
// 環境変数・フラグで変えられるようにしない。設計上の【仮定】）。
var ingestGHTimeout time.Duration

// runIngest は `flywheel ingest [--source <id>]` の実装。
//
// 宣言の読み込みと検証（core.LoadSourcesDeclaration）・`--source` の対象の
// 絞り込み（core.SelectSources）は #54 の範囲のまま。本チケット（#59）は、
// その後に internal/adapters/github で `gh` を子プロセスとして起動する
// アダプタを組み立て、core.Store.Ingest（取得・冪等な作成と更新・上流の
// close の検出。すべて core の規則）へ渡し、結果を「成功時の JSON 出力の
// 規約」の形へ写すだけを行う（P2「規則は core、CLI は写すだけ」）。
//
// 宣言ファイルが無ければ core.ErrConfigNotFound（→ config_not_found・終了 2）、
// 解釈できない・規則に反する宣言なら core.ErrConfigInvalid（→ config_invalid・
// 終了 2）。`--source` が宣言に無い id なら core.ErrValidation
// （→ validation_failed・終了 1）。`gh` が PATH に無ければ
// github.ErrGHNotFound（→ upstream_unavailable・終了 2）。いずれもストアを
// 変えない。
//
// RequiresStore: true（commands.go）により、ここへ到達する前に
// core.OpenWorkspace がストアを開いている。ストアの版が新しすぎる場合は
// store_too_new でその時点で終わり、宣言の読み込み・--source の判定・`gh` の
// 呼び出しのいずれにも進まない（AC-98「gh が呼ばれない」は、この事前チェックが
// Run より前に決着することで自動的に成り立つ）。
func runIngest(a Args) (any, error) {
	decl, err := core.LoadSourcesDeclaration(a.Store.Workspace())
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// --source の指定の有無を区別する（self-review 指摘: 文字列の値だけで
	// 判定すると `--source ""`・`--source=` が「省略」と同じ「全件選択」に化け、
	// AC-13 の fail-closed から外れる）。既存の任意フラグ（runList の
	// `--status`）と同じ形。
	var sourceID *string
	if v, ok := a.Values["source"]; ok {
		sourceID = &v
	}
	selected, err := core.SelectSources(decl, sourceID)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// gh の有無は、宣言・--source の判定の後、core.Ingest（実際の取得）の前に
	// 確かめる。ここまでの判定はストアを変えないので、upstream_unavailable
	// でもストアは不変のまま終わる。
	client, err := newIngestClient()
	if err != nil {
		return nil, err
	}

	result, err := a.Store.Ingest(context.Background(), core.ChannelCLI, core.IngestInput{
		Sources:  selected,
		Upstream: client,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	return textOutput{
		json: view.FromIngestResult(result),
		text: ingestResultText(result),
	}, nil
}

// newIngestClient は取り込み（`ingest`・`cycle` の取り込みの段）が使う `gh` のクライアントを
// 組み立てる。`gh` が PATH に無ければ upstream_unavailable（終了 2）、それ以外の組み立ての
// 失敗は fail-closed に internal_error へ倒す（gh は見つかったが Options の解決自体が
// 失敗する経路は現状無いが、防御的）。`ingest` と `cycle` が同じ写像を共有する。
func newIngestClient() (*github.Client, error) {
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		if errors.Is(err, github.ErrGHNotFound) {
			return nil, NewError(CodeUpstreamUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
	}
	return client, nil
}

// ingestResultText は --json 無しの `ingest` の表示（形式の安定は保証しない。
// 「成功時の JSON 出力の規約」）。取り込み元・リポジトリごとに件数の要約を出す。
func ingestResultText(res *core.IngestResult) string {
	var b strings.Builder
	if len(res.Sources) == 0 {
		return "取り込み元がありません\n"
	}
	for _, sr := range res.Sources {
		fmt.Fprintf(&b, "取り込み元 %s（self_assignees_resolved=%t）:\n", sr.ID, sr.SelfAssigneesResolved)
		for _, rr := range sr.Repos {
			if rr.Error != nil {
				fmt.Fprintf(&b, "  %s: エラー: %s\n", rr.Repo, *rr.Error)
				continue
			}
			counts := map[core.IngestOutcome]int{}
			for _, item := range rr.Items {
				counts[item.Result]++
			}
			fmt.Fprintf(&b, "  %s: 作成=%d 更新=%d 変化なし=%d スキップ=%d fingerprint未知=%d 失敗=%d 対象外=%d\n",
				rr.Repo,
				counts[core.IngestOutcomeCreated],
				counts[core.IngestOutcomeUpdated],
				counts[core.IngestOutcomeUnchanged],
				counts[core.IngestOutcomeSkippedDone],
				counts[core.IngestOutcomeFingerprintUnknownVersion],
				counts[core.IngestOutcomeFailed],
				rr.Excluded,
			)
		}
	}
	return b.String()
}
