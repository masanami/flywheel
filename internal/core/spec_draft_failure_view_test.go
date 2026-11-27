package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// このファイルは、下書きづくりの保留の問いの文と、失敗の保留の選択肢（docs/features/request-to-plan-s2b.md「保留の問いの文と選択肢」）を
// 実ストアで確かめる。テストの名前と注記の AC の番号は、Issue #1376 の受け持ち（親 #1374 の受入基準の並び順）。

// 理由 spec_question・spec_attempt_limit の問いの文は見出しだけで、問いは記録にだけ入る。
func TestSpecDraftHoldText_QuestionHoldsAreHeadlines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		reason   SpecDraftHoldReason
		attempts int
		want     []string
	}{
		"AC-153〜157 spec_question":      {SpecDraftHoldQuestion, 1, []string{"2 件"}},
		"AC-161・162 spec_attempt_limit": {SpecDraftHoldAttemptLimit, 3, []string{"回数の上限（3 回）", "2 件"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, SpecDraftStateRunning, tc.attempts)
			hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: tc.reason, Questions: shQuestions()})
			for _, w := range append(tc.want, d.SpecDraft.Path) { // AC-154
				if !strings.Contains(hold.Question, w) {
					t.Errorf("question lacks %q: %q", w, hold.Question)
				}
			}
			for _, q := range shQuestions() {
				deny := []string{q.Question, q.Reason} // AC-155・157・162
				for _, o := range q.Options {
					deny = append(deny, o.Label) // AC-156
				}
				for _, v := range deny {
					if strings.Contains(hold.Question, v) {
						t.Errorf("question has %q: %q", v, hold.Question)
					}
				}
			}
			if !reflect.DeepEqual(d.SpecDraft.Questions, shQuestions()) { // AC-163
				t.Errorf("record questions = %+v", d.SpecDraft.Questions)
			}
		})
	}
}

// AC-158
func TestSpecDraftHoldText_EightLongQuestionsStayOnOneLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("長\n", 250)
	qs := make([]SpecDraftQuestion, 8)
	for i := range qs {
		qs[i] = SpecDraftQuestion{Question: long, Options: []SpecDraftOption{{Label: long}, {Label: long}}, Recommended: 1, Reason: long}
	}
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
	hold, _ := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldQuestion, Questions: qs})
	if strings.ContainsAny(hold.Question, "\r\n") || !strings.Contains(hold.Question, "8 件") {
		t.Errorf("question = %q", hold.Question)
	}
}

// 失敗の保留の選択肢と「下書き:」は、下書きが通常のファイルとして在るかで決まる。
func TestSpecDraftFailedHold_OptionsFollowTheDraftFile(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		attempts int
		place    string // "file"・"dir"・"outside"（ワークスペースの外を指すリンク）・""（無い）
		want     []string
	}{
		"AC-164・169 無い・1 回": {1, "", []string{"retry", "skip"}},
		"AC-165 無い・3 回":     {3, "", []string{"skip"}},
		"AC-166 ディレクトリ":     {1, "dir", []string{"retry", "skip"}},
		"ワークスペースの外を指すリンク":   {1, "outside", []string{"retry", "skip"}},
		"AC-167・170 在る・1 回": {1, "file", []string{"retry", "proceed", "skip"}},
		"AC-168 在る・3 回":     {3, "file", []string{"proceed", "skip"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, SpecDraftStateRunning, tc.attempts)
			cid := mustChallengeID(t, id)
			switch tc.place {
			case "file":
				shWriteDraft(t, f, id)
			case "dir":
				writeWorkspaceFile(t, f.s.workspace, specDraftPathFor(cid)+"/inner.md", "x")
			case "outside":
				outside := filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(f.s.workspace, specDraftPathFor(cid))
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
			}
			hold, d := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"})
			if got := shOptionIDs(hold); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("options = %v, want %v", got, tc.want)
			}
			want := fmt.Sprintf("仕様の下書きづくりが失敗しました（実行 %d 回）。", tc.attempts)
			if tc.place == "file" {
				want += "下書き: " + d.SpecDraft.Path
			}
			if got, _, _ := strings.Cut(hold.Question, "\n"); got != want {
				t.Errorf("first line = %q, want %q", got, want)
			}
			if tc.place != "file" && strings.Contains(hold.Question, "下書き:") {
				t.Errorf("question has the draft place: %q", hold.Question)
			}
		})
	}
}

