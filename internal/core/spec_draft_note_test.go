package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 下書きづくりの保留への回答の文・添え書き・答えの要素の kind・文面のファイル（docs/features/request-to-plan-s2b.md の
// 「保留への回答と添え書き」「文面のファイル」）を実ストアで確かめる。テストの名前の AC の番号は、Issue #1377 の受け持ち。

// snQuestions は問い 3 件（どれも推奨は 1 番目の選択肢）。
func snQuestions() []SpecDraftQuestion {
	var qs []SpecDraftQuestion
	for _, q := range []string{"問い 1", "問い 2", "問い 3"} {
		qs = append(qs, SpecDraftQuestion{Question: q, Options: []SpecDraftOption{{Label: q + " の案 1"}, {Label: q + " の案 2"}},
			Recommended: 1, Reason: "理由"})
	}
	return qs
}

// snFixture は理由 reason の下書きづくりの保留を持つ、実行 attempts 回の人間対応待ちの課題を作る。
func snFixture(t *testing.T, reason SpecDraftHoldReason, attempts int) (*j2Fixture, string, Hold) {
	t.Helper()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, attempts)
	h := specDraftHold{Reason: reason, Questions: snQuestions()}
	if reason == SpecDraftHoldFailed {
		h = specDraftHold{Reason: reason, Detail: "失敗した"}
	}
	hold, _ := shRaise(t, f, runID, id, h)
	return f, id, hold
}

// snRun は回答の確かめと回答を続けて行い、確かめの結果と閉じた保留を返す。
func snRun(t *testing.T, f *j2Fixture, id, option, answer string, items ...PlanAnswerItem) (*AnswerPreview, *Hold, error) {
	t.Helper()
	ctx := context.Background()
	prev, err := f.s.PrepareAnswerWithItems(ctx, id, "", answer, option, false, items)
	if err != nil {
		return nil, nil, err
	}
	_, hold, err := f.s.ExecuteAnswer(ctx, AnswerRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Answer: answer, Option: option, Items: items,
		HoldID: prev.HoldID, HoldQuestion: prev.Question, HoldOptions: prev.Options, HoldOptionRequired: prev.OptionRequired,
	}, verifiedAttestationForTest(t, id))
	return prev, hold, err
}

// snAnswered は snRun が成立することを確かめ、保留の回答の文と、答えた後の詳細を返す。
func snAnswered(t *testing.T, f *j2Fixture, id, option, answer string, items ...PlanAnswerItem) (string, *ChallengeDetail) {
	t.Helper()
	_, hold, err := snRun(t, f, id, option, answer, items...)
	if err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, id)
	for _, h := range d.Holds {
		if h.HoldID == hold.HoldID {
			if h.Answer == nil || *h.Answer != *hold.Answer {
				t.Fatalf("recorded hold answer = %v, returned %q", h.Answer, *hold.Answer)
			}
			return *h.Answer, d
		}
	}
	t.Fatalf("hold %s not found", hold.HoldID)
	return "", nil
}

func snNotes(d *ChallengeDetail) []SpecDraftAnswer {
	var out []SpecDraftAnswer
	for _, a := range d.SpecDraft.Answers {
		if a.Kind == SpecDraftAnswerKindNote {
			out = append(out, a)
		}
	}
	return out
}

func TestSpecDraftHoldAnswerText(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		items  []PlanAnswerItem
		answer string
		want   string
	}{
		"AC-187 推奨のまま":     {nil, "", "推奨のまま進める"},
		"AC-188 1 件が推奨と違う": {[]PlanAnswerItem{saChoice(1, 2)}, "", "問いに答えた（推奨と違う答え: Q1）"},
		"AC-189 2 件は番号の昇順": {[]PlanAnswerItem{saChoice(3, 2), saChoice(1, 2)}, "", "問いに答えた（推奨と違う答え: Q1・Q3）"},
		"AC-190 推奨を番号で選ぶ":  {[]PlanAnswerItem{saChoice(1, 1)}, "", "推奨のまま進める"},
		"AC-191 文の答え":      {[]PlanAnswerItem{saText(2, "文の答え")}, "", "問いに答えた（推奨と違う答え: Q2）"},
		"推奨の見出しと同じ文を渡しても、推奨と違う答え":      {[]PlanAnswerItem{saText(1, "問い 1 の案 1")}, "", "問いに答えた（推奨と違う答え: Q1）"},
		"AC-202 添え書きは改行の後":             {nil, "短くする", "推奨のまま進める\n短くする"},
		"AC-203・AC-204 --choice と添え書き": {[]PlanAnswerItem{saChoice(1, 2)}, "短くする", "問いに答えた（推奨と違う答え: Q1）\n短くする"},
		"AC-205 --text と添え書き":          {[]PlanAnswerItem{saText(1, "文")}, "短くする", "問いに答えた（推奨と違う答え: Q1）\n短くする"},
		"AC-216 空白だけの添え書きは続けない":        {nil, "   ", "推奨のまま進める"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
			if got, _ := snAnswered(t, f, id, "answer", c.answer, c.items...); got != c.want {
				t.Errorf("hold answer = %q, want %q", got, c.want)
			}
		})
	}
}

