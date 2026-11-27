package core

// 区切りを分ける判断点の出力の写し（single・split の証跡と分けるかの保留）を、実ストアで検証する
// （docs/features/request-to-plan-s3.md）。AC の番号は親の Issue の受入基準の並び順。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// j10HookInvoker は、区切りを分ける判断点が出力を返す前に hook を呼ぶ偽の判断の IF。
type j10HookInvoker struct {
	*j10Invoker
	hook func()
}

func (h j10HookInvoker) InvokeJudgment(ctx context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	if in.Judgment == JudgmentJ10 && h.hook != nil {
		h.hook()
	}
	return h.j10Invoker.InvokeJudgment(ctx, in)
}

// applyTestSlices は、項目ごとに見分けられる値を持つ 3 つの区切り。
func applyTestSlices() []any {
	slice := func(n, repo string, deps ...int) map[string]any {
		if deps == nil {
			deps = []int{}
		}
		return map[string]any{"title": "TITLE-" + n, "headline": "HEADLINE-" + n, "context": "CONTEXT-" + n,
			"summary": "SUMMARY-" + n, "scope": "SCOPE-" + n, "repo": repo, "depends_on": deps}
	}
	return []any{slice("1", "app"), slice("2", "lib", 1), slice("3", "app", 1, 2)}
}

func applySplitOutput(t *testing.T) []byte {
	return j10TestOutput(t, func(m map[string]any) {
		m["reason"] = "SPLIT-REASON"
		m["slices"] = applyTestSlices()
		m["evidence"] = []any{
			map[string]any{"path": "docs/a.md", "note": "NOTE-A"},
			map[string]any{"path": "docs/b.md", "note": "NOTE-B"},
		}
	})
}

// judge は pending の課題に、出力 output を返す区切りを分ける判断点を 1 回呼ぶ。
func (f *j10Fixture) judge(t *testing.T, output []byte, hook func()) *J1AutoResult {
	t.Helper()
	inv := j10HookInvoker{j10Invoker: &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ10: output}}, hook: hook}
	return f.classify(t, inv, f.conn, nil)
}

func (f *j10Fixture) evidenceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.s.Workspace(), requirementEvidenceDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// activities は種類 action の作業ログの after を返す。
func (f *j10Fixture) activities(t *testing.T, action string) []map[string]any {
	t.Helper()
	all, err := f.s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, a := range all {
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

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- 判断の間に変わったとき ---

// wantDiscarded は、split の出力が捨てられた（保留も証跡のファイルも無く、記録が state のまま）ことを確かめる。
func wantDiscarded(t *testing.T, f *j10Fixture, id string, state SliceSplitState) {
	t.Helper()
	d := f.detail(t, id)
	if len(d.Holds) != 0 {
		t.Errorf("holds = %+v, want none", d.Holds)
	}
	if got := f.evidenceFiles(t); len(got) != 0 {
		t.Errorf("evidence files = %v, want none", got)
	}
	if d.SliceSplit.State != state || d.SliceSplit.HoldID != nil || d.SliceSplit.EvidencePath != nil || len(d.SliceSplit.Slices) != 0 {
		t.Errorf("slice_split = %+v, want state %s without a proposal", d.SliceSplit, state)
	}
}

// AC-36・37・38。
func TestApplyJ10_ChallengeEditedDuringJudgment_DiscardsAndCallsAgain(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	res := f.judge(t, applySplitOutput(t), func() {
		if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Description: strPtr("EDITED-DESCRIPTION")}); err != nil {
			t.Errorf("EditChallenge: %v", err)
		}
	})
	wantDiscarded(t, f, id, SliceSplitStatePending)
	if len(res.Items) != 1 || res.Items[0].Status != nil {
		t.Errorf("items = %+v, want one item without a mapped status", res.Items)
	}

	inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ10: j10SingleOutput(t)}}
	f.classify(t, inv, f.conn, nil)
	calls := inv.callsFor(JudgmentJ10)
	if len(calls) != 1 {
		t.Fatalf("J10 calls in the next round = %d, want 1", len(calls))
	}
	var challenge string
	for _, sec := range calls[0].Sections {
		if sec.Label == "課題" {
			challenge = sec.Content
		}
	}
	if !strings.Contains(challenge, "EDITED-DESCRIPTION") {
		t.Errorf("section 課題 = %q, want the edited description", challenge)
	}
}