// AC-172
func TestSpecDraftQuestionHold_OptionsIgnoreTheDraftFile(t *testing.T) {
	t.Parallel()
	for name, write := range map[string]bool{"下書きが在る": true, "下書きが無い": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
			if write {
				shWriteDraft(t, f, id)
			}
			hold, _ := shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldQuestion, Questions: shQuestions()})
			if got := shOptionIDs(hold); !reflect.DeepEqual(got, []string{"answer", "proceed", "skip"}) {
				t.Errorf("options = %v", got)
			}
		})
	}
}

// AC-171
func TestSpecDraftFailedHold_OwnerCanAttachADraftWrittenLater(t *testing.T) {
	t.Parallel()
	f, id, runID := shDrafting(t, SpecDraftStateRunning, 1)
	shRaise(t, f, runID, id, specDraftHold{Reason: SpecDraftHoldFailed, Detail: "失敗した"})
	p := specDraftPathFor(mustChallengeID(t, id))
	writeWorkspaceFile(t, f.s.workspace, p, sfDraftDoc)
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Spec: &p}); err != nil {
		t.Fatalf("edit --spec: %v", err)
	}
	if d := f.detail(t, id); d.SpecPath == nil || *d.SpecPath != p {
		t.Errorf("spec_path = %v, want %s", d.SpecPath, p)
	}
}

// 開始の失敗の保留（AC-173・174・177・185）。
func TestSpecDraftFailedStart_HoldFollowsTheDraftFile(t *testing.T) {
	t.Parallel()
	for name, exists := range map[string]bool{"AC-173・177・185 下書きが無い": false, "AC-174・185 下書きが在る": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			// 開始の失敗の応答がステップを持っていても、開始の失敗の保留には失敗したステップを書かない。
			outcome := "failure"
			f.client.startOut = contractOut(ContractStateFailed, nil, func(st *ContractStatus) {
				st.Steps = []ContractStep{{Seq: 1, Step: "write", Kind: "llm", Attempt: 1, State: "finished", Outcome: &outcome, StartedAt: "2026-01-01T00:00:00Z"}}
				st.Facts = map[string]string{"spec_step_summary": "要約"}
			})
			id := f.record(t, "x", SpecDraft{})
			p := specDraftPathFor(mustChallengeID(t, id))
			if exists {
				writeWorkspaceFile(t, f.s.workspace, p, sfDraftDoc)
			}
			f.delegate(t, 100)
			hold := f.requireSpecFailed(t, id, 1)
			want := []string{"retry", "skip"}
			if exists {
				want = []string{"retry", "proceed", "skip"}
			}
			if got := shOptionIDs(hold); !reflect.DeepEqual(got, want) {
				t.Errorf("options = %v, want %v", got, want)
			}
			if got := strings.Contains(hold.Question, "下書き: "+p); got != exists {
				t.Errorf("question has the draft place = %v: %q", got, hold.Question)
			}
			if !exists && strings.Contains(hold.Question, "下書き:") {
				t.Errorf("question = %q", hold.Question)
			}
			if strings.Contains(hold.Question, "失敗したステップ:") || strings.Contains(hold.Question, "ステップの要約:") {
				t.Errorf("question = %q", hold.Question)
			}
		})
	}
}