// 回答の確かめが返す文・保留に記録される文・作業ログの after の answer は、同じ。
func TestSpecDraftHoldAnswerText_IsTheSameEverywhere(t *testing.T) { // AC-222・AC-223
	t.Parallel()
	f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
	prev, hold, err := snRun(t, f, id, "answer", "短くする", saChoice(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	const want = "問いに答えた（推奨と違う答え: Q1）\n短くする"
	if prev.AnswerText != want || hold.Answer == nil || *hold.Answer != want {
		t.Errorf("preview = %q, hold = %v, want %q", prev.AnswerText, hold.Answer, want)
	}
	logs := saLogs(t, f.s, id, "answer")
	if len(logs) != 1 || logs[0]["answer"] != want {
		t.Errorf("activity = %v, want answer %q", logs, want)
	}
	// 確かめが返す、問いごとの答えには、添え書きを混ぜない。
	if len(prev.SpecAnswers) != 3 {
		t.Errorf("preview spec answers = %+v", prev.SpecAnswers)
	}
}

func TestSpecDraftAnswerActivity_ChangedQuestions(t *testing.T) {
	t.Parallel()
	t.Run("AC-192 推奨と違う問いの番号を昇順で残す", func(t *testing.T) {
		t.Parallel()
		f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
		snAnswered(t, f, id, "answer", "", saChoice(3, 2), saChoice(1, 2))
		logs := saLogs(t, f.s, id, "answer")
		if len(logs) != 1 || !reflect.DeepEqual(logs[0]["changed_questions"], []any{float64(1), float64(3)}) {
			t.Errorf("activity = %v", logs)
		}
	})
	t.Run("AC-193 推奨のままはキーを出さない", func(t *testing.T) {
		t.Parallel()
		f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
		snAnswered(t, f, id, "answer", "", saChoice(1, 1))
		logs := saLogs(t, f.s, id, "answer")
		if len(logs) != 1 {
			t.Fatalf("activity = %v", logs)
		}
		if _, ok := logs[0]["changed_questions"]; ok {
			t.Errorf("activity has changed_questions: %v", logs[0])
		}
	})
}

func TestSpecDraftAnswer_OptionLabelIsUnchanged(t *testing.T) { // AC-194
	t.Parallel()
	f, id, hold := snFixture(t, SpecDraftHoldQuestion, 1)
	_, d := snAnswered(t, f, id, "answer", "", saChoice(1, 2))
	for _, h := range d.Holds {
		if h.HoldID != hold.HoldID {
			continue
		}
		if len(h.Options) == 0 || h.Options[0].ID != "answer" || h.Options[0].Label != "推奨のまま進める" {
			t.Errorf("options = %+v", h.Options)
		}
		return
	}
	t.Fatal("hold not found")
}

func TestSpecDraftNote_Element(t *testing.T) {
	t.Parallel()
	f, id, _ := snFixture(t, SpecDraftHoldQuestion, 2)
	_, d := snAnswered(t, f, id, "answer", "短くする")
	notes := snNotes(d)
	if len(notes) != 1 { // AC-195
		t.Fatalf("notes = %+v, want 1", notes)
	}
	n := notes[0]
	if n.Answer != "短くする" { // AC-196
		t.Errorf("answer = %q", n.Answer)
	}
	if n.Question != "" { // AC-197
		t.Errorf("question = %q", n.Question)
	}
	if n.By != "owner" { // AC-198
		t.Errorf("by = %q", n.By)
	}
	if n.Basis == nil || len(n.Basis) != 0 { // AC-199
		t.Errorf("basis = %#v", n.Basis)
	}
	if n.Attempt != 2 { // AC-200
		t.Errorf("attempt = %d, want 2", n.Attempt)
	}
	var answers int
	for _, a := range d.SpecDraft.Answers {
		if a.Kind == SpecDraftAnswerKindAnswer {
			answers++
		}
	}
	if answers != 3 || len(d.SpecDraft.Answers) != 4 { // AC-201
		t.Errorf("answers = %+v", d.SpecDraft.Answers)
	}
	var raw string
	saQueryRow(t, f.s, `SELECT answers FROM spec_draft`, &raw)
	if !strings.Contains(raw, `"kind":"note"`) || !strings.Contains(raw, `"kind":"answer"`) || !strings.Contains(raw, `"basis":[]`) {
		t.Errorf("stored answers = %s", raw)
	}
}

func TestSpecDraftNote_Retry(t *testing.T) {
	t.Parallel()
	f, id, _ := snFixture(t, SpecDraftHoldFailed, 1)
	text, d := snAnswered(t, f, id, "retry", "権限を直した")
	if notes := snNotes(d); len(notes) != 1 || len(d.SpecDraft.Answers) != 1 || notes[0].Answer != "権限を直した" || notes[0].Attempt != 1 { // AC-206
		t.Errorf("answers = %+v", d.SpecDraft.Answers)
	}
	if d.SpecDraft.State != SpecDraftStatePending { // AC-207
		t.Errorf("state = %s", d.SpecDraft.State)
	}
	if text != "やり直す\n権限を直した" {
		t.Errorf("hold answer = %q", text)
	}
}

// proceed・skip の添え書きは、保留の回答の文にだけ残し、記録へは入れない。
func TestSpecDraftNote_ProceedAndSkipKeepOnlyTheAnswerText(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ option, note, want string }{
		"AC-208・AC-209 proceed": {"proceed", "これで進める", "いまの下書きで計画へ進む\nこれで進める"},
		"AC-210 skip":           {"skip", "不要", "仕様の文書なしで計画する\n不要"},
		"proceed の添え書きは上限を掛けない": {"proceed", strings.Repeat("あ", 2001), "いまの下書きで計画へ進む\n" + strings.Repeat("あ", 2001)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
			saWriteDraft(t, f, f.detail(t, id).SpecDraft.Path, saDraftWithCriterion)
			text, d := snAnswered(t, f, id, c.option, c.note)
			if text != c.want {
				t.Errorf("hold answer = %q, want %q", text, c.want)
			}
			if len(d.SpecDraft.Answers) != 0 {
				t.Errorf("answers = %+v, want none", d.SpecDraft.Answers)
			}
		})
	}
}

