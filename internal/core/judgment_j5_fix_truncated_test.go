package core

// このファイルは、衝突の解消の確かめ（J5）の入力の差分が上限（maxPullRequestDiffBytes）を超えて切られたときの決まり
// （docs/features/core-redesign.md の S3c「差分が上限を超えたとき」）を固定する。J5 は確かめ待ちの中身の作業ツリーしか読めず、落ちた分の
// 「通した中身の側からの差分」を作り直せないので、切られたときは「満たした」を通さず、判断できないと同じに扱う。入力を組む所から宛先ごとの書き込みの口まで、
// 本物の経路を通す。

import (
	"encoding/json"
	"strings"
	"testing"
)

// setConflictFixFiles は、setConflictFixHistory が置いた履歴の、直したファイルの差分を files に置き換える。
func setConflictFixFiles(cmp *fakeCompare, files []UpstreamFile) {
	cmp.set(fixPassed, fixPending, withFiles(cmp.results[[2]string{fixPassed, fixPending}], files...))
	cmp.set("m1", fixPending, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1,
		Commits: []UpstreamCommit{commit(fixPassed, "fix", "p0")}, Files: files})
}

// bigPatchFiles は、差分が合計で size バイト程度になるファイルの組。後ろのファイル z.go は、切られると名前ごと落ちる。
func bigPatchFiles(size int) []UpstreamFile {
	return []UpstreamFile{
		{Filename: "a.go", Patch: "@@ -1 +1 @@\n+" + strings.Repeat("x", size)},
		{Filename: "z.go", Patch: "@@ -1 +1 @@\n+TAIL-LINE"},
	}
}

type truncFixCase struct {
	dest j5FixDest
	mk   func(*testing.T) *fixFixture
}

func truncFixCases(realWriters bool) map[string]truncFixCase {
	wrap := func(mk func(*testing.T) *fixFixture) func(*testing.T) *fixFixture {
		return func(t *testing.T) *fixFixture {
			f := mk(t)
			if realWriters {
				f.s.fixWriters = f.s.defaultFixWriters()
			}
			return f
		}
	}
	return map[string]truncFixCase{
		"integration": {j5FixDestIntegration, wrap(newFixIntegrationFixture)},
		"reflect":     {j5FixDestReflect, wrap(newFixReflectFixture)},
	}
}

// 差分が上限を超えて切られたとき、J5 が「満たした」を返しても、書き込みの口へは「判断できない」が届く（理由に、上限を超えて切られたので通さなかった旨）。
func TestJ5Fix_TruncatedDiffMetReachesTheWriterAsUncertain(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(false) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, bigPatchFiles(maxPullRequestDiffBytes))
			f.setVerdict("met")
			res := f.verify(t)
			if len(res.Items) != 1 || res.Items[0].Outcome != "uncertain" {
				t.Fatalf("items = %+v, want one uncertain", res.Items)
			}
			if n := f.j5RunCount(t); n != 1 {
				t.Fatalf("J5 runs = %d, want 1（呼んだうえで読み替える）", n)
			}
			if len(f.log.results) != 1 || f.log.dests[0] != c.dest || f.log.results[0].Verdict != J5VerdictUncertain {
				t.Fatalf("writer calls = %+v, want one uncertain for %s", f.log.results, c.dest)
			}
			if r := f.log.results[0].Reason; !strings.Contains(r, "上限") || !strings.Contains(r, "通さなかった") {
				t.Errorf("reason = %q, want it to say the diff was cut at the limit and not passed", r)
			}
		})
	}
}

// 切られた差分への「満たした」は、本物の書き込みの口でも通らない。通した中身は進まず、通した記録は残らず、判断できないのときと同じ後始末になる。
func TestJ5Fix_TruncatedDiffMetDoesNotAdvanceThePassedContent(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, bigPatchFiles(maxPullRequestDiffBytes))
			f.setVerdict("met")
			f.verify(t)
			if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 0 {
				t.Fatalf("conflict_fix_passed = %d, want none", n)
			}
			if got := f.pendingRef(t); got != "" {
				t.Errorf("fix_pending_ref = %q, want cleared", got)
			}
			if c.dest == j5FixDestIntegration {
				if got := f.text(t, `SELECT verified_ref FROM contract_run`); got != "" {
					t.Errorf("verified_ref = %q, want cleared as for uncertain", got)
				}
				if got := f.text(t, `SELECT status FROM task`); got != "verifying" {
					t.Errorf("task = %s, want verifying", got)
				}
				return
			}
			if got := f.refsOf(t); got != (contractFixRefs{Rejected: fixPending}) {
				t.Errorf("fix refs = %+v, want rejected %s only", got, fixPending)
			}
			if f.challengeStatusOf(t) != "done" || f.opStateOf(t) != "approved" {
				t.Errorf("challenge = %s operation = %s", f.challengeStatusOf(t), f.opStateOf(t))
			}
		})
	}
}

