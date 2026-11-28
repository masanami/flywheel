package cli

import (
	"slices"
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

// 以下は、区切り S4b-2 の文書の受入基準（#1084。AC-258〜AC-267）のうち、コードの値と文書を照らすもの。

const (
	archSection7   = "## 7. サイクルの再設計"
	archSection215 = "### 21.5 スーパーバイザー（SV）（D27）"
)

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

// AC-260: §21.5 に、止まり方の一覧がある（コードの止まり方の全部と、選べる手を、1 行ずつ）。表の行の止まり方の集合は、コードの閉集合と同じ。
func TestSpec_AC260_ArchSection215ListsStopKinds(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection215)
	// 逆向きを含めて、表の行の止まり方の集合がコードの閉集合と同じ（表に余分な行・重なる行が無い）。
	var listed, closed []string
	inTable := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "| 止まり方 |"):
			inTable = true
		case !strings.HasPrefix(line, "|"):
			inTable = false
		case inTable && !strings.HasPrefix(line, "|---"):
			listed = append(listed, strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`"))
		}
	}
	for _, k := range core.StopKindValues() {
		closed = append(closed, string(k))
	}
	slices.Sort(listed)
	slices.Sort(closed)
	if !slices.Equal(listed, closed) {
		t.Errorf("the stop kinds of the table = %v, want the closed set %v", listed, closed)
	}
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

// AC-264: §7 に、遷移 supervisor_resolve（T20）の注記がある。
func TestSpec_AC264_ArchSection7NotesSupervisorResolve(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection7)
	docLineWith(t, body, "`supervisor_resolve`", "T20", "本人確認の要らない", "T12")
	if string(core.OpSupervisorResolve) != "supervisor_resolve" {
		t.Errorf("AC-264: the operation = %q", core.OpSupervisorResolve)
	}
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
