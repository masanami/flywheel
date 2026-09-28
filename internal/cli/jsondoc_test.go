package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// このファイルは Issue #15 の完了条件「成功時の JSON のフィールドが、全コマンド
// ぶん docs/features/m1-core.md に書き戻されている」を、文書と実際の出力の
// 照合で固定する。文書の「成功時の JSON 出力の規約」節の各コマンドの小見出し
// （##### `cmd`）から、最上位のキーの形（`{"challenge": {…}, …}` の行、または
// init のようにフィールドの表）と、要素の形（challenge・plan・approval・hold・
// operation・activity）を読み取り、実際の --json の出力のキーと比べる。

// documentedJSON は文書から読み取った成功時の JSON の形。
type documentedJSON struct {
	// topLevel はコマンド名ごとの、最上位のキーの集合の候補（approve・reject は
	// <C-ID> と <OP-ID> で形が 2 つある）。
	topLevel map[string][]map[string]bool
	// entity は要素の名前（challenge・plan・approval・hold・operation・activity）
	// ごとのキーの集合。
	entity map[string]map[string]bool
}

var (
	backtickRe = regexp.MustCompile("`([^`]+)`")
	quotedRe   = regexp.MustCompile(`"([a-z_]+)"`)
	// 見出しは「#」の並びと空白で始まる行（本文の「#9 は…」を見出しと取り違えない）。
	markdownHeadingRe = regexp.MustCompile(`^#{1,6} `)
)

// topLevelKeys は `{"a": {…}, "b": [...]}` の形の文字列から、深さ 1 のキーを返す。
func topLevelKeys(shape string) map[string]bool {
	keys := map[string]bool{}
	depth := 0
	for i := 0; i < len(shape); i++ {
		switch shape[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			end := strings.IndexByte(shape[i+1:], '"')
			if end < 0 {
				return keys
			}
			key := shape[i+1 : i+1+end]
			i += end + 1
			if depth == 1 && strings.HasPrefix(strings.TrimSpace(shape[i+1:]), ":") {
				keys[key] = true
			}
		}
	}
	return keys
}

