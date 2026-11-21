package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S3a の文書の受入基準（#853。docs/features/core-redesign.md の「文書」の節）を、基準ごとに文書の本文で確かめる。
// AC-383・AC-384（claude-harness#388 の版と組み合わせた通し 2 本）と AC-385（ストアの写しでの起動）は、テストにせず、
// オーケストレーターが区切りの承認の前に行う確かめで、手順と結果の置き場は PR の説明に書く。
// AC-386（make gate が終了コード 0）は、テストで確かめず、make gate の実行で確かめる。

const (
	m3ContractHeading = "### 3. 接続ツールとの契約（§10。外部システム連携）【決定 2026-09-28 オーナー M3H2】"
	m3SecondHalf      = "##### 後半の流れ（決定 2026-11-12 オーナー。Issue #550）"
	m45ReflectHeading = "### 承認済みの `release` の実行 [S1]"
	archSection10     = "## 10. 接続ツールとの契約（D7・D8）"
	archSection21     = "## 21. 設計の見直し（2026-11-17）: 新しい形"
)

var s3aPlaceholders = []string{"`{task_file}`", "`{task_id}`", "`{uses_integration}`", "`{approved_ref}`", "`{artifact_ref}`"}

// firstLineAfter は、見出しの直後の、最初の空でない行を返す。
func firstLineAfter(t *testing.T, doc, heading string) string {
	t.Helper()
	for _, l := range strings.Split(strings.TrimLeft(sectionBody(t, doc, heading), "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	t.Fatalf("section %q is empty", heading)
	return ""
}

// docTableRow は、文書の表の、prefix で始まる行をちょうど 1 つ返す。
func docTableRow(t *testing.T, doc, prefix string) string {
	t.Helper()
	var rows []string
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, prefix) {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly one row starting with %q, got %d", prefix, len(rows))
	}
	return rows[0]
}

// AC-368 #853: m3 の「接続ツールとの契約」の節に、S3a で足した差し込みの名前がある。
func TestSpec_AC368_M3ContractHasS3aPlaceholders(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), m3ContractHeading)
	for _, p := range s3aPlaceholders {
		if !strings.Contains(body, p) {
			t.Errorf("AC-368: the contract section of m3 does not mention %s", p)
		}
	}
}

// AC-369 #853: 同じ節に、land・role・facts の語がある。
func TestSpec_AC369_M3ContractHasLandRoleFacts(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), m3ContractHeading)
	for _, w := range []string{"`land`", "`role`", "`facts`"} {
		if !strings.Contains(body, w) {
			t.Errorf("AC-369: the contract section of m3 does not mention %s", w)
		}
	}
}

// AC-370 #853: m3 の「後半の流れ」に、flywheel が ready を返さなくなった旨がある。
func TestSpec_AC370_M3SecondHalfSaysNoMoreReady(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), m3SecondHalf)
	if !strings.Contains(body, "`ready` を返さなくなった") {
		t.Error("AC-370: the second-half section of m3 does not say that flywheel no longer returns `ready`")
	}
}

// AC-371 #853: 同じ節に、完了の課題の契約の実行を、反映の段が読む旨がある。
func TestSpec_AC371_M3SecondHalfSaysReflectStageReadsRun(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), m3SecondHalf)
	if !strings.Contains(body, "反映の段が、完了の課題の契約の実行を先に読み") {
		t.Error("AC-371: the second-half section of m3 does not say that the reflect stage reads the completed challenge's contract run")
	}
}

// AC-372 #853: m4.5 の、反映の実行の節の見出しの直後に、S3a で置き換えた旨と core-redesign.md が正本である旨がある。
func TestSpec_AC372_M45ReflectSectionNote(t *testing.T) {
	first := firstLineAfter(t, readRepoDoc(t, "docs", "features", "m4.5-learning-and-self-improvement.md"), m45ReflectHeading)
	if !strings.Contains(first, "S3a で置き換えた") {
		t.Errorf("AC-372: the line after the heading does not say S3a replaced the section: %q", first)
	}
	if !strings.Contains(first, "docs/features/core-redesign.md") || !strings.Contains(first, "正本") {
		t.Errorf("AC-372: the line after the heading does not say core-redesign.md is the source of truth: %q", first)
	}
}

// AC-373 #853: m1 の遷移表の T18 の戻り先が、検証中である。
func TestSpec_AC373_M1T18ReturnsToVerifying(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "docs", "features", "m1-core.md"), "| T18 |")
	cols := strings.Split(strings.Trim(row, "| "), " | ")
	if len(cols) < 5 || cols[3] != "検証中" {
		t.Errorf("AC-373: the destination of T18 = %q, want 検証中 (row: %q)", cols, row)
	}
}

// AC-374 #853: architecture.md の §10 に、S3a で契約に足した項目の名前がある。
func TestSpec_AC374_ArchSection10HasS3aItems(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection10)
	for _, w := range append([]string{"`land`", "`role`", "`facts`"}, s3aPlaceholders...) {
		if !strings.Contains(body, w) {
			t.Errorf("AC-374: §10 does not mention %s", w)
		}
	}
}

// AC-375 #853: architecture.md の §10 に、core-redesign.md への参照がある。
func TestSpec_AC375_ArchSection10RefersToCoreRedesign(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection10)
	if !strings.Contains(body, "features/core-redesign.md") {
		t.Error("AC-375: §10 does not refer to features/core-redesign.md")
	}
}

// AC-376 #853: architecture.md の §21 の冒頭の注記に、区切り S3a で実装した範囲がある。
func TestSpec_AC376_ArchSection21NoteHasS3a(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection21)
	first := strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 2)[0]
	if !strings.Contains(first, "区切り S3a") || !strings.Contains(first, "区切り S3a で実装した範囲は") {
		t.Error("AC-376: the opening note of §21 does not list the range implemented in S3a")
	}
}

