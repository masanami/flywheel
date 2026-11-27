package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// 下書きづくりの保留への回答（docs/features/request-to-plan-s2b.md の「保留への答え方」）を実ストアで確かめる。
// テストの名前の AC の番号は、Issue #1354 の受け持ち（親 #1339 の受入基準の並び順）。

// saFixture は理由 reason の下書きづくりの保留を持つ人間対応待ちの課題を作る。
func saFixture(t *testing.T, reason SpecDraftHoldReason, attempts int) (*j2Fixture, string, Hold) {
	t.Helper()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, attempts)
	h := specDraftHold{Reason: reason, Questions: shQuestions()}
	if reason == SpecDraftHoldFailed {
		h = specDraftHold{Reason: reason, Detail: "失敗した"}
	}
	hold, _ := shRaise(t, f, runID, id, h)
	return f, id, hold
}

// saAnswer は回答の確かめと回答を続けて行う（CLI と同じ順）。確かめの誤りは、そのまま返す。
func saAnswer(t *testing.T, f *j2Fixture, id, option string, items ...PlanAnswerItem) (*Challenge, error) {
	t.Helper()
	ctx := context.Background()
	prev, err := f.s.PrepareAnswerWithItems(ctx, id, "", "", option, false, items)
	if err != nil {
		return nil, err
	}
	c, _, err := f.s.ExecuteAnswer(ctx, AnswerRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Option: option, Items: items,
		HoldID: prev.HoldID, HoldQuestion: prev.Question, HoldOptions: prev.Options, HoldOptionRequired: prev.OptionRequired,
	}, verifiedAttestationForTest(t, id))
	return c, err
}

// saExecute は回答の確かめを通さずに回答する（書き込みの側の検査を確かめる）。
func saExecute(t *testing.T, f *j2Fixture, id, option string, items ...PlanAnswerItem) error {
	t.Helper()
	_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: id, ExpectedVersion: f.detail(t, id).Version, Option: option, Items: items,
	}, verifiedAttestationForTest(t, id))
	return err
}

func saChoice(q, c int64) PlanAnswerItem { return PlanAnswerItem{Question: q, Choice: &c} }

func saText(q int64, s string) PlanAnswerItem { return PlanAnswerItem{Question: q, Text: &s} }

func saQueryRow(t *testing.T, s *Store, query string, dest ...any) {
	t.Helper()
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query).Scan(dest...) }); err != nil {
		t.Fatal(err)
	}
}

func saAnswerTexts(d *ChallengeDetail) []string {
	var out []string
	for _, a := range d.SpecDraft.Answers {
		out = append(out, a.Answer)
	}
	return out
}

// saRequireUntouched は、保留が開いたまま、記録が回答待ちのままであることを確かめる。
func saRequireUntouched(t *testing.T, f *j2Fixture, id string, hold Hold) {
	t.Helper()
	d := f.detail(t, id)
	if d.Status != StatusAwaitingHuman || d.SpecDraft.State != SpecDraftStateAwaitingOwner || d.SpecDraft.Answers != nil ||
		d.SpecDraft.HoldID == nil || *d.SpecDraft.HoldID != hold.HoldID || d.SpecPath != nil {
		t.Errorf("changed: status = %s, record = %+v, spec_path = %v", d.Status, d.SpecDraft, d.SpecPath)
	}
}