func TestSpecDraftNote_Limit(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		reason SpecDraftHoldReason
		option string
	}{"answer": {SpecDraftHoldQuestion, "answer"}, "retry": {SpecDraftHoldFailed, "retry"}} {
		t.Run("AC-211・AC-212 2001 文字は断り、保留は開いたまま "+name, func(t *testing.T) {
			t.Parallel()
			f, id, hold := snFixture(t, c.reason, 1)
			long := strings.Repeat("あ", 2001)
			if _, _, err := snRun(t, f, id, c.option, long); !errors.Is(err, ErrValidation) {
				t.Fatalf("answer = %v, want ErrValidation", err)
			}
			// 回答の確かめを通らない回答も、書き込みの側で断る。
			_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: f.detail(t, id).Version,
				Option: c.option, Answer: long}, verifiedAttestationForTest(t, id))
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("execute = %v, want ErrValidation", err)
			}
			saRequireUntouched(t, f, id, hold)
		})
	}
	t.Run("AC-213 2000 文字は成立する", func(t *testing.T) {
		t.Parallel()
		f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
		note := strings.Repeat("あ", 2000)
		_, d := snAnswered(t, f, id, "answer", note)
		if notes := snNotes(d); len(notes) != 1 || notes[0].Answer != note {
			t.Errorf("notes = %d", len(notes))
		}
	})
}

func TestSpecDraftNote_BlankIsIgnored(t *testing.T) { // AC-214・AC-215
	t.Parallel()
	for name, note := range map[string]string{"空白だけ": "   ", "上限を超える空白だけ": strings.Repeat(" ", 2001)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
			text, d := snAnswered(t, f, id, "answer", note)
			if text != "推奨のまま進める" {
				t.Errorf("hold answer = %q", text)
			}
			if len(snNotes(d)) != 0 || len(d.SpecDraft.Answers) != 3 {
				t.Errorf("answers = %+v", d.SpecDraft.Answers)
			}
		})
	}
}

