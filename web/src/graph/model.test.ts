import { expect, it } from "vitest";
import type { GraphEdge, GraphNode, GraphResponse } from "../api/types.gen.ts";
import { CARD_ACTION_CODES } from "../api/types.gen.ts";
import { HUMAN_KINDS, HUMAN_KIND_LABELS } from "../board/filter.ts";
import { HUMAN_KIND_OF_ACTION } from "../board/filter.ts";
import {
  type ChallengeRelation,
  type Dims,
  HUMAN_KIND_GLYPHS,
  OTHERS_ID,
  basisOf,
  categoryLabel,
  categoryOf,
  chipLines,
  chipText,
  downstreamOf,
  fitChips,
  focusOf,
  humanActionLabels,
  humanChips,
  layoutGraph,
  minChipRoom,
  othersOf,
  parentMap,
  reasonText,
  relationsOf,
  upstreamOf,
  visibleIds,
} from "./model.ts";

const D: Dims = {
  nodeW: 100,
  nodeH: 40,
  gapX: 10,
  gapY: 20,
  pad: 5,
  labelH: 20,
  headH: 20,
  inset: 5,
  maxW: 1000,
};
const dep = (from: string, to: string): ChallengeRelation => ({
  from,
  kind: "depends_on",
  to,
});
const par = (from: string, to: string): ChallengeRelation => ({
  from,
  kind: "parent_of",
  to,
});

const node = (id: string, over: Partial<GraphNode> = {}): GraphNode => ({
  category: "system",
  context: null,
  headline: null,
  id,
  next_human_actions: [],
  parent_id: null,
  priority: null,
  running: false,
  stale: false,
  state: "in_progress",
  title: `題名${id}`,
  ...over,
});
const edge = (from: string, to: string, over: Partial<GraphEdge> = {}) => ({
  from,
  kind: "prerequisite",
  registered_at: "2026-11-01T00:00:00.000Z",
  registered_via: "plan",
  satisfied: false,
  to,
  ...over,
});

it("区分: API の区分をそのまま使い、システムの課題だけ run が動いていれば実行中の色になる", () => {
  expect(categoryOf(node("C-1", { category: "human" }))).toBe("human");
  expect(categoryOf(node("C-1", { category: "external" }))).toBe("external");
  expect(categoryOf(node("C-1", { category: "done" }))).toBe("done");
  expect(categoryOf(node("C-1", { category: "system" }))).toBe("system");
  expect(categoryOf(node("C-1", { category: "withdrawn" }))).toBe("withdrawn");
  expect(categoryLabel("withdrawn")).toBe("取り下げ");
  expect(categoryOf(node("C-1", { category: "parent_agent" }))).toBe(
    "parent_agent",
  );
  expect(categoryLabel("parent_agent")).toBe("親のエージェントが動く番");
  // 親のエージェントが動く番は run が動いていても、その区分の色
  expect(
    categoryOf(node("C-1", { category: "parent_agent", running: true })),
  ).toBe("parent_agent");
  expect(categoryOf(node("C-1", { running: true }))).toBe("running");
  // 人間の操作待ちは run が動いていても人間の色（光の輪は別に出す）
  expect(categoryOf(node("C-1", { category: "human", running: true }))).toBe(
    "human",
  );
});

it("関係: parent_id は親子の関係に、辺は依存の関係（先行が from）になる", () => {
  const g: GraphResponse = {
    nodes: [node("C-1"), node("C-2", { parent_id: "C-1" })],
    edges: [edge("C-2", "C-3")],
  };
  const rels = relationsOf(g);
  expect(rels.map((r) => [r.kind, r.from, r.to])).toEqual([
    ["parent_of", "C-1", "C-2"],
    ["depends_on", "C-2", "C-3"],
  ]);
  expect(rels[1]?.edge?.registered_via).toBe("plan");
});

it("線の種類: 衝突だけの線は破線、先行の成果を使う依存を含めば実線", () => {
  const e = (kind: string): ChallengeRelation => ({
    ...dep("C-1", "C-2"),
    edge: edge("C-1", "C-2", { kind }),
  });
  expect(basisOf([e("conflict")])).toBe("conflict");
  expect(basisOf([e("conflict"), e("prerequisite")])).toBe("prerequisite");
  expect(basisOf([e("prerequisite")])).toBe("prerequisite");
});

