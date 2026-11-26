package core

import "testing"

// 計画に書き足した一覧の読み取り: 項目が無い計画は「一覧なし」、項目があるのに空・null の計画は壊れた出力として誤りにする
// （一覧なしと読むと、委譲の段の検査し直しが黙って外れる）。
func TestLoadPlanAcceptanceCriteria_EmptyListIsMalformed(t *testing.T) {
	for _, spec := range []string{"", `{"verdict":"plan"}`} {
		list, err := loadPlanAcceptanceCriteria(spec)
		if err != nil || list != nil {
			t.Errorf("spec %q: list = %v, err = %v, want no list and no error", spec, list, err)
		}
	}
	for _, spec := range []string{
		`{"acceptance_criteria":null}`,
		`{"acceptance_criteria":{"path":"docs/x.md","items":[]}}`,
		`{"acceptance_criteria":{"path":"docs/x.md"}}`,
		`{"acceptance_criteria":[]}`,
	} {
		if list, err := loadPlanAcceptanceCriteria(spec); err == nil {
			t.Errorf("spec %q: list = %v, want an error", spec, list)
		}
	}
	list, err := loadPlanAcceptanceCriteria(`{"acceptance_criteria":{"path":"docs/x.md","items":[{"id":"AC-00000000","text":"a"}]}}`)
	if err != nil || list == nil || len(list.Items) != 1 || list.Path != "docs/x.md" {
		t.Errorf("list = %v, err = %v, want one item", list, err)
	}
}
