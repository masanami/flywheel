package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
)

// 学びの判断点（J6）がフォローアップを followup の行として書く（#772。受入基準 AC-12〜AC-31）を、
// 実ストア・偽の判断の IF で検証する。

func j6Followups(n int) []any {
	var fu []any
	for i := 0; i < n; i++ {
		fu = append(fu, map[string]any{"title": "follow " + j6itoa(int64(i)), "description": "desc " + j6itoa(int64(i))})
	}
	return fu
}

// learnFollowups は課題 1 件を学びの対象にして、フォローアップ n 件を返す J6 を走らせる。
func learnFollowups(t *testing.T, n int) (f *j6Fixture, challenge string) {
	t.Helper()
	f = newJ6Fixture(t)
	challenge = f.challenge(t, StatusDone)
	o := f.occ(t, challenge, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, j6Followups(n))
	res := f.mustLearn(t, idp(challenge))
	if len(res.Items) != 1 || res.Items[0].Outcome != "learned" {
		t.Fatalf("items = %+v", res.Items)
	}
	return f, challenge
}

type followupRow struct {
	ID, Challenge, Title, Detail, Source, Status string
}

func (f *j6Fixture) followupRows(t *testing.T) []followupRow {
	t.Helper()
	var out []followupRow
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, challenge_id, title, COALESCE(detail, ''), source, status FROM followup ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, cid int64
			var r followupRow
			if err := rows.Scan(&id, &cid, &r.Title, &r.Detail, &r.Source, &r.Status); err != nil {
				return err
			}
			r.ID, r.Challenge = formatFollowupID(id), formatChallengeID(cid)
			out = append(out, r)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *j6Fixture) activityAfter(t *testing.T, q string, args ...any) map[string]any {
	t.Helper()
	var raw string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&raw) }); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// AC-12（#772）: 出力の型の followups の要素に done_criteria のキーが無い。
func TestJ6OutputSchema_FollowupHasNoDoneCriteria_AC12(t *testing.T) {
	t.Parallel()
	var sch map[string]any
	if err := json.Unmarshal(j6OutputSchema(), &sch); err != nil {
		t.Fatal(err)
	}
	item := sch["properties"].(map[string]any)["followups"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	if _, ok := props["done_criteria"]; ok {
		t.Error("followups items still have done_criteria")
	}
	if _, ok := props["title"]; !ok {
		t.Error("followups items lack title")
	}
	if _, ok := props["description"]; !ok {
		t.Error("followups items lack description")
	}
}

// AC-13（#772）: followups を 1 件返すと followup の行が 1 つ増える。
func TestLearnJ6_OneFollowupAddsOneRow_AC13(t *testing.T) {
	t.Parallel()
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusDone)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, j6Followups(1))
	before := f.count(t, `SELECT COUNT(*) FROM followup`)
	f.mustLearn(t, idp(c))
	if got := f.count(t, `SELECT COUNT(*) FROM followup`); got != before+1 {
		t.Errorf("followup rows %d → %d, want +1", before, got)
	}
}

// AC-14（#772）: followups を 1 件返しても challenge の表の行数は変わらない（保留も作らない）。
func TestLearnJ6_FollowupDoesNotAddChallenge_AC14(t *testing.T) {
	t.Parallel()
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusDone)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, j6Followups(1))
	before, holds := f.count(t, `SELECT COUNT(*) FROM challenge`), f.count(t, `SELECT COUNT(*) FROM hold`)
	f.mustLearn(t, idp(c))
	if got := f.count(t, `SELECT COUNT(*) FROM challenge`); got != before {
		t.Errorf("challenge rows %d → %d, want unchanged", before, got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != holds {
		t.Errorf("hold rows %d → %d, want unchanged", holds, got)
	}
}

// AC-15（#772）: 書いた followup の行の challenge_id は学びの対象の課題。
func TestLearnJ6_FollowupHangsFromTheLearnedChallenge_AC15(t *testing.T) {
	t.Parallel()
	f, c := learnFollowups(t, 1)
	if rows := f.followupRows(t); len(rows) != 1 || rows[0].Challenge != c {
		t.Errorf("rows = %+v, want challenge %s", rows, c)
	}
}

// AC-16（#772）: 書いた followup の行の status は open。
func TestLearnJ6_FollowupStatusIsOpen_AC16(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	if rows := f.followupRows(t); len(rows) != 1 || rows[0].Status != "open" {
		t.Errorf("rows = %+v", rows)
	}
}

// AC-17（#772）: 書いた followup の行の source は learn。
func TestLearnJ6_FollowupSourceIsLearn_AC17(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	if rows := f.followupRows(t); len(rows) != 1 || rows[0].Source != "learn" {
		t.Errorf("rows = %+v", rows)
	}
}

// AC-18（#772）: 書いた followup の行の title は出力の title と同じ。
func TestLearnJ6_FollowupTitleIsTheOutputTitle_AC18(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	if rows := f.followupRows(t); len(rows) != 1 || rows[0].Title != "follow 0" {
		t.Errorf("rows = %+v", rows)
	}
}

// AC-19（#772）: 書いた followup の行の detail は出力の description と同じ。
func TestLearnJ6_FollowupDetailIsTheOutputDescription_AC19(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	if rows := f.followupRows(t); len(rows) != 1 || rows[0].Detail != "desc 0" {
		t.Errorf("rows = %+v", rows)
	}
}

func learnInvalidFollowups(t *testing.T, fu []any) {
	t.Helper()
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusDone)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, []any{j6Mem("create", "tacit", "m")}, fu)
	res := f.mustLearn(t, idp(c))
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v, want invalid_output", res.Items)
	}
	if f.count(t, `SELECT COUNT(*) FROM followup`) != 0 || f.count(t, `SELECT COUNT(*) FROM challenge`) != 1 || len(f.memFiles(t)) != 0 ||
		f.unclassified(t) != 1 || f.learnActivities(t) != 0 || f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'followup'`) != 0 {
		t.Error("something was written for an invalid output")
	}
}

// AC-20（#772）: title が改行を含む出力は不正な出力で、followup の行は増えない。
func TestLearnJ6_NewlineTitleIsInvalidOutput_AC20(t *testing.T) {
	t.Parallel()
	learnInvalidFollowups(t, []any{map[string]any{"title": "a\nb", "description": "d"}})
}

// AC-21（#772）: title が空の文字列の出力は不正な出力で、followup の行は増えない。
func TestLearnJ6_EmptyTitleIsInvalidOutput_AC21(t *testing.T) {
	t.Parallel()
	learnInvalidFollowups(t, []any{map[string]any{"title": "", "description": "d"}})
}

// AC-22（#772）: followups が 4 件ある出力は不正な出力（見直し前と同じ）。
func TestLearnJ6_FourFollowupsIsInvalidOutput_AC22(t *testing.T) {
	t.Parallel()
	learnInvalidFollowups(t, j6Followups(4))
}

// AC-23（#772）: フォローアップを書くと、entity=followup・action=create の作業ログが 1 件増える。
func TestLearnJ6_FollowupWritesOneCreateActivity_AC23(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	if got := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'followup' AND action = 'create'`); got != 1 {
		t.Errorf("create activities = %d, want 1", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'followup'`); got != 0 {
		t.Errorf("legacy followup activities = %d, want 0", got)
	}
}

// AC-24（#772）: その記録の entity_id は F-<番号> の形（表示の形に写る内部の整数が followup の行の ID と一致する）。
func TestLearnJ6_CreateActivityEntityIDIsAFollowupID_AC24(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 1)
	rows := f.followupRows(t)
	var entityID int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT entity_id FROM activity WHERE entity = 'followup' AND action = 'create'`).Scan(&entityID)
	}); err != nil {
		t.Fatal(err)
	}
	if got := formatFollowupID(entityID); !regexp.MustCompile(`^F-[1-9][0-9]*$`).MatchString(got) || len(rows) != 1 || got != rows[0].ID {
		t.Errorf("entity_id = %d (%s), rows = %+v", entityID, got, rows)
	}
}

