package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// AC-265: docs/features/m1-core.md の遷移表に、T20 の行があり、コードの遷移表（core.Table）の行と一致する
// （遷移元・操作の種類・本人確認の要否。コードの遷移表の行の番号が、文書の表に 1 行ずつある。T16 は M4.5 の文書にある）。
func TestSpec_AC265_M1TransitionTableMatchesTheCodeTable(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m1-core.md")
	seen := map[string]bool{}
	for _, tr := range core.Table {
		if seen[tr.ID] || tr.ID == "T16" { // T16（取り下げ）の行は m1 の表に無く、M4.5 の文書にある。
			continue
		}
		seen[tr.ID] = true
		docTableRow(t, doc, "| "+tr.ID+" |")
	}
	row := docTableRow(t, doc, "| T20 |")
	cols := strings.Split(row, "|")
	if len(cols) < 6 {
		t.Fatalf("the T20 row has %d columns: %q", len(cols), row)
	}
	from, op, to, verification := cols[2], cols[3], cols[4], cols[5]
	tr, ok := core.LookupRestricted(core.StatusAwaitingHuman, core.OpSupervisorResolve)
	if !ok {
		t.Fatal("core.Table has no T20")
	}
	if label, _ := tr.From.Label(); !strings.Contains(from, label) || !strings.Contains(from, "pending") {
		t.Errorf("the source of T20 = %q, want %q and supervisor_state pending", from, label)
	}
	if !strings.Contains(op, string(core.OpSupervisorResolve)) {
		t.Errorf("the operation of T20 = %q, want %q", op, core.OpSupervisorResolve)
	}
	if !strings.Contains(to, "保留に入る直前の状態") {
		t.Errorf("the target of T20 = %q, want the status preceding the hold", to)
	}
	if tr.RequiresVerification || !strings.Contains(verification, "要らない") {
		t.Errorf("T20 verification: code requires=%v, doc %q; want a row that needs no verification", tr.RequiresVerification, verification)
	}
	// T12（人の回答）は、本人確認の要る行のまま。
	t12 := docTableRow(t, doc, "| T12 |")
	if c := strings.Split(t12, "|"); len(c) < 6 || !strings.HasPrefix(strings.TrimSpace(c[5]), "要") {
		t.Errorf("the T12 row of the doc must still require verification: %q", t12)
	}
	// 作業ログの操作の種類の一覧に supervisor_resolve がある。
	if !strings.Contains(doc, `\|`+"`supervisor_resolve`") && !strings.Contains(doc, "`supervisor_resolve`\\|") {
		t.Error("the list of activity actions in m1-core.md lacks supervisor_resolve")
	}
}

// このファイルの AC-258〜AC-267 は、区切り S4b-2 の文書の受入基準（#1084）を、文書の本文で確かめる。

const (
	archSection7   = "## 7. サイクルの再設計"
	archSection215 = "### 21.5 スーパーバイザー（SV）（D27）"
)

// AC-258: architecture.md の §21 の実装済みの範囲に、S4b-2 がある。
func TestSpec_AC258_ArchSection21NoteHasS4b2(t *testing.T) {
	first := archS21First(t)
	for _, want := range []string{"区切り S4b-2〔スーパーバイザーのその場の対処〕の範囲は実装済み", "区切り S4b-2 で実装した範囲は", "`supervisor_resolve`〔T20〕", "区切り S4b-3"} {
		if !strings.Contains(first, want) {
			t.Errorf("AC-258: the opening note of §21 lacks %q", want)
		}
	}
}

// AC-259: §21.5 に、手 4 を入れなかった旨の注記がある。手は 7 つで、コードの手の一覧と一致する。
func TestSpec_AC259_ArchSection215NotesHand4IsNotIncluded(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	line := docLineWith(t, body, "手 4 は入れなかった", "委譲先に片付けを頼んで、起動し直す")
	for _, h := range core.SupervisorHandValues() {
		if !strings.Contains(line, "`"+string(h)+"`") {
			t.Errorf("AC-259: the hand-4 note lacks the implemented hand %q: %q", h, line)
		}
	}
	if got, want := len(backticked(line)), len(core.SupervisorHandValues()); got != want+0 && !strings.Contains(line, "`answer`") {
		t.Errorf("AC-259: the note lists %d backticked words, want the %d hands", got, want)
	}
	if strings.Contains(line, "`answer`") && !strings.Contains(line, "区切り S4b-3") {
		t.Errorf("AC-259: answer must be marked as S4b-3: %q", line)
	}
}

