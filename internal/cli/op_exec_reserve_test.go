package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newReservingGH は、`gh pr merge --auto` が予約（auto_merge を付ける）だけをして PR を open のままにする偽の gh。
// 反映（merged）は、テストが <dir>/merged を作ったとき（接続先が反映したとき）に起きる。
func newReservingGH(t *testing.T, head string) *releaseGH {
	t.Helper()
	g := newReleaseGH(t, head)
	script := fmt.Sprintf(`#!/bin/sh
D=%s
printf 'CALL %%s\n' "$*" >> "$D/calls.log"
case "$*" in
*"pr merge "*)
  case "$*" in *--auto*) : > "$D/reserved"; exit 0;; esac
  echo "fake gh: merge without --auto" >&2; exit 1;;
*repos/o/r/pulls/1)
  HEAD=$(cat "$D/head")
  AUTO=null
  if [ -f "$D/reserved" ]; then AUTO='{"merge_method":"squash"}'; fi
  if [ -f "$D/merged" ]; then
    printf '{"html_url":"https://github.com/o/r/pull/1","title":"PR","state":"closed","merged_at":"2026-10-23T00:00:00Z","base":{"ref":"main"},"head":{"sha":"%%s"},"mergeable":null,"merge_commit_sha":"%s","auto_merge":null}' "$HEAD"
  else
    printf '{"html_url":"https://github.com/o/r/pull/1","title":"PR","state":"open","merged_at":null,"base":{"ref":"main"},"head":{"sha":"%%s"},"mergeable":true,"auto_merge":%%s}' "$HEAD" "$AUTO"
  fi
  exit 0;;
*check-runs*) echo '{"total_count":0,"check_runs":[]}'; exit 0;;
*status*) echo '{"state":"success","total_count":0,"statuses":[]}'; exit 0;;
*) echo "fake gh: no route for: $*" >&2; exit 1;;
esac
`, shellSingleQuote(g.dir), execMerged)
	if err := os.WriteFile(filepath.Join(g.dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return g
}

// 予約の往復（CLI から実ストアまで）: 1 周目は予約だけして release は approved のまま（merge_requested）、
// 2 周目は予約し直さず待ち、接続先が反映した後の周で反映済み（already_merged）になり executed になる。
func TestOpExec_ReservationRoundTrip_RequestsOnceThenSeesTheMerge(t *testing.T) {
	ws, cid, op := execWorkspace(t)
	gh := newReservingGH(t, execHead)

	_, items, _ := execItems(t, runJSON(t, ws, "op", "exec"))
	if len(items) != 1 || items[0].(map[string]any)["result"] != "merge_requested" {
		t.Fatalf("run 1 items = %v, want merge_requested", items)
	}
	if m := gh.mergeCalls(t); len(m) != 1 || !strings.Contains(m[0], "--auto") || !strings.HasSuffix(m[0], "--match-head-commit "+execHead) {
		t.Fatalf("run 1 merge calls = %v, want one --auto call on the approved head", m)
	}
	shown := runJSON(t, ws, "show", cid)["operations"].([]any)[0].(map[string]any)
	if shown["state"] != "approved" {
		t.Fatalf("after run 1 state = %v, want approved", shown["state"])
	}

	_, items, _ = execItems(t, runJSON(t, ws, "op", "exec"))
	if len(items) != 1 || items[0].(map[string]any)["result"] != "waiting_checks" || items[0].(map[string]any)["consecutive_failures"] != float64(0) {
		t.Fatalf("run 2 items = %v, want waiting_checks (not counted)", items)
	}
	if m := gh.mergeCalls(t); len(m) != 1 {
		t.Fatalf("run 2 merge calls = %v, want no second request", m)
	}

	gh.write(t, "merged", "")
	_, items, _ = execItems(t, runJSON(t, ws, "op", "exec"))
	it := items[0].(map[string]any)
	if it["operation_id"] != op || it["result"] != "already_merged" || it["merge_commit"] != execMerged {
		t.Fatalf("run 3 item = %v, want already_merged with the merge commit", it)
	}
	shown = runJSON(t, ws, "show", cid)["operations"].([]any)[0].(map[string]any)
	if shown["state"] != "executed" || shown["merge_commit"] != execMerged {
		t.Errorf("final state = %v, want executed", shown)
	}
}

// 予約の後に別の head が反映されたときは、反映済みにせず承認なしの反映（merged_unapproved）として残る。
func TestOpExec_ReservationThenOtherHeadMerged_IsMergedUnapproved(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	gh := newReservingGH(t, execHead)
	runJSON(t, ws, "op", "exec")
	gh.write(t, "head", execHead2)
	gh.write(t, "merged", "")
	_, items, _ := execItems(t, runJSON(t, ws, "op", "exec"))
	if len(items) != 1 || items[0].(map[string]any)["result"] != "merged_unapproved" {
		t.Fatalf("items = %v, want merged_unapproved", items)
	}
}