// saLogs は課題の作業ログのうち action のものの after を返す。
func saLogs(t *testing.T, s *Store, id, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range activitiesFor(t, s, id) {
		if a.Action != action {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(a.After, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func saWriteDraft(t *testing.T, f *j2Fixture, path, body string) string {
	t.Helper()
	full := filepath.Join(f.s.workspace, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

const (
	saDraftWithCriterion    = "# 下書き\n\n## 受入基準\n- [ ] 取り消せる\n"
	saDraftWithoutCriterion = "# 下書き\n\n## 受入基準\n"
)

func TestSpecDraftAnswer_Answer(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		items []PlanAnswerItem
		want  []string
	}{
		"AC-303 全部が推奨の見出し":            {nil, []string{"実行中にも効かせる", "残す"}},
		"AC-304・AC-305 Q1 を選び、Q2 は推奨": {[]PlanAnswerItem{saChoice(1, 2)}, []string{"待ちの処理だけ", "残す"}},
		"AC-306 Q1 は文":                {[]PlanAnswerItem{saText(1, "文の答え")}, []string{"文の答え", "残す"}},
		"渡した順によらず問いの順に記録し、推奨と同じ番号も選べる": {[]PlanAnswerItem{saChoice(2, 3), saChoice(1, 1)}, []string{"実行中にも効かせる", "選べるようにする"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, hold := saFixture(t, SpecDraftHoldQuestion, 1)
			ch, err := saAnswer(t, f, id, "answer", c.items...)
			if err != nil {
				t.Fatal(err)
			}
			d := f.detail(t, id)
			if got := saAnswerTexts(d); !reflect.DeepEqual(got, c.want) {
				t.Errorf("answers = %q, want %q", got, c.want)
			}
			for i, a := range d.SpecDraft.Answers { // AC-308・AC-331・AC-332
				if a.By != "owner" || a.Basis == nil || len(a.Basis) != 0 || a.Attempt != 1 || a.Question != shQuestions()[i].Question {
					t.Errorf("answer %d = %+v", i, a)
				}
			}
			if d.SpecDraft.State != SpecDraftStatePending { // AC-309
				t.Errorf("state = %s", d.SpecDraft.State)
			}
			if ch.Status != StatusClassified || d.Status != StatusClassified { // AC-310
				t.Errorf("status = %s / %s", ch.Status, d.Status)
			}
			var holdID, holdReason sql.NullString
			saQueryRow(t, f.s, `SELECT CAST(hold_id AS TEXT), hold_reason FROM spec_draft`, &holdID, &holdReason)
			if holdID.Valid || holdReason.Valid {
				t.Errorf("hold_id = %v, hold_reason = %v", holdID, holdReason)
			}
			if d.SpecDraft.Attempts != 1 || !reflect.DeepEqual(d.SpecDraft.Questions, shQuestions()) {
				t.Errorf("record = %+v", d.SpecDraft)
			}
			for _, h := range d.Holds {
				if h.HoldID == hold.HoldID && (h.Open() || h.AnswerOption == nil || *h.AnswerOption != "answer") {
					t.Errorf("hold = %+v", h)
				}
			}
		})
	}
}

func TestSpecDraftAnswer_AnswersAreAppendOnly(t *testing.T) { // AC-312・AC-332
	t.Parallel()
	f, id, _ := saFixture(t, SpecDraftHoldQuestion, 1)
	if _, err := saAnswer(t, f, id, "answer", saText(1, "1 回目")); err != nil {
		t.Fatal(err)
	}
	first := f.detail(t, id).SpecDraft.Answers
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: SpecDraftStateRunning, Attempts: 2, Answers: first}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	var runRow int64
	saQueryRow(t, f.s, `SELECT MAX(id) FROM run`, &runRow)
	second := []SpecDraftQuestion{{Question: "2 回目の問い", Options: []SpecDraftOption{{Label: "x"}, {Label: "y"}}, Recommended: 2, Reason: "r"}}
	shRaise(t, f, formatRunID(runRow), id, specDraftHold{Reason: SpecDraftHoldQuestion, Questions: second})
	if _, err := saAnswer(t, f, id, "answer"); err != nil {
		t.Fatal(err)
	}
	got := f.detail(t, id).SpecDraft.Answers
	want := append(first, SpecDraftAnswer{Kind: SpecDraftAnswerKindAnswer, Attempt: 2, Question: "2 回目の問い", Answer: "y", By: "owner", Basis: []SpecDraftBasis{}})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("answers = %+v, want %+v", got, want)
	}
}

func TestSpecDraftAnswer_InvalidItems(t *testing.T) {
	t.Parallel()
	both := saChoice(1, 1)
	both.Text = saText(1, "x").Text
	for name, c := range map[string]struct {
		option string
		items  []PlanAnswerItem
	}{
		"AC-313・AC-314 無い問いの番号": {"answer", []PlanAnswerItem{saChoice(9, 1)}},
		"問いの番号 0":               {"answer", []PlanAnswerItem{saChoice(0, 1)}},
		"AC-315 無い選択肢の番号":       {"answer", []PlanAnswerItem{saChoice(1, 9)}},
		"選択肢の番号 0":              {"answer", []PlanAnswerItem{saChoice(1, 0)}},
		"AC-316 同じ問いへの 2 つの答え":  {"answer", []PlanAnswerItem{saChoice(1, 1), saChoice(1, 2)}},
		"番号と文の併用":               {"answer", []PlanAnswerItem{both}},
		"番号も文も無い":               {"answer", []PlanAnswerItem{{Question: 1}}},
		"空の文":                   {"answer", []PlanAnswerItem{saText(1, " \n")}},
		"AC-317 proceed との併用":   {"proceed", []PlanAnswerItem{saChoice(1, 2)}},
		"skip との併用":             {"skip", []PlanAnswerItem{saChoice(1, 2)}},
		"AC-328 選択肢に無い識別":       {"retry", nil},
		"AC-327 識別なし":           {"", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, hold := saFixture(t, SpecDraftHoldQuestion, 1)
			saWriteDraft(t, f, f.detail(t, id).SpecDraft.Path, saDraftWithCriterion)
			_, err := f.s.PrepareAnswerWithItems(context.Background(), id, "", "答えの文", c.option, false, c.items)
			if !errors.Is(err, ErrValidation) || errors.Is(err, ErrSpecAnswersNotAccepted) {
				t.Errorf("prepare = %v, want a plain ErrValidation", err)
			}
			_, _, err = f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: f.detail(t, id).Version,
				Answer: "答えの文", Option: c.option, Items: c.items}, verifiedAttestationForTest(t, id))
			if !errors.Is(err, ErrValidation) {
				t.Errorf("execute = %v, want ErrValidation", err)
			}
			saRequireUntouched(t, f, id, hold)
		})
	}
}

