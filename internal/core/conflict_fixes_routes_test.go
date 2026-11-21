package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// 課題の詳細の runs[].fix_pending_ref（fillRunFixPending）が、反映先宛ての 3 つの経路（統合先を使わない課題のタスクの実行・委譲先のフローを
// 通らない成果物の反映の依頼・上げる依頼）のどれでも値を出すことを、実ストアで固定する。

// pendingRuns は課題 id の詳細の runs のうち、fix_pending_ref を持つものを "R-<n>=<ref>" の形で返す。
func pendingRuns(t *testing.T, s *Store, id string) []string {
	t.Helper()
	d, err := s.GetChallengeWithoutReap(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallengeWithoutReap: %v", err)
	}
	var out []string
	for _, r := range d.Runs {
		if r.FixPendingRef != nil {
			out = append(out, r.ID+"="+*r.FixPendingRef)
		}
	}
	return out
}

// setPendingOnOpenRun は終わっていない契約の実行（1 本）に確かめ待ちの中身を置き、その実行を始めた run の表示形と種類を返す。
func setPendingOnOpenRun(t *testing.T, s *Store, ref string) (startRun, kind string) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE contract_run SET fix_pending_ref = ? WHERE ended_at IS NULL`, ref)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("open contract runs = %d, want 1", n)
		}
		var id int64
		if err := tx.QueryRow(`SELECT start_run_id, kind FROM contract_run WHERE ended_at IS NULL`).Scan(&id, &kind); err != nil {
			return err
		}
		startRun = formatRunID(id)
		return nil
	})
	return startRun, kind
}

func TestRunFixPending_IsShownOnEveryReflectDestinationRoute(t *testing.T) {
	t.Parallel()
	t.Run("task run of a challenge without an integration branch", func(t *testing.T) {
		t.Parallel()
		f := newLandFixture(t)
		start, kind := setPendingOnOpenRun(t, f.s, "p1")
		if kind != contractRunKindTask {
			t.Fatalf("kind = %s", kind)
		}
		if got := pendingRuns(t, f.s, f.cid); len(got) != 1 || got[0] != start+"=p1" {
			t.Fatalf("runs with fix_pending_ref = %v, want %s=p1", got, start)
		}
	})
	t.Run("reflect request of an artifact outside the delegate flow", func(t *testing.T) {
		t.Parallel()
		f := newReflFixture(t)
		f.started(t)
		start, kind := setPendingOnOpenRun(t, f.s, "p2")
		if kind != contractRunKindReflect {
			t.Fatalf("kind = %s", kind)
		}
		if got := pendingRuns(t, f.s, formatChallengeID(f.cid)); len(got) != 1 || got[0] != start+"=p2" {
			t.Fatalf("runs with fix_pending_ref = %v, want %s=p2", got, start)
		}
	})
	t.Run("up request", func(t *testing.T) {
		t.Parallel()
		f, id := newUpFixture(t, upConnectorsJSON())
		f.setUpPR("main")
		f.startUp(t)
		f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
		start, kind := setPendingOnOpenRun(t, f.s, "p3")
		if kind != contractRunKindReflect {
			t.Fatalf("kind = %s", kind)
		}
		if got := pendingRuns(t, f.s, id); len(got) != 1 || got[0] != start+"=p3" {
			t.Fatalf("runs with fix_pending_ref = %v, want %s=p3", got, start)
		}
	})
}

// 課題の詳細の runs は件数を限る（新しい順に showRunsLimit 件）。契約の実行を始めた run が一覧から外れても、一覧にある、その契約の実行の
// 契約のコマンドの run のうち最も新しいものに、確かめ待ちの中身が出る（反映の段が状態を読むたびに run が増えても消えない）。
func TestRunFixPending_FallsBackToTheNewestListedRunOfTheContractRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	start, _ := setPendingOnOpenRun(t, f.s, "p1")
	startID, _ := parseRunID(start)
	var newest int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT name FROM pragma_table_info('run') WHERE name <> 'id'`)
		if err != nil {
			return err
		}
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				return err
			}
			cols = append(cols, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		list := strings.Join(cols, ", ")
		for i := 0; i < showRunsLimit+3; i++ {
			res, err := tx.Exec(`INSERT INTO run (`+list+`) SELECT `+list+` FROM run WHERE id = ?`, startID)
			if err != nil {
				return err
			}
			newest, _ = res.LastInsertId()
		}
		return nil
	})
	if got := pendingRuns(t, f.s, f.cid); len(got) != 1 || got[0] != formatRunID(newest)+"=p1" {
		t.Fatalf("runs with fix_pending_ref = %v, want %s=p1 (the start run %s is no longer listed)", got, formatRunID(newest), start)
	}
	// 別のタスクの run・契約のコマンドでない run には載せない。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET task_id = NULL WHERE id = ?`, newest)
		return err
	})
	if got := pendingRuns(t, f.s, f.cid); len(got) != 1 || got[0] != formatRunID(newest-1)+"=p1" {
		t.Fatalf("runs with fix_pending_ref = %v, want %s=p1", got, formatRunID(newest-1))
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET kind = 'delegate' WHERE id = ?`, newest-1)
		return err
	})
	if got := pendingRuns(t, f.s, f.cid); len(got) != 1 || got[0] != formatRunID(newest-2)+"=p1" {
		t.Fatalf("runs with fix_pending_ref = %v, want %s=p1", got, formatRunID(newest-2))
	}
}
