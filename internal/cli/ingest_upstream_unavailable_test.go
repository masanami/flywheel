package cli

import (
	"bytes"
	"testing"
)

// このファイルは AC-15「PATH に `gh` が無い環境で `ingest` を実行すると、
// 終了コード 2・upstream_unavailable で終わり、ストアが変わらない」を検証する。

func TestIngest_GHNotOnPATH_UpstreamUnavailable(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	before := storeBytes(t, ws)

	// PATH を空のディレクトリだけにする（`gh` は元より、テスト実行環境の他の
	// コマンドも一切解決できない状態にする）。
	t.Setenv("PATH", t.TempDir())

	requireJSONErrorEnvelope(t, []string{"ingest", "--workspace", ws}, 2, CodeUpstreamUnavailable)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("upstream_unavailable must not change the store file")
	}
}

// TestIngest_GHNotOnPATH_UnknownSourceStillWinsAsValidationFailed は判定の
// 順序の確認: --source が宣言に無い id なら、gh の有無を確かめるより前に
// validation_failed（終了コード 1）で終わる（宣言・--source の判定 →
// upstream_unavailable の順。設計上の決定）。
func TestIngest_GHNotOnPATH_UnknownSourceStillWinsAsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	t.Setenv("PATH", t.TempDir())

	requireJSONErrorEnvelope(t, []string{"ingest", "--source", "does-not-exist", "--workspace", ws}, 1, CodeValidationFailed)
}