// AC-39・40・41。
func TestApplyJ10_ChallengeWithdrawnDuringJudgment_Discards(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	f.judge(t, applySplitOutput(t), func() {
		if _, err := withdrawForTest(t, f.s, id, "r"); err != nil {
			t.Errorf("withdraw: %v", err)
		}
	})
	wantDiscarded(t, f, id, SliceSplitStatePending)
	if got := f.detail(t, id).Status; got != StatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", got)
	}
}

// AC-42・43・44。
func TestApplyJ10_RecordNoLongerPending_Discards(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	f.judge(t, applySplitOutput(t), func() { f.setSplitState(t, id, SliceSplitStateSingle) })
	wantDiscarded(t, f, id, SliceSplitStateSingle)
}

// 版が同じでも、分類済でなくなっていれば single の出力も写さない。記録が pending でなければ作業ログを残さない。
func TestApplyJ10_SingleIsDiscardedWhenNotEligible(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(t *testing.T, f *j10Fixture, id string){
		"版が上がった": func(t *testing.T, f *j10Fixture, id string) {
			if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Description: strPtr("EDITED")}); err != nil {
				t.Errorf("EditChallenge: %v", err)
			}
		},
		"分類済でない": func(t *testing.T, f *j10Fixture, id string) {
			f.write(t, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(StatusInProgress), mustChallengeInternalID(t, id))
				return err
			})
		},
		"記録が awaiting_owner": func(t *testing.T, f *j10Fixture, id string) {
			f.setSplitState(t, id, SliceSplitStateAwaitingOwner)
		},
	} {
		f := newJ10Fixture(t)
		id := f.pending(t)
		f.judge(t, j10SingleOutput(t), func() { change(t, f, id) })
		if got := f.splitState(t, id); got == SliceSplitStateSingle {
			t.Errorf("%s: slice_split.state = %s, want unchanged", name, got)
		}
		if got := f.activities(t, ActivityActionSliceSplitSingle); len(got) != 0 {
			t.Errorf("%s: slice_split_single = %v, want none", name, got)
		}
	}
}

// --- single ---

// AC-65〜68。
func TestApplyJ10_Single(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	res := f.judge(t, j10SingleOutput(t), nil)

	d := f.detail(t, id)
	if d.SliceSplit.State != SliceSplitStateSingle { // AC-65
		t.Errorf("slice_split.state = %s, want single", d.SliceSplit.State)
	}
	if d.Status != StatusClassified || len(d.Holds) != 0 { // AC-66
		t.Errorf("status = %s holds = %+v, want classified without holds", d.Status, d.Holds)
	}
	got := f.activities(t, ActivityActionSliceSplitSingle) // AC-67
	if ActivityActionSliceSplitSingle != "slice_split_single" || len(got) != 1 || got[0]["by"] != "judgment" {
		t.Errorf("slice_split_single = %v, want one by judgment", got)
	}
	if files := f.evidenceFiles(t); len(files) != 0 { // AC-68
		t.Errorf("evidence files = %v, want none", files)
	}
	if len(res.Items) != 1 || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusClassified) {
		t.Errorf("items = %+v", res.Items)
	}
}

// --- split の証跡 ---