// 問いごとの答えは、問いの保留でなければ受けない。その誤りは検査の誤りで、問いの保留への誤った答えと見分けられる。
func TestSpecDraftAnswer_ItemsNeedAQuestionHold(t *testing.T) {
	t.Parallel()
	items := []PlanAnswerItem{saChoice(1, 2)}
	require := func(t *testing.T, f *j2Fixture, id, option string) {
		t.Helper()
		before := f.detail(t, id)
		_, err := f.s.PrepareAnswerWithItems(context.Background(), id, "", "", option, false, items)
		if !errors.Is(err, ErrSpecAnswersNotAccepted) {
			t.Errorf("prepare = %v, want ErrSpecAnswersNotAccepted", err)
		}
		if err := saExecute(t, f, id, option, items...); !errors.Is(err, ErrValidation) {
			t.Errorf("execute = %v, want ErrValidation", err)
		}
		if after := f.detail(t, id); after.Version != before.Version || !reflect.DeepEqual(after.Holds, before.Holds) {
			t.Errorf("changed: %+v", after.Holds)
		}
	}
	t.Run("AC-318 失敗の保留", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldFailed, 1)
		require(t, f, id, "retry")
	})
	t.Run("AC-319 回数の上限の保留（下書きが読めなくても同じ）", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldAttemptLimit, 3)
		require(t, f, id, "proceed")
	})
	t.Run("AC-320 下書きづくりでない保留", func(t *testing.T) {
		t.Parallel()
		f := newJ2Fixture(t)
		id := f.newClassified(t, "plain hold", "P1").ID
		if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "どうする？"}); err != nil {
			t.Fatal(err)
		}
		require(t, f, id, "a")
	})
	t.Run("保留の無い課題", func(t *testing.T) {
		t.Parallel()
		f := newJ2Fixture(t)
		id := f.newClassified(t, "no hold", "P1").ID
		_, err := f.s.PrepareAnswerWithItems(context.Background(), id, "", "", "answer", false, items)
		if !errors.Is(err, ErrSpecAnswersNotAccepted) {
			t.Errorf("prepare = %v, want ErrSpecAnswersNotAccepted", err)
		}
	})
}

