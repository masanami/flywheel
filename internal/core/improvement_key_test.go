package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// 出どころ improvement の課題だけが改修の鍵を持つ（AC-407・AC-408）。
func TestInsertChallengeRow_ImprovementKeyInvariant(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	of := "C-1"
	key := &ImprovementKey{Layer: "prompt", Target: "j1.md"}
	cases := []struct {
		name    string
		origin  ChallengeOrigin
		of      *string
		key     *ImprovementKey
		wantErr bool
	}{
		{"improvement with key", ChallengeOriginImprovement, nil, key, false},
		{"improvement without key", ChallengeOriginImprovement, nil, nil, true},
		{"improvement with empty layer", ChallengeOriginImprovement, nil, &ImprovementKey{Target: "t"}, true},
		{"manual with key", ChallengeOriginManual, nil, key, true},
		{"ingest with key", ChallengeOriginIngest, nil, key, true},
		{"followup with key", ChallengeOriginFollowup, &of, key, true},
		{"manual without key", ChallengeOriginManual, nil, nil, false},
		{"followup without key", ChallengeOriginFollowup, &of, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.db.Write(ctx, func(tx *sql.Tx) error {
				_, _, err := insertChallengeRow(ctx, tx, time.Now(), "t", "", "", nil, "r", c.origin, c.of, c.key)
				return err
			})
			if c.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var layer, target sql.NullString
			if err := s.db.Read(ctx, func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT improvement_layer, improvement_target FROM challenge ORDER BY id DESC LIMIT 1`).Scan(&layer, &target)
			}); err != nil {
				t.Fatal(err)
			}
			if c.key == nil {
				if layer.Valid || target.Valid {
					t.Errorf("key = %v/%v, want NULL", layer, target)
				}
				return
			}
			if layer.String != c.key.Layer || target.String != c.key.Target {
				t.Errorf("key = %v/%v, want %v", layer, target, c.key)
			}
		})
	}
}

// show の challenge.Improvement は改修課題でだけ値を持つ（AC-353）。
func TestShowChallenge_Improvement(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	a := cardChallenge(t, s, StatusInProgress)
	d, err := s.GetChallengeWithoutReap(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Improvement != nil {
		t.Fatalf("Improvement = %v, want nil", *d.Improvement)
	}
	n, _ := parseChallengeID(a.ID)
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET origin = 'improvement', improvement_layer = 'prompt', improvement_target = 'j1.md' WHERE id = ?`, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err = s.GetChallengeWithoutReap(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Improvement == nil || d.Improvement.Layer != "prompt" || d.Improvement.Target != "j1.md" {
		t.Errorf("Improvement = %v", d.Improvement)
	}
}

// closed_at を持つ保留は開いていない保留として数えない（AC-409 の前提）。
func TestOpenHold_ExcludesClosedHolds(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	a := cardChallenge(t, s, StatusInProgress)
	n, _ := parseChallengeID(a.ID)
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'in_progress', '2026-10-26T00:00:00.000Z')`, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	holds, err := s.ListUnansweredHolds(ctx)
	if err != nil || len(holds) != 1 {
		t.Fatalf("holds = %v, err = %v, want 1 open hold", holds, err)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		id, _, _, _, err := loadUnansweredHold(ctx, tx, n)
		if err == nil && id == 0 {
			err = errors.New("hold id 0")
		}
		return err
	}); err != nil {
		t.Fatalf("loadUnansweredHold before close: %v", err)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE hold SET closed_at = '2026-10-26T01:00:00.000Z'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	holds, err = s.ListUnansweredHolds(ctx)
	if err != nil || len(holds) != 0 {
		t.Fatalf("holds = %v, err = %v, want none", holds, err)
	}
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		_, _, _, _, err := loadUnansweredHold(ctx, tx, n)
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("loadUnansweredHold err = %v, want sql.ErrNoRows", err)
	}
}
