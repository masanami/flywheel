package cli

import (
	"encoding/json"
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
	addM3ShowPlanSpecShape(t, &doc)
	addM3ShowRunsShape(t, &doc)
	addM3CycleShape(t, &doc)
	addM3SlotClearShape(t, &doc)
	addM3RunShape(t, &doc)
	addM3BudgetShapes(t, &doc)
	addM3WaitingExternalShapes(t, &doc)
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
		// 文書の要素はキーの列挙（`{"id", "kind", …}`。値の例を持たない）なので、
		// topLevelKeys（`"key":` の形だけを拾う）ではなく、引用符で囲まれたキーを読む
		// （#86: 以前は topLevelKeys で読み、要素の形が常に空集合になっていた。
		// runs の要素を 1 件も出さない成功経路の照合では気付けなかった）。
		elem := map[string]bool{}
		for _, q := range quotedRe.FindAllStringSubmatch(shape[open+1:closeIdx], -1) {
			elem[q[1]] = true
		}
		if len(elem) == 0 {
			t.Fatalf("m3 spec `runs` element shape has no keys: %q", shape)
		}
		doc.entity["run"] = elem
		return
	}
	t.Fatal("m3 spec does not document the `runs` JSON shape (expected a line starting with \"- `runs`: \")")
}

// addM3ShowPlanSpecShape は show の `plans` の要素の形（m1 の plan の形に spec を
// 足したもの）を doc.entity["show_plan"] へ合成する（#85）。m3-invoker-delegation.md
// §IF / API「`status`・`show`・`runs` の拡張」の該当の 1 行が、`plans` の要素へ
// `spec` を足すと書いていることを確かめた上で合成する（第 2 の正本を持たず、
// 文書の記述が消えたらここで落ちる）。単発の `plan` の成功出力の plan オブジェクト
// （doc.entity["plan"]）は変えない。
func addM3ShowPlanSpecShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	documented := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- `show`: ") && strings.Contains(trimmed, "`plans` の要素に `spec`") {
			documented = true
			break
		}
	}
	if !documented {
		t.Fatal("m3 spec does not document that show's `plans` elements gain `spec` (expected a `- `show`: …`plans` の要素に `spec`…` line)")
	}
	elem := map[string]bool{"spec": true}
	for k := range doc.entity["plan"] {
		elem[k] = true
	}
	doc.entity["show_plan"] = elem
}

// addM3ShowRunsShape は show の最上位に足された `runs`（run の一覧。要素は `runs` の要素と
// 同じ形＝doc.entity["run"]）を、show の最上位の形へ足す（#86）。m3-invoker-delegation.md
// §IF / API「`status`・`show`・`runs` の拡張」の該当の 1 行が、show の最上位に `runs` を足すと
// 書いていることを確かめた上で合成する（第 2 の正本を持たず、文書の記述が消えたらここで
// 落ちる。addM3ShowPlanSpecShape と同じ形）。
func addM3ShowRunsShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	documented := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- `show`: ") && strings.Contains(trimmed, "最上位に `runs`") {
			documented = true
			break
		}
	}
	if !documented {
		t.Fatal("m3 spec does not document that show's top level gains `runs` (expected a `- `show`: 最上位に `runs`…` line)")
	}
	if len(doc.topLevel["show"]) == 0 {
		t.Fatal("documented success JSON shape of show not found")
	}
	for _, shape := range doc.topLevel["show"] {
		shape["runs"] = true
	}
}

