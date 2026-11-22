package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S4a の文書の受入基準（#996。AC-190〜AC-197）を、文書の本文で確かめる。

func archS21First(t *testing.T) string {
	t.Helper()
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection21)
	return strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 2)[0]
}

// AC-190: architecture.md の §21 の実装済みの範囲の記述に、区切り S4a の範囲がある。
func TestSpec_AC190_ArchSection21NoteHasS4a(t *testing.T) {
	first := archS21First(t)
	for _, want := range []string{"区切り S4a〔長く走るサイクル〕の範囲も実装済み", "区切り S4a で実装した範囲は"} {
		if !strings.Contains(first, want) {
			t.Errorf("AC-190: the opening note of §21 lacks %q", want)
		}
	}
	if strings.Contains(first, "まだ実装していない") {
		t.Error("AC-190: §21 still says S4a is not implemented")
	}
}

// AC-191: 同じ記述に、区切り S3d〔画面〕の範囲も実装済みがある。
func TestSpec_AC191_ArchSection21NoteHasS3dImplemented(t *testing.T) {
	if !strings.Contains(archS21First(t), "区切り S3d〔画面〕の範囲も実装済み") {
		t.Error("AC-191: the opening note of §21 lacks the S3d implemented note")
	}
}

// AC-192: 用語集に、「回り」の行がある。
func TestSpec_AC192_GlossaryHasRound(t *testing.T) {
	cols := glossaryRow(t, "回り")
	if !strings.Contains(cols[1], "進める段") {
		t.Errorf("AC-192: the definition = %q", cols[1])
	}
}

// AC-193: 用語集の「サイクル」の行に、「回り」の語がある。
func TestSpec_AC193_GlossaryCycleMentionsRound(t *testing.T) {
	cols := glossaryRow(t, "サイクル")
	if !strings.Contains(cols[1], "回り") {
		t.Errorf("AC-193: the definition = %q", cols[1])
	}
}

// AC-194: m3-invoker-delegation.md の宣言の項目の一覧に、cycle_poll_interval_sec がある。
func TestSpec_AC194_M3DeclarationHasPollInterval(t *testing.T) {
	docLineWith(t, sectionBody(t, m3Doc(t), "#### `.flywheel/agent.json`【決定 2026-09-28 親 M3P1・M3P9】"), "- `cycle_poll_interval_sec`", "既定 30")
}

// AC-195: 同じ一覧に、auto_cycle がある。
func TestSpec_AC195_M3DeclarationHasAutoCycle(t *testing.T) {
	docLineWith(t, sectionBody(t, m3Doc(t), "#### `.flywheel/agent.json`【決定 2026-09-28 親 M3P1・M3P9】"), "`auto_cycle`", "既定は真", "`--no-cycle`")
}

// AC-196: m1-core.md の、サイクルの記録の説明に、end_reason がある。
func TestSpec_AC196_M1CycleRecordHasEndReason(t *testing.T) {
	docLineWith(t, readRepoDoc(t, "docs", "features", "m1-core.md"), "サイクルの記録", "`end_reason`", "`settled`", "`interrupted`")
}

// AC-197: 同じ説明に、rounds がある。
func TestSpec_AC197_M1CycleRecordHasRounds(t *testing.T) {
	docLineWith(t, readRepoDoc(t, "docs", "features", "m1-core.md"), "サイクルの記録", "`rounds`", "`trigger`", "自由な文字列のまま")
}
