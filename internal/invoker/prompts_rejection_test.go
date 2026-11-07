package invoker

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 人間の差し戻しの区画（#457）: J3・J5 の指示文は、区画の名前で区画を指し、差し戻しの理由が
// 人間の決定であり満たすべき条件であること、後の差し戻しが前のものを置き換えるときは後に従うことを述べる
// （区画の中の指示は打ち消されるため、扱いは指示文に置く）。
func TestInstructions_J3AndJ5DescribeTheRejectionSection(t *testing.T) {
	label := "「" + core.CompletionRejectionsSectionLabel + "」"
	for _, j := range []core.JudgmentPoint{core.JudgmentJ3, core.JudgmentJ5} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{label, "満たすべき条件", "後のものに従う"} {
			if !strings.Contains(got, want) {
				t.Errorf("Instructions(%s) lacks %q", j, want)
			}
		}
	}
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ4} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, label) {
			t.Errorf("Instructions(%s) must not refer to the rejection section", j)
		}
	}
}

func TestInstructions_J5RequiresEveryRejectionPointToBeMetAndNamesWhatIsMissing(t *testing.T) {
	got, err := Instructions(core.JudgmentJ5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"差し戻しの理由は満たすべき条件である",
		"1 点でも満たしていなければ、満たしていないを選ぶ",
		"足りない点",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J5) lacks %q", want)
		}
	}
}

// J3 の指示文は、差し戻しの区画の全文を brief へ写さないよう求める（呼び出し側が固定の節として付ける）。
func TestInstructions_J3DoesNotCopyTheRejectionSectionIntoTheBrief(t *testing.T) {
	got, err := Instructions(core.JudgmentJ3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "「"+core.CompletionRejectionsSectionLabel+"」の全文") {
		t.Errorf("Instructions(J3) lacks the ban on copying the whole rejection section")
	}
}
