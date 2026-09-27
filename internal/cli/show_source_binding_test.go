package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #56 の完了条件「show の最上位に source_binding を出す」を
// CLI の JSON 出力の形で検証する（AC-42〜AC-48）。`ingest` コマンド自体の結線は
// #59 の範囲であり、まだ存在しないため、対応（source_binding）の作成は
// coretest.InsertSourceBinding（テスト専用のフィクスチャ）で行う。取り込みの
// 規則（ポリシー・冪等な作成）は internal/core/ingest_test.go が検証する。

func TestRunShow_SourceBindingNullForPlainCreatedChallenge(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)

	doc := runJSON(t, ws, "show", id)
	if v, ok := doc["source_binding"]; !ok || v != nil {
		t.Fatalf("source_binding = %#v, want null for a challenge created via `create` (AC-48)", v)
	}
}

func TestRunShow_SourceBindingReflectsIngestedChallenge(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)

	const (
		sourceID      = "harness-repo-issues"
		externalKey   = "masanami/flywheel#49"
		url           = "https://github.com/masanami/flywheel/issues/49"
		fingerprint   = "2:9c56b5c0a92d"
		upstreamState = "open"
		policyState   = "in_policy"
		at            = "2026-09-26T00:00:00.000Z"
	)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, id), sourceID, externalKey, url, fingerprint, upstreamState, policyState, at)

	doc := runJSON(t, ws, "show", id)
	sb, ok := doc["source_binding"].(map[string]any)
	if !ok {
		t.Fatalf("source_binding = %#v, want an object", doc["source_binding"])
	}
	want := map[string]any{
		"source_id":      sourceID,
		"external_key":   externalKey,
		"url":            url,
		"fingerprint":    fingerprint,
		"upstream_state": upstreamState,
		"policy_state":   policyState,
		// coretest.InsertSourceBinding は観測値・読んだ時点の値の列を指定しない
		// フィクスチャなので、既定値（0003 の前に作られた対応と同じ、コメント数
		// 0・更新日時未設定）のまま出力される（§クリティカル設計決定 1）。
		"comments_count":           0.0,
		"upstream_updated_at":      nil,
		"read_comments_count":      0.0,
		"read_upstream_updated_at": nil,
		"created_at":               at,
		"updated_at":               at,
	}
	for k, v := range want {
		if sb[k] != v {
			t.Errorf("source_binding[%q] = %v, want %v", k, sb[k], v)
		}
	}
	if len(sb) != len(want) {
		t.Errorf("source_binding keys = %v, want exactly %v", sb, want)
	}
	// 文書（m1-core.md の show の節）に書いた source_binding の形とも照合する
	// （create で作った課題では source_binding が null で照合されないため）。
	assertDocumentedJSON(t, loadDocumentedJSON(t), "show", doc)

	// テキスト出力にも対応の記録を出す（m2 仕様 §食い違いの表示）。
	text := runText(t, ws, "show", id)
	for _, s := range []string{sourceID, externalKey, url, upstreamState, policyState, fingerprint} {
		if !strings.Contains(text, s) {
			t.Errorf("show text output does not contain %q:\n%s", s, text)
		}
	}
}

func TestRunShow_TextOutputShowsNoSourceBinding(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	if text := runText(t, ws, "show", id); !strings.Contains(text, "取り込み元:    -\n") {
		t.Errorf("show text output = %q, want a line saying the challenge has no source binding", text)
	}
}
