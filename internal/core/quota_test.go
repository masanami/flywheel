package core

import "testing"

// このファイルは claude-flywheel a5dfd43 の scripts/tests/quota-check.test.sh
// のケースを Go のテストへ移植する（§枠超過「現行scripts/tests/quota-check.
// test.shの判定のケースをGoのテストへ移植し、すべて同じ判定になる」）。
// --result-file・引数解析・起動失敗（exit 126/127）・SKILL.mdとの結線など、
// シェルスクリプトとしての事情に由来するケースは対象外とし、判定規則
// （IsRateLimited）そのものの振る舞いを移植する。
//
// self-review 指摘（design-reviewer, round1）でこのファイルを
// internal/invoker から internal/core へ移した（quota.go 参照）。

func TestIsRateLimited_ExactPrefixMatch(t *testing.T) {
	if !IsRateLimited(rateLimitPrefix + "weekly limit · resets 3pm") {
		t.Fatal("exact prefix match should be rate limited")
	}
}

func TestIsRateLimited_StripsLeadingWhitespace(t *testing.T) {
	if !IsRateLimited("   " + rateLimitPrefix + "weekly limit") {
		t.Fatal("leading spaces should be stripped before matching")
	}
	if !IsRateLimited("\n\n\t" + rateLimitPrefix + "weekly limit\n") {
		t.Fatal("leading newlines/tabs should be stripped before matching")
	}
}

func TestIsRateLimited_MultilineResultStillMatchesOnFirstLine(t *testing.T) {
	if !IsRateLimited(rateLimitPrefix + "Opus limit · resets 5pm\n続きの行") {
		t.Fatal("multi-line result should match on the first line")
	}
}

// 枠の名前を限定しない（weekly 決め打ちでない）。
func TestIsRateLimited_DoesNotHardcodeLimitName(t *testing.T) {
	names := []string{
		"weekly limit · resets 3pm",
		"session limit · resets 5pm",
		"Opus limit · resets Feb 3 at 10am",
		"Sonnet limit",
		"usage credit limit",
		"5-hour limit · resets 2:00",
		"組織の利用枠",
	}
	for _, name := range names {
		if !IsRateLimited(rateLimitPrefix + name) {
			t.Errorf("limit name %q should be accepted (prefix must not be hardcoded to one name)", name)
		}
	}
}

// 先頭一致であって部分一致ではない（自己言及の誤検知を塞ぐ）。
func TestIsRateLimited_DoesNotMatchSelfReferenceMidString(t *testing.T) {
	selfRef := "この規定を実装しました。判定は `" + rateLimitPrefix + "` の先頭一致であって部分一致ではありません。"
	if IsRateLimited(selfRef) {
		t.Fatal("a report that quotes the prefix mid-string must not be treated as rate limited")
	}
}

func TestIsRateLimited_DoesNotMatchBacktickQuotedAtStart(t *testing.T) {
	quoted := "`" + rateLimitPrefix + "` を先頭一致で判定する判定器を追加しました。"
	if IsRateLimited(quoted) {
		t.Fatal("a backtick-quoted mention must not match (only an exact literal prefix matches)")
	}
}

func TestIsRateLimited_DoesNotMatchSecondLineHeadOfMultilineReport(t *testing.T) {
	report := "実装が完了しました。変更点:\n" + rateLimitPrefix + "への先頭一致で枠超過を判定します。"
	if IsRateLimited(report) {
		t.Fatal("a mention on the second line's head must not match")
	}
}

// 境界（1文字ずれ・大小・引用符の異体字）は安全側へ倒す。
func TestIsRateLimited_BoundaryCasesFailSafe(t *testing.T) {
	short := rateLimitPrefix[:len(rateLimitPrefix)-1] // 末尾の半角スペースを欠く
	if IsRateLimited(short + "weekly limit") {
		t.Fatal("missing the trailing space boundary must not match")
	}

	lower := "you've hit your weekly limit"
	if IsRateLimited(lower) {
		t.Fatal("a different-case prefix must not match")
	}

	apostropheVariant := "You’ve hit your weekly limit" // U+2019 curly apostrophe
	if IsRateLimited(apostropheVariant) {
		t.Fatal("an apostrophe variant (U+2019) must not match")
	}
}

// 判定不能は「枠超過ではない」側へ倒す。
func TestIsRateLimited_UndeterminableFailsToNotRateLimited(t *testing.T) {
	cases := []string{"", "  \n\t  ", "実装が完了しました。PR を作成済みです。"}
	for _, c := range cases {
		if IsRateLimited(c) {
			t.Errorf("undeterminable input %q must be treated as not rate limited", c)
		}
	}
}

// 返り値 JSON をまるごと流し込む誤用も安全側へ倒れる（"{" で始まり先頭一致
// しない）。
func TestIsRateLimited_RawJSONDumpDoesNotMatch(t *testing.T) {
	raw := `{"type":"result","subtype":"success","result":"` + rateLimitPrefix + `weekly limit"}`
	if IsRateLimited(raw) {
		t.Fatal("a raw JSON dump of the response must not match (it does not start with the prefix itself)")
	}
}
