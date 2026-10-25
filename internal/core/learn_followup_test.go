package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// S3 フォローアップの起票（AC-280〜AC-285）を実ストア・偽の判断の IF で検証する。

func j6Followups(n int) []any {
	var fu []any
	for i := 0; i < n; i++ {
		fu = append(fu, map[string]any{"title": "follow " + j6itoa(int64(i)), "description": "d", "done_criteria": "c"})
	}
	return fu
}

func TestLearnJ6_Followups_FiledAsUnclassified_AC280_281_283_284(t *testing.T) {
	for _, n := range []int{1, 3} {
		t.Run(j6itoa(int64(n)), func(t *testing.T) {
			f := newJ6Fixture(t)
			c := f.challenge(t, StatusDone)
			o := f.occ(t, c, "plan_rejected", "bad", nil)
			f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, j6Followups(n))
			holdsBefore := f.count(t, `SELECT COUNT(*) FROM hold`)
			challengesBefore := f.count(t, `SELECT COUNT(*) FROM challenge`)

			res := f.mustLearn(t, idp(c))
			if len(res.Items) != 1 || res.Items[0].Outcome != "learned" {
				t.Fatalf("items = %+v", res.Items)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM challenge`); got != challengesBefore+n {
				t.Fatalf("challenges %d → %d, want +%d", challengesBefore, got, n)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != holdsBefore {
				t.Errorf("holds %d → %d", holdsBefore, got)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'followup' AND followup_of = ? AND status = 'unclassified'`, c); got != n {
				t.Errorf("followup challenges = %d, want %d", got, n)
			}
			var ids []string
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				rows, err := tx.Query(`SELECT id FROM challenge WHERE origin = 'followup' ORDER BY id`)
				if err != nil {
					return err
				}
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var id int64
					if err := rows.Scan(&id); err != nil {
						return err
					}
					ids = append(ids, formatChallengeID(id))
				}
				return rows.Err()
			}); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				if f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'followup'`, mustCID(t, id)) != 1 {
					t.Errorf("%s: want exactly one followup activity", id)
				}
				var rawAct string
				if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
					return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'followup'`, mustCID(t, id)).Scan(&rawAct)
				}); err != nil {
					t.Fatal(err)
				}
				var aa map[string]any
				if err := json.Unmarshal([]byte(rawAct), &aa); err != nil || aa["origin"] != "followup" || aa["followup_of"] != c {
					t.Errorf("%s: followup activity after = %s", id, rawAct)
				}
			}
			var raw string
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT after FROM activity WHERE action = 'learn'`).Scan(&raw)
			}); err != nil {
				t.Fatal(err)
			}
			var a struct {
				Followups []string `json:"followups"`
			}
			if err := json.Unmarshal([]byte(raw), &a); err != nil {
				t.Fatal(err)
			}
			if len(a.Followups) != n {
				t.Fatalf("learn after = %s", raw)
			}
			for i, id := range ids {
				if a.Followups[i] != id {
					t.Errorf("learn after followups = %v, want %v", a.Followups, ids)
				}
			}
			// AC-285: 分類の段（J1）の対象になる。
			var targets []int64
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				var err error
				targets, err = selectJ1AutoTargets(context.Background(), tx)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				n, _ := parseChallengeID(id)
				found := false
				for _, tg := range targets {
					found = found || tg == n
				}
				if !found {
					t.Errorf("%s is not a J1 target (targets %v)", id, targets)
				}
			}
		})
	}
}

func TestLearnJ6_Followups_FourOrMore_InvalidOutputWritesNothing_AC282(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusDone)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, []any{j6Mem("create", "tacit", "m")}, j6Followups(4))
	challengesBefore := f.count(t, `SELECT COUNT(*) FROM challenge`)
	res := f.mustLearn(t, idp(c))
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v", res.Items)
	}
	if f.count(t, `SELECT COUNT(*) FROM challenge`) != challengesBefore || len(f.memFiles(t)) != 0 ||
		f.unclassified(t) != 1 || f.learnActivities(t) != 0 || f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'followup'`) != 0 {
		t.Error("something was written for a 4-followup output")
	}
}

// 起票は J6 の写像と同じトランザクション。起票の後の失敗で、起票した課題・作業ログ・記憶が残らない。
func TestLearnJ6_Followups_AtomicWithMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(row activityRow, followups int) bool
	}{
		{"second followup activity fails", func(row activityRow, n int) bool {
			return row.Action == ActivityActionFollowup && n >= 2
		}},
		{"learn activity fails after filing", func(row activityRow, _ int) bool { return row.Action == ActivityActionLearn }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJ6Fixture(t)
			c := f.challenge(t, StatusDone)
			o := f.occ(t, c, "plan_rejected", "bad", nil)
			f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, []any{j6Mem("create", "tacit", "m")}, j6Followups(2))
			boom := errors.New("injected activity failure")
			seen := 0
			f.s.insertActivity = func(tx *sql.Tx, row activityRow) error {
				if row.Action == ActivityActionFollowup {
					seen++
				}
				if tc.fail(row, seen) {
					return boom
				}
				return defaultInsertActivity(tx, row)
			}
			challengesBefore := f.count(t, `SELECT COUNT(*) FROM challenge`)
			occBefore := f.occurrences(t)
			if _, err := f.learn(t, idp(c)); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if f.count(t, `SELECT COUNT(*) FROM challenge`) != challengesBefore || f.count(t, `SELECT COUNT(*) FROM activity WHERE action IN ('followup','learn')`) != 0 ||
				len(f.memFiles(t)) != 0 || f.unclassified(t) != 1 || f.occurrences(t) != occBefore {
				t.Error("partial write remained")
			}
		})
	}
}