it("理由の文: 種類・登録の経路と日時・満たされているかを core の記録から組み立てる", () => {
  const r = (over: Partial<GraphEdge>): ChallengeRelation => ({
    ...dep("C-1", "C-2"),
    edge: edge("C-1", "C-2", over),
  });
  const plan = reasonText(r({ registered_via: "plan", satisfied: true }));
  expect(plan).toContain("終わらないと始められない");
  expect(plan).toContain("計画の判断で登録");
  expect(plan).toContain("満たされている");
  expect(reasonText(r({ registered_via: "manual" }))).toContain(
    "端末のコマンドで登録",
  );
  expect(reasonText(r({ registered_via: "unknown" }))).toContain("経路は不明");
  expect(reasonText(r({ kind: "conflict" }))).toContain("衝突する");
  expect(reasonText(r({}))).toContain("まだ満たされていない");
  const chip = chipText(r({ registered_via: "manual", satisfied: false }));
  expect(chip).toContain("終わらないと始められない");
  expect(chip).toContain("端末のコマンド");
  expect(chip).toMatch(/\d\d\/\d\d \d\d:\d\d/);
  expect(chipLines(r({ satisfied: true }))[1]).toMatch(/満たされている$/);
  // 読めない日時はそのまま出す
  expect(chipText(r({ registered_at: "とある日" }))).toContain("とある日");
});

it("配置: 先行・親を上の層に置き、関係の無い課題は下にまとめる", () => {
  const l = layoutGraph(
    ["C-1", "C-2", "C-3", "C-4", "C-9"],
    [
      dep("C-1", "C-2"),
      dep("C-1", "C-3"),
      dep("C-2", "C-4"),
      dep("C-3", "C-4"),
    ],
    D,
  );
  const layer = Object.fromEntries(l.nodes.map((n) => [n.id, n.layer]));
  expect(layer).toEqual({ "C-1": 0, "C-2": 1, "C-3": 1, "C-4": 2, "C-9": -1 });
  expect(l.isolated).toEqual(["C-9"]);
  expect(l.hasCycle).toBe(false);
  expect(l.edges).toHaveLength(4);
});

it("配置: 循環を見つけたら印を立て、全部の課題を置く", () => {
  const l = layoutGraph(
    ["C-1", "C-2", "C-3"],
    [dep("C-1", "C-2"), dep("C-2", "C-3"), dep("C-3", "C-2")],
    D,
  );
  expect(l.hasCycle).toBe(true);
  expect(l.nodes.map((n) => n.id).sort()).toEqual(["C-1", "C-2", "C-3"]);
});

it("たどり: 先行・後続は依存だけを連鎖してたどり、親子はたどらない", () => {
  const rels = [
    dep("C-4", "C-1"),
    dep("C-4", "C-10"),
    dep("C-1", "C-9"),
    dep("C-10", "C-9"),
    par("C-2", "C-4"),
  ];
  expect(downstreamOf("C-4", rels)).toEqual(["C-1", "C-9", "C-10"]);
  expect(upstreamOf("C-9", rels)).toEqual(["C-1", "C-4", "C-10"]);
  expect(upstreamOf("C-4", rels)).toEqual([]);
});

it("入れ子: 開いた親は大枠になり子を中に置き、畳んだ親は 1 つの箱で、畳んだ親の中の依存は親の箱へつなぎ替わる", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4"];
  const rels = [par("C-1", "C-2"), par("C-1", "C-3"), dep("C-2", "C-4")];
  const closed = layoutGraph(ids, rels, D, new Set());
  expect(closed.groups).toHaveLength(0);
  expect(closed.nodes.map((n) => n.id).sort()).toEqual(["C-1", "C-4"]);
  expect(closed.edges.map((e) => [e.from, e.to])).toEqual([["C-1", "C-4"]]);
  const open = layoutGraph(ids, rels, D, new Set(["C-1"]));
  expect(open.groups.map((g) => g.id)).toEqual(["C-1"]);
  expect(open.nodes.map((n) => n.id).sort()).toEqual(["C-2", "C-3", "C-4"]);
  expect(open.edges.map((e) => [e.from, e.to])).toEqual([["C-2", "C-4"]]);
  const g = open.groups[0];
  const c2 = open.nodes.find((n) => n.id === "C-2");
  expect(g && c2 && c2.x >= g.x && c2.x + c2.w <= g.x + g.w).toBe(true);
});

it("入れ子: 親子が循環する関係は捨てる", () => {
  const pm = parentMap(new Set(["C-1", "C-2"]), [
    par("C-1", "C-2"),
    par("C-2", "C-1"),
  ]);
  // 子の ID の小さい関係を先に採り、輪になる 2 つ目は捨てる
  expect([...pm.entries()]).toEqual([["C-1", "C-2"]]);
});