func TestSpecDraftAnswer_PreviewReportsTheReasonAndQuestions(t *testing.T) {
	t.Parallel()
	t.Run("AC-325 問いの保留", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldQuestion, 1)
		p, err := f.s.PrepareAnswerOption(context.Background(), id, "", "answer", false)
		if err != nil {
			t.Fatal(err)
		}
		if p.SpecHoldReason != SpecDraftHoldQuestion || !reflect.DeepEqual(p.SpecQuestions, shQuestions()) {
			t.Errorf("reason = %q, questions = %+v", p.SpecHoldReason, p.SpecQuestions)
		}
		if got := len(p.SpecAnswers); got != 2 || p.SpecAnswers[1].Answer != "残す" {
			t.Errorf("answers to record = %+v", p.SpecAnswers)
		}
	})
	t.Run("AC-326 下書きづくりでない保留は理由が空", func(t *testing.T) {
		t.Parallel()
		f := newJ2Fixture(t)
		id := f.newClassified(t, "plain hold", "P1").ID
		if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "どうする？"}); err != nil {
			t.Fatal(err)
		}
		p, err := f.s.PrepareAnswer(context.Background(), id, "a")
		if err != nil {
			t.Fatal(err)
		}
		if p.SpecHoldReason != "" || p.SpecQuestions != nil || p.SpecAnswers != nil {
			t.Errorf("preview = %+v", p)
		}
	})
}