// addM3CycleShape は `cycle` の成功時の JSON の形を doc へ合成する（#86）。m3-invoker-
// delegation.md §IF / API「`cycle` の JSON 出力」の見出しの下の最初の ```json の例を、文書から
// 直接読む（第 2 の正本を持たない）。最上位の形は doc.topLevel["cycle"]、周の記録は
// doc.entity["cycle_record"]、段の形は取り込みの段（cycle_phase_ingest）と判断の段
// （cycle_phase_judgment。分類・計画。`--auto` の個別の操作の `{"phase": {…}}` の phase
// もこの形）、items の要素（cycle_item）・not_started の要素（cycle_not_started）。
func addM3CycleShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#### `cycle` の JSON 出力") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("m3 spec has no `#### `cycle` の JSON 出力` heading")
	}
	var block []string
	in := false
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if !in {
			if trimmed == "```json" {
				in = true
			}
			continue
		}
		if trimmed == "```" {
			break
		}
		block = append(block, line)
	}
	if len(block) == 0 {
		t.Fatal("m3 spec's `cycle` JSON section has no ```json example")
	}
	var example map[string]any
	if err := json.Unmarshal([]byte(strings.Join(block, "\n")), &example); err != nil {
		t.Fatalf("m3 spec's `cycle` JSON example is not valid JSON: %v", err)
	}
	doc.topLevel["cycle"] = []map[string]bool{keysOf(example)}
	rec, ok := example["cycle"].(map[string]any)
	if !ok {
		t.Fatal("m3 spec's `cycle` JSON example has no `cycle` object")
	}
	doc.entity["cycle_record"] = keysOf(rec)
	phases, ok := example["phases"].([]any)
	if !ok || len(phases) == 0 {
		t.Fatal("m3 spec's `cycle` JSON example has no `phases`")
	}
	for _, p := range phases {
		pm := p.(map[string]any)
		switch pm["phase"] {
		case "ingest":
			doc.entity["cycle_phase_ingest"] = keysOf(pm)
		case "classify", "plan":
			shape := keysOf(pm)
			if prev, seen := doc.entity["cycle_phase_judgment"]; seen && !reflect.DeepEqual(prev, shape) {
				t.Fatalf("m3 spec's `cycle` example: phases classify and plan have different shapes: %v vs %v", sortedKeys(prev), sortedKeys(shape))
			}
			doc.entity["cycle_phase_judgment"] = shape
			if items, ok := pm["items"].([]any); ok && len(items) > 0 {
				doc.entity["cycle_item"] = keysOf(items[0].(map[string]any))
			}
			if ns, ok := pm["not_started"].([]any); ok && len(ns) > 0 {
				doc.entity["cycle_not_started"] = keysOf(ns[0].(map[string]any))
			}
		}
	}
	for _, e := range []string{"cycle_phase_ingest", "cycle_phase_judgment", "cycle_item", "cycle_not_started"} {
		if len(doc.entity[e]) == 0 {
			t.Fatalf("m3 spec's `cycle` JSON example does not show the shape of %q", e)
		}
	}
}

// documentedCyclePhaseNames は cycle の JSON の phase の閉集合（§IF / API「`phase` は
// `ingest | classify | plan | run | verify` の閉集合」）。cycle は 5 つすべてを出す。
var documentedCyclePhaseNames = map[string]bool{"ingest": true, "classify": true, "plan": true, "run": true, "verify": true}

// assertDocumentedCycle は cycle の成功時の出力 out の内側（周の記録・段・items・not_started・
// ingest の result）が、文書の例と同じ形であることを検査する（最上位のキーは
// assertDocumentedJSON の共通の検査が見る）。
func assertDocumentedCycle(t *testing.T, doc documentedJSON, out map[string]any) {
	t.Helper()
	rec, ok := out["cycle"].(map[string]any)
	if !ok {
		t.Errorf("cycle: `cycle` is not an object: %#v", out["cycle"])
		return
	}
	if got := keysOf(rec); !reflect.DeepEqual(got, doc.entity["cycle_record"]) {
		t.Errorf("cycle.cycle keys = %v, documented = %v", sortedKeys(got), sortedKeys(doc.entity["cycle_record"]))
	}
	phases, ok := out["phases"].([]any)
	if !ok || len(phases) == 0 {
		t.Errorf("cycle: phases = %#v, want a non-empty array", out["phases"])
		return
	}
	for i, p := range phases {
		pm, ok := p.(map[string]any)
		if !ok {
			t.Errorf("cycle.phases[%d] is not an object: %#v", i, p)
			continue
		}
		name, _ := pm["phase"].(string)
		if !documentedCyclePhaseNames[name] {
			t.Errorf("cycle.phases[%d].phase = %v, want a member of the documented closed set", i, pm["phase"])
		}
		where := fmt.Sprintf("cycle.phases[%d](%s)", i, name)
		if name == "ingest" {
			if got := keysOf(pm); !reflect.DeepEqual(got, doc.entity["cycle_phase_ingest"]) {
				t.Errorf("%s keys = %v, documented = %v", where, sortedKeys(got), sortedKeys(doc.entity["cycle_phase_ingest"]))
			}
			if res, ok := pm["result"].(map[string]any); ok {
				// ingest の result は ingest --json と同じ形（sources > repos > items の各要素）。
				if _, has := res["sources"]; !has {
					t.Errorf("%s.result lacks `sources`", where)
				}
				assertDocumentedEntities(t, doc, where+".result", res)
			}
			continue
		}
		assertDocumentedPhase(t, doc, where, pm)
	}
}