it("入れ子: 幅の上限を超える段は折り返す", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4", "C-5"];
  const rels = ids.slice(1).map((c) => dep("C-1", c));
  const l = layoutGraph(ids, rels, { ...D, maxW: 220 }, new Set());
  expect(l.width).toBeLessThanOrEqual(220 + 2 * D.pad);
  const ys = new Set(l.nodes.filter((n) => n.layer === 1).map((n) => n.y));
  expect(ys.size).toBe(2);
});

// --- 終わった課題（完了・取り下げ）の既定の隠し方 ---

const done = (id: string, over: Partial<GraphNode> = {}) =>
  node(id, { category: "done", ...over });
const withdrawn = (id: string, over: Partial<GraphNode> = {}) =>
  node(id, { category: "withdrawn", ...over });
const shown = (nodes: GraphNode[], rels: ChallengeRelation[], all = false) =>
  visibleIds(nodes, rels, all).sort();

it("終わった課題の隠し方: 動いている課題はすべて残り、つながりの無い終わった課題は隠れる", () => {
  const nodes = [
    node("C-1"),
    node("C-2", { category: "human" }),
    done("C-3"),
    withdrawn("C-4"),
  ];
  expect(shown(nodes, [])).toEqual(["C-1", "C-2"]);
});

it("終わった課題の隠し方: 動いている課題の前提（依存の先行）になっている終わった課題は残る", () => {
  const nodes = [done("C-1"), withdrawn("C-2"), node("C-3")];
  const rels = [dep("C-1", "C-3"), dep("C-2", "C-3")];
  expect(shown(nodes, rels)).toEqual(["C-1", "C-2", "C-3"]);
});

it("終わった課題の隠し方: 前提の種類は問わず、衝突のための依存の先行も残る", () => {
  const nodes = [done("C-1"), node("C-2")];
  const rels: ChallengeRelation[] = [
    { ...dep("C-1", "C-2"), edge: edge("C-1", "C-2", { kind: "conflict" }) },
  ];
  expect(shown(nodes, rels)).toEqual(["C-1", "C-2"]);
});

it("終わった課題の隠し方: 動いている課題の後続にしかならない終わった課題は隠れる", () => {
  const nodes = [node("C-1"), done("C-2")];
  expect(shown(nodes, [dep("C-1", "C-2")])).toEqual(["C-1"]);
});

it("終わった課題の隠し方: 終わった課題どうしの前提は残さない（動いている課題へつながらない）", () => {
  const nodes = [done("C-1"), done("C-2"), node("C-3")];
  // C-1 → C-2 は終わった同士。C-2 は C-3 の前提ではない
  expect(shown(nodes, [dep("C-1", "C-2")])).toEqual(["C-3"]);
});

it("終わった課題の隠し方: 動いている子を持つ終わった親は、祖先の連鎖ごと残る", () => {
  const nodes = [
    done("C-1"),
    done("C-2", { parent_id: "C-1" }),
    node("C-3", { parent_id: "C-2" }),
    done("C-4"),
    done("C-5", { parent_id: "C-4" }),
  ];
  const rels = relationsOf({ nodes, edges: [] });
  // C-3（動いている）の親 C-2 と祖父 C-1 は残り、動いている子の無い C-4・C-5 は隠れる
  expect(shown(nodes, rels)).toEqual(["C-1", "C-2", "C-3"]);
});

it("終わった課題の隠し方: 残る終わった前提が親の中にあるときは、その親も入れ子を保つために残る", () => {
  const nodes = [
    done("C-1"),
    done("C-2", { parent_id: "C-1" }),
    node("C-3"),
    done("C-4", { parent_id: "C-1" }),
  ];
  const rels = relationsOf({ nodes, edges: [edge("C-2", "C-3")] });
  expect(shown(nodes, rels)).toEqual(["C-1", "C-2", "C-3"]);
});

it("終わった課題の隠し方: 動いている親の終わった子は、前提になっていなければ隠れる", () => {
  const nodes = [node("C-1"), done("C-2", { parent_id: "C-1" })];
  expect(shown(nodes, relationsOf({ nodes, edges: [] }))).toEqual(["C-1"]);
});

it("終わった課題の隠し方: 全部を見る切り替えを入れると、終わった課題も含めてすべて出る", () => {
  const nodes = [node("C-1"), done("C-2"), withdrawn("C-3")];
  expect(shown(nodes, [], true)).toEqual(["C-1", "C-2", "C-3"]);
});

// --- 注目の課題と「ほかの課題」の大枠 ---

// 注目の判定の入力: 表示中の課題 ids の関係と、課題の行（nodes）
const focus = (nodes: GraphNode[], rels: ChallengeRelation[] = []) =>
  focusOf(
    nodes.map((n) => n.id),
    rels,
    nodes,
  );
