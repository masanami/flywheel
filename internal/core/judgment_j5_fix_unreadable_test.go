package core

// このファイルは、コンフリクトの解消の確かめ（J5）の入力に、差分を上流が返さなかったファイル（PatchUnavailable）があるときの決まり
// （docs/features/core-redesign.md の S3c「差分を読めないファイルがあるとき」）を固定する。判断点は確かめ待ちの中身の作業ツリーしか読めず、
// 入力に無い差分を作り直せないので、判断点が「満たした」を返しても通さず、判断できないと同じに扱う。本物の経路（入力を組む所から宛先ごとの書き込みの口まで）を通す。

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// unreadableFiles は a.go（差分あり）と、差分を上流が返さないファイル names の組。
func unreadableFiles(names ...string) []UpstreamFile {
	files := []UpstreamFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+RESOLVED-LINE"}}
	for _, n := range names {
		files = append(files, UpstreamFile{Filename: n})
	}
	return files
}

func runOutputOf(t *testing.T, f *fixFixture) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(f.text(t, `SELECT output FROM run WHERE judgment = 'J5'`)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-86: 読めなかったファイルがあるとき、入力には、名前と読めなかった旨が入る。そのうえで、J5 が「満たした」を返しても、書き込みの口へは「判断できない」が届く。理由にそのファイルの名前が入る。
func TestJ5Fix_UnreadableFileMetReachesTheWriterAsUncertain(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(false) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, unreadableFiles("big.bin"))
			f.setVerdict("met")
			res := f.verify(t)
			if len(res.Items) != 1 || res.Items[0].Outcome != "uncertain" {
				t.Fatalf("items = %+v, want one uncertain", res.Items)
			}
			if len(f.log.results) != 1 || f.log.dests[0] != c.dest || f.log.results[0].Verdict != J5VerdictUncertain {
				t.Fatalf("writer calls = %+v, want one uncertain for %s", f.log.results, c.dest)
			}
			if r := f.log.results[0].Reason; !strings.Contains(r, "big.bin") || !strings.Contains(r, "通さなかった") {
				t.Errorf("reason = %q, want the unreadable file name and that it was not passed", r)
			}
			diff := f.sectionsOf(t)[labelFixDiffSection]
			if !strings.Contains(diff, "big.bin") || !strings.Contains(diff, "読めなかった") {
				t.Errorf("diff section lacks the unreadable file or its note:\n%s", diff)
			}
		})
	}
}

// 本物の書き込みの口でも通らない（通した中身は進まず、通した記録は残らず、判断できないのときと同じ後始末）。
func TestJ5Fix_UnreadableFileMetDoesNotAdvanceThePassedContent(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			setConflictFixFiles(f.cmp, unreadableFiles("big.bin"))
			f.setVerdict("met")
			f.verify(t)
			assertNothingPassed(t, f, c.dest)
		})
	}
}

// 読み替えたことは実行の記録の保存した出力に、切られたときと区別できるキーで残る。「満たしていない」「判断できない」はそのまま届き、出力も書き換えない。
func TestJ5Fix_UnreadableFileOverrideIsRecordedAndOtherVerdictsStay(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(false) {
		for _, verdict := range []string{"met", "not_met", "uncertain"} {
			c, verdict := c, verdict
			t.Run(name+"/"+verdict, func(t *testing.T) {
				t.Parallel()
				f := c.mk(t)
				setConflictFixFiles(f.cmp, unreadableFiles("big.bin"))
				f.setVerdict(verdict)
				f.verify(t)
				out := runOutputOf(t, f)
				if verdict != "met" {
					if string(f.log.results[0].Verdict) != verdict {
						t.Fatalf("writer verdict = %s, want %s", f.log.results[0].Verdict, verdict)
					}
					if _, ok := out["patch_unavailable_overridden_from"]; ok {
						t.Errorf("run output was rewritten although J5 did not say met: %v", out)
					}
					if verdict == "not_met" && f.log.results[0].Feedback != "FEEDBACK-TEXT" {
						t.Errorf("feedback = %q", f.log.results[0].Feedback)
					}
					if verdict == "uncertain" && f.log.results[0].Question != "QUESTION-TEXT" {
						t.Errorf("question = %q", f.log.results[0].Question)
					}
					return
				}
				if string(out["patch_unavailable_overridden_from"]) != `"met"` || string(out["verdict"]) != `"uncertain"` || len(out["j5_output"]) == 0 {
					t.Errorf("run output = %v", out)
				}
				if _, ok := out["diff_truncated_overridden_from"]; ok {
					t.Errorf("the truncation key must not be set when the diff was not cut: %v", out)
				}
			})
		}
	}
}

// 切られたことと、読めなかったファイルの両方が当たるときは、1 回の読み替えで、理由に両方を書き、両方のキーを残す。
func TestJ5Fix_TruncatedAndUnreadableBothAreRecorded(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := c.mk(t)
			files := append(bigPatchFiles(maxPullRequestDiffBytes), UpstreamFile{Filename: "big.bin"})
			setConflictFixFiles(f.cmp, files)
			f.setVerdict("met")
			f.verify(t)
			assertNothingPassed(t, f, c.dest)
			out := runOutputOf(t, f)
			if string(out["patch_unavailable_overridden_from"]) != `"met"` || string(out["diff_truncated_overridden_from"]) != `"met"` {
				t.Fatalf("run output = %v, want both keys", out)
			}
			var reason string
			_ = json.Unmarshal(out["reason"], &reason)
			if !strings.Contains(reason, "上限") || !strings.Contains(reason, "big.bin") {
				t.Errorf("reason = %q, want both causes", reason)
			}
			if n := f.j5RunCount(t); n != 1 {
				t.Errorf("J5 runs = %d, want 1", n)
			}
		})
	}
}

// 理由に書く名前は先頭の数件と件数。同じ名前は 1 件に数える。
func TestInputGaps_UnreadableFileNamesAreShortened(t *testing.T) {
	t.Parallel()
	var fixes []ConflictFix
	for i := 0; i < 5; i++ {
		fixes = append(fixes, ConflictFix{MergeCommit: "m", Files: []ConflictFixFile{
			{Filename: fmt.Sprintf("f%d.bin", i), PatchUnavailable: true}, {Filename: "ok.go", Patch: "x"}, {Filename: "f0.bin", PatchUnavailable: true}}})
	}
	sc := &j5FixScope{UnreadableFiles: unreadableConflictFiles(fixes)}
	gaps := sc.inputGaps()
	if len(gaps) != 1 || gaps[0].Key != "patch_unavailable_overridden_from" {
		t.Fatalf("gaps = %+v", gaps)
	}
	for _, want := range []string{"f0.bin、f1.bin、f2.bin", "ほか 2 件"} {
		if !strings.Contains(gaps[0].Reason, want) {
			t.Errorf("reason %q lacks %q", gaps[0].Reason, want)
		}
	}
	if strings.Contains(gaps[0].Reason, "f3.bin") || strings.Contains(gaps[0].Reason, "ok.go") {
		t.Errorf("reason %q names too many files", gaps[0].Reason)
	}
	if got := (&j5FixScope{}).inputGaps(); len(got) != 0 {
		t.Errorf("gaps of a clean scope = %+v", got)
	}
}