// AC-25（#772）: 学びの記録（action=learn）の after.followups は、書いたフォローアップの ID（F-<番号>）の配列。
func TestLearnJ6_LearnActivityListsFollowupIDs_AC25(t *testing.T) {
	t.Parallel()
	f, _ := learnFollowups(t, 3)
	after := f.activityAfter(t, `SELECT after FROM activity WHERE action = 'learn'`)
	got, _ := after["followups"].([]any)
	rows := f.followupRows(t)
	if len(rows) != 3 || len(got) != 3 {
		t.Fatalf("rows = %+v, after = %v", rows, after)
	}
	for i, r := range rows {
		if got[i] != r.ID {
			t.Errorf("after.followups = %v, want ids of %+v", got, rows)
		}
	}
}

// 書き込みは J6 の写像と同じトランザクション。後の失敗で followup の行・作業ログ・記憶が残らない。
func TestLearnJ6_Followups_AtomicWithMapping_772(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		fail func(row activityRow, creates int) bool
	}{
		{"second followup activity fails", func(row activityRow, n int) bool {
			return row.Entity == "followup" && row.Action == "create" && n >= 2
		}},
		{"learn activity fails after writing", func(row activityRow, _ int) bool { return row.Action == ActivityActionLearn }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJ6Fixture(t)
			c := f.challenge(t, StatusDone)
			o := f.occ(t, c, "plan_rejected", "bad", nil)
			f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, []any{j6Mem("create", "tacit", "m")}, j6Followups(2))
			boom := errors.New("injected activity failure")
			seen := 0
			f.s.insertActivity = func(tx *sql.Tx, row activityRow) error {
				if row.Entity == "followup" && row.Action == "create" {
					seen++
				}
				if tc.fail(row, seen) {
					return boom
				}
				return defaultInsertActivity(tx, row)
			}
			occBefore := f.occurrences(t)
			if _, err := f.learn(t, idp(c)); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if f.count(t, `SELECT COUNT(*) FROM followup`) != 0 || f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'followup' OR action = 'learn'`) != 0 ||
				len(f.memFiles(t)) != 0 || f.unclassified(t) != 1 || f.occurrences(t) != occBefore {
				t.Error("partial write remained")
			}
		})
	}
}

// AC-31（#772）: 改修提案の判断点（J8）が提案を 1 件返すと、origin が improvement の課題が 1 つ増える（変えていない）。
func TestImproveJ8_OneProposalFilesOneImprovementChallenge_AC31(t *testing.T) {
	t.Parallel()
	f := newJ8Fixture(t)
	p := f.occ(t, 2, j8Row{})
	f.out([]any{j8Proposal("prompt", "T", formatOccurrenceID(p[0]), formatOccurrenceID(p[1]))})
	before := f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'improvement'`)
	res := f.manual(t)
	if len(res.Filed) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'improvement'`); got != before+1 {
		t.Errorf("improvement challenges %d → %d, want +1", before, got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM followup`); got != 0 {
		t.Errorf("followup rows = %d, want 0", got)
	}
}
