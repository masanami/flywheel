import { expect, it } from "vitest";
import type { GraphEdge, GraphNode, GraphResponse } from "../api/types.gen.ts";
import { CARD_ACTION_CODES } from "../api/types.gen.ts";
import { HUMAN_KINDS, HUMAN_KIND_LABELS } from "../board/filter.ts";
import { HUMAN_KIND_OF_ACTION } from "../board/filter.ts";
import {
  type ChallengeRelation,
  type Dims,
  OTHERS_ID,
  basisOf,
  categoryLabel,
  categoryOf,
  chipLines,
  chipText,
  downstreamOf,
  humanActionLabels,
  isTodo,
  layoutGraph,
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

// --- 「ほかの課題」の大枠 ---

// 「ほかの課題」に入るのは、次の周以降で着手される TODO（分類待ち・計画待ち）で、線を持たない課題だけ
const todo = (id: string) => id.length > 0;
it("ほかの課題: 親も子も依存も持たない TODO の課題だけが入る", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4", "C-5", "C-6", "C-7", "C-8"];
  const rels = [
    par("C-1", "C-2"), // C-1 は親、C-2 は子
    dep("C-3", "C-4"), // 前提と後続
    dep("C-5", "C-4"),
  ];
  // C-6・C-7・C-8 は何ともつながらない。C-7 は TODO ではない（着手中など）
  expect(othersOf(ids, rels, (id) => id !== "C-7")).toEqual(["C-6", "C-8"]);
  expect(othersOf(ids, rels, todo)).toEqual(["C-6", "C-7", "C-8"]);
});

it("ほかの課題: 隠れた課題につながる辺は数えない（表示中の課題だけで決める）", () => {
  // C-2 は隠れた課題（ids に無い C-9）の前提だが、表示中の辺は持たない
  expect(othersOf(["C-1", "C-2"], [dep("C-2", "C-9")], todo)).toEqual([
    "C-1",
    "C-2",
  ]);
});

it("ほかの課題: 衝突の依存も、どちらの向きでも数える", () => {
  const rels: ChallengeRelation[] = [
    { ...dep("C-1", "C-2"), edge: edge("C-1", "C-2", { kind: "conflict" }) },
  ];
  expect(othersOf(["C-1", "C-2", "C-3"], rels, todo)).toEqual(["C-3"]);
});

it("ほかの課題: 該当が無ければ空で、大枠は出ない", () => {
  expect(othersOf(["C-1", "C-2"], [dep("C-1", "C-2")], todo)).toEqual([]);
});

it("TODO の判定: 分類待ち・計画待ちで人間の操作待ちでないものだけ。着手中・検証中・人間の操作待ち・終わった課題は入らない", () => {
  const yes = (state: string) =>
    isTodo(node("C-1", { state, category: "system" }));
  expect(yes("unclassified")).toBe(true);
  expect(yes("classified")).toBe(true);
  for (const s of [
    "awaiting_plan_approval",
    "in_progress",
    "verifying",
    "awaiting_completion_approval",
    "awaiting_human",
    "done",
    "withdrawn",
  ])
    expect(yes(s), s).toBe(false);
  // 分類待ち・計画待ちでも、人間の操作待ち（本番反映の承認など）を持てば TODO ではない
  expect(
    isTodo(
      node("C-1", {
        state: "classified",
        category: "human",
        next_human_actions: ["approve_operation"],
      }),
    ),
  ).toBe(false);
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
