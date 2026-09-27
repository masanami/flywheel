package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは #72（親要件チケット #51 §上流の更新の観測と既読）の
// 受入基準 AC-131〜AC-133 を検証する: `ingest --json` の各要素の
// comments_count・upstream_updated_at・unread が反映後の値になる。

func TestIngest_JSONItem_ReflectsObservationValues(t *testing.T) {
	ws := initializedWorkspace(t)
	const repo = "owner/observation-json-repo"
	decl := `{
  "version": 1,
  "sources": [
    {"id": "observation-json-source", "type": "github-issue", "repos": ["` + repo + `"], "self_assignees": ["someone"]}
  ]
}`
	writeSourcesDeclaration(t, ws, decl)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(repo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "t", Body: "b", Repo: repo, Comments: 4, UpdatedAt: "2026-09-25T08:00:00Z"},
		}), 0),
	})

	res := runJSON(t, ws, "ingest")
	sources := res["sources"].([]any)
	items := itemsOf(t, repoOf(t, findSourceByID(t, sources, "observation-json-source"), repo))
	item := findItemByExternalKey(t, items, repo+"#1")

	// AC-131: comments_count は反映後の観測値のコメント数。
	if item["comments_count"] != 4.0 {
		t.Errorf("comments_count = %v, want 4 (AC-131)", item["comments_count"])
	}
	// AC-132: upstream_updated_at は反映後の観測値の更新日時。
	if item["upstream_updated_at"] != "2026-09-25T08:00:00Z" {
		t.Errorf("upstream_updated_at = %v, want %q (AC-132)", item["upstream_updated_at"], "2026-09-25T08:00:00Z")
	}
	// AC-133: unread は反映後の記録から導いた部分集合（作成直後はコメントが
	// あるので upstream_commented を含み、upstream_updated は含まない）。
	unread, ok := item["unread"].([]any)
	if !ok {
		t.Fatalf("unread = %#v, want an array", item["unread"])
	}
	unreadStrings := make([]string, 0, len(unread))
	for _, u := range unread {
		unreadStrings = append(unreadStrings, u.(string))
	}
	if !containsString(unreadStrings, "upstream_commented") {
		t.Errorf("unread = %v, want to contain upstream_commented (AC-133)", unreadStrings)
	}
	if containsString(unreadStrings, "upstream_updated") {
		t.Errorf("unread = %v, must not contain upstream_updated right after creation (AC-133)", unreadStrings)
	}
}

// self-review 指摘の再発防止: challenge_id・upstream_state・policy_state が
// null になる（対応の情報を積まずに failed を返す）反映失敗では、
// comments_count も同じく「不明」を表すため null にする（0 のままだと
// 「コメント 0 件を観測した」と区別できない。M1 §成功時の JSON 出力の規約
// 「未設定の任意値は null」）。unread は「空の一覧は []」の規則どおり、この
// 場合も [] のまま。ingestResultJSON はネットワークにも DB にも触れない
// 純粋な変換関数なので、core.IngestItemResult を直接組み立てて検証する。
func TestIngestResultJSON_TotalFailureItem_NullsOutUnknownFields(t *testing.T) {
	errMsg := "boom"
	result := &core.IngestResult{Sources: []core.SourceIngestResult{{
		ID:                    "s",
		SelfAssigneesResolved: true,
		Repos: []core.RepoIngestResult{{
			Repo: "o/r",
			Items: []core.IngestItemResult{{
				ExternalKey: "o/r#1",
				Result:      core.IngestOutcomeFailed,
				// ChallengeID・UpstreamState・PolicyState・CommentsCount・
				// UpstreamUpdatedAt・Unread はすべてゼロ値（対応の情報を
				// 積まずに failed を返す実装。ingest.go の
				// `case err != nil:` 分岐と同じ形）。
				Error: &errMsg,
			}},
		}},
	}}}

	got := ingestResultJSON(result)
	sources := got["sources"].([]any)
	items := itemsOf(t, repoOf(t, sources[0].(map[string]any), "o/r"))
	item := findItemByExternalKey(t, items, "o/r#1")

	if item["challenge_id"] != nil || item["upstream_state"] != nil || item["policy_state"] != nil {
		t.Fatalf("item = %+v, want challenge_id/upstream_state/policy_state all null (test の前提)", item)
	}
	if item["comments_count"] != nil {
		t.Errorf("comments_count = %v, want null", item["comments_count"])
	}
	if item["upstream_updated_at"] != nil {
		t.Errorf("upstream_updated_at = %v, want null", item["upstream_updated_at"])
	}
	// ingestResultJSON を直接呼ぶ（JSON へのマーシャルを経由しない）ため、
	// discrepancyKindsJSON の戻り値の型（[]string）のまま入っている。
	if unread, ok := item["unread"].([]string); !ok || unread == nil || len(unread) != 0 {
		t.Errorf("unread = %#v, want a non-nil empty []string (not null)", item["unread"])
	}
}
