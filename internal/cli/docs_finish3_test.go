package cli

import (
	"strings"
	"testing"
)

// 区切り S3b の仕上げ 3 項目 6: m1-core の作業ログの `after` の規則の「…以外は常に `version` を含む」の例外に、`integration_ref_set` がある
// （`integration_ref_set` の `after` は `integration_ref` だけで、版を持たない）。
func TestSpec_Finish3_M1AfterRuleExcludesIntegrationRefSetFromVersion(t *testing.T) {
	t.Parallel()
	doc := readRepoDoc(t, "docs", "features", "m1-core.md")
	line := docLineWith(t, doc, "| `after` |", "以外は常に `version` を含む")
	rule := line[:strings.Index(line, "以外は常に `version` を含む")]
	if !strings.Contains(rule[strings.LastIndex(rule, "`create`"):], "`integration_ref_set`") {
		t.Error("the exceptions of the `after` rule (before 「以外は常に `version` を含む」) do not list `integration_ref_set`")
	}
	if !strings.Contains(line, "`integration_ref_set` の `after` は `integration_ref` だけで、版") {
		t.Error("the `after` rule does not explain that `integration_ref_set` carries no `version`")
	}
}