const reasons = (nodes: GraphNode[], rels: ChallengeRelation[] = []) =>
  Object.fromEntries(focus(nodes, rels));
const todoNode = (id: string, over: Partial<GraphNode> = {}) =>
  node(id, { state: "classified", ...over });

it("注目 ①: 人間の操作待ち（next_human_actions が空でない）は、線が無くても状態が何でも前に出る", () => {
  const nodes = [
    todoNode("C-1", { category: "human", next_human_actions: ["triage"] }),
    node("C-2", {
      state: "awaiting_plan_approval",
      category: "human",
      next_human_actions: ["approve_plan", "answer_question"],
    }),
    // 人間の操作待ちが無ければ、分類待ち・計画待ちの P1 は前に出ない
    todoNode("C-3", { priority: "P1" }),
  ];
  expect(reasons(nodes)).toEqual({ "C-1": "human", "C-2": "human" });
});

it("注目 ②: 着手中・検証中・実行中（run が動いている）は前に出る", () => {
  const nodes = [
    node("C-1", { state: "in_progress" }),
    node("C-2", { state: "verifying" }),
    // 状態が計画待ちでも、run が動いていれば前に出る
    todoNode("C-3", { running: true }),
    // 完了確認待ちでも、人間の操作待ちが無く run も動いていなければ前に出ない
    node("C-4", { state: "awaiting_completion_approval" }),
  ];
  expect(reasons(nodes)).toEqual({
    "C-1": "working",
    "C-2": "working",
    "C-3": "working",
  });
});

it("注目 ③: 分類待ち・計画待ちは P0 だけ前に出る。P1・P2・優先度なしは前に出ない", () => {
  const nodes = [
    node("C-1", { state: "unclassified", priority: "P0" }),
    todoNode("C-2", { priority: "P0" }),
    todoNode("C-3", { priority: "P1" }),
    todoNode("C-4", { priority: "P2" }),
    node("C-5", { state: "unclassified" }),
    // P0 でも、分類待ち・計画待ちでなければ③ではない（着手前の TODO ではない）
    node("C-6", { state: "awaiting_completion_approval", priority: "P0" }),
  ];
  expect(reasons(nodes)).toEqual({ "C-1": "urgent", "C-2": "urgent" });
});

it("注目: 複数に当たるときは①②③の順で最初に当たったものを返す", () => {
  const nodes = [
    node("C-1", {
      state: "in_progress",
      running: true,
      category: "human",
      next_human_actions: ["approve_operation"],
    }),
    todoNode("C-2", { priority: "P0", running: true }),
  ];
  expect(reasons(nodes)).toEqual({ "C-1": "human", "C-2": "working" });
});

it("注目 ④: 起点の依存の相手（前提・後続・衝突）は、段数に関係なく推移的に前に出る", () => {
  const nodes = [
    node("C-1", { state: "in_progress" }), // 起点
    todoNode("C-2"), // C-1 の前提
    todoNode("C-3"), // C-1 の後続
    todoNode("C-4"), // C-1 と衝突
    todoNode("C-5"), // C-2 の前提（C-1 から見て 2 段先）
    todoNode("C-6"), // C-5 の前提（3 段先）
    todoNode("C-7"), // 何ともつながらない
  ];
  const rels: ChallengeRelation[] = [
    dep("C-2", "C-1"),
    dep("C-1", "C-3"),
    { ...dep("C-4", "C-1"), edge: edge("C-4", "C-1", { kind: "conflict" }) },
    dep("C-5", "C-2"),
    dep("C-6", "C-5"),
  ];
  expect(reasons(nodes, rels)).toEqual({
    "C-1": "working",
    "C-2": "linked",
    "C-3": "linked",
    "C-4": "linked",
    "C-5": "linked",
    "C-6": "linked",
  });
});

it("注目 ④: 親子の線も両方向にたどる。起点の祖先・子・孫・兄弟と、その先の依存の相手まで前に出る", () => {
  const nodes = [
    todoNode("C-1"), // 祖父
    todoNode("C-2", { parent_id: "C-1" }), // 親
    node("C-3", { state: "in_progress", parent_id: "C-2" }), // 起点
    todoNode("C-4", { parent_id: "C-2" }), // 起点の兄弟
    todoNode("C-5", { parent_id: "C-3" }), // 起点の子
    todoNode("C-6", { parent_id: "C-5" }), // 起点の孫
    todoNode("C-7"), // 孫 C-6 の後続
    todoNode("C-8"), // 何ともつながらない
  ];
  const rels = [
    par("C-1", "C-2"),
    par("C-2", "C-3"),
    par("C-2", "C-4"),
    par("C-3", "C-5"),
    par("C-5", "C-6"),
    dep("C-6", "C-7"),
  ];
  expect(reasons(nodes, rels)).toEqual({
    "C-1": "linked",
    "C-2": "linked",
    "C-3": "working",
    "C-4": "linked",
    "C-5": "linked",
    "C-6": "linked",
    "C-7": "linked",
  });
});