// 切られたときでも、「満たしていない」「判断できない」は J5 が返したまま届く（指摘・問いも失わない）。
func TestJ5Fix_TruncatedDiffKeepsNotMetAndUncertain(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(false) {
		for _, verdict := range []string{"not_met", "uncertain"} {
			c, verdict := c, verdict
			t.Run(name+"/"+verdict, func(t *testing.T) {
				t.Parallel()
				f := c.mk(t)
				setConflictFixFiles(f.cmp, bigPatchFiles(maxPullRequestDiffBytes))
				f.setVerdict(verdict)
				f.verify(t)
				if len(f.log.results) != 1 || string(f.log.results[0].Verdict) != verdict {
					t.Fatalf("writer calls = %+v, want one %s", f.log.results, verdict)
				}
				if verdict == "not_met" && f.log.results[0].Feedback != "FEEDBACK-TEXT" {
					t.Errorf("feedback = %q", f.log.results[0].Feedback)
				}
				if verdict == "uncertain" && f.log.results[0].Question != "QUESTION-TEXT" {
					t.Errorf("question = %q", f.log.results[0].Question)
				}
				if out := f.text(t, `SELECT output FROM run WHERE judgment = 'J5'`); strings.Contains(out, "diff_truncated_overridden_from") {
					t.Errorf("run output was rewritten although J5 did not say met: %s", out)
				}
			})
		}
	}
}

// 上限以内なら、大きな差分でも、いまのまま「満たした」が通る（通した中身が進み、通した記録が 1 件残る）。
func TestJ5Fix_DiffWithinTheLimitStillPasses(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, bigPatchFiles(maxPullRequestDiffBytes/2))
			f.setVerdict("met")
			res := f.verify(t)
			if len(res.Items) != 1 || res.Items[0].Outcome != "met" {
				t.Fatalf("items = %+v, want one met", res.Items)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 1 {
				t.Fatalf("conflict_fix_passed = %d, want 1", n)
			}
			if out := f.text(t, `SELECT output FROM run WHERE judgment = 'J5'`); strings.Contains(out, "diff_truncated_overridden_from") {
				t.Errorf("run output was rewritten although the diff is within the limit: %s", out)
			}
			if !strings.Contains(f.sectionsOf(t)[labelFixDiffSection], "TAIL-LINE") {
				t.Error("the diff section lost the last file although it is within the limit")
			}
		})
	}
}

// 読み替えたことは、J5 の実行の記録の保存した出力にも残る（判断できない・読み替えた理由・元の判定と元の出力）。
func TestJ5Fix_TruncatedDiffOverrideIsRecordedOnTheRunOutput(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, bigPatchFiles(maxPullRequestDiffBytes))
			f.setVerdict("met")
			f.verify(t)
			var out struct {
				Verdict, Reason, Question string
				From                      string          `json:"diff_truncated_overridden_from"`
				Orig                      json.RawMessage `json:"j5_output"`
			}
			if err := json.Unmarshal([]byte(f.text(t, `SELECT output FROM run WHERE judgment = 'J5'`)), &out); err != nil {
				t.Fatal(err)
			}
			if out.Verdict != "uncertain" || out.From != "met" || !strings.Contains(out.Reason, "通さなかった") || out.Question == "" || !strings.Contains(string(out.Orig), `"met"`) {
				t.Fatalf("run output = %+v", out)
			}
		})
	}
}

// 切られたかどうかの境目: 上限ちょうどは切られず、1 バイト多いと切られる。
func TestFormatConflictFixes_TruncationBoundary(t *testing.T) {
	t.Parallel()
	mk := func(patchLen int) []ConflictFix {
		return []ConflictFix{{MergeCommit: "m", Files: []ConflictFixFile{{Filename: "a.go", Patch: strings.Repeat("x", patchLen)}}}}
	}
	base, _ := formatConflictFixes(mk(0))
	atLimit := maxPullRequestDiffBytes - len(base)
	if _, cut := formatConflictFixes(mk(atLimit)); cut {
		t.Error("a diff of exactly the limit was reported as cut")
	}
	if _, cut := formatConflictFixes(mk(atLimit + 1)); !cut {
		t.Error("a diff one byte over the limit was not reported as cut")
	}
}