func loadDocumentedJSON(t *testing.T) documentedJSON {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m1-core.md"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	doc := documentedJSON{topLevel: map[string][]map[string]bool{}, entity: map[string]map[string]bool{}}
	inConvention := false
	var names []string
	tableKeys := map[string]bool{}
	flush := func() {
		// フィールドの表だけを持つ小見出し（init）は、表の 1 列目が最上位のキー。
		for _, n := range names {
			if len(doc.topLevel[n]) == 0 && len(tableKeys) > 0 {
				doc.topLevel[n] = append(doc.topLevel[n], tableKeys)
			}
		}
		// create・edit の表は challenge の形、log の表は activity の形。
		for _, n := range names {
			switch n {
			case "create":
				doc.entity["challenge"] = tableKeys
			case "log":
				doc.entity["activity"] = tableKeys
			}
		}
		names, tableKeys = nil, map[string]bool{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if markdownHeadingRe.MatchString(trimmed) {
			if inConvention {
				flush()
			}
			switch {
			case strings.HasPrefix(trimmed, "#### 成功時の JSON 出力の規約"):
				inConvention = true
			case strings.HasPrefix(trimmed, "##### ") && inConvention:
				// 見出しのコマンド名は「（」の前だけ（「#13 で `<OP-ID>` を拡張」を拾わない）。
				head, _, _ := strings.Cut(trimmed, "（")
				for _, m := range backtickRe.FindAllStringSubmatch(head, -1) {
					names = append(names, m[1])
				}
			case inConvention && !strings.HasPrefix(trimmed, "##### "):
				inConvention = false
			}
			continue
		}
		if !inConvention || len(names) == 0 {
			continue
		}
		switch {
		case !strings.HasPrefix(trimmed, "|"):
			// 本文中の `{…}` が最上位の形（approve・reject は <C-ID> と <OP-ID> の 2 つ）。
			for _, m := range backtickRe.FindAllStringSubmatch(trimmed, -1) {
				if strings.HasPrefix(m[1], "{") {
					for _, n := range names {
						doc.topLevel[n] = append(doc.topLevel[n], topLevelKeys(m[1]))
					}
				}
			}
		case !strings.HasPrefix(trimmed, "|---"):
			cols := strings.Split(trimmed, "|")
			m := backtickRe.FindStringSubmatch(cols[1])
			if m == nil || strings.Contains(m[1], ".") {
				continue // 見出し行・status の区分（needs_human.challenges など）
			}
			if len(cols) > 2 && strings.Contains(cols[2], "{") {
				// show の「一覧 | 要素の形」の表。
				elem := map[string]bool{}
				for _, q := range quotedRe.FindAllStringSubmatch(cols[2], -1) {
					elem[q[1]] = true
				}
				doc.entity[strings.TrimSuffix(m[1], "s")] = elem
				continue
			}
			tableKeys[m[1]] = true
		}
	}
	flush()
	for _, e := range []string{"challenge", "plan", "approval", "hold", "operation", "activity", "source_binding", "discrepancy", "source", "repo", "item"} {
		if len(doc.entity[e]) == 0 {
			t.Fatalf("documented shape of %q not found in the JSON output section", e)
		}
	}

	addM3RunsShape(t, &doc)
	return doc
}

// addM3RunsShape は `runs` コマンドの成功時のJSON形を doc へ足す。m1-core.md の
// 「成功時のJSON出力の規約」節と違い、m3-invoker-delegation.md の該当箇所
// （§IF / API「`status`・`show`・`runs`の拡張」）は見出し＋表の形ではなく
// 箇条書きの1行（“ `runs`: `{"runs": [{...}]}` “）であり、上の汎用パーサ
// （loadDocumentedJSON。m1-core.md の見出し構造専用）では拾えない。#81
// （docs/features/m3-invoker-delegation.md §IF / API・受入基準AC-151〜153）の
// 追加分として、その1行を直接パースして doc へ合成する（第2の正本を持たない
// よう、逐語をここへ複製せずファイルから読む）。
func addM3RunsShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- `runs`: ") {
			continue
		}
		backticks := backtickRe.FindAllStringSubmatch(trimmed, -1)
		if len(backticks) < 2 {
			t.Fatalf("m3 spec `runs` line does not have the expected two backtick spans: %q", trimmed)
		}
		shape := backticks[1][1] // 2つ目の`…`が {"runs": [{...}]} の形
		top := topLevelKeys(shape)
		doc.topLevel["runs"] = append(doc.topLevel["runs"], top)

		// 要素の形（"runs"配下の配列要素）は、shape内の最初の "[" から対応する
		// "]" までを抜き出し、その中の最上位のキーを読む。
		open := strings.Index(shape, "[")
		if open < 0 {
			t.Fatalf("m3 spec `runs` shape has no array: %q", shape)
		}
		depth := 0
		closeIdx := -1
		for i := open; i < len(shape); i++ {
			switch shape[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					closeIdx = i
				}
			}
			if closeIdx >= 0 {
				break
			}
		}
		if closeIdx < 0 {
			t.Fatalf("m3 spec `runs` shape has an unbalanced array: %q", shape)
		}
		elem := topLevelKeys(shape[open+1 : closeIdx])
		doc.entity["run"] = elem
		return
	}
	t.Fatal("m3 spec does not document the `runs` JSON shape (expected a line starting with \"- `runs`: \")")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// jsonEntityOf は出力のキー名から、その値（オブジェクト、または配列の要素）の
// 要素の名前を返す。
var jsonEntityOf = map[string]string{
	"challenge": "challenge", "challenges": "challenge",
	"plan": "plan", "plans": "plan",
	"approval": "approval", "approvals": "approval",
	"hold": "hold", "holds": "hold",
	"operation": "operation", "operations": "operation",
	"activities":     "activity",
	"source_binding": "source_binding",
	// discrepancies は status の needs_human 配下の食い違いの一覧
	// （docs/features/m2-github-issue-ingest.md §食い違いの表示。#58 で追加）。
	"discrepancies": "discrepancy",
	// ingest の入れ子の要素（#59）。sources（最上位の一覧）> repos（取り込み元の
	// 中の一覧）> items（リポジトリの中の一覧）の3段。
	"sources": "source",
	"repos":   "repo",
	"items":   "item",
	// runs（#81。docs/features/m3-invoker-delegation.md §IF / API「runs」）。
	"runs": "run",
}

