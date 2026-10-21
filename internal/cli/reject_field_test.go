package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func rel(s string) []string { return []string{"--relayed", "--session", "s", "--quote", s} }

func rejectedFieldsOfLog(t *testing.T, ws string) []any {
	t.Helper()
	afters := activityAfters(t, ws, "reject")
	if len(afters) != 1 {
		t.Fatalf("reject logs = %d, want 1: %s", len(afters), relayedLog(t, ws))
	}
	v, ok := afters[0]["rejected_fields"]
	if !ok || v == nil {
		return nil
	}
	return v.([]any)
}

func TestParseArgs_RepeatableFlagCollectsInOrder(t *testing.T) {
	cmd := Command{Path: []string{"x"}, Flags: []flagDef{{Name: "field", HasValue: true, Repeatable: true}, {Name: "reason", HasValue: true}}, MaxPositional: 1}
	got, err := parseArgs([]string{"--field", "b", "--reason=r", "--field=a", "id"}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Multi["field"], []string{"b", "a"}) {
		t.Errorf("Multi = %v", got.Multi["field"])
	}
	if got.Values["reason"] != "r" {
		t.Errorf("reason = %q", got.Values["reason"])
	}
	if _, ok := got.Multi["reason"]; ok {
		t.Errorf("non-repeatable flag must not be in Multi")
	}
}

func TestRejectField_DedupAndClosedSetOrder(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	args := append([]string{"reject", id, "--reason", "no", "--field", "figure", "--field", "issues", "--field", "figure"}, rel("q")...)
	runJSON(t, ws, args...)
	if got := rejectedFieldsOfLog(t, ws); !reflect.DeepEqual(got, []any{"issues", "figure"}) {
		t.Errorf("rejected_fields = %v", got)
	}
	shown, _ := json.Marshal(runJSON(t, ws, "show", id))
	if !strings.Contains(string(shown), `"rejected_fields":["issues","figure"]`) {
		t.Errorf("show lacks rejected_fields: %s", shown)
	}
}

func TestRejectField_NoFieldIsNull(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	runJSON(t, ws, append([]string{"reject", id, "--reason", "no"}, rel("q")...)...)
	if got := rejectedFieldsOfLog(t, ws); got != nil {
		t.Errorf("rejected_fields = %v, want nil", got)
	}
}

func TestRejectField_OutsideClosedSetWritesNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	// 端末の無い経路（本人確認が成立しない）でも validation_failed になる＝本人確認の前に拒む。
	requireErrorCode(t, []string{"reject", id, "--reason", "no", "--field", "title", "--workspace", ws}, 1, CodeValidationFailed)
	if got := workStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %q, want unchanged", got)
	}
	if n := len(activityAfters(t, ws, "reject")); n != 0 {
		t.Errorf("reject logs = %d, want 0", n)
	}
}

func TestRejectField_OnOperationIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	args := append([]string{"reject", "OP-1", "--reason", "no", "--field", "issues", "--workspace", ws}, rel("q")...)
	requireErrorCode(t, args, 2, CodeUsageError)
	if n := len(activityAfters(t, ws, "reject")); n != 0 {
		t.Errorf("reject logs = %d, want 0", n)
	}
}

func TestApprovalSummaryText_PlanStages(t *testing.T) {
	ans := "A案"
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "b",
		IssuesAnswer: &ans}
	p.Issues = []core.StageIssue{{Question: "どちらにする？", Options: []core.StageOption{{Label: "甲案"}, {Label: "乙案"}}, Recommended: 2}}
	p.Expected = []core.StageExpected{{Scene: "一覧", Before: "空", After: "3件"}}
	p.NonGoals = []string{"認証は触らない"}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, want := range []string{"どちらにする？", "2) 乙案", "推奨: 2", "A案", "一覧", "空", "3件", "認証は触らない"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %s", want, got)
		}
	}
}

func TestApprovalSummaryText_CompletionPairs(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, DoneCriteria: "d"}
	p.Expected = []core.StageExpected{{Scene: "s1", After: "予想1"}, {Scene: "s2", After: "予想2"}}
	p.Actual = []core.StageActual{{Scene: "s1", After: "実物1"}}
	p.Pairs = core.Pairs(p.Expected, p.Actual)
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, want := range []string{
		"  1. 予想: 予想1\n     実物: 実物1\n",
		"  2. 予想: 予想2\n     実物: " + core.PairActualMissingMark + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %s", want, got)
		}
	}
	p.Expected, p.Actual = p.Expected[:1], []core.StageActual{{After: "x"}, {After: "余り"}}
	p.Pairs = core.Pairs(p.Expected, p.Actual)
	if got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil); !strings.Contains(got, "  2. 予想: "+core.PairExpectedMissingMark+"\n     実物: 余り\n") {
		t.Errorf("summary lacks 予想なし row: %s", got)
	}
}