it("注目 ④: 注目している親の子・孫は、自分が起点に当たらなくても前に出る", () => {
  const nodes = [
    node("C-1", { category: "human", next_human_actions: ["approve_plan"] }), // 起点の親
    todoNode("C-2", { parent_id: "C-1" }), // 子
    todoNode("C-3", { parent_id: "C-2" }), // 孫
  ];
  const rels = [par("C-1", "C-2"), par("C-2", "C-3")];
  expect(reasons(nodes, rels)).toEqual({
    "C-1": "human",
    "C-2": "linked",
    "C-3": "linked",
  });
});

it("注目 ④: 表示している終わった課題を通る線もつながりに数える。終わった課題そのものは注目に入らない", () => {
  const nodes = [
    node("C-1", { state: "in_progress" }),
    node("C-2", { state: "done", category: "done" }), // C-1 と C-3 の前提
    todoNode("C-3"),
  ];
  const rels = [dep("C-2", "C-1"), dep("C-2", "C-3")];
  expect(reasons(nodes, rels)).toEqual({ "C-1": "working", "C-3": "linked" });
});

it("注目: 終わった課題（完了・取り下げ）は注目に入らない。隠れた課題につながる関係は数えない", () => {
  const nodes = [
    node("C-1", { state: "in_progress" }),
    node("C-2", { state: "done", category: "done" }),
    node("C-3", { state: "withdrawn", category: "withdrawn", priority: "P0" }),
    todoNode("C-4"),
  ];
  // C-2 は C-1 の前提、C-3 は C-1 の親。C-4 は表示していない C-9 の注目の課題の前提
  const rels = [dep("C-2", "C-1"), par("C-3", "C-1"), dep("C-4", "C-9")];
  expect(reasons(nodes, rels)).toEqual({ "C-1": "working" });
  // ids に無い課題は、nodes に載っていても判定の対象にしない
  expect(
    Object.fromEntries(
      focusOf(["C-1"], rels, [...nodes, node("C-9", { running: true })]),
    ),
  ).toEqual({ "C-1": "working" });
});

it("ほかの課題: どの線でも注目の課題とつながらない、注目でない動いている課題がすべて入り、ID の数の昇順で返る", () => {
  const nodes = [
    todoNode("C-10"),
    node("C-2", { state: "in_progress" }),
    todoNode("C-3", { priority: "P1" }),
    todoNode("C-4", { priority: "P0" }),
    todoNode("C-5", { category: "human", next_human_actions: ["triage"] }),
    node("C-6", { state: "awaiting_completion_approval" }),
  ];
  const ids = nodes.map((n) => n.id);
  expect(othersOf(ids, [], nodes)).toEqual(["C-3", "C-6", "C-10"]);
});

it("ほかの課題: 注目の親の子・孫は大枠へ移らない。起点を含まないつながりのまとまりは、まとまりごと大枠に入る", () => {
  const nodes = [
    node("C-1", { state: "in_progress" }), // 起点の親
    todoNode("C-2", { parent_id: "C-1" }), // 子 → 前
    todoNode("C-3", { parent_id: "C-2" }), // 孫 → 前
    todoNode("C-4"), // 起点を含まないまとまり（親）
    todoNode("C-5", { parent_id: "C-4" }), // その子
    todoNode("C-6"), // C-5 の後続
  ];
  const rels = [
    par("C-1", "C-2"),
    par("C-2", "C-3"),
    par("C-4", "C-5"),
    dep("C-5", "C-6"),
  ];
  expect(
    othersOf(
      nodes.map((n) => n.id),
      rels,
      nodes,
    ),
  ).toEqual(["C-4", "C-5", "C-6"]);
});

