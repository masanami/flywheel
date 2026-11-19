package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルのサブテスト名の AC-<n>（Issue813 を添えたものは S2c の #813 の番号）は、core の見直しの区切り S1（docs/features/core-redesign.md。番号は masanami/flywheel#732 の
// 受入基準の並び順）の受入基準の番号。同じファイルの別の仕様の番号とは無関係。

// 課題の詳細の API の waiting を、実ストア・実際の HTTP の要求で検証する。関係グラフの API は外した（#813・AC-72）。

// relationsWorkspace は cardsEndToEndWorkspace（C-1〜C-6。人間の操作待ち・応答なし・CI の待ちを含む）に、
// 依存・親子を足したワークスペース。
func relationsWorkspace(t *testing.T, now time.Time) string {
	t.Helper()
	ws := cardsEndToEndWorkspace(t, now)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.AddDependencies(ctx, core.ChannelCLI, "C-4", []string{"C-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParent(ctx, core.ChannelCLI, "C-5", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParent(ctx, core.ChannelCLI, "C-6", "C-1"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return ws
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	m := decodeMap(t, body)
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// 課題の詳細の API（GET …/challenges/{id}）の waiting は、まだ終わっていない前提の課題だけを返す（AC-46）。
func TestChallengeAPI_WaitingHasOnlyPrerequisites(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := relationsWorkspace(t, now)
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	t.Run("AC-46_waiting_has_no_conflicts_key", func(t *testing.T) {
		for _, id := range []string{"C-2", "C-3", "C-4", "C-6"} {
			w := mustJSON(t, port, "/api/v1/workspaces/w/challenges/"+id)["waiting"].(map[string]any)
			assertKeys(t, id+" waiting", w, "prerequisites")
		}
		w4 := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-4")["waiting"].(map[string]any)
		pre := w4["prerequisites"].([]any)
		if len(pre) != 1 || pre[0].(map[string]any)["challenge_id"] != "C-5" {
			t.Errorf("C-4 prerequisites = %v, want C-5", pre)
		}
	})
}

// GET …/graph は、ほかの無いパスと同じ HTTP 404・コード not_found を返す（AC-72・#813）。
// 課題が前提・親子を持つ（グラフがあった頃なら応答が出た）ワークスペースでも、書き込みの動詞でも同じ。
func TestGraphAPI_IsGone_AC72_Issue813(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := relationsWorkspace(t, now)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := apiGet(t, port, "/api/v1/workspaces/w/graph")
	if code != http.StatusNotFound || errorCode(t, body) != "not_found" {
		t.Errorf("GET graph = %d %s, want 404 not_found", code, body)
	}
	// ほかの無いパスと同じ応答（コードと状態が一致する）
	code2, body2 := apiGet(t, port, "/api/v1/workspaces/w/no-such-path")
	if code2 != code || errorCode(t, body2) != errorCode(t, body) {
		t.Errorf("graph (%d %s) differs from an unknown path (%d %s)", code, body, code2, body2)
	}
	token, _ := fetchToken(t, port)
	if c, _ := post(t, port, "/api/v1/workspaces/w/graph", token, token); c != http.StatusNotFound {
		t.Errorf("POST graph = %d, want 404", c)
	}
}
