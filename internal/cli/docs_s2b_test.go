package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、区切り S2b の文書の受入基準（#780。docs/features/core-redesign.md の「文書」の節）を、基準ごとに文書の本文で確かめる。
// AC-323（make gate が終了コード 0）は、テストで確かめず、make gate の実行で確かめる。

func readRepoDoc(t *testing.T, rel ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// glossaryRow は用語集の表の、語（先頭の列）が term の行を、列に分けて返す（先頭と末尾の空の列は除く）。
func glossaryRow(t *testing.T, term string) []string {
	t.Helper()
	for _, line := range strings.Split(readRepoDoc(t, "docs", "glossary.md"), "\n") {
		if !strings.HasPrefix(line, "| "+term+" |") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "| "), " | ")
		if len(cols) != 5 {
			t.Fatalf("glossary row %q has %d columns, want 5", term, len(cols))
		}
		return cols
	}
	t.Fatalf("glossary has no row for %q", term)
	return nil
}

// sectionBody は、見出しの行が heading の節の本文（次の同じ深さ以上の見出しまで）を返す。
func sectionBody(t *testing.T, doc, heading string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	depth := len(heading) - len(strings.TrimLeft(heading, "#"))
	var out []string
	in := false
	for _, l := range lines {
		if !in {
			in = l == heading
			continue
		}
		if strings.HasPrefix(l, "#") {
			d := len(l) - len(strings.TrimLeft(l, "#"))
			if d <= depth {
				break
			}
		}
		out = append(out, l)
	}
	if !in {
		t.Fatalf("no heading %q", heading)
	}
	return strings.Join(out, "\n")
}

// AC-306 #780: 学びの判断点のフォローアップを定める節に、課題を起票しなくなった旨と core-redesign.md への参照がある。
func TestSpec_AC306_M45FollowupSectionSaysNoLongerFilesChallenges(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "m4.5-learning-and-self-improvement.md"), "### S3: フォローアップ")
	if !strings.Contains(body, "起票しなくなった") {
		t.Error("AC-306: the follow-up section does not say that challenges are no longer filed")
	}
	if !strings.Contains(body, "core-redesign.md") {
		t.Error("AC-306: the follow-up section does not refer to core-redesign.md")
	}
}

// AC-314 #780: 用語集の「フォローアップ」の行の「コードでの名前」が followup で、「いつから」に S2b で入った範囲がある。
func TestSpec_AC314_GlossaryFollowupRow(t *testing.T) {
	cols := glossaryRow(t, "フォローアップ")
	if !strings.HasPrefix(cols[3], "`followup`") {
		t.Errorf("AC-314: code name = %q, want it to start with `followup`", cols[3])
	}
	if !strings.Contains(cols[4], "S2b で入った範囲") {
		t.Errorf("AC-314: since = %q, want the range that S2b brought", cols[4])
	}
}

// AC-315 #780: 用語集に「取り込み元の子」の行がある。
func TestSpec_AC315_GlossaryHasSourceChildRow(t *testing.T) {
	cols := glossaryRow(t, "取り込み元の子")
	if !strings.Contains(cols[3], "source_child") {
		t.Errorf("AC-315: code name = %q, want source_child", cols[3])
	}
}

// AC-316 #780: 用語集に「取り込めるもの」の行がある。
func TestSpec_AC316_GlossaryHasIngestableRow(t *testing.T) {
	cols := glossaryRow(t, "取り込めるもの")
	if !strings.Contains(cols[3], "ingestable") {
		t.Errorf("AC-316: code name = %q, want ingestable", cols[3])
	}
}

// AC-317 #780: 用語集の「課題」の行に、取り込み元の子を、その課題のタスクの初期値として記録する旨がある。
func TestSpec_AC317_GlossaryChallengeRowRecordsChildrenAsTaskInitialValue(t *testing.T) {
	cols := glossaryRow(t, "課題")
	if !strings.Contains(cols[1], "タスクの初期値として記録") {
		t.Errorf("AC-317: meaning = %q, want it to say that the children are recorded as the initial value of the challenge's tasks", cols[1])
	}
}

// AC-318 #780: 用語集の「サイクル」の行の「いつから」に、進められるものが 0 件なら始めない形が S2b で入った旨がある。
func TestSpec_AC318_GlossaryCycleRowSince(t *testing.T) {
	since := glossaryRow(t, "サイクル")[4]
	if !strings.Contains(since, "S2b") || !strings.Contains(since, "0 件") || !strings.Contains(since, "始めない") {
		t.Errorf("AC-318: since = %q, want S2b and 'not starting at 0 items'", since)
	}
}