it("ほかの課題: 残した終わった課題は、属するまとまりが大枠なら大枠の中、注目のまとまりなら前に置く", () => {
  const nodes = [
    node("C-1", { state: "done", category: "done" }), // 大枠の C-4 の前提だけ → 大枠
    node("C-2", { state: "done", category: "done" }), // 注目の C-5 の前提 → 前
    node("C-3", { state: "withdrawn", category: "withdrawn" }), // 関係なし（全部を見る） → 前
    todoNode("C-4"),
    node("C-5", { state: "in_progress" }),
    node("C-6", { state: "done", category: "done" }), // C-7（終わった）の親、C-7 は大枠の C-4 の前提 → 大枠
    node("C-7", { state: "done", category: "done", parent_id: "C-6" }),
  ];
  const rels = [
    dep("C-1", "C-4"),
    dep("C-2", "C-5"),
    dep("C-7", "C-4"),
    par("C-6", "C-7"),
  ];
  expect(
    othersOf(
      nodes.map((n) => n.id),
      rels,
      nodes,
    ),
  ).toEqual(["C-1", "C-4", "C-6", "C-7"]);
});

it("人間の操作待ちの札: 帯の区分の語を、next_human_actions の並びで、同じ語は 1 つにして返す", () => {
  const labels = (acts: string[], category = "human") =>
    humanActionLabels(node("C-1", { category, next_human_actions: acts }));
  expect(labels(["approve_completion", "approve_operation"])).toEqual([
    HUMAN_KIND_LABELS.approvals,
    HUMAN_KIND_LABELS.operations,
  ]);
  // 承認 2 つ（計画・完了）は 1 つの語にまとまる
  expect(labels(["approve_plan", "approve_completion"])).toEqual(["承認待ち"]);
  // 並びは next_human_actions のとおり（帯の並びに直さない）
  expect(labels(["clear_slot", "answer_question", "triage"])).toEqual([
    "片づけ待ちのスロット",
    "未回答の問い",
    "トリアージ",
  ]);
  // 人間の操作待ちでなければ札は無い。知らない行動の値は飛ばす
  expect(labels([], "system")).toEqual([]);
  expect(labels(["unknown_action", "increase_budget"])).toEqual([
    "予算の使い切り",
  ]);
});

it("人間の操作待ちのチップ: 帯の区分ごとの記号と語を、next_human_actions の並びで、同じ区分は 1 つにして返す", () => {
  const chips = (acts: string[], category = "human") =>
    humanChips(node("C-1", { category, next_human_actions: acts }));
  expect(chips(["approve_plan", "approve_completion", "triage"])).toEqual([
    { kind: "approvals", glyph: "✓", label: "承認待ち" },
    { kind: "triage", glyph: "↕", label: "トリアージ" },
  ]);
  expect(chips([], "system")).toEqual([]);
  // 語は帯の語のまま（チップの語と labels は同じ並び）
  expect(chips(["clear_slot", "answer_question"]).map((c) => c.label)).toEqual([
    "片づけ待ちのスロット",
    "未回答の問い",
  ]);
});

it("人間の操作待ちのチップの並べ方: 右寄せの 1 行に並べ、入らないチップは「…」で切り詰めるか「+n」にまとめる", () => {
  const chips = humanChips(
    node("C-1", {
      category: "human",
      next_human_actions: ["approve_plan", "triage", "clear_slot"],
    }),
  );
  const wide = fitChips(chips, 400);
  expect(wide.items.map((c) => c.label)).toEqual([
    "承認待ち",
    "トリアージ",
    "片づけ待ちのスロット",
  ]);
  expect(wide.more).toBeNull();
  // 左から詰めて並べ、全体の幅が 1 行の幅になる
  const xs = wide.items.map((c) => c.x);
  expect(xs[0]).toBe(0);
  expect(xs).toEqual([...xs].sort((a, b) => a - b));
  const last = wide.items[2];
  expect(last && last.x + last.w).toBe(wide.width);
  expect(wide.width).toBeLessThanOrEqual(400);

  // 2 つ目以降が入らなければ、入る分だけ並べ、残りを「+n」にまとめる（まとめた区分は hidden に残る）
  const narrow = fitChips(chips, 110);
  expect(narrow.width).toBeLessThanOrEqual(110);
  expect(narrow.items.map((c) => c.label)).toEqual(["承認待ち"]);
  expect(narrow.more?.label).toBe("+2");
  expect(narrow.more?.hidden.map((c) => c.kind)).toEqual(["triage", "slots"]);
  expect(narrow.more && narrow.more.x + narrow.more.w).toBe(narrow.width);

  // 「…」で切れば入るなら切って置き、残りを「+n」にまとめる
  const mid = fitChips(chips, 150);
  expect(mid.width).toBeLessThanOrEqual(150);
  expect(mid.items.map((c) => c.label)).toEqual(["承認待ち", "トリ…"]);
  expect(mid.items[1]?.chip.label).toBe("トリアージ");
  expect(mid.more?.label).toBe("+1");
  expect(mid.more?.hidden.map((c) => c.kind)).toEqual(["slots"]);

  // 入らないのが最後の 1 つだけなら、その語を「…」で切って置く（+n にしない）
  const two = chips.slice(0, 2);
  const full = fitChips(two, 400).width;
  const cut = fitChips(two, full - 12);
  expect(cut.more).toBeNull();
  expect(cut.items).toHaveLength(2);
  expect(cut.items[1]?.label.endsWith("…")).toBe(true);
  expect(cut.items[1]?.chip.label).toBe("トリアージ");
  expect(cut.width).toBeLessThanOrEqual(full - 12);

  // 1 つも入らなくても、全部をまとめた「+n」は置く（何かを待っていることは必ず見せる）。その幅が minChipRoom
  const none = fitChips(chips, 5);
  expect(none.items).toEqual([]);
  expect(none.more?.label).toBe("+3");
  expect(none.width).toBe(minChipRoom(chips));
  expect(minChipRoom([])).toBe(0);
  expect(fitChips([], 100)).toEqual({ items: [], more: null, width: 0 });
});

