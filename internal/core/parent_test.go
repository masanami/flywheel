package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// docs/features/ui-relation-graph.md §親子（core）。

// parentRows は challenge_parent の行を 子 → 親 の写しで返す。
func parentRows(t *testing.T, s *Store) map[int64]int64 {
	t.Helper()
	out := map[int64]int64{}
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT challenge_id, parent_id FROM challenge_parent ORDER BY challenge_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c, p int64
			if err := rows.Scan(&c, &p); err != nil {
				return err
			}
			out[c] = p
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read parents: %v", err)
	}
	return out
}

func lastParentActivity(t *testing.T, s *Store, id string) Activity {
	t.Helper()
	acts := depActivities(t, s, id)
	if len(acts) == 0 {
		t.Fatalf("no activities for %s", id)
	}
	return acts[len(acts)-1]
}

func TestSetParent_SavesTheParentWithoutBumpingTheVersionAndLogs(t *testing.T) {
	s := depFixture(t, 3)
	before, err := s.GetChallenge(context.Background(), "C-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(context.Background(), ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatalf("SetParent: %v", err)
	}
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{2: 1}) {
		t.Errorf("parents = %v", got)
	}
	after, err := s.GetChallenge(context.Background(), "C-2")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version {
		t.Errorf("Version = %d, want unchanged %d (親子の変更は版を上げない)", after.Version, before.Version)
	}
	last := lastParentActivity(t, s, "C-2")
	if last.Entity != "challenge" || last.Action != "parent_set" || last.Channel != "cli" {
		t.Fatalf("last activity = %+v", last)
	}
	var a struct {
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(last.After, &a); err != nil || a.ParentID != "C-1" {
		t.Errorf("after = %s (%v)", last.After, err)
	}
}

// 2 つ目の親の登録は付け替えになる（親は 1 つまで）。
func TestSetParent_ASecondParentReplacesTheFirst(t *testing.T) {
	s := depFixture(t, 3)
	ctx := context.Background()
	if err := s.SetParent(ctx, ChannelCLI, "C-3", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-3", "C-2"); err != nil {
		t.Fatalf("SetParent (replace): %v", err)
	}
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{3: 2}) {
		t.Errorf("parents = %v, want only C-3 -> C-2", got)
	}
	last := lastParentActivity(t, s, "C-3")
	if last.Action != "parent_set" {
		t.Fatalf("last action = %q", last.Action)
	}
	var b, a struct {
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(last.Before, &b); err != nil || b.ParentID != "C-1" {
		t.Errorf("before = %s (%v)", last.Before, err)
	}
	if err := json.Unmarshal(last.After, &a); err != nil || a.ParentID != "C-2" {
		t.Errorf("after = %s (%v)", last.After, err)
	}
}

func TestSetParent_TheSameParentAgainWritesNothing(t *testing.T) {
	s := depFixture(t, 2)
	ctx := context.Background()
	if err := s.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	n := len(depActivities(t, s, "C-2"))
	if err := s.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatalf("SetParent (again): %v", err)
	}
	if got := len(depActivities(t, s, "C-2")); got != n {
		t.Errorf("activities = %d, want %d (冪等)", got, n)
	}
}

func TestSetParent_RejectsSelfReferenceAndCyclesWithoutWriting(t *testing.T) {
	s := depFixture(t, 3)
	ctx := context.Background()
	if err := s.SetParent(ctx, ChannelCLI, "C-1", "C-1"); !errors.Is(err, ErrSelfParent) {
		t.Errorf("self parent err = %v, want ErrSelfParent", err)
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-3", "C-2"); err != nil {
		t.Fatal(err)
	}
	n := len(depActivities(t, s, "C-1"))
	for _, tt := range []struct{ child, parent string }{{"C-1", "C-2"}, {"C-1", "C-3"}} {
		if err := s.SetParent(ctx, ChannelCLI, tt.child, tt.parent); !errors.Is(err, ErrParentCycle) {
			t.Errorf("SetParent(%s, %s) err = %v, want ErrParentCycle", tt.child, tt.parent, err)
		}
	}
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{2: 1, 3: 2}) {
		t.Errorf("parents = %v (拒否したら何も書かない)", got)
	}
	if got := len(depActivities(t, s, "C-1")); got != n {
		t.Errorf("activities of C-1 = %d, want %d", got, n)
	}
}

func TestSetParent_MissingOrMalformedChallengeIsNotFound(t *testing.T) {
	s := depFixture(t, 1)
	ctx := context.Background()
	for _, tt := range []struct{ child, parent string }{{"C-1", "C-9"}, {"C-9", "C-1"}, {"x", "C-1"}, {"C-1", "y"}} {
		if err := s.SetParent(ctx, ChannelCLI, tt.child, tt.parent); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetParent(%s, %s) err = %v, want ErrNotFound", tt.child, tt.parent, err)
		}
	}
	if got := parentRows(t, s); len(got) != 0 {
		t.Errorf("parents = %v", got)
	}
}

func TestClearParent_RemovesTheParentAndLogsOnlyWhenThereWasOne(t *testing.T) {
	s := depFixture(t, 2)
	ctx := context.Background()
	if err := s.ClearParent(ctx, ChannelCLI, "C-2"); err != nil {
		t.Fatalf("ClearParent (none): %v", err)
	}
	if got := lastParentActivity(t, s, "C-2").Action; got == "parent_clear" {
		t.Errorf("parent_clear logged although there was no parent")
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearParent(ctx, ChannelCLI, "C-2"); err != nil {
		t.Fatalf("ClearParent: %v", err)
	}
	if got := parentRows(t, s); len(got) != 0 {
		t.Errorf("parents = %v", got)
	}
	last := lastParentActivity(t, s, "C-2")
	if last.Action != "parent_clear" {
		t.Fatalf("last action = %q", last.Action)
	}
	var b struct {
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(last.Before, &b); err != nil || b.ParentID != "C-1" {
		t.Errorf("before = %s (%v)", last.Before, err)
	}
	if err := s.ClearParent(ctx, ChannelCLI, "C-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ClearParent(C-9) err = %v, want ErrNotFound", err)
	}
}

// 親子は表示にだけ使い、完了した課題にも付け替えできる（状態機械・依存の評価に使わない）。
func TestSetParent_AllowedOnADoneChallenge(t *testing.T) {
	s := depFixture(t, 2)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'done' WHERE id = 2`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(context.Background(), ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatalf("SetParent on a done challenge: %v", err)
	}
}

// 親子は書き出しと再構築で保たれる（ui-relation-graph.md 受入基準）。
func TestParent_SurvivesExportAndImport(t *testing.T) {
	src := depFixture(t, 3)
	ctx := context.Background()
	if err := src.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := src.SetParent(ctx, ChannelCLI, "C-3", "C-2"); err != nil {
		t.Fatal(err)
	}
	data, _, err := src.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(ctx, data); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := parentRows(t, dst); !reflect.DeepEqual(got, map[int64]int64{2: 1, 3: 2}) {
		t.Errorf("parents after import = %v", got)
	}
}