// assertDocumentedPhase は判断の段（分類・計画）の 1 要素 pm が文書の形と一致することを検査する
// （cycle の phases の要素も、`--auto` の個別の操作の `{"phase": {…}}` の phase も同じ形）。
func assertDocumentedPhase(t *testing.T, doc documentedJSON, where string, pm map[string]any) {
	t.Helper()
	if pm["phase"] == "run" {
		// 委譲の段は判断の段の形に `serial_groups` を足した形（m3 §IF / API「`cycle` の JSON 出力」）。
		groups, ok := pm["serial_groups"].([]any)
		if !ok {
			t.Errorf("%s: serial_groups = %#v, want an array", where, pm["serial_groups"])
		}
		for i, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				t.Errorf("%s.serial_groups[%d] is not an object: %#v", where, i, g)
				continue
			}
			if got := keysOf(gm); !reflect.DeepEqual(got, doc.entity["cycle_serial_group"]) {
				t.Errorf("%s.serial_groups[%d] keys = %v, documented = %v", where, i, sortedKeys(got), sortedKeys(doc.entity["cycle_serial_group"]))
			}
		}
		rest := map[string]any{}
		for k, v := range pm {
			if k != "serial_groups" {
				rest[k] = v
			}
		}
		pm = rest
	}
	if got := keysOf(pm); !reflect.DeepEqual(got, doc.entity["cycle_phase_judgment"]) {
		t.Errorf("%s keys = %v, documented = %v", where, sortedKeys(got), sortedKeys(doc.entity["cycle_phase_judgment"]))
	}
	items, _ := pm["items"].([]any)
	for j, it := range items {
		im, ok := it.(map[string]any)
		if !ok {
			t.Errorf("%s.items[%d] is not an object", where, j)
			continue
		}
		if got := keysOf(im); !reflect.DeepEqual(got, doc.entity["cycle_item"]) {
			t.Errorf("%s.items[%d] keys = %v, documented = %v", where, j, sortedKeys(got), sortedKeys(doc.entity["cycle_item"]))
		}
	}
	ns, _ := pm["not_started"].([]any)
	for j, n := range ns {
		nm, ok := n.(map[string]any)
		if !ok {
			t.Errorf("%s.not_started[%d] is not an object", where, j)
			continue
		}
		if got := keysOf(nm); !reflect.DeepEqual(got, doc.entity["cycle_not_started"]) {
			t.Errorf("%s.not_started[%d] keys = %v, documented = %v", where, j, sortedKeys(got), sortedKeys(doc.entity["cycle_not_started"]))
		}
	}
	if _, ok := pm["items"].([]any); !ok {
		t.Errorf("%s.items = %#v, want an array (never null)", where, pm["items"])
	}
	if _, ok := pm["not_started"].([]any); !ok {
		t.Errorf("%s.not_started = %#v, want an array (never null)", where, pm["not_started"])
	}
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
	// show の plans の要素は plan に spec を足した形（#85。m3 §IF / API「show」）。
	"plan": "plan", "plans": "show_plan",
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
	// slot clear の slot（S2。§IF / API の「slot clear」の行）。
	"slot": "slot",
	// status.needs_human.budget_exhausted（S2。§IF / API の「status.needs_human.budget_exhausted」の行）。
	"budget_exhausted": "budget_exhausted",
}

