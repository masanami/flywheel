import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { type ApiClient, resetApi, setApi } from "../api/client.ts";
import type { GraphEdge, GraphNode, GraphResponse } from "../api/types.gen.ts";
import type { LiveState, WorkspaceData } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { RelationScreen } from "./RelationScreen.tsx";
import { WAITING_TITLE } from "./WaitingReasons.tsx";

// 関係グラフの画面（既定はグラフ・ボードへの切り替え・ワークスペースの選択・親の大枠の開閉・区分の色・
// 詳細のモーダルと待っている理由・線の種類と札・更新なしと実施中・幅 480px 以下の一覧）。
// 部品は本物で、データの出どころ（api）だけが偽の API。

const node = (id: string, over: Partial<GraphNode> = {}): GraphNode => ({
  category: "system",
  context: null,
  headline: null,
  id,
  parent_id: null,
  running: false,
  stale: false,
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

// alpha の課題 C-1〜C-20 のうち一部を使う関係。C-14 は実施中（更新なし）の子を持つので開き、
// C-2 は人間の操作待ちの子を持つので開き、C-15 は子が静かなので畳む。C-7 > C-8 > C-9 は孫が人間の操作待ち。
// C-3（実施中）と C-19（静か）は何ともつながらない動いている課題なので「ほかの課題」にまとまる（人間の操作待ちが
// 無いので畳む）。C-16 は取り下げで C-10 の前提なので既定でも出る。C-13（完了）・C-17（取り下げ）は何ともつながらず、
// C-18（完了）は動いている C-10 の後続にしかならないので、既定では隠れる。
const GRAPH: GraphResponse = {
  nodes: [
    node("C-1", { parent_id: "C-2" }),
    node("C-2", { headline: "検索のページング", context: "位置づけの文" }),
    node("C-4", { parent_id: "C-2", category: "human" }),
    node("C-5", { parent_id: "C-15" }),
    node("C-6", { parent_id: "C-14", running: true, stale: true }),
    node("C-7"),
    node("C-8", { parent_id: "C-7" }),
    node("C-9", { parent_id: "C-8", category: "human" }),
    node("C-10"),
    node("C-11", { parent_id: "C-15" }),
    node("C-12", { parent_id: "C-14", category: "done" }),
    node("C-14", { headline: "旧基盤を外す" }),
    node("C-15"),
    node("C-3", { running: true }),
    node("C-19"),
    node("C-16", { category: "withdrawn" }),
    node("C-13", { category: "done" }),
    node("C-17", { category: "withdrawn" }),
    node("C-18", { category: "done" }),
  ],
  edges: [
    edge("C-12", "C-6", { satisfied: true }),
    edge("C-5", "C-4", { kind: "conflict", registered_via: "manual" }),
    edge("C-12", "C-10", { registered_via: "unknown", satisfied: true }),
    edge("C-16", "C-10"),
    edge("C-10", "C-18"),
  ],
};

let api: ApiClient;
let graphFor: (ws: string) => Promise<GraphResponse>;
const calls: string[] = [];

async function liveOf(): Promise<LiveState> {
  const { workspaces } = await api.workspaces();
  const data: Record<string, WorkspaceData> = {};
  for (const w of workspaces.filter((w) => w.state === "ok")) {
    data[w.name] = {
      status: await api.status(w.name),
      challenges: await api.challenges(w.name),
    };
  }
  return {
    connected: true,
    lastUpdated: new Date(0),
    workspaces,
    data,
    needsHuman: await api.needsHuman(),
    loadError: null,
    fleetError: null,
  };
}

const realMatchMedia = window.matchMedia;
beforeEach(() => {
  calls.length = 0;
  const mock = createMockApi(new MockStore(), { latencyMs: 0 });
  graphFor = async () => GRAPH;
  api = {
    ...mock,
    challengeGraph: (ws) => {
      calls.push(ws);
      return graphFor(ws);
    },
  };
  setApi(api);
});
afterEach(() => {
  resetApi();
  window.matchMedia = realMatchMedia;
  vi.unstubAllGlobals();
});

function narrowScreen(matches: boolean) {
  window.matchMedia = ((query: string) => ({
    matches,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

const graphGroup = (ws = "alpha") =>
  screen.findByRole("group", { name: `${ws} の課題の関係グラフ` });
// グラフの中の課題の箱（畳む・開くのボタンと、いま実施中の行のボタンは除く）
const box = (group: HTMLElement, id: string) =>
  within(group).getByRole("button", { name: new RegExp(`^${id} (?!を)`) });

// 「ほかの課題」の大枠を開く（既定では畳んでいる）
const openOthers = (group: HTMLElement) =>
  fireEvent.click(
    within(group).getByRole("button", { name: /^ほかの課題を開く/ }),
  );

async function mount(initialView?: "graph" | "board") {
  const live = await liveOf();
  const view = render(<RelationScreen live={live} />);
  void initialView;
  return view;
}

it("既定はグラフで、ボードへ切り替えられ、グラフへ戻せる", async () => {
  await mount();
  expect(
    screen.getByRole("region", { name: "関係グラフ" }),
  ).toBeInTheDocument();
  const group = await graphGroup();
  expect(group).toBeInTheDocument();
  const sw = screen.getByRole("group", { name: "表示" });
  expect(within(sw).getByRole("button", { name: "グラフ" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  fireEvent.click(within(sw).getByRole("button", { name: "ボード" }));
  expect(screen.getByRole("region", { name: "ボード" })).toBeInTheDocument();
  expect(
    await screen.findByRole("region", { name: "人間待ちの帯" }),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole("group", { name: /の課題の関係グラフ$/ }),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "グラフ" }));
  expect(await graphGroup()).toBeInTheDocument();
});

it("ワークスペースは選択欄で選び、読めないワークスペースは出ない。右側はターミナル／対話の欄のために空けてある", async () => {
  graphFor = async (ws) =>
    ws === "beta" ? { nodes: [node("C-1")], edges: [] } : GRAPH;
  await mount();
  await graphGroup();
  const select = screen.getByRole("combobox", { name: "ワークスペース" });
  expect(
    within(select)
      .getAllByRole("option")
      .map((o) => o.textContent),
  ).toEqual(["alpha", "beta"]);
  fireEvent.change(select, { target: { value: "beta" } });
  expect(await graphGroup("beta")).toBeInTheDocument();
  expect(
    screen.getByRole("heading", { name: "beta の関係グラフ" }),
  ).toBeInTheDocument();
  expect(calls).toEqual(["alpha", "beta"]);
  const space = document.querySelector(".graph-dock-space");
  expect(space).not.toBeNull();
  expect(space).toBeEmptyDOMElement();
  expect(space).toHaveAttribute("aria-hidden", "true");
});

it("親の大枠: 開いたときは実施中か人間の操作待ちの子孫（孫を含む）を持つ親だけを開き、押して畳める・開ける", async () => {
  await mount();
  const group = await graphGroup();
  const btn = (name: string | RegExp) =>
    within(group).queryByRole("button", { name });
  // 開いている親: C-14（実施中の子）・C-2（人間の操作待ちの子）・C-7 と C-8（孫が人間の操作待ち）
  for (const id of ["C-14", "C-2", "C-7", "C-8"])
    expect(btn(`${id} を畳む`), id).not.toBeNull();
  // 畳んでいる親: C-15（子が静か）。重ねた箱で、中の件数を示す
  expect(btn(/^C-15 を開く（子 2 件）/)).not.toBeNull();
  expect(btn(/^C-5 /)).toBeNull();
  expect(box(group, "C-15")).toHaveAccessibleName(
    "C-15 題名C-15（システムが次に進めるもの・畳んだ子 2 件）",
  );
  // 大枠の中の子は見える
  expect(btn(/^C-6 /)).not.toBeNull();
  // 畳む
  fireEvent.click(within(group).getByRole("button", { name: "C-14 を畳む" }));
  expect(btn(/^C-6 /)).toBeNull();
  expect(btn(/^C-14 を開く/)).not.toBeNull();
  // 開く
  fireEvent.click(within(group).getByRole("button", { name: /^C-15 を開く/ }));
  expect(btn(/^C-5 /)).not.toBeNull();
  // すべて畳む／開く
  fireEvent.click(screen.getByRole("button", { name: "親をすべて畳む" }));
  expect(btn(/を畳む$/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(btn(/^C-5 /)).not.toBeNull();
  expect(btn(/^C-9 /)).not.toBeNull();
});

it("色: 人間の操作待ちは区分の色で区別でき、システムの課題は実行中かどうかで色が分かれる。完了は灰色", async () => {
  await mount();
  const group = await graphGroup();
  openOthers(group);
  const cat = (id: string) =>
    box(group, id).closest("[data-category]")?.getAttribute("data-category");
  expect(cat("C-4")).toBe("human");
  expect(cat("C-9")).toBe("human");
  expect(cat("C-3")).toBe("running");
  expect(cat("C-6")).toBe("running");
  expect(cat("C-10")).toBe("system");
  expect(cat("C-12")).toBe("done");
  expect(cat("C-16")).toBe("withdrawn");
  // 凡例は区分の名前と色と件数だけ（実装の語は出さない）
  const legend = screen.getByRole("list", { name: "区分の凡例" });
  const items = within(legend)
    .getAllByRole("button")
    .map((b) => b.textContent?.replace(/\s+/g, " ").trim());
  expect(items).toEqual([
    "人間の操作待ち 2",
    "外部待ち 0",
    "システムが実行中 2",
    "システムが次に進めるもの 10",
    "完了 1",
    "取り下げ 1",
  ]);
  expect(document.body.textContent).not.toMatch(
    /waiting_ci|next_human_actions|unresponsive/,
  );
});

it("区分の凡例を押すとその区分以外を薄くし、絞り込みの解除で戻る", async () => {
  await mount();
  const group = await graphGroup();
  fireEvent.click(screen.getByRole("button", { name: /^人間の操作待ち/ }));
  expect(box(group, "C-4").closest("[data-dim]")).toBeNull();
  expect(box(group, "C-10").closest("[data-dim]")).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "絞り込みを解除" }));
  expect(box(group, "C-10").closest("[data-dim]")).toBeNull();
});

const hasBox = (group: HTMLElement, id: string) =>
  within(group).queryByRole("button", { name: new RegExp(`^${id} (?!を)`) }) !==
  null;

it("終わった課題（完了・取り下げ）は既定で隠れ、動いている課題の前提と親になっているものだけが灰色で残る。切り替えで全部出る", async () => {
  await mount();
  const group = await graphGroup();
  // 動いている C-6・C-10 の前提（完了の C-12・取り下げの C-16）は残る
  expect(hasBox(group, "C-12")).toBe(true);
  expect(hasBox(group, "C-16")).toBe(true);
  // 何ともつながらない終わった課題と、動いている課題の後続にしかならない終わった課題は隠れる
  for (const id of ["C-13", "C-17", "C-18"])
    expect(hasBox(group, id)).toBe(false);
  // 隠れた課題につながる辺は描かない
  const edgesTo = (id: string) =>
    group.querySelectorAll(`path.graph-edge[data-to="${id}"]`).length;
  expect(edgesTo("C-18")).toBe(0);
  expect(edgesTo("C-10")).toBe(2);
  // 切り替えは「終わった課題もすべて表示」の 1 つだけ（既定は OFF）
  const sw = screen.getByRole("checkbox", { name: "終わった課題もすべて表示" });
  expect(sw).not.toBeChecked();
  expect(screen.queryByRole("checkbox", { name: "完了を隠す" })).toBeNull();
  fireEvent.click(sw);
  for (const id of ["C-13", "C-17", "C-18"])
    expect(hasBox(group, id)).toBe(true);
  expect(edgesTo("C-18")).toBe(1);
  // 終わった課題は「ほかの課題」へは入らない（切り替えを入れても、関係の無い課題の段に並ぶ）
  expect(
    within(group).getByRole("button", { name: /^ほかの課題を開く（2 件）/ }),
  ).toBeInTheDocument();
  // 戻すとまた隠れる
  fireEvent.click(sw);
  expect(hasBox(group, "C-13")).toBe(false);
});

it("取り下げた課題は完了と同じ灰色で、「取り下げ」の札で完了と区別し、取り消し線は付けない。凡例にも出る", async () => {
  await mount();
  const group = await graphGroup();
  const w = box(group, "C-16");
  expect(w.closest("[data-category]")?.getAttribute("data-category")).toBe(
    "withdrawn",
  );
  expect(within(w).getByText("取り下げ")).toBeInTheDocument();
  expect(w).toHaveAccessibleName("C-16 題名C-16（取り下げ）");
  // 完了の課題には札が付かない
  expect(within(box(group, "C-12")).queryByText("取り下げ")).toBeNull();
  expect(w.querySelector("[text-decoration], .line-through")).toBeNull();
  const legend = screen.getByRole("list", { name: "区分の凡例" });
  const item = within(legend).getByRole("button", { name: /^取り下げ/ });
  expect(item).toHaveAttribute("data-category", "withdrawn");
});

it("取り下げた親が動いている子のために開いた大枠になっても、見出しに「取り下げ」の札が付く", async () => {
  graphFor = async () => ({
    nodes: [
      node("C-1", { category: "withdrawn" }),
      node("C-2", { parent_id: "C-1", category: "human" }),
    ],
    edges: [],
  });
  await mount();
  const group = await graphGroup();
  const head = group.querySelector(".graph-group-head");
  expect(head?.querySelector(".graph-node-withdrawn-tag")?.textContent).toBe(
    "取り下げ",
  );
});

it("ほかの課題: 親も子も依存も持たない動いている課題を 1 つの大枠にまとめ、人間の操作待ちが無ければ畳んで件数を示す。押しても詳細は開かない", async () => {
  await mount();
  const group = await graphGroup();
  // C-3（実施中）・C-19 だけが入る。畳んでいるので中は見えない
  const closed = within(group).getByRole("button", {
    name: "ほかの課題を開く（2 件）",
  });
  expect(closed.textContent).toContain("ほかの課題");
  expect(closed.textContent).toContain("2件");
  expect(closed.textContent).toContain("▶実施中1");
  expect(closed.textContent).not.toContain("人間待ち");
  expect(hasBox(group, "C-3")).toBe(false);
  expect(hasBox(group, "C-19")).toBe(false);
  // 押すと開き、中に課題が並ぶ。詳細のモーダルは開かない
  fireEvent.click(closed);
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
  expect(hasBox(group, "C-3")).toBe(true);
  expect(hasBox(group, "C-19")).toBe(true);
  const open = within(group).getByRole("button", {
    name: "ほかの課題を畳む（2 件）",
  });
  expect(open).toHaveAttribute("aria-expanded", "true");
  fireEvent.click(open);
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
  expect(hasBox(group, "C-3")).toBe(false);
  // 「親をすべて開く」でも開く
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(hasBox(group, "C-3")).toBe(true);
});

it("ほかの課題: 中に人間の操作待ちの課題があれば既定で開き、動いている課題が無ければ大枠を出さない", async () => {
  graphFor = async () => ({
    nodes: [
      node("C-1"),
      node("C-2", { category: "human" }),
      node("C-3", { category: "done" }),
    ],
    edges: [],
  });
  await mount();
  const group = await graphGroup();
  expect(
    within(group).getByRole("button", { name: "ほかの課題を畳む（2 件）" }),
  ).toBeInTheDocument();
  expect(hasBox(group, "C-1")).toBe(true);
  expect(hasBox(group, "C-2")).toBe(true);
  // 完了の C-3 は隠れ、切り替えを入れても大枠には入らない
  expect(hasBox(group, "C-3")).toBe(false);
  fireEvent.click(
    screen.getByRole("checkbox", { name: "終わった課題もすべて表示" }),
  );
  expect(hasBox(group, "C-3")).toBe(true);
  expect(
    within(group).getByRole("button", { name: "ほかの課題を畳む（2 件）" }),
  ).toBeInTheDocument();
});

it("ほかの課題: 該当が 0 件なら大枠を出さない（依存を持つ課題・親子の課題は入らない）", async () => {
  graphFor = async () => ({
    nodes: [node("C-1"), node("C-2"), node("C-3", { parent_id: "C-1" })],
    edges: [edge("C-2", "C-3")],
  });
  await mount();
  const group = await graphGroup();
  expect(
    within(group).queryByRole("button", { name: /ほかの課題/ }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "親をすべて畳む" }),
  ).not.toBeNull();
});

it("更新なしの課題は「⚠ 更新なし（要確認）」と出し、実施中の課題には光の輪が付く", async () => {
  await mount();
  const group = await graphGroup();
  openOthers(group);
  const stale = box(group, "C-6");
  expect(within(stale).getByText("⚠ 更新なし（要確認）")).toBeInTheDocument();
  expect(stale.closest("[data-unresponsive]")).not.toBeNull();
  expect(stale.querySelector(".graph-node-halo")).not.toBeNull();
  expect(box(group, "C-3").querySelector(".graph-node-halo")).not.toBeNull();
  expect(box(group, "C-3").textContent).not.toContain("更新なし");
  expect(box(group, "C-10").querySelector(".graph-node-halo")).toBeNull();
  // 更新なしの課題は読み上げの名前にも出る
  expect(stale).toHaveAccessibleName(/更新なし/);
});

it("線: 先行の成果を使う依存は実線の矢じり、衝突は破線の矢じりで、押すと理由の札（種類・経路・日時・満たされているか）が出る", async () => {
  await mount();
  const group = await graphGroup();
  const edges = [...group.querySelectorAll<SVGPathElement>("path.graph-edge")];
  const prereq = edges.filter((e) => e.dataset.basis === "prerequisite");
  const conflict = edges.filter((e) => e.dataset.basis === "conflict");
  expect(prereq.length).toBeGreaterThan(0);
  expect(conflict).toHaveLength(1);
  for (const e of prereq)
    expect(e.getAttribute("marker-end")).toBe("url(#graph-arrow)");
  expect(conflict[0]?.getAttribute("marker-end")).toBe(
    "url(#graph-arrow-conflict)",
  );
  // 札は、押すまで（重ねるまで）出ない
  expect(group.querySelector(".graph-edge-label")).toBeNull();
  // C-5 は畳んだ C-15 の中にあるので、線は C-15 の箱から出る。C-15 を開くと C-5 から出る
  expect(
    within(group).getByRole("button", { name: /^依存 C-15 → C-4/ }),
  ).toBeInTheDocument();
  fireEvent.click(within(group).getByRole("button", { name: /^C-15 を開く/ }));
  const hit = within(group).getByRole("button", { name: /^依存 C-5 → C-4/ });
  expect(hit).toHaveAccessibleName(/同時に走らせると衝突する/);
  expect(hit).toHaveAccessibleName(/端末のコマンドで登録/);
  expect(hit).toHaveAccessibleName(/まだ満たされていない/);
  fireEvent.click(hit);
  const label = group.querySelector(".graph-edge-label");
  expect(label?.getAttribute("data-basis")).toBe("conflict");
  // 札: 1 行目は種類、2 行目は登録の経路・日時・満たされているか
  const rows = [...(label?.querySelectorAll("tspan") ?? [])].map(
    (t) => t.textContent,
  );
  expect(rows[0]).toBe("同時に走らせると衝突する");
  expect(rows[1]).toMatch(
    /^端末のコマンド・\d\d\/\d\d \d\d:\d\d・満たされていない$/,
  );
  // もう一度押すと外す
  fireEvent.click(hit);
  expect(group.querySelector(".graph-edge-label")).toBeNull();
  // 課題に重ねても札は出ない
  fireEvent.mouseEnter(box(group, "C-10"));
  expect(group.querySelector(".graph-edge-label")).toBeNull();
});

it("一言では、課題に重ねたとき（キーボードで移ったときも）の吹き出しで出し、開いた大枠は見出しの欄に出す", async () => {
  graphFor = async () => ({
    nodes: [
      node("C-1", { parent_id: "C-2", headline: "子の一言で" }),
      node("C-2", { headline: "親の一言で", context: "親の位置づけ" }),
      node("C-3", { headline: "単独の一言で" }),
    ],
    edges: [],
  });
  await mount();
  const group = await graphGroup();
  // 開いた大枠（C-2。実施中も人間待ちも子に無いので既定は畳み）を開く
  fireEvent.click(within(group).getByRole("button", { name: /^C-2 を開く/ }));
  expect(group.querySelector(".graph-callout")).toBeNull();
  openOthers(group);
  const c3 = box(group, "C-3");
  fireEvent.focus(c3);
  expect(group.querySelector('[data-callout="C-3"]')?.textContent).toContain(
    "単独の一言で",
  );
  fireEvent.blur(c3);
  expect(group.querySelector(".graph-callout")).toBeNull();
  // 大枠の見出しの欄: 一言でと位置づけ（開いた大枠には吹き出しを出さない）
  const head = group.querySelector(".graph-group-head");
  expect(head?.textContent).toContain("一言で: 親の一言で");
  expect(head?.textContent).toContain("位置づけ: 親の位置づけ");
  fireEvent.mouseEnter(head as Element);
  expect(group.querySelector(".graph-callout")).toBeNull();
});

it("課題を押すと、ボードと同じ詳細のモーダルが開き、待っている理由の節を出す。閉じるとフォーカスが戻る", async () => {
  await mount();
  const group = await graphGroup();
  const c6 = box(group, "C-6");
  c6.focus();
  fireEvent.click(c6);
  const content = await screen.findByTestId("card-detail-content");
  // 詳細はボードのカードと同じ部品（課題の題名は課題の一覧のもの）
  expect(within(content).getByText("古いバッチの撤去")).toBeInTheDocument();
  const waiting = within(content).getByRole("region", {
    name: WAITING_TITLE,
    hidden: true,
  });
  // 直接待っている先行（C-12）と、題名・完了の印・種類・理由
  expect(within(waiting).getByText("C-12")).toBeInTheDocument();
  expect(waiting.textContent).toContain("題名C-12");
  expect(waiting.textContent).toContain("（完了）");
  expect(waiting.textContent).toContain("終わらないと始められない");
  expect(waiting.textContent).toContain("計画の判断で登録");
  expect(waiting.textContent).toContain("満たされている");
  // 閉じるとフォーカスが押した箱へ戻る
  const dialog = content.closest("dialog") as HTMLDialogElement;
  act(() => {
    dialog.dispatchEvent(new Event("close"));
  });
  await waitFor(() =>
    expect(screen.queryByTestId("card-detail-content")).toBeNull(),
  );
  expect(document.activeElement).toBe(c6);
});

it("待っている先行が無い課題の詳細には、待っている理由の節を出さない", async () => {
  await mount();
  const group = await graphGroup();
  fireEvent.click(box(group, "C-12"));
  const content = await screen.findByTestId("card-detail-content");
  expect(within(content).getByText("古い設定の削除")).toBeInTheDocument();
  expect(within(content).queryByText(WAITING_TITLE)).toBeNull();
});

it("ボードから開いた詳細には、待っている理由の節を出さない（ボードの挙動は変えない）", async () => {
  await mount();
  await graphGroup();
  fireEvent.click(screen.getByRole("button", { name: "ボード" }));
  const card = await screen.findByTestId("card-C-6");
  fireEvent.click(within(card).getByRole("button"));
  const content = await screen.findByTestId("card-detail-content");
  expect(within(content).getByText("古いバッチの撤去")).toBeInTheDocument();
  expect(within(content).queryByText(WAITING_TITLE)).toBeNull();
});

it("読み込み中・失敗・課題が無いときの表示", async () => {
  let reject: (e: Error) => void = () => {};
  graphFor = () =>
    new Promise((_, r) => {
      reject = r;
    });
  await mount();
  expect(screen.getByText("読み込み中…")).toBeInTheDocument();
  await act(async () => reject(new Error("boom")));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "関係の読み込みに失敗",
  );
});

it("課題が無いワークスペースは「表示する課題がありません」", async () => {
  graphFor = async () => ({ nodes: [], edges: [] });
  await mount();
  expect(
    await screen.findByText("表示する課題がありません。"),
  ).toBeInTheDocument();
});

it("幅 480px 以下ではグラフの代わりに親ごとに畳んだ一覧を出し、各課題に区分と先行・後続を文字で付ける", async () => {
  narrowScreen(true);
  await mount();
  const list = await screen.findByRole("list", {
    name: "課題の一覧（親ごと）",
  });
  expect(
    screen.queryByRole("group", { name: /の課題の関係グラフ$/ }),
  ).toBeNull();
  expect(document.querySelector("svg")).toBeNull();
  const item = (id: string) =>
    within(list)
      .getByRole("button", { name: new RegExp(`^${id} (?!を)`) })
      .closest("li") as HTMLElement;
  // 一番外は親ごと: 子は親を開いたときだけ見える
  expect(within(list).queryByRole("button", { name: /^C-5 / })).toBeNull();
  const c15 = item("C-15");
  expect(c15.textContent).toContain("システムが次に進めるもの");
  expect(c15.textContent).toContain("畳んだ子 2 件");
  fireEvent.click(within(c15).getByRole("button", { name: /^C-15 を開く/ }));
  expect(
    within(list).getByRole("button", { name: /^C-5 / }),
  ).toBeInTheDocument();
  // 区分・先行・後続が文字で出る
  const c6 = item("C-6");
  expect(c6.textContent).toContain("システムが実行中");
  expect(c6.textContent).toContain("先行: C-12");
  expect(c6.textContent).toContain("後続: なし");
  expect(c6.textContent).toContain("⚠ 更新なし（要確認）");
  const c12 = item("C-12");
  expect(c12.textContent).toContain("完了");
  expect(c12.textContent).toContain("先行: なし");
  expect(c12.textContent).toContain("後続: C-6、C-10");
  expect(item("C-4").textContent).toContain("人間の操作待ち");
  expect(item("C-4").textContent).toContain("先行: C-5");
  // 開閉は広い画面と同じ規則（実施中・人間待ちの子孫を持つ親は開いている）
  expect(
    within(list).getByRole("button", { name: "C-14 を畳む" }),
  ).toBeInTheDocument();
  // 課題を押すと詳細のモーダルが開き、待っている理由も出る
  fireEvent.click(within(c6).getByRole("button", { name: /^C-6 / }));
  const content = await screen.findByTestId("card-detail-content");
  expect(
    within(content).getByRole("region", { name: WAITING_TITLE, hidden: true }),
  ).toBeInTheDocument();
});

it("幅 480px 以下の一覧でも、終わった課題は既定で隠し、ほかの課題を同じ規則で畳んだ見出しにまとめる", async () => {
  narrowScreen(true);
  await mount();
  const list = await screen.findByRole("list", {
    name: "課題の一覧（親ごと）",
  });
  const has = (id: string) =>
    within(list).queryByRole("button", {
      name: new RegExp(`^${id} (?!を)`),
    }) !== null;
  // 前提として残る終わった課題は灰色で出し、取り下げは区分の名前で区別する
  expect(has("C-12")).toBe(true);
  const c16 = within(list)
    .getByRole("button", { name: /^C-16 / })
    .closest("li") as HTMLElement;
  expect(c16.textContent).toContain("取り下げ");
  expect(c16.getAttribute("data-category")).toBe("withdrawn");
  for (const id of ["C-13", "C-17", "C-18"]) expect(has(id)).toBe(false);
  // 隠れた C-18 は後続にも出ない
  expect(
    (
      within(list)
        .getByRole("button", { name: /^C-10 / })
        .closest("li") as HTMLElement
    ).textContent,
  ).not.toContain("C-18");
  // ほかの課題は畳んだ見出し（件数と実施中の件数）。人間の操作待ちが無いので畳む
  const head = within(list).getByRole("button", {
    name: "ほかの課題を開く（2 件）",
  });
  const li = head.closest("li") as HTMLElement;
  expect(li.textContent).toContain("ほかの課題 2件");
  expect(li.textContent).toContain("実施中 1");
  expect(has("C-3")).toBe(false);
  fireEvent.click(head);
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
  expect(has("C-3")).toBe(true);
  expect(has("C-19")).toBe(true);
  // 全部を見る切り替えで終わった課題も並ぶ。ほかの課題には入らない
  fireEvent.click(
    screen.getByRole("checkbox", { name: "終わった課題もすべて表示" }),
  );
  for (const id of ["C-13", "C-17", "C-18"]) expect(has(id)).toBe(true);
  expect(
    within(list).getByRole("button", { name: "ほかの課題を畳む（2 件）" }),
  ).toBeInTheDocument();
});

it("幅 480px 以下の一覧でも、ほかの課題に人間の操作待ちの課題があれば既定で開く", async () => {
  narrowScreen(true);
  graphFor = async () => ({
    nodes: [node("C-1", { category: "human" }), node("C-2")],
    edges: [],
  });
  await mount();
  const list = await screen.findByRole("list", {
    name: "課題の一覧（親ごと）",
  });
  expect(
    within(list).getByRole("button", { name: "ほかの課題を畳む（2 件）" }),
  ).toBeInTheDocument();
  expect(
    within(list).getByRole("button", { name: /^C-1 / }),
  ).toBeInTheDocument();
});

it("狭い画面の一覧は、ボードへ切り替えるとボードを出し、グラフへ戻すと一覧に戻る", async () => {
  narrowScreen(true);
  await mount();
  await screen.findByRole("list", { name: "課題の一覧（親ごと）" });
  fireEvent.click(screen.getByRole("button", { name: "ボード" }));
  expect(
    await screen.findByRole("region", { name: "人間待ちの帯" }),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "グラフ" }));
  expect(
    await screen.findByRole("list", { name: "課題の一覧（親ごと）" }),
  ).toBeInTheDocument();
});

it("ボードと同じく、最後に読み込めた時刻が変わるたびに関係を読み直す（ストアの変更の通知・60 秒ごとの取り直し）", async () => {
  const live = await liveOf();
  const view = render(<RelationScreen live={live} />);
  await graphGroup();
  expect(calls).toEqual(["alpha"]);
  graphFor = async () => ({
    nodes: [node("C-1", { title: "更新後" }), node("C-2")],
    edges: [edge("C-1", "C-2")],
  });
  view.rerender(
    <RelationScreen live={{ ...live, lastUpdated: new Date(1000) }} />,
  );
  await waitFor(() => expect(calls).toEqual(["alpha", "alpha"]));
  expect(
    await screen.findByRole("button", { name: /^C-1 更新後/ }),
  ).toBeInTheDocument();
});