// S2b-1 で書いた形（kind の無い要素）は、答えとして読む。
func TestSpecDraftAnswers_WithoutKindAreAnswers(t *testing.T) { // AC-279・AC-236
	t.Parallel()
	f, id, _ := snFixture(t, SpecDraftHoldQuestion, 1)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE spec_draft SET answers = ?, updated_at = ?`,
			`[{"attempt":1,"question":"前の問い","answer":"前の答え","by":"owner","basis":[]}]`, formatTimestamp(time.Now()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, id)
	if len(d.SpecDraft.Answers) != 1 || d.SpecDraft.Answers[0].Kind != SpecDraftAnswerKindAnswer {
		t.Fatalf("answers = %+v", d.SpecDraft.Answers)
	}
	brief := formatSpecBrief("題名", "", nil, *d.SpecDraft)
	if !strings.Contains(brief, "## これまでの問いと答え\n### 1-1\n- 問い: 前の問い\n- 答え: 前の答え\n- 答えた者: owner\n") ||
		strings.Contains(brief, "## オーナーの添え書き") {
		t.Errorf("brief =\n%s", brief)
	}
}

func snAnswerElement(attempt int, by string, basis ...SpecDraftBasis) SpecDraftAnswer {
	return SpecDraftAnswer{Kind: SpecDraftAnswerKindAnswer, Attempt: attempt, Question: "問い", Answer: "答え", By: by, Basis: basis}
}

func snNoteElement(attempt int, text string) SpecDraftAnswer {
	return SpecDraftAnswer{Kind: SpecDraftAnswerKindNote, Attempt: attempt, Answer: text, By: "owner", Basis: []SpecDraftBasis{}}
}

func snBrief(answers ...SpecDraftAnswer) string {
	return formatSpecBrief("題名", "", nil, SpecDraft{Reason: "理由", Answers: answers})
}

func TestSpecBrief_Basis(t *testing.T) {
	t.Parallel()
	a := SpecDraftBasis{Path: ".flywheel/repos/app/docs/a.md", Locator: "節 A"}
	b := SpecDraftBasis{Path: "docs/b.md", Locator: "L10\n- 問い: 偽の行"}
	t.Run("AC-226 根拠の行は答えた者の行の次", func(t *testing.T) {
		t.Parallel()
		got := snBrief(snAnswerElement(1, "supervisor", a))
		if !strings.Contains(got, "- 答えた者: supervisor\n- 根拠: .flywheel/repos/app/docs/a.md（節 A）\n") {
			t.Errorf("brief =\n%s", got)
		}
	})
	t.Run("AC-227 根拠 2 件は 2 行", func(t *testing.T) {
		t.Parallel()
		got := snBrief(snAnswerElement(1, "supervisor", a, b))
		if n := strings.Count(got, "\n- 根拠:"); n != 2 {
			t.Errorf("basis lines = %d\n%s", n, got)
		}
		if strings.Contains(got, "\n- 問い: 偽の行") {
			t.Errorf("a basis broke into another line:\n%s", got)
		}
	})
	t.Run("AC-228 根拠の無い答えには行が無い", func(t *testing.T) {
		t.Parallel()
		if got := snBrief(snAnswerElement(1, "owner")); strings.Contains(got, "- 根拠:") {
			t.Errorf("brief =\n%s", got)
		}
	})
}

func TestSpecBrief_Notes(t *testing.T) {
	t.Parallel()
	full := snBrief(snAnswerElement(1, "owner"), snNoteElement(1, "短くする"), snAnswerElement(2, "owner"), snNoteElement(2, "2 回目の添え書き\n2 行目"))
	t.Run("AC-229・AC-230 節は問いと答えの後ろ", func(t *testing.T) {
		t.Parallel()
		qa, notes := strings.Index(full, "\n## これまでの問いと答え\n"), strings.Index(full, "\n## オーナーの添え書き\n")
		if qa < 0 || notes < 0 || notes < qa {
			t.Errorf("brief =\n%s", full)
		}
	})
	t.Run("AC-231・AC-232 見出しは何回目の実行の後かの順", func(t *testing.T) {
		t.Parallel()
		const want = "\n## オーナーの添え書き\n### 1\n短くする\n### 2\n2 回目の添え書き\n2 行目\n"
		if !strings.HasSuffix(full, want) {
			t.Errorf("brief =\n%s\nwant suffix\n%s", full, want)
		}
	})
	t.Run("AC-233 添え書きが無ければ節が無い", func(t *testing.T) {
		t.Parallel()
		if got := snBrief(snAnswerElement(1, "owner")); strings.Contains(got, "オーナーの添え書き") {
			t.Errorf("brief =\n%s", got)
		}
	})
	t.Run("AC-234 添え書きは問いと答えの節に入らない", func(t *testing.T) {
		t.Parallel()
		qa, _, _ := strings.Cut(full, "\n## オーナーの添え書き\n")
		if strings.Contains(qa, "短くする") || strings.Contains(full, "- 問い: \n") || strings.Count(full, "- 問い:") != 2 ||
			!strings.Contains(full, "### 2-1\n") || strings.Contains(full, "### 1-2\n") {
			t.Errorf("brief =\n%s", full)
		}
	})
	t.Run("AC-235 添え書きだけなら問いと答えの節が無い", func(t *testing.T) {
		t.Parallel()
		got := snBrief(snNoteElement(1, "権限を直した"))
		if strings.Contains(got, "## これまでの問いと答え") || !strings.Contains(got, "## オーナーの添え書き\n### 1\n権限を直した\n") {
			t.Errorf("brief =\n%s", got)
		}
	})
}
