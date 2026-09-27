package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
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
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		if errors.Is(err, github.ErrGHNotFound) {
			return nil, NewError(CodeUpstreamUnavailable, err.Error())
		}
		// gh は見つかったが Options の解決自体が失敗する経路は現状無いが、
		// fail-closed に internal_error へ倒す（防御的）。
		return nil, NewError(CodeInternalError, err.Error())
	}

	result, err := a.Store.Ingest(context.Background(), core.ChannelCLI, core.IngestInput{
		Sources:  selected,
		Upstream: client,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	return textOutput{
		json: ingestResultJSON(result),
		text: ingestResultText(result),
	}, nil
}

// ingestResultJSON は core.IngestResult を「成功時の JSON 出力の規約」の
// `ingest` の形（docs/features/m1-core.md ##### `ingest`）へ変換する。
func ingestResultJSON(res *core.IngestResult) map[string]any {
	sources := make([]any, 0, len(res.Sources))
	for _, sr := range res.Sources {
		repos := make([]any, 0, len(sr.Repos))
		for _, rr := range sr.Repos {
			items := make([]any, 0, len(rr.Items))
			for _, item := range rr.Items {
				// challenge_id・upstream_state・policy_state が null になる
				// （対応の情報を積まずに failed を返す）反映失敗では、
				// comments_count も同じく「不明」を表すため null にする
				// （self-review 指摘: 0 のままだと「コメント 0 件を観測した」と
				// 区別できず、M1 §成功時の JSON 出力の規約「未設定の任意値は
				// null」と食い違っていた）。unread は「空の一覧は []」の規則
				// どおり、この場合も [] のまま（他の一覧フィールドと同じ扱い）。
				var commentsCount any = item.CommentsCount
				if item.ChallengeID == "" {
					commentsCount = nil
				}
				items = append(items, map[string]any{
					"external_key":        item.ExternalKey,
					"challenge_id":        nullableString(item.ChallengeID),
					"result":              string(item.Result),
					"upstream_state":      nullableString(item.UpstreamState),
					"policy_state":        nullableString(item.PolicyState),
					"comments_count":      commentsCount,
					"upstream_updated_at": nullableString(item.UpstreamUpdatedAt),
					"unread":              discrepancyKindsJSON(item.Unread),
					"error":               nullableStringPtr(item.Error),
				})
			}
			repos = append(repos, map[string]any{
				"repo":     rr.Repo,
				"error":    nullableStringPtr(rr.Error),
				"items":    items,
				"excluded": rr.Excluded,
			})
		}
		sources = append(sources, map[string]any{
			"id":                      sr.ID,
			"self_assignees_resolved": sr.SelfAssigneesResolved,
			"repos":                   repos,
		})
	}
	return map[string]any{"sources": sources}
}

// nullableString は s が空文字列なら null、そうでなければ s 自身を返す
// （「成功時の JSON 出力の規約」§未設定と空「未設定の任意値は null」）。
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableStringPtr は p が nil なら null、そうでなければ *p を返す。
func nullableStringPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
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