it("人間の操作待ちのチップ: 記号の表が帯の全区分を覆い、記号は区分ごとに異なる", () => {
  expect(Object.keys(HUMAN_KIND_GLYPHS).sort()).toEqual(
    [...HUMAN_KINDS].sort(),
  );
  expect(HUMAN_KIND_GLYPHS).toEqual({
    approvals: "✓",
    questions: "?",
    operations: "!",
    discrepancies: "≠",
    budget: "$",
    triage: "↕",
    slots: "⌫",
  });
  expect(new Set(Object.values(HUMAN_KIND_GLYPHS)).size).toBe(
    HUMAN_KINDS.length,
  );
});

it("人間の操作待ちの札: 行動の値ごとの帯の区分の対応が、閉集合の全値を覆う", () => {
  expect(Object.keys(HUMAN_KIND_OF_ACTION).sort()).toEqual(
    [...CARD_ACTION_CODES].sort(),
  );
  for (const k of Object.values(HUMAN_KIND_OF_ACTION))
    expect(HUMAN_KINDS).toContain(k);
  expect(HUMAN_KIND_OF_ACTION).toMatchObject({
    approve_plan: "approvals",
    approve_completion: "approvals",
    answer_question: "questions",
    triage: "triage",
    increase_budget: "budget",
    approve_operation: "operations",
    review_discrepancy: "discrepancies",
    clear_slot: "slots",
  });
});

it("配置: ほかの課題は畳むと 1 つの箱、開くと大枠になり、中に動いている課題を置く", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4"];
  const rels = [dep("C-1", "C-2")];
  const isOther = (id: string) => id === "C-3" || id === "C-4";
  const closed = layoutGraph(ids, rels, D, new Set(), () => D.headH, isOther);
  expect(closed.groups).toHaveLength(0);
  expect(closed.nodes.map((n) => n.id).sort()).toEqual([
    "C-1",
    "C-2",
    OTHERS_ID,
  ]);
  expect(closed.isolated).toEqual([]);
  const open = layoutGraph(
    ids,
    rels,
    D,
    new Set([OTHERS_ID]),
    () => D.headH,
    isOther,
  );
  expect(open.groups.map((g) => g.id)).toEqual([OTHERS_ID]);
  expect(open.nodes.map((n) => n.id).sort()).toEqual([
    "C-1",
    "C-2",
    "C-3",
    "C-4",
  ]);
  const g = open.groups[0];
  for (const id of ["C-3", "C-4"]) {
    const n = open.nodes.find((b) => b.id === id);
    expect(
      g &&
        n &&
        n.x >= g.x &&
        n.x + n.w <= g.x + g.w &&
        n.y >= g.y + g.headH &&
        n.y + n.h <= g.y + g.h,
    ).toBe(true);
    expect(n?.depth).toBe(1);
  }
});

it("配置: ほかの課題に当たらない孤立した課題（終わった課題）は、従来の関係の無い課題の段に残る", () => {
  const l = layoutGraph(
    ["C-1", "C-2"],
    [],
    D,
    new Set(),
    () => D.headH,
    (id) => id === "C-1",
  );
  expect(l.isolated).toEqual(["C-2"]);
  expect(l.nodes.map((n) => n.id).sort()).toEqual(["C-2", OTHERS_ID]);
});

// --- 配置: 注目でない課題の大枠 ---