// assertDocumentedEntities は出力の中の要素（オブジェクトと配列の要素）の
// キーが、文書の要素の形と一致することを再帰的に検査する。
func assertDocumentedEntities(t *testing.T, doc documentedJSON, where string, v map[string]any) {
	t.Helper()
	for k, val := range v {
		entity, known := jsonEntityOf[k]
		if k == "challenges" && strings.HasSuffix(where, "waiting_external") {
			// status.waiting_external.challenges の要素は課題の形ではない（S2）。
			entity, known = "waiting_external_challenge", true
		}
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
	if name != "run" { // run の phase の items は ingest の item と名前が同じなので別に照合する。
		assertDocumentedEntities(t, doc, name, out)
	}
	if name == "cycle" {
		assertDocumentedCycle(t, doc, out)
	}
	if name == "run" {
		if pm, ok := out["phase"].(map[string]any); ok {
			assertDocumentedPhase(t, doc, "run", pm)
		} else {
			t.Errorf("run: `phase` is not an object: %#v", out["phase"])
		}
	}
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

// #86（仕様の仮定。受入基準は無い）: `--auto` の個別の操作（classify・plan）の --json は、cycle の
// phases の 1 要素と同じ形を {"phase": {…}} で返す（m3-invoker-delegation.md §IF / API
// 「`cycle` の JSON 出力」の最後の行）。`run` の --json も同じ規則だが、`run` コマンドは S2 で
// 足すため S1 には存在せず、ここでは検査しない（S2 のチケットが同じ照合を足す）。
func TestDocumentedJSON_AutoOperationsReturnAPhaseObject(t *testing.T) {
	m3SpecLine(t, "`--auto` の個別の操作と `run` の `--json` は", "`{\"phase\": {…}}` で返す")
	doc := loadDocumentedJSON(t)

	ws := setupJ2Workspace(t)
	createTitled(t, ws, "auto-phase")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	for _, op := range []string{"classify", "plan"} {
		t.Run(op, func(t *testing.T) {
			out := runJSON(t, ws, op, "--auto")
			if got := keysOf(out); !reflect.DeepEqual(got, map[string]bool{"phase": true}) {
				t.Fatalf("%s --auto --json top-level keys = %v, want exactly [phase]", op, sortedKeys(got))
			}
			pm, ok := out["phase"].(map[string]any)
			if !ok {
				t.Fatalf("%s --auto --json: `phase` is not an object: %#v", op, out["phase"])
			}
			if pm["phase"] != op || pm["skipped"] != false {
				t.Errorf("phase = %v skipped = %v, want %q and false", pm["phase"], pm["skipped"], op)
			}
			if items, _ := pm["items"].([]any); len(items) != 1 {
				t.Fatalf("%s --auto items = %v, want 1 item so that the element shape is compared", op, pm["items"])
			}
			assertDocumentedPhase(t, doc, op+" --auto", pm)
		})
	}
}

// addM3SlotClearShape は `slot clear` の成功時の JSON の形を doc へ足す（#101）。
// m3-invoker-delegation.md §IF / API「`status`・`show`・`runs` の拡張」の
// 「- `slot clear`（S2）: `{"slot": {…}}`」の 1 行を直接パースする（第 2 の正本を
// 持たない。addM3RunsShape と同じ形）。
func addM3SlotClearShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- `slot clear`（S2）: ") {
			continue
		}
		backticks := backtickRe.FindAllStringSubmatch(trimmed, -1)
		if len(backticks) < 2 {
			t.Fatalf("m3 spec `slot clear` line does not have the expected backtick spans: %q", trimmed)
		}
		shape := backticks[1][1] // {"slot": {"slot_id", …}}
		doc.topLevel["slot clear"] = append(doc.topLevel["slot clear"], topLevelKeys(shape))
		open := strings.Index(shape[1:], "{")
		if open < 0 {
			t.Fatalf("m3 spec `slot clear` shape has no object: %q", shape)
		}
		elem := map[string]bool{}
		for _, q := range quotedRe.FindAllStringSubmatch(shape[open+2:], -1) {
			elem[q[1]] = true
		}
		doc.entity["slot"] = elem
		return
	}
	t.Fatal("m3 spec does not document the `slot clear` JSON shape (expected a line starting with \"- `slot clear`（S2）: \")")
}

// addM3RunShape は `run` の成功時の JSON の形を doc へ足す（#102）。m3 §IF / API「`cycle` の JSON
// 出力」の「`--auto` の個別の操作と `run` の `--json` は、…`{"phase": {…}}` で返す」の 1 行を直接
// パースする（第 2 の正本を持たない）。phase の要素の形は cycle_phase_judgment・cycle_item・
// cycle_not_started の照合（assertDocumentedPhase）が持つ。`jsondoc_test.go` の仕上げは #108。
func addM3RunShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	addM3SerialGroupShape(t, doc)
	line := m3SpecLine(t, "`--auto` の個別の操作と `run` の `--json` は", "`{\"phase\": {…}}`")
	for _, b := range backtickRe.FindAllStringSubmatch(line, -1) {
		if strings.HasPrefix(b[1], `{"phase"`) {
			doc.topLevel["run"] = append(doc.topLevel["run"], topLevelKeys(b[1]))
			return
		}
	}
	t.Fatalf("m3 spec line has no `{\"phase\": …}` shape: %q", line)
}