// AC-69〜79。
func TestApplyJ10_SplitWritesTheEvidenceFile(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	f.judge(t, applySplitOutput(t), nil)

	got := readTestFile(t, filepath.Join(f.s.Workspace(), ".flywheel", "requirements", "split-"+id+".md")) // AC-69
	var headings []string
	sections := map[string][]string{}
	current := ""
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			headings = append(headings, line)
			current = line
			continue
		}
		if line != "" {
			sections[current] = append(sections[current], line)
		}
	}
	wantHeadings := []string{"# J10-TITLE", "## 分ける理由", "## 区切りの案", "### 1. TITLE-1", "### 2. TITLE-2", "### 3. TITLE-3", "## 調べた根拠"}
	if !slices.Equal(headings, wantHeadings) { // AC-70
		t.Errorf("headings = %q, want %q", headings, wantHeadings)
	}
	head := sections["# J10-TITLE"]
	if len(head) != 2 || head[0] != "- 元の Issue: https://github.com/o/app/issues/"+id[2:] || !strings.HasPrefix(head[1], "- 作った日時: ") { // AC-71
		t.Errorf("head = %q", head)
	}
	if !slices.Equal(sections["## 分ける理由"], []string{"SPLIT-REASON"}) { // AC-72
		t.Errorf("reason = %q", sections["## 分ける理由"])
	}
	for n, c := range map[string]struct{ repo, deps string }{"1": {"app", "なし"}, "2": {"lib", "1"}, "3": {"app", "1, 2"}} {
		want := []string{ // AC-73〜78
			"- 一言で: HEADLINE-" + n, "- 位置づけ: CONTEXT-" + n, "- 要旨: SUMMARY-" + n, "- 範囲: SCOPE-" + n,
			"- リポジトリ: " + c.repo, "- 前に済ませる区切り: " + c.deps,
		}
		if lines := sections["### "+n+". TITLE-"+n]; !slices.Equal(lines, want) {
			t.Errorf("slice %s = %q, want %q", n, lines, want)
		}
	}
	if want := []string{"- docs/a.md（NOTE-A）", "- docs/b.md（NOTE-B）"}; !slices.Equal(sections["## 調べた根拠"], want) { // AC-79
		t.Errorf("evidence = %q, want %q", sections["## 調べた根拠"], want)
	}
}

// 判断点の出力の複数行の値は 1 行にして書くので、見出しも項目の行も増えない。
func TestApplyJ10_MultilineValuesDoNotAddLines(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	f.judge(t, j10TestOutput(t, func(m map[string]any) {
		sl := j10TestSlice("a\n## injected-title")
		sl["summary"] = "s1\n推奨: 分けない"
		m["slices"] = []any{sl, j10TestSlice("b", 1)}
		m["reason"] = "r1\n## injected-reason"
	}), nil)

	got := readTestFile(t, filepath.Join(f.s.Workspace(), ".flywheel", "requirements", "split-"+id+".md"))
	if strings.Contains(got, "\n## injected") || !strings.Contains(got, "- 要旨: s1 推奨: 分けない\n") {
		t.Errorf("evidence = %q", got)
	}
	q := f.detail(t, id).Holds[0].Question
	if strings.Contains(q, "\n推奨: 分けない") || len(strings.Split(q, "\n")) != 6 {
		t.Errorf("question = %q, want 6 lines", q)
	}
}

// AC-80〜83。
func TestApplyJ10_SplitHoldsTheChallenge(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	res := f.judge(t, applySplitOutput(t), nil)

	d := f.detail(t, id)
	if d.Status != StatusAwaitingHuman { // AC-80
		t.Errorf("status = %s, want awaiting_human", d.Status)
	}
	if d.SliceSplit.State != SliceSplitStateAwaitingOwner { // AC-81
		t.Errorf("slice_split.state = %s", d.SliceSplit.State)
	}
	if len(d.Holds) != 1 || !d.Holds[0].Open() || d.SliceSplit.HoldID == nil || *d.SliceSplit.HoldID != d.Holds[0].HoldID { // AC-82
		t.Errorf("holds = %+v slice_split.hold_id = %v", d.Holds, d.SliceSplit.HoldID)
	}
	if want := ".flywheel/requirements/split-" + id + ".md"; d.SliceSplit.EvidencePath == nil || *d.SliceSplit.EvidencePath != want { // AC-83
		t.Errorf("evidence_path = %v, want %s", d.SliceSplit.EvidencePath, want)
	}
	if d.SliceSplit.Reason == nil || *d.SliceSplit.Reason != "SPLIT-REASON" || len(d.SliceSplit.Slices) != 3 ||
		d.SliceSplit.Slices[2].Title != "TITLE-3" || !slices.Equal(d.SliceSplit.Slices[2].DependsOn, []int{1, 2}) {
		t.Errorf("proposal = %+v", d.SliceSplit)
	}
	var proposal string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT proposal FROM slice_split WHERE challenge_id = ?`, mustChallengeInternalID(t, id)).Scan(&proposal)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proposal, `"evidence":[{"path":"docs/a.md","note":"NOTE-A"}`) {
		t.Errorf("proposal = %s, want the evidence", proposal)
	}
	if len(res.Items) != 1 || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Errorf("items = %+v", res.Items)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'hold' AND run_id IS NOT NULL AND channel = 'invoker'`); n != 1 {
		t.Errorf("hold activities of the judgment run = %d, want 1", n)
	}
}