// AC-260: §21.5 に、止まり方の一覧がある（コードの止まり方の 9 つと、選べる手を、1 行ずつ）。
func TestSpec_AC260_ArchSection215ListsStopKinds(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	for _, k := range core.StopKindValues() {
		row := docTableRow(t, body, "| `"+string(k)+"` |")
		for _, h := range core.SupervisorHandsFor(k) {
			if !strings.Contains(row, "`"+string(h)+"`") {
				t.Errorf("AC-260: the row of %q lacks the hand %q: %q", k, h, row)
			}
		}
		// 逆向き: 行の手の名前の集合が、コードの選べる手の表と一致する（`send_back` は事前に外れる組がある旨の括弧書きを除く）。
		cols := strings.Split(row, "|")
		got := map[string]bool{}
		for _, w := range backticked(strings.SplitN(cols[3], "（", 2)[0]) {
			got[w] = true
		}
		want := core.SupervisorHandsFor(k)
		if len(got) != len(want) {
			t.Errorf("AC-260: the row of %q lists %v, want exactly %v", k, got, want)
		}
	}
}

// AC-261: §21.5 に、見送りは同じ保留に 3 サイクルまでである旨の注記がある。
func TestSpec_AC261_ArchSection215NotesDeferLimit(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	docLineWith(t, body, "`defer`", "同じ保留に 3 サイクルまで")
}

// AC-262: §21.5 に、retry は 1 回までである旨の注記がある。
func TestSpec_AC262_ArchSection215NotesRetryLimit(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	docLineWith(t, body, "`retry` は 1 回まで")
}

// AC-263: §7 の判断点の表に、スーパーバイザーの判断点（SV）がある。
func TestSpec_AC263_ArchSection7TableHasSV(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection7)
	docLineWith(t, body, "| （想定外の止まり", "**SV スーパーバイザー**", "`judgment_budget_usd.SV`")
	// 子の問いに答える判断点（J4）は、S4b-3 で表から外した（AC-62）。
	docLineWith(t, body, "| 3. 実行 |", "**J3 ブリーフ**")
	if strings.Contains(body, "**J4 応答**") {
		t.Errorf("AC-62: the §7 table still lists **J4 応答**")
	}
}

// AC-264: §7 に、遷移 supervisor_resolve（T20）の注記がある。
func TestSpec_AC264_ArchSection7NotesSupervisorResolve(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection7)
	docLineWith(t, body, "`supervisor_resolve`", "T20", "本人確認の要らない", "T12")
	if string(core.OpSupervisorResolve) != "supervisor_resolve" {
		t.Errorf("AC-264: the operation = %q", core.OpSupervisorResolve)
	}
}

// AC-266: 用語集に、「止まり方」「対処」「スーパーバイザー待ち」の行がある。
func TestSpec_AC266_GlossaryHasStopKindActionAndSupervisorWaiting(t *testing.T) {
	for term, want := range map[string]string{"止まり方": "`StopKind`", "対処": "`supervisor_action`", "スーパーバイザー待ち": "`supervisor_state`"} {
		cols := glossaryRow(t, term)
		if !strings.Contains(cols[3], want) {
			t.Errorf("AC-266: the code name of %q = %q, want %q", term, cols[3], want)
		}
	}
}

// AC-267: m3-invoker-delegation.md の宣言の項目の一覧に、supervisor と judgment_budget_usd.SV がある。
func TestSpec_AC267_M3DeclarationHasSupervisorAndSVBudget(t *testing.T) {
	body := sectionBody(t, m3Doc(t), "#### `.flywheel/agent.json`【決定 2026-09-28 親 M3P1・M3P9】")
	docLineWith(t, body, "- `supervisor`", "既定は真", "`judgment_budget_usd.SV`", "`config_invalid`")
}

// backticked は s の中の `...` で囲んだ語を順に返す。
func backticked(s string) []string {
	var out []string
	parts := strings.Split(s, "`")
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// AC-61: §21.5 に、J4 を外した旨の注記がある。
func TestSpec_AC61_ArchSection215NotesJ4Removed(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	docLineWith(t, body, "J4 を外した旨", "`j4.md`")
}

// AC-60: architecture.md の §21 の実装済みの範囲に、S4b-3 が実装済みとして書かれている（「区切り S4b-3」の語があるだけでは足りない）。
func TestSpec_AC60_ArchSection21NoteHasS4b3Implemented(t *testing.T) {
	first := archS21First(t)
	for _, want := range []string{"区切り S4b-3〔委譲先の問いに答える〕の範囲も実装済み", "区切り S4b-3 で実装した範囲は", "`child_question`", "`answer`"} {
		if !strings.Contains(first, want) {
			t.Errorf("AC-60: the opening note of §21 lacks %q", want)
		}
	}
}
