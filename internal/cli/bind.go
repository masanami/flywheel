package cli

import (
	"context"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runBind は `flywheel bind <C-ID> <owner/repo#番号>` の実装
// （docs/features/m2-github-issue-ingest.md §課題と Issue の紐づけ）。4 つの条件の検査・上流の取得・
// 書き込みはすべて core.Store.BindChallenge が行い、ここは宣言の読み込みと `gh` のクライアントの
// 組み立て、結果の写像だけを行う（P2「規則は core、CLI は写すだけ」）。承認も本人確認も要らない
// （`dep add`・`edit` と同じ。--relayed は持たない）。
//
// 宣言が無い・壊れていれば config_not_found・config_invalid（終了 2）、`gh` が PATH に無ければ
// upstream_unavailable（終了 2）。いずれも core を呼ぶ前に決まるのでストアは変わらない。
func runBind(a Args) (any, error) {
	decl, err := core.LoadSourcesDeclaration(a.Store.Workspace())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	client, err := newIngestClient()
	if err != nil {
		return nil, err
	}
	c, err := a.Store.BindChallenge(context.Background(), core.ChannelCLI, core.BindInput{
		ChallengeID: a.Positional[0],
		ExternalKey: a.Positional[1],
		Sources:     decl.Sources,
		Upstream:    client,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// runUnbind は `flywheel unbind <C-ID>` の実装。上流へは接続しない（宣言も読まない）。
func runUnbind(a Args) (any, error) {
	c, err := a.Store.UnbindChallenge(context.Background(), core.ChannelCLI, a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}