// addM3BudgetShapes は `status.needs_human.budget_exhausted` の要素の形（m3 §IF / API「`status`・
// `show`・`runs` の拡張」の 1 行を直接パースする。第 2 の正本を持たない）と、`budget` の成功時の
// JSON の形を doc へ足す（#105）。`budget` の成功時の形は仕様に書かれていないため、`approve` の
// `approval` に課題・計画の版・置き換えた額を足した形をここで定義する（仕様への指摘）。
func addM3BudgetShapes(t *testing.T, doc *documentedJSON) {
	t.Helper()
	line := m3SpecLine(t, "- `status.needs_human.budget_exhausted`（S2）: ")
	backticks := backtickRe.FindAllStringSubmatch(line, -1)
	if len(backticks) < 2 {
		t.Fatalf("m3 spec budget_exhausted line has no shape: %q", line)
	}
	elem := map[string]bool{}
	for _, q := range quotedRe.FindAllStringSubmatch(backticks[1][1], -1) {
		elem[q[1]] = true
	}
	if len(elem) == 0 {
		t.Fatalf("m3 spec budget_exhausted element shape has no keys: %q", line)
	}
	doc.entity["budget_exhausted"] = elem
	doc.topLevel["budget"] = append(doc.topLevel["budget"], map[string]bool{
		"challenge_id": true, "plan_version": true, "impl_usd": true, "review_usd": true, "approval": true,
	})
}

// addM3WaitingExternalShapes は `status.waiting_external` の形（m3 §IF / API「`status`・`show`・
// `runs` の拡張」の 1 行を直接パースする。第 2 の正本を持たない）を doc へ足す（S2）: status の
// 最上位のキーに `waiting_external` を足し、`challenges` の要素の形を登録する。
func addM3WaitingExternalShapes(t *testing.T, doc *documentedJSON) {
	t.Helper()
	line := m3SpecLine(t, "- `status.waiting_external`（S2。最上位の 4 つ目のキー）: ")
	backticks := backtickRe.FindAllStringSubmatch(line, -1)
	if len(backticks) < 2 {
		t.Fatalf("m3 spec waiting_external line has no shape: %q", line)
	}
	shape := backticks[1][1] // {"challenges": [{"challenge_id", "pr_url", "checks": "pending"}]}
	open := strings.Index(shape, "[{")
	if open < 0 {
		t.Fatalf("m3 spec waiting_external shape has no element object: %q", shape)
	}
	elem := map[string]bool{}
	for _, part := range strings.Split(shape[open:], ",") {
		if q := quotedRe.FindStringSubmatch(part); q != nil {
			elem[q[1]] = true
		}
	}
	if len(elem) == 0 {
		t.Fatalf("m3 spec waiting_external element shape has no keys: %q", line)
	}
	doc.entity["waiting_external_challenge"] = elem
	if len(doc.topLevel["status"]) == 0 {
		t.Fatal("no documented success JSON shape for status")
	}
	for _, s := range doc.topLevel["status"] {
		s["waiting_external"] = true
	}
	m3SpecLine(t, "最上位のキーは、M1 の 3 つと `waiting_external` の 4 つである")
}

// addM3SerialGroupShape は委譲の段の `serial_groups` の要素の形を doc へ足す（#106）。m3 §IF / API
// 「`cycle` の JSON 出力」の「`run` の段（S2）は `serial_groups: [{…}]` を持つ」の 1 行を直接パース
// する（第 2 の正本を持たない）。
func addM3SerialGroupShape(t *testing.T, doc *documentedJSON) {
	t.Helper()
	line := m3SpecLine(t, "`run` の段（S2）は `serial_groups:")
	for _, b := range backtickRe.FindAllStringSubmatch(line, -1) {
		if !strings.HasPrefix(b[1], "serial_groups:") {
			continue
		}
		elem := map[string]bool{}
		// 要素の形は `[{"repo", "challenges": …}]` の中の引用符つきのキー。
		open := strings.Index(b[1], "{")
		for _, q := range quotedRe.FindAllStringSubmatch(b[1][open:], -1) {
			elem[q[1]] = true
		}
		if len(elem) == 0 {
			break
		}
		doc.entity["cycle_serial_group"] = elem
		return
	}
	t.Fatalf("m3 spec line has no `serial_groups: [{…}]` shape: %q", line)
}