// assertDocumentedEntities は出力の中の要素（オブジェクトと配列の要素）の
// キーが、文書の要素の形と一致することを再帰的に検査する。
func assertDocumentedEntities(t *testing.T, doc documentedJSON, where string, v map[string]any) {
	t.Helper()
	for k, val := range v {
		entity, known := jsonEntityOf[k]
		switch x := val.(type) {
		case map[string]any:
			if !known {
				assertDocumentedEntities(t, doc, where+"."+k, x) // status の区分
				continue
			}
			if got := keysOf(x); !reflect.DeepEqual(got, doc.entity[entity]) {
				t.Errorf("%s.%s keys = %v, documented %s shape = %v", where, k, sortedKeys(got), entity, sortedKeys(doc.entity[entity]))
			}
			// known な単一エンティティの内側も再帰する（今のところ source_binding
			// に入れ子の既知エンティティは無いが、ingest の source/repo/item と
			// 同じ規律で将来の追加に備える）。
			assertDocumentedEntities(t, doc, where+"."+k, x)
		case []any:
			if !known {
				continue
			}
			for i, e := range x {
				em, ok := e.(map[string]any)
				if !ok {
					t.Errorf("%s.%s[%d] is not an object: %#v", where, k, i, e)
					continue
				}
				if got := keysOf(em); !reflect.DeepEqual(got, doc.entity[entity]) {
					t.Errorf("%s.%s[%d] keys = %v, documented %s shape = %v", where, k, i, sortedKeys(got), entity, sortedKeys(doc.entity[entity]))
				}
				// ingest の source/repo/item のように、要素自身がさらに入れ子の
				// 一覧（repos・items）を持つ場合もここで再帰的に照合する。
				assertDocumentedEntities(t, doc, fmt.Sprintf("%s.%s[%d]", where, k, i), em)
			}
		}
	}
}

// assertDocumentedJSON は name コマンドの成功時の出力 out が、文書の最上位の形の
// いずれかと一致し、含まれる要素が文書の要素の形と一致することを検査する。
func assertDocumentedJSON(t *testing.T, doc documentedJSON, name string, out map[string]any) {
	t.Helper()
	shapes := doc.topLevel[name]
	if len(shapes) == 0 {
		t.Errorf("%s: no documented success JSON shape in docs/features/m1-core.md", name)
		return
	}
	got := keysOf(out)
	matched := false
	for _, s := range shapes {
		if reflect.DeepEqual(got, s) {
			matched = true
		}
	}
	if !matched {
		var want [][]string
		for _, s := range shapes {
			want = append(want, sortedKeys(s))
		}
		t.Errorf("%s: top-level keys = %v, documented shapes = %v", name, sortedKeys(got), want)
	}
	assertDocumentedEntities(t, doc, name, out)
}

// TestDocumentedJSON_EveryRegisteredCommandHasASection は、登録表の全コマンドに
// 成功時の JSON の形が文書に書かれていることを検査する（出力との照合は
// TestAllCommands_JSONSuccessWritesSingleDocumentToStdout が各コマンドの成功経路で行う）。
func TestDocumentedJSON_EveryRegisteredCommandHasASection(t *testing.T) {
	doc := loadDocumentedJSON(t)
	for name := range registeredCommands() {
		if len(doc.topLevel[name]) == 0 {
			t.Errorf("registered command %q has no documented success JSON shape", name)
		}
	}
	for name := range doc.topLevel {
		if _, ok := registeredCommands()[name]; !ok {
			t.Errorf("documented success JSON shape for %q, which is not a registered command", name)
		}
	}
}