func TestSpecDraftAnswer_Proceed(t *testing.T) {
	t.Parallel()
	for name, base := range map[string]*string{"置き換える元が無い": nil, "AC-336 置き換える元がある": ptrTo("docs/features/old.md")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, _ := saFixture(t, SpecDraftHoldQuestion, 1)
			if base != nil {
				if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE spec_draft SET base_path = ?`, *base)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			path := f.detail(t, id).SpecDraft.Path
			saWriteDraft(t, f, path, saDraftWithCriterion)
			ch, err := saAnswer(t, f, id, "proceed")
			if err != nil {
				t.Fatal(err)
			}
			d := f.detail(t, id)
			if d.SpecDraft.State != SpecDraftStateDone || d.SpecDraft.HoldID != nil || d.SpecDraft.Answers != nil { // AC-334
				t.Errorf("record = %+v", d.SpecDraft)
			}
			if d.SpecPath == nil || *d.SpecPath != path || ch.SpecPath == nil || *ch.SpecPath != path { // AC-335
				t.Errorf("spec_path = %v / %v, want %s", d.SpecPath, ch.SpecPath, path)
			}
			if !reflect.DeepEqual(d.SpecBasePath, base) || !reflect.DeepEqual(ch.SpecBasePath, base) {
				t.Errorf("spec_base_path = %v / %v, want %v", d.SpecBasePath, ch.SpecBasePath, base)
			}
			if d.Status != StatusClassified { // AC-337
				t.Errorf("status = %s", d.Status)
			}
			logs := saLogs(t, f.s, id, "spec_draft_done") // AC-338
			if len(logs) != 1 || logs[0]["detail"] != "owner" {
				t.Errorf("spec_draft_done = %v", logs)
			}
		})
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestSpecDraftAnswer_ProceedNeedsAReadableDraftWithCriteria(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]*string{
		"AC-339・AC-340 下書きが無い":    nil,
		"AC-341・AC-342 受入基準が 0 件": ptrTo(saDraftWithoutCriterion),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, hold := saFixture(t, SpecDraftHoldQuestion, 1)
			if body != nil {
				saWriteDraft(t, f, f.detail(t, id).SpecDraft.Path, *body)
			}
			if _, err := f.s.PrepareAnswerOption(context.Background(), id, "", "proceed", false); !errors.Is(err, ErrValidation) {
				t.Errorf("prepare = %v, want ErrValidation", err)
			}
			// 確かめを通らない回答（画面のボタン）も、書き込みの側で断る。
			if err := saExecute(t, f, id, "proceed"); !errors.Is(err, ErrValidation) {
				t.Errorf("execute = %v, want ErrValidation", err)
			}
			saRequireUntouched(t, f, id, hold)
		})
	}
	t.Run("AC-343 保留の後に基準を足せば成立する", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldAttemptLimit, 3)
		path := f.detail(t, id).SpecDraft.Path
		saWriteDraft(t, f, path, saDraftWithoutCriterion)
		if _, err := saAnswer(t, f, id, "proceed"); !errors.Is(err, ErrValidation) {
			t.Fatalf("before = %v", err)
		}
		saWriteDraft(t, f, path, saDraftWithCriterion)
		if _, err := saAnswer(t, f, id, "proceed"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSpecDraftAnswer_Retry(t *testing.T) { // AC-344・AC-345
	t.Parallel()
	f, id, _ := saFixture(t, SpecDraftHoldFailed, 1)
	if _, err := saAnswer(t, f, id, "retry"); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, id)
	if d.SpecDraft.State != SpecDraftStatePending || d.SpecDraft.Attempts != 1 || d.SpecDraft.HoldID != nil || d.Status != StatusClassified {
		t.Errorf("status = %s, record = %+v", d.Status, d.SpecDraft)
	}
}

func TestSpecDraftAnswer_Skip(t *testing.T) {
	t.Parallel()
	t.Run("AC-346〜AC-349 省いて下書きを消す", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldQuestion, 1)
		full := saWriteDraft(t, f, f.detail(t, id).SpecDraft.Path, saDraftWithCriterion)
		if _, err := saAnswer(t, f, id, "skip"); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, id)
		if d.SpecDraft.State != SpecDraftStateSkipped || d.SpecDraft.HoldID != nil || d.Status != StatusClassified {
			t.Errorf("status = %s, record = %+v", d.Status, d.SpecDraft)
		}
		if _, err := os.Stat(full); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("draft remains: %v", err)
		}
		if d.SpecPath != nil {
			t.Errorf("spec_path = %v", *d.SpecPath)
		}
		logs := saLogs(t, f.s, id, "spec_draft_skipped")
		if len(logs) != 1 || logs[0]["detail"] != "answer" {
			t.Errorf("spec_draft_skipped = %v", logs)
		}
		if n := len(scFailures(t, f.s, id)); n != 0 {
			t.Errorf("cleanup failures = %d", n)
		}
	})
	t.Run("AC-350・AC-351 消せなくても成立する", func(t *testing.T) {
		t.Parallel()
		f, id, _ := saFixture(t, SpecDraftHoldFailed, 1)
		path := f.detail(t, id).SpecDraft.Path
		saWriteDraft(t, f, path+"/child", "x")
		if _, err := saAnswer(t, f, id, "skip"); err != nil {
			t.Fatal(err)
		}
		if got := f.detail(t, id).SpecDraft.State; got != SpecDraftStateSkipped {
			t.Errorf("state = %s", got)
		}
		if n := len(scFailures(t, f.s, id)); n != 1 {
			t.Errorf("cleanup failures = %d, want 1", n)
		}
	})
}

func TestSpecDraftAnswer_FromTheScreen(t *testing.T) {
	t.Parallel()
	// 経路 ui の回答は選択肢だけで、問いごとの答えを持たない。
	f, id, hold := saFixture(t, SpecDraftHoldQuestion, 1)
	err := func() error {
		_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, Option: "proceed",
			HoldID: hold.HoldID, HoldCheck: hold.Check}, uiAttestationForTest(t, id))
		return err
	}()
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("proceed without a draft from the screen = %v, want ErrValidation", err)
	}
	saRequireUntouched(t, f, id, hold)
}