// AC-319 #780: 用語集の「オーケストレーター」の行の「いつから」に、いま進められることを読む口（flywheel status）が S2b で入った旨がある。
func TestSpec_AC319_GlossaryOrchestratorRowSince(t *testing.T) {
	since := glossaryRow(t, "オーケストレーター")[4]
	if !strings.Contains(since, "読む口〔`flywheel status`〕は S2b で入った") {
		t.Errorf("AC-319: since = %q, want S2b and `flywheel status`", since)
	}
}

// AC-322 #780: 設計書 §21 の冒頭の注記に、区切り S2b で実装した範囲がある。
func TestSpec_AC322_ArchitectureSection21NoteHasS2b(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "## 21. 設計の見直し（2026-11-17）: 新しい形")
	// 冒頭の注記 = 最初の小節（### ）より前の本文。
	head, _, _ := strings.Cut(body, "\n### ")
	if !strings.Contains(head, "S2b で実装した範囲") {
		t.Error("AC-322: the opening note of §21 does not state the range implemented in S2b")
	}
	for _, want := range []string{"followup", "source_child", "スキーマ版 27"} {
		if !strings.Contains(head, want) {
			t.Errorf("AC-322: the opening note of §21 lacks %q", want)
		}
	}
}

// sectionBodyByPrefix は、見出しの行が prefix で始まる節の本文を返す（見出しの後ろに決定の印が付く文書のため）。
func sectionBodyByPrefix(t *testing.T, doc, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, prefix) {
			return sectionBody(t, doc, l)
		}
	}
	t.Fatalf("no heading starting with %q", prefix)
	return ""
}

// AC-304（#775 の文書の確かめの補い）: m2 の「取り込みの対象」の節に、取り込みが新しい課題を自動では作らなくなった旨と、
// core-redesign.md への参照がある。
func TestSpec_AC304_M2IngestTargetSectionSaysNoAutomaticChallenges(t *testing.T) {
	t.Parallel()
	body := sectionBodyByPrefix(t, readRepoDoc(t, "docs", "features", "m2-github-issue-ingest.md"), "### 取り込みの対象")
	if !strings.Contains(body, "core-redesign.md") {
		t.Error("AC-304: the section does not refer to core-redesign.md")
	}
	if !strings.Contains(body, "自動") || !strings.Contains(body, "課題に") {
		t.Errorf("AC-304: the section does not say that ingest no longer creates challenges automatically:\n%s", body)
	}
}

// AC-305: m2 の「実行の起点」の節に、選んで取り込む口（flywheel ingest <指定>...）がある。
func TestSpec_AC305_M2EntryPointSectionHasTheSelectiveIngest(t *testing.T) {
	t.Parallel()
	body := sectionBodyByPrefix(t, readRepoDoc(t, "docs", "features", "m2-github-issue-ingest.md"), "### 実行の起点")
	if !strings.Contains(body, "`flywheel ingest <指定>...") {
		t.Errorf("AC-305: the section lacks `flywheel ingest <指定>...`:\n%s", body)
	}
}

// worklogDocLine は、m1-core.md の作業ログの説明のうち、キー key（entity・entity_id・action）を説明する行を返す。
func worklogDocLine(t *testing.T, key string) string {
	t.Helper()
	for _, l := range strings.Split(readRepoDoc(t, "docs", "features", "m1-core.md"), "\n") {
		if strings.HasPrefix(l, "| `"+key+"` |") {
			return l
		}
	}
	t.Fatalf("m1-core.md has no worklog row for %q", key)
	return ""
}

// AC-309: m1-core.md の作業ログの entity の一覧に、followup がある。
func TestSpec_AC309_M1CoreWorklogEntityListHasFollowup(t *testing.T) {
	t.Parallel()
	if l := worklogDocLine(t, "entity"); !strings.Contains(l, "`followup`") {
		t.Errorf("AC-309: the entity row lacks `followup`: %s", l)
	}
}

// AC-310: m1-core.md の作業ログの entity_id の説明に、F-<番号> の形がある。
func TestSpec_AC310_M1CoreWorklogEntityIDHasTheFollowupIDShape(t *testing.T) {
	t.Parallel()
	if l := worklogDocLine(t, "entity_id"); !strings.Contains(l, `"F-<n>"`) {
		t.Errorf("AC-310: the entity_id row lacks the F-<n> shape: %s", l)
	}
}

// AC-311: m1-core.md の作業ログの action の一覧に、followup_done・followup_drop・followup_promote がある。
func TestSpec_AC311_M1CoreWorklogActionListHasTheFollowupActions(t *testing.T) {
	t.Parallel()
	l := worklogDocLine(t, "action")
	for _, a := range []string{"`followup_done`", "`followup_drop`", "`followup_promote`"} {
		if !strings.Contains(l, a) {
			t.Errorf("AC-311: the action row lacks %s: %s", a, l)
		}
	}
}