// AC-84。2 回目に写しても行は重ならない。
func TestApplyJ10_SplitAddsTheRequirementsIgnoreLine(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	f.pending(t)
	path := filepath.Join(f.s.Workspace(), ".flywheel", ".gitignore")
	if err := os.WriteFile(path, []byte("flywheel.db\n# mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.judge(t, applySplitOutput(t), nil)
	if got, want := readTestFile(t, path), "flywheel.db\n# mine\nrequirements/\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
	f.pending(t)
	f.judge(t, applySplitOutput(t), nil)
	if got := strings.Count(readTestFile(t, path), "requirements/"); got != 1 {
		t.Errorf("requirements/ lines = %d, want 1", got)
	}
}

// 既定の .gitignore の内容に requirements/ の行がある。
func TestInit_NewGitignoreExcludesRequirementEvidence(t *testing.T) {
	_, dir := newWorkspaceStore(t)
	data := readTestFile(t, filepath.Join(dir, flywheelDirName, ".gitignore"))
	if !strings.Contains("\n"+data, "\nrequirements/\n") {
		t.Errorf(".gitignore lacks the requirements/ line:\n%s", data)
	}
}

// 書く先が通常のファイルなら、書き直す。
func TestApplyJ10_SplitOverwritesARegularFile(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	rel := requirementEvidenceDir + "/split-" + id + ".md"
	writeWorkspaceFile(t, f.s.Workspace(), rel, "OLD")
	f.judge(t, applySplitOutput(t), nil)
	if got := readTestFile(t, filepath.Join(f.s.Workspace(), rel)); !strings.HasPrefix(got, "# J10-TITLE\n") {
		t.Errorf("evidence = %q", got)
	}
	if files := f.evidenceFiles(t); !slices.Equal(files, []string{"split-" + id + ".md"}) {
		t.Errorf("evidence files = %v, want only the evidence", files)
	}
}

// AC-85〜92。書く先の確かめに落ちたら、書かず、保留も作らず、記録は pending のまま、作業ログを 1 件残す。
func TestApplyJ10_SplitEvidenceCannotBeWritten(t *testing.T) {
	t.Parallel()
	mkdir := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, p string) {
		t.Helper()
		mkdir(t, filepath.Dir(p))
		if err := os.WriteFile(p, []byte("KEEP"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(t *testing.T, target, at string) {
		t.Helper()
		mkdir(t, filepath.Dir(at))
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	// 各 setup は、中身が変わらないことを確かめるファイル（無ければ空）と、ファイルができないことを確かめるディレクトリ（同）を返す。
	for name, setup := range map[string]func(t *testing.T, ws, dir, dest string) (keep, empty string){
		// AC-85・86。
		"書く先がワークスペースの外のファイルを指すリンク": func(t *testing.T, _, _, dest string) (string, string) {
			outside := filepath.Join(t.TempDir(), "outside.md")
			write(t, outside)
			link(t, outside, dest)
			return outside, ""
		},
		// AC-87。
		"書く先が置き場の中の別のファイルを指すリンク": func(t *testing.T, _, dir, dest string) (string, string) {
			other := filepath.Join(dir, "RQ-9.md")
			write(t, other)
			link(t, other, dest)
			return other, ""
		},
		"書く先が置き場の中の無いファイルを指すリンク": func(t *testing.T, _, _, dest string) (string, string) {
			link(t, "RQ-9.md", dest)
			return "", ""
		},
		// AC-88。
		"置き場がワークスペースの外のディレクトリを指すリンク": func(t *testing.T, _, dir, _ string) (string, string) {
			outside := t.TempDir()
			link(t, outside, dir)
			return "", outside
		},
		"置き場がワークスペースの中の .flywheel の外を指すリンク": func(t *testing.T, ws, dir, _ string) (string, string) {
			outside := filepath.Join(ws, "docs")
			mkdir(t, outside)
			link(t, outside, dir)
			return "", outside
		},
		"置き場が .flywheel そのものを指すリンク": func(t *testing.T, _, dir, _ string) (string, string) {
			link(t, ".", dir)
			return "", ""
		},
		// AC-89。
		"書く先がディレクトリ": func(t *testing.T, _, _, dest string) (string, string) {
			mkdir(t, dest)
			return "", dest
		},
		// AC-90〜92。
		"置き場がファイルで塞がれている": func(t *testing.T, _, dir, _ string) (string, string) {
			write(t, dir)
			return dir, ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newJ10Fixture(t)
			id := f.pending(t)
			ws := f.s.Workspace()
			dir := filepath.Join(ws, requirementEvidenceDir)
			keep, empty := setup(t, ws, dir, filepath.Join(dir, "split-"+id+".md"))

			res := f.judge(t, applySplitOutput(t), nil)

			if keep != "" {
				if got := readTestFile(t, keep); got != "KEEP" {
					t.Errorf("%s = %q, want unchanged", keep, got)
				}
			}
			if empty != "" {
				if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
					t.Errorf("entries of %s = %v (err %v), want none", empty, entries, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(ws, ".flywheel", "RQ-9.md")); err == nil {
				t.Error("a file appeared directly under .flywheel")
			}
			if info, err := os.Stat(filepath.Join(dir, "split-"+id+".md")); err == nil && info.Mode().IsRegular() &&
				readTestFile(t, filepath.Join(dir, "split-"+id+".md")) != "KEEP" {
				t.Error("the evidence was written")
			}
			d := f.detail(t, id)
			if d.Status != StatusClassified || len(d.Holds) != 0 {
				t.Errorf("status = %s holds = %+v, want classified without holds", d.Status, d.Holds)
			}
			if d.SliceSplit.State != SliceSplitStatePending || d.SliceSplit.EvidencePath != nil || len(d.SliceSplit.Slices) != 0 {
				t.Errorf("slice_split = %+v, want pending without a proposal", d.SliceSplit)
			}
			got := f.activities(t, ActivityActionSliceSplitEvidenceFailed)
			if ActivityActionSliceSplitEvidenceFailed != "slice_split_evidence_failed" || len(got) != 1 ||
				got[0]["path"] != requirementEvidenceDir+"/split-"+id+".md" || got[0]["reason"] == "" {
				t.Errorf("slice_split_evidence_failed = %v, want one with the path and the reason", got)
			}
			if len(res.Items) != 1 || res.Items[0].Status != nil {
				t.Errorf("items = %+v, want one item without a mapped status", res.Items)
			}
		})
	}
}

// --- 分けるかの保留と問いの文 ---

// AC-93〜96。画面のボタンはどちらの選択肢も選べる。
func TestApplyJ10_SliceSplitHoldShape(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	f.decl.Supervisor = true
	id := f.pending(t)
	f.judge(t, applySplitOutput(t), nil)

	h := f.detail(t, id).Holds[0]
	want := []HoldOption{{ID: "split", Label: "この案で分ける"}, {ID: "single", Label: "分けずに 1 つの課題で進める"}}
	if !slices.Equal(h.Options, want) { // AC-93
		t.Errorf("options = %+v, want %+v", h.Options, want)
	}
	if !slices.Equal(h.OptionButtons, []bool{true, true}) {
		t.Errorf("option buttons = %v, want both true", h.OptionButtons)
	}
	if h.FromStatus != StatusClassified { // AC-95
		t.Errorf("from_status = %s, want classified", h.FromStatus)
	}
	for query, wantN := range map[string]int{
		`SELECT COUNT(*) FROM hold WHERE option_required = 1`:                                                     1, // AC-94
		`SELECT COUNT(*) FROM hold WHERE scope IS NULL AND task_id IS NULL`:                                       1, // AC-95
		`SELECT COUNT(*) FROM hold WHERE kind = 'human' AND stop_kind IS NULL AND supervisor_state IS NULL`:       1, // AC-96
		`SELECT COUNT(*) FROM hold h JOIN run r ON r.id = h.run_id WHERE r.judgment = 'J10' AND h.reason IS NULL`: 1,
	} {
		if n := f.count(t, query); n != wantN {
			t.Errorf("%s = %d, want %d", query, n, wantN)
		}
	}
	// AC-94: 識別の無い回答を断る。
	_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, Answer: "これで", ExpectedVersion: f.detail(t, id).Version},
		verifiedAttestationForTest(t, id))
	if err == nil || !strings.Contains(err.Error(), "--option") {
		t.Errorf("answer without an option: err = %v, want a refusal asking for --option", err)
	}
}

// AC-97〜103。
func TestApplyJ10_SliceSplitQuestion(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	f.judge(t, applySplitOutput(t), nil)

	got := f.detail(t, id).Holds[0].Question
	want := strings.Join([]string{
		"この課題は、区切り 1 つに収まらないと見ました。次の 3 つの課題に分ける案を作りました。推奨: この案で分ける", // AC-97
		"1. TITLE-1 — SUMMARY-1（リポジトリ: app）",        // AC-98〜100
		"2. TITLE-2 — SUMMARY-2（リポジトリ: lib。1 の後）",   // AC-101
		"3. TITLE-3 — SUMMARY-3（リポジトリ: app。1・2 の後）", // AC-101
		"分ける理由: SPLIT-REASON",                       // AC-102
		"調べた根拠: .flywheel/requirements/split-" + id + ".md",
		"案を直したいときは、この案で分けてから、対話の欄で課題を足す・取り下げる・依存を直してください", // AC-103
	}, "\n")
	if got != want {
		t.Errorf("question =\n%s\nwant\n%s", got, want)
	}
}

// 写した記録の状態の変化は、サイクルの回りの進みに数える。
func TestProgressFingerprint_IncludesSliceSplitState(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	before, err := f.s.progressFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.setSplitState(t, id, SliceSplitStateSingle)
	after, err := f.s.progressFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("the fingerprint did not change with slice_split.state")
	}
}

// --- 消す処理の置き場の確かめ ---

// 置き場そのものがシンボリックリンクで .flywheel の外を指すとき、要求の証跡も仕様の下書きも消さず、消せなかったと残す。
func TestCleanup_PlacementOutsideTheFlywheelDirIsNotRemoved(t *testing.T) {
	t.Parallel()
	for _, kind := range []struct {
		name, dir, action string
		clean             func(s *Store, rel string, cid int64)
	}{
		{"要求の証跡", requirementEvidenceDir, ActivityActionRequirementEvidenceCleanupFailed, func(s *Store, rel string, cid int64) {
			s.discardRequirementEvidence(context.Background(), rel, "challenge", cid, "tester")
		}},
		{"仕様の下書き", specDraftDir, ActivityActionSpecDraftCleanupFailed, func(s *Store, rel string, cid int64) {
			s.removeDraftAfterWrite(context.Background(), cid, "tester",
				func(context.Context, *sql.Tx) (string, error, error) { return rel, nil, nil })
		}},
	} {
		for place, target := range map[string]func(t *testing.T, ws string) string{
			"ワークスペースの外": func(t *testing.T, _ string) string { return t.TempDir() },
			"ワークスペースの中の .flywheel の外": func(t *testing.T, ws string) string {
				p := filepath.Join(ws, "docs")
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
		} {
			t.Run(kind.name+"/"+place, func(t *testing.T) {
				f := newJ10Fixture(t)
				id := f.ingested(t)
				ws := f.s.Workspace()
				outside := target(t, ws)
				file := filepath.Join(outside, "x.md")
				if err := os.WriteFile(file, []byte("KEEP"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(ws, kind.dir)); err != nil {
					t.Fatal(err)
				}
				kind.clean(f.s, kind.dir+"/x.md", mustChallengeInternalID(t, id))
				if got := readTestFile(t, file); got != "KEEP" {
					t.Errorf("the outside file = %q", got)
				}
				got := f.activities(t, kind.action)
				if len(got) != 1 || got[0]["path"] != kind.dir+"/x.md" {
					t.Errorf("%s = %v, want one", kind.action, got)
				}
			})
		}
	}
}