// AC-377 #853: architecture.md の §21.3 に、作業ツリーの持ち主のキーを S3a では足さなかった旨の注記がある。
func TestSpec_AC377_ArchSection213NoteNoSlotOwner(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "### 21.3 委譲先との境界（D24・D25・D31〜D33）")
	if !strings.Contains(body, "S3a では足さなかった") || !strings.Contains(body, "`slot_owner`") {
		t.Error("AC-377: §21.3 does not say that `slot_owner` was not added in S3a")
	}
}

// AC-378 #853: 用語集の「反映」の行の「いつから」に、委譲先が実行する形が S3a で入った旨がある。
func TestSpec_AC378_GlossaryReflectSinceS3a(t *testing.T) {
	cols := glossaryRow(t, "反映")
	if !strings.Contains(cols[4], "委譲先が実行する形は、S3a で入った") {
		t.Errorf("AC-378: since = %q, want it to say the delegate-executed form came in with S3a", cols[4])
	}
}

// AC-379 #853: 用語集の「反映」の行に、コンフリクトの解消を承認し直さずに通す形は先の区切りから、の旨がある（S3c〔#926〕で、S3c で入ったへ改めた）。
func TestSpec_AC379_GlossaryReflectConflictFromLaterSlice(t *testing.T) {
	cols := glossaryRow(t, "反映")
	if !strings.Contains(strings.Join(cols, " | "), "コンフリクトの解消を承認し直さずに通す形は、S3c で入った") {
		t.Errorf("AC-379: the 反映 row does not say the no-reapproval conflict resolution form came in with S3c: %q", cols)
	}
}

// AC-380 #853: 用語集の「反映先」の行の「いつから」に、S3a で入った旨がある。
func TestSpec_AC380_GlossaryReflectTargetSinceS3a(t *testing.T) {
	cols := glossaryRow(t, "反映先")
	if !strings.Contains(cols[4], "S3a で入った") {
		t.Errorf("AC-380: since = %q, want it to say it came in with S3a", cols[4])
	}
}

// AC-381 #853: CLAUDE.md のパッケージの表の internal/adapters/github の行に、語 gh pr merge と update-branch が無い。
func TestSpec_AC381_ClaudeMDGithubRowHasNoMerge(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "CLAUDE.md"), "| `internal/adapters/github` |")
	for _, w := range []string{"gh pr merge", "update-branch"} {
		if strings.Contains(row, w) {
			t.Errorf("AC-381: the internal/adapters/github row still has %q", w)
		}
	}
}

// AC-382 #853: CLAUDE.md のパッケージの表の internal/invoker の行に、form: plugin の委譲では J3 を呼ばない旨がある。
func TestSpec_AC382_ClaudeMDInvokerRowNoJ3ForPlugin(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "CLAUDE.md"), "| `internal/invoker` |")
	if !strings.Contains(row, "`form: plugin` の委譲では J3 を呼ばない") {
		t.Error("AC-382: the internal/invoker row does not say that J3 is not called for form: plugin delegation")
	}
}

// 以下は、レビューでの追加の直し（#853）。core-redesign.md の記述を、S3a の実装（flywheel#864・#865）に合わせた行があることを確かめる。

// docLineWith は、文書の行のうち、all の語をすべて含む最初の行を返す。
func docLineWith(t *testing.T, doc string, all ...string) string {
	t.Helper()
	for _, l := range strings.Split(doc, "\n") {
		ok := true
		for _, w := range all {
			if !strings.Contains(l, w) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no line contains all of %q", all)
	return ""
}

// 片付けの結果（tidied[]）の記述に、action の値 skipped（理由は wait_reason）と cancel_note がある（flywheel#864）。
func TestSpec_S3a_CoreRedesignTidiedHasSkippedAndCancelNote(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	docLineWith(t, doc, "`tidied[]`", "`action`", "`skipped`")
	docLineWith(t, doc, "- `skipped`:", "`wait_reason`")
	docLineWith(t, doc, "- `cancel_note`:", "`closed`", "出さない")
}

// 反映の依頼の起動の受入基準が、応答がすでに終わった状態のときに、終わった契約の実行の行が残る形になっている。
func TestSpec_S3a_CoreRedesignReflectFailedKeepsEndedRun(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	if strings.Contains(doc, "契約の実行の記録は作られない。") || strings.Contains(doc, "契約の実行は作らない。") {
		t.Error("core-redesign.md still says that no contract run is recorded when commands.reflect fails")
	}
	docLineWith(t, doc, "`commands.reflect` が失敗したとき", "`reflect_failed`", "`failed`・`cancelled`・`succeeded`", "終わった契約の実行の行は残る")
}

// flywheel#865 で足した 2 つの確かめ（始める書き込み・完了から戻す書き込みの中の確かめ直し）と、起動の前に分かる結果の route の読みがある。
func TestSpec_S3a_CoreRedesignHasRechecksAndNullRoute(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	docLineWith(t, doc, "反映の依頼を始める書き込み", "承認済みのまま", "確かめ直す")
	docLineWith(t, doc, "T18 で戻す書き込みの中で", "終わっていない反映の依頼が無いこと", "確かめ直す", "`conflict`")
	docLineWith(t, doc, "起動する前に分かる `unsupported`・`upstream_failed`", "`route` は null")
}

// 前の区切りで足した形のうち、試験が正本として読む節は、消していない。
func TestSpec_S3a_CoreRedesignKeepsEarlierShapesSection(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	if !strings.Contains(doc, "前の区切りで足した形のうち、試験がこの文書の記述を正本として読むもの（S3a では変えない）:") {
		t.Error("core-redesign.md lost the section of earlier shapes that tests read as the source of truth")
	}
}