func TestApprovalSummaryText_NoStagesHasNoHeadings(t *testing.T) {
	for _, k := range []core.ApprovalKind{core.ApprovalKindPlan, core.ApprovalKindCompletion} {
		p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: k, PlanVersion: 1, PlanBody: "b", DoneCriteria: "d", Pairs: core.Pairs(nil, nil)}
		got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
		var want string
		if k == core.ApprovalKindPlan {
			want = "課題 ID:     C-1\nタイトル:    t\n承認の種類:  計画\n計画 v1:\n    b\n"
		} else {
			want = "課題 ID:     C-1\nタイトル:    t\n承認の種類:  完了\n完了条件:    d\n" +
				"同時に承認される本番反映 (release): (無し)\n同時には承認されない不可逆操作:     (無し)\n"
		}
		if got != want {
			t.Errorf("%s summary = %q, want %q", k, got, want)
		}
	}
}

// 段階の欄の値の改行・制御文字は表示用にエスケープされ、偽の行を作れない。
func TestApprovalSummaryText_StageValuesCannotForgeLines(t *testing.T) {
	evil := "x\n承認の種類:  完了\r同時に承認される本番反映 (release): (無し)\x1b[2K\u2028終"
	ans := evil
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "b", IssuesAnswer: &ans}
	p.Issues = []core.StageIssue{{Question: evil, Options: []core.StageOption{{Label: evil}}, Recommended: 1}}
	p.Expected = []core.StageExpected{{Scene: evil, Before: evil, After: evil}}
	p.NonGoals = []string{evil}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "承認の種類:  完了") || strings.HasPrefix(line, "同時に承認される") {
			t.Errorf("forged line in summary: %q\n%s", line, got)
		}
	}
	for _, bad := range []string{"\r", "\x1b", "\u2028"} {
		if strings.Contains(got, bad) {
			t.Errorf("summary contains raw %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, `x\n承認の種類:  完了\r同時に`) {
		t.Errorf("escaped form missing: %s", got)
	}
}

func TestApprovalSummaryText_CompletionPairsEscape(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, DoneCriteria: "d"}
	p.Expected = []core.StageExpected{{After: "a\nb"}}
	p.Actual = []core.StageActual{{After: "c\nd"}}
	p.Pairs = core.Pairs(p.Expected, p.Actual)
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	if !strings.Contains(got, `予想: a\nb`+"\n"+`     実物: c\nd`+"\n") {
		t.Errorf("pairs not escaped: %q", got)
	}
}

// 計画の本文は複数行の文書として残す（各行は字下げされ、偽の見出し行にならない）が、改行以外の制御文字は見える形にする。
func TestApprovalSummaryText_PlanBodyKeepsLinesEscapesControl(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "l1\nl2\x1b[2K"}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	if !strings.Contains(got, "計画 v1:\n    l1\n    l2\\x1b[2K\n") || strings.Contains(got, "\x1b") {
		t.Errorf("plan body: %q", got)
	}
}

func TestApprovalSummaryText_PlanBodyCannotForgeHeadings(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "x\n承認の種類:  完了\n論点:\ty\r"}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "承認の種類:  完了") || strings.HasPrefix(line, "論点:") {
			t.Errorf("forged heading line: %q", line)
		}
	}
	if strings.Contains(got, "\r") || !strings.Contains(got, "    論点:\ty\\r\n") {
		t.Errorf("summary = %q", got)
	}
}