// 行き止まりの検出の保留（AC-175・176）。
func TestSpecDraftStall_HoldFollowsTheDraftFile(t *testing.T) {
	t.Parallel()
	for name, exists := range map[string]bool{"AC-175 下書きが無い": false, "AC-176 下書きが在る": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			id := f.record(t, "x", SpecDraft{})
			f.delegate(t, 100)
			f.write(t, func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?`, formatTimestamp(time.Now()))
				return err
			})
			if exists {
				writeWorkspaceFile(t, f.s.workspace, specDraftPathFor(mustChallengeID(t, id)), sfDraftDoc)
			}
			f.delegate(t, 100)
			hold := f.requireSpecFailed(t, id, 1)
			if got := slices.Contains(shOptionIDs(hold), "proceed"); got != exists {
				t.Errorf("options = %v", shOptionIDs(hold))
			}
			if got := strings.Contains(hold.Question, "下書き:"); got != exists {
				t.Errorf("question = %q", hold.Question)
			}
		})
	}
}

// 実行が終わった後の失敗の保留も、下書きのファイルを確かめる。
func TestSpecDraftFinish_FailedHoldWithoutADraftHasNoProceed(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	if err := os.Remove(filepath.Join(f.s.workspace, f.draft)); err != nil {
		t.Fatal(err)
	}
	d := f.finish(t, sfOut(ContractStateFailed, ""))
	hold := f.requireSpecFailed(t, f.id, 1)
	if got := shOptionIDs(hold); !reflect.DeepEqual(got, []string{"retry", "skip"}) {
		t.Errorf("options = %v (holds %+v)", got, d.Holds)
	}
	if strings.Contains(hold.Question, "下書き:") {
		t.Errorf("question = %q", hold.Question)
	}
}

// sfNamedStep はステップ step が結果 outcome（空は結果なし）で終わった steps を出力に足す。前に成功したステップを 1 つ置く。
func sfNamedStep(step, outcome string) func(*ContractStatus) {
	return func(st *ContractStatus) {
		ok, last := "pass", outcome
		x := ContractStep{Seq: 2, Step: step, Kind: "llm", Attempt: 1, State: "finished", StartedAt: "2026-01-01T00:00:00Z"}
		if outcome != "" {
			x.Outcome = &last
		}
		st.Steps = []ContractStep{{Seq: 1, Step: "prepare", Kind: "llm", Attempt: 1, State: "finished", Outcome: &ok, StartedAt: "2026-01-01T00:00:00Z"}, x}
	}
}

func sfSummary(v string) func(*ContractStatus) {
	return func(st *ContractStatus) { st.Facts = map[string]string{"spec_step_summary": v} }
}

// sfLine は問いの文の、head で始まる行を返す（無ければ空。2 行以上あれば失敗）。
func sfLine(t *testing.T, question, head string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(question, "\n") {
		if strings.HasPrefix(line, head) {
			found = append(found, line)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%q appears %d times: %q", head, len(found), question)
	}
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// 失敗したステップと要約の行（AC-178〜182）。
func TestSpecDraftFinish_FailedStepLines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		out               ContractLaunchOutput
		wantStep, wantSum string
	}{
		"AC-178・179 結果 failure": {sfOut(ContractStateFailed, "", sfSummary("書き込みが拒否された"), sfNamedStep("write", "failure")),
			"失敗したステップ: write（結果 failure）", "ステップの要約: 書き込みが拒否された"},
		"AC-180・181 時間切れは要約を出さない": {sfOut(ContractStateFailed, "", sfSummary("前のステップの要約"), sfNamedStep("critique", "step_timeout")),
			"失敗したステップ: critique（結果 step_timeout）", ""},
		"step_error は要約を出さない": {sfOut(ContractStateFailed, "", sfSummary("前の要約"), sfNamedStep("critique", "step_error")),
			"失敗したステップ: critique（結果 step_error）", ""},
		"AC-182 要約が無い": {sfOut(ContractStateFailed, "", sfNamedStep("write", "failure")),
			"失敗したステップ: write（結果 failure）", ""},
		"要約が空白だけ": {sfOut(ContractStateFailed, "", sfSummary(" \n "), sfNamedStep("write", "failure")),
			"失敗したステップ: write（結果 failure）", ""},
		"取り消しで終わった": {sfOut(ContractStateCancelled, "", sfNamedStep("write", "cancelled")),
			"失敗したステップ: write（結果 cancelled）", ""},
		"結果の語が無い": {sfOut(ContractStateCancelled, "", sfNamedStep("write", "")),
			"失敗したステップ: write（結果 -）", ""},
		"ステップの記録が無い": {sfOut(ContractStateFailed, "", sfSummary("要約")), "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			f.finish(t, tc.out)
			q := f.failed(t)
			if got := sfLine(t, q, "失敗したステップ:"); got != tc.wantStep {
				t.Errorf("step line = %q, want %q (question %q)", got, tc.wantStep, q)
			}
			if got := sfLine(t, q, "ステップの要約:"); got != tc.wantSum {
				t.Errorf("summary line = %q, want %q (question %q)", got, tc.wantSum, q)
			}
		})
	}
}

// 行の並びは、見出し・失敗の内容・失敗したステップ・ステップの要約の順。
func TestSpecDraftFinish_FailedHoldLineOrder(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	f.finish(t, sfOut(ContractStateFailed, "", sfCost(15), sfSummary("要約の文"), sfNamedStep("write", "failure")))
	q := f.failed(t)
	last := -1
	for _, part := range []string{"仕様の下書きづくりが失敗しました（実行 1 回）。下書き: " + f.draft, "下書きづくりの実行が終わりました", "上限額",
		"\n失敗したステップ: write（結果 failure）", "\nステップの要約: 要約の文"} {
		i := strings.Index(q, part)
		if i <= last {
			t.Fatalf("%q is missing or out of order: %q", part, q)
		}
		last = i
	}
	if !strings.HasSuffix(q, "ステップの要約: 要約の文") {
		t.Errorf("question = %q", q)
	}
}

// 要約は 1 行・500 文字まで（AC-183・184）。制御文字は見える形にする。
func TestSpecDraftFinish_StepSummaryIsOneBoundedLine(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ summary, want string }{
		"AC-183 改行は空白":       {"1 行目\n2 行目\r\n3 行目\u20284 行目", "1 行目 2 行目 3 行目 4 行目"},
		"AC-184 600 文字は 500": {strings.Repeat("あ", 600), strings.Repeat("あ", 500)},
		"500 文字は切らない":        {strings.Repeat("あ", 500), strings.Repeat("あ", 500)},
		"制御文字":               {"a\x1b[2Jb", `a\x1b[2Jb`},
		"制御文字を直した後も 500":     {strings.Repeat("\x1b", 600), strings.Repeat(`\x1b`, 125)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := sfStart(t, SpecDraft{})
			f.finish(t, sfOut(ContractStateFailed, "", sfSummary(tc.summary), sfNamedStep("write", "failure")))
			q := f.failed(t)
			got := strings.TrimPrefix(sfLine(t, q, "ステップの要約:"), "ステップの要約: ")
			if got != tc.want || utf8.RuneCountInString(got) > 500 {
				t.Errorf("summary = %q (%d runes)", got, utf8.RuneCountInString(got))
			}
			if !strings.HasSuffix(q, "ステップの要約: "+tc.want) {
				t.Errorf("the summary spills to another line: %q", q)
			}
		})
	}
}

// AC-186。ステップの記録があっても、成功で終わった後の確かめの失敗には書かない。
func TestSpecDraftFinish_SucceededRunHasNoFailedStepLine(t *testing.T) {
	t.Parallel()
	f := sfStart(t, SpecDraft{})
	writeWorkspaceFile(t, f.s.workspace, f.draft, "# 受入基準が無い下書き\n")
	f.finish(t, sfOut(ContractStateSucceeded, "[]", sfNamedStep("fix", "failure"), func(st *ContractStatus) {
		st.Facts["spec_step_summary"] = "要約"
	}))
	q := f.failed(t)
	if !strings.Contains(q, "下書きを使えません") || strings.Contains(q, "失敗したステップ:") || strings.Contains(q, "ステップの要約:") {
		t.Errorf("question = %q", q)
	}
}