it("配置: 大枠の仲間どうしの依存は、大枠を開いたときだけ中の線として残り、畳むと 1 つの箱に隠れる", () => {
  const ids = ["C-1", "C-2", "C-3"];
  const rels = [dep("C-2", "C-3")];
  const isOther = (id: string) => id !== "C-1";
  const closed = layoutGraph(ids, rels, D, new Set(), () => D.headH, isOther);
  expect(closed.edges).toEqual([]);
  expect(closed.nodes.map((n) => n.id).sort()).toEqual(["C-1", OTHERS_ID]);
  const open = layoutGraph(
    ids,
    rels,
    D,
    new Set([OTHERS_ID]),
    () => D.headH,
    isOther,
  );
  expect(open.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["C-2>C-3"]);
  // 大枠の中で先行が上、後続が下
  const y = (id: string) => open.nodes.find((n) => n.id === id)?.y ?? -1;
  expect(y("C-2")).toBeLessThan(y("C-3"));
});

it("配置: 大枠の外の課題との依存は、大枠を畳むと大枠の箱へ、開くと中の課題へつながる", () => {
  const ids = ["C-1", "C-2", "C-3"];
  const rels = [dep("C-1", "C-2")];
  const isOther = (id: string) => id !== "C-1";
  const keys = (l: ReturnType<typeof layoutGraph>) =>
    l.edges.map((e) => `${e.from}>${e.to}`);
  const closed = layoutGraph(ids, rels, D, new Set(), () => D.headH, isOther);
  expect(keys(closed)).toEqual([`C-1>${OTHERS_ID}`]);
  // 大枠との間にだけ線を持つ課題は、関係の無い課題の段に落ちない
  expect(closed.isolated).toEqual([]);
  const open = layoutGraph(
    ids,
    rels,
    D,
    new Set([OTHERS_ID]),
    () => D.headH,
    isOther,
  );
  expect(keys(open)).toEqual(["C-1>C-2"]);
  expect(open.isolated).toEqual([]);
});

it("配置: 注目の親の子で大枠に入るものは、親の入れ子から外れて大枠の中へ置く", () => {
  const ids = ["C-1", "C-2", "C-3"];
  const rels = [par("C-1", "C-2"), par("C-1", "C-3")];
  const isOther = (id: string) => id === "C-3";
  const l = layoutGraph(
    ids,
    rels,
    D,
    new Set(["C-1", OTHERS_ID]),
    () => D.headH,
    isOther,
  );
  expect(l.parents.get("C-2")).toBe("C-1");
  expect(l.parents.has("C-3")).toBe(false);
  expect(l.groups.map((g) => g.id).sort()).toEqual(["C-1", OTHERS_ID]);
  const c3 = l.nodes.find((n) => n.id === "C-3");
  const others = l.groups.find((g) => g.id === OTHERS_ID);
  const c1 = l.groups.find((g) => g.id === "C-1");
  expect(c3?.depth).toBe(1);
  expect(
    others &&
      c3 &&
      c3.x >= others.x &&
      c3.x + c3.w <= others.x + others.w &&
      c3.y >= others.y + others.headH,
  ).toBe(true);
  // C-3 は親の大枠の中に入っていない
  expect(
    c1 &&
      c3 &&
      c3.y >= c1.y &&
      c3.y + c3.h <= c1.y + c1.h &&
      c3.x >= c1.x &&
      c3.x + c3.w <= c1.x + c1.w,
  ).toBe(false);
});

it("配置: 大枠の中の親子は入れ子のまま、畳んだ親は大枠の中で 1 つの箱になる", () => {
  const ids = ["C-1", "C-2", "C-3"];
  const rels = [par("C-2", "C-3")];
  const isOther = (id: string) => id !== "C-1";
  const l = layoutGraph(
    ids,
    rels,
    D,
    new Set([OTHERS_ID]),
    () => D.headH,
    isOther,
  );
  expect(l.nodes.map((n) => n.id).sort()).toEqual(["C-1", "C-2"]);
  expect(l.nodes.find((n) => n.id === "C-2")?.depth).toBe(1);
  const nested = layoutGraph(
    ids,
    rels,
    D,
    new Set([OTHERS_ID, "C-2"]),
    () => D.headH,
    isOther,
  );
  expect(nested.nodes.find((n) => n.id === "C-3")?.depth).toBe(2);
});

it("注目: 親のエージェントが動く番の課題は起点になり、線が無くても「ほかの課題」に隠れない", () => {
  const nodes = [
    node("C-1", {
      category: "parent_agent",
      state: "awaiting_human",
    }),
    node("C-2", { state: "classified", priority: "P2" }),
  ];
  const ids = ["C-1", "C-2"];
  expect(focusOf(ids, [], nodes).get("C-1")).toBe("working");
  expect(othersOf(ids, [], nodes)).toEqual(["C-2"]);
});
