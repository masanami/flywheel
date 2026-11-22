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