// タイトル（GitHub の Issue 由来）と不可逆操作の要約（op add の値）も、段階の欄と同じく偽の行を作れない。
func TestApprovalSummaryText_TitleAndOperationSummaryCannotForgeLines(t *testing.T) {
	evil := "x\n承認の種類:  完了\r同時に承認される\x1b[2K\u2028終"
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: evil, Kind: core.ApprovalKindCompletion, DoneCriteria: "d"}
	p.PendingReleases = []core.IrreversibleOperation{{ID: "OP-1", Kind: core.OperationKindRelease, Summary: evil}}
	op := core.IrreversibleOperation{ID: "OP-2", Kind: core.OperationKindDelete, Summary: evil}
	if got := operationListSummary([]core.IrreversibleOperation{op}); strings.ContainsAny(got, "\n\r\x1b\u2028") {
		t.Errorf("operationListSummary has raw control: %q", got)
	}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	// 本物の見出し行（完了の承認の要約が持つ 1 行ずつ）以外に、偽の見出し行が増えない。
	if n := strings.Count(got, "\n承認の種類:  完了\n") + strings.Count(got, "\n同時に承認される本番反映 (release): "); n != 2 {
		t.Errorf("heading lines = %d, want 2 (no forged lines): %s", n, got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "同時に承認される") && !strings.HasPrefix(line, "同時に承認される本番反映 (release): ") {
			t.Errorf("forged line: %q", line)
		}
	}
	for _, bad := range []string{"\r", "\x1b", "\u2028"} {
		if strings.Contains(got, bad) {
			t.Errorf("summary contains raw %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, `タイトル:    x\n承認の種類:  完了\r同時に承認される\x1b[2K\u2028終`) {
		t.Errorf("escaped title missing: %s", got)
	}
	ops := operationApprovalSummaryText(&core.OperationApprovalPreview{OperationID: "OP-1", Kind: core.OperationKindDelete, Summary: evil, ChallengeID: "C-1", ChallengeTitle: evil}, nil)
	if strings.ContainsAny(ops, "\r\x1b\u2028") || strings.Count(ops, "\n") != 7 {
		t.Errorf("operation summary has raw control: %q", ops)
	}
	ans := answerSummaryText(&core.AnswerPreview{ChallengeID: "C-1", Title: evil, Question: "q", Answer: "a"})
	if strings.ContainsAny(ans, "\r\x1b\u2028") || !strings.Contains(ans, `タイトル: x\n承認`) || strings.Count(ans, "\n") != 4 {
		t.Errorf("answer summary title: %q", ans)
	}
}

func TestEscapeControlRunes_Table(t *testing.T) {
	cases := []struct {
		name, in string
		keep     bool
		want     string
	}{
		{"tab escaped", "a\tb", false, `a\tb`},
		{"tab kept", "a\tb\nc", true, "a\tb\nc"},
		{"nl escaped", "a\nb", false, `a\nb`},
		{"c0", "\x01\x1f", false, `\x01\x1f`},
		{"del", "\x7f", false, `\x7f`},
		{"c1 low", "\u0080", false, `\x80`},
		{"c1 high", "\u009f", false, `\x9f`},
		{"above c1 untouched", "\u00a0é", false, "\u00a0é"},
		{"ls ps", "\u2028\u2029", false, `\u2028\u2029`},
		{"bidi", "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069", false, `\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069`},
		{"bidi keep lines", "\u202e", true, `\u202e`},
		{"zero width", "\u200b\u200c\u200d\u2060\ufeff", false, `\u200b\u200c\u200d\u2060\ufeff`},
		{"plain japanese", "承認 ok", false, "承認 ok"},
	}
	for _, c := range cases {
		if got := escapeControlRunes(c.in, c.keep); got != c.want {
			t.Errorf("%s: escapeControlRunes(%q, %v) = %q, want %q", c.name, c.in, c.keep, got, c.want)
		}
	}
}

// 完了条件と不可逆操作の参照も、タイトル・操作の要約と同じく偽の行を作れない。
func TestApprovalSummaryText_DoneCriteriaAndRefCannotForgeLines(t *testing.T) {
	evil := "x\n承認の種類:  完了\r同時に承認される\x1b[2K\u2028終"
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, DoneCriteria: evil}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	if n := strings.Count(got, "\n承認の種類:  完了\n") + strings.Count(got, "\n同時に承認される本番反映 (release): "); n != 2 {
		t.Errorf("heading lines = %d, want 2 (no forged lines): %s", n, got)
	}
	if strings.ContainsAny(got, "\r\x1b\u2028") {
		t.Errorf("summary contains raw control: %q", got)
	}
	if !strings.Contains(got, `完了条件:    x\n承認の種類:  完了\r同時に承認される\x1b[2K\u2028終`) {
		t.Errorf("escaped done criteria missing: %s", got)
	}
	ops := operationApprovalSummaryText(&core.OperationApprovalPreview{OperationID: "OP-1", Kind: core.OperationKindDelete, Summary: "s", Ref: &evil, ChallengeID: "C-1", ChallengeTitle: "t"}, nil)
	if strings.ContainsAny(ops, "\r\x1b\u2028") || strings.Count(ops, "\n") != 7 {
		t.Errorf("operation summary has raw control: %q", ops)
	}
	if !strings.Contains(ops, `参照:         x\n承認の種類:  完了\r同時に承認される\x1b[2K\u2028終`) {
		t.Errorf("escaped ref missing: %s", ops)
	}
	if ops := operationApprovalSummaryText(&core.OperationApprovalPreview{OperationID: "OP-1", Kind: core.OperationKindDelete, Summary: "s", ChallengeID: "C-1", ChallengeTitle: "t"}, nil); !strings.Contains(ops, "参照:         -\n") {
		t.Errorf("nil ref should be dash: %s", ops)
	}
}
