// 3 巡目（flywheel#176・2026-10-14 オーナー）の検査: 依存の矢印に理由を持たせ、選んだとき・重ねたときに出す。
// 「終わらないと始められない」と「同時に走らせると衝突する」は常に線の形で分ける（2026-10-17 オーナー）。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { ChallengeRelation } from "../../api/proposed.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { seedGraphs } from "../../mock/graph.ts";
import { MockStore } from "../../mock/store.ts";
import { placeLabels } from "./GraphCanvas.tsx";
import { GraphScreen, WIDE, resetGraphMemory } from "./GraphScreen.tsx";
import { basisOf, layoutGraph, reasonText } from "./model.ts";

let api: ApiClient;
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

beforeEach(() => {
  resetGraphMemory();
  api = createMockApi(new MockStore(), { latencyMs: 0 });
  setApi(api);
});
afterEach(() => resetApi());

const graphOf = () =>
  screen.getByRole("group", { name: /の課題の関係グラフ$/ });
const node = (id: string) =>
  within(graphOf()).getByRole("button", { name: new RegExp(`^${id} (?!を)`) });

const need = (
  from: string,
  to: string,
  reason?: string,
): ChallengeRelation => ({
  basis: "prerequisite",
  from,
  kind: "depends_on",
  reason,
  to,
});
const clash = (
  from: string,
  to: string,
  reason: string,
): ChallengeRelation => ({
  basis: "conflict",
  from,
  kind: "depends_on",
  reason,
  to,
});
const edgePath = (from: string, to: string) =>
  graphOf().querySelector(
    `path.graph-edge[data-from="${from}"][data-to="${to}"]`,
  );
const edgeButton = (from: string, to: string) =>
  within(graphOf()).getByRole("button", {
    name: new RegExp(`^依存 ${from} → ${to}（`),
  });
const labels = () =>
  [...graphOf().querySelectorAll(".graph-edge-label")].map(
    (g) => `${g.getAttribute("data-from")}>${g.getAttribute("data-to")}`,
  );

it("偽の関係: 依存はすべて理由を持ち、2 種（先行の成果を使う・同時に走らせると衝突する）が両方ある", () => {
  for (const g of Object.values(seedGraphs())) {
    for (const r of g.relations.filter((r) => r.kind === "depends_on")) {
      expect(r.reason, `${r.from} → ${r.to}`).toBeTruthy();
      expect(["prerequisite", "conflict"]).toContain(r.basis);
    }
  }
  const kinds = new Set(seedGraphs().alpha.relations.map((r) => r.basis));
  expect(kinds).toEqual(new Set(["prerequisite", "conflict", undefined]));
});

it("線の種類: 1 つでも先行の成果を使う依存があれば実線、すべて衝突なら破線。畳んだ親へつなぎ替えた線は中の依存を全部持つ", () => {
  expect(basisOf([clash("C-1", "C-2", "x")])).toBe("conflict");
  expect(basisOf([clash("C-1", "C-2", "x"), need("C-3", "C-2")])).toBe(
    "prerequisite",
  );
  // 種類の無い依存は、終わらないと始められない側に倒す
  expect(basisOf([{ from: "C-1", kind: "depends_on", to: "C-2" }])).toBe(
    "prerequisite",
  );
  expect(reasonText(need("C-1", "C-2"))).toBe("終わらないと始められない");

  const rels: ChallengeRelation[] = [
    { from: "C-1", kind: "parent_of", to: "C-2" },
    { from: "C-1", kind: "parent_of", to: "C-3" },
    clash("C-2", "C-4", "同じファイルを触る"),
    clash("C-3", "C-4", "同じ設定を触る"),
  ];
  const shut = layoutGraph(["C-1", "C-2", "C-3", "C-4"], rels, WIDE);
  expect(shut.edges).toHaveLength(1);
  const e = shut.edges[0];
  expect(`${e?.from}>${e?.to}`).toBe("C-1>C-4");
  expect(e?.relations.map((r) => r.reason)).toEqual([
    "同じファイルを触る",
    "同じ設定を触る",
  ]);
  expect(e?.basis).toBe("conflict");
  // 線の中ほどは両端の中点
  const open = layoutGraph(
    ["C-1", "C-4"],
    [need("C-1", "C-4", "API を使う")],
    WIDE,
  );
  const [a, b] = ["C-1", "C-4"].map((id) =>
    open.nodes.find((n) => n.id === id),
  );
  if (!a || !b) throw new Error("箱が無い");
  expect(open.edges[0]?.mid).toEqual({
    x: (a.x + a.w / 2 + b.x + b.w / 2) / 2,
    y: (a.y + a.h + b.y) / 2,
  });
});

it("線の種類: 衝突のための順番は破線と中抜きの矢じりで、先行の成果を使う依存は実線で描く", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(edgePath("C-12", "C-8")?.getAttribute("data-basis")).toBe("conflict");
  expect(edgePath("C-12", "C-8")?.getAttribute("marker-end")).toBe(
    "url(#graph-arrow-conflict)",
  );
  expect(edgePath("C-12", "C-6")?.getAttribute("data-basis")).toBe(
    "prerequisite",
  );
  expect(edgePath("C-12", "C-6")?.getAttribute("marker-end")).toBe(
    "url(#graph-arrow)",
  );
  // 線の形の切り替えは置かない
  expect(screen.queryByRole("group", { name: "依存の線" })).toBeNull();
});

it("理由: 線は押せて、名前と重ねたときの吹き出しに理由を持つ。重ねると線の中ほどに理由の札が出る", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  const b = edgeButton("C-8", "C-18");
  expect(b.getAttribute("aria-label")).toBe(
    "依存 C-8 → C-18（移行が全件済んだと確かめてから消す）",
  );
  expect(b.querySelector("title")?.textContent).toBe(
    b.getAttribute("aria-label"),
  );
  expect(labels()).toEqual([]);
  fireEvent.mouseEnter(b);
  expect(labels()).toEqual(["C-8>C-18"]);
  expect(graphOf().querySelector(".graph-edge-label")?.textContent).toContain(
    "移行が全件済んだ",
  );
  fireEvent.mouseLeave(b);
  expect(labels()).toEqual([]);
});

it("理由: 線を押すと理由の札を出したままにし、もう一度押すと外す。課題に重ねても、つながる線に札は出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  const b = edgeButton("C-10", "C-4");
  fireEvent.click(b);
  expect(b).toHaveAttribute("aria-pressed", "true");
  expect(labels()).toEqual(["C-10>C-4"]);
  expect(graphOf().querySelector(".graph-edge-label")?.textContent).toContain(
    "同じ画面の部品",
  );
  expect(edgePath("C-10", "C-4")).toHaveAttribute("data-lit", "true");
  expect(edgePath("C-9", "C-4")).toHaveAttribute("data-dim", "true");
  // 理由のパネルは置かない（右は欄の場所）。種類と理由の全文は線の名前と吹き出しに持つ
  expect(
    screen.queryByRole("complementary", { name: "選んだ依存" }),
  ).toBeNull();
  expect(b.getAttribute("aria-label")).toBe(
    "依存 C-10 → C-4（同じ画面の部品（AdminToolbar）を触る）",
  );
  fireEvent.click(b);
  expect(labels()).toEqual([]);

  // 課題に重ねると先行・後続を強調するが、つながる線に札は出さない
  fireEvent.mouseEnter(node("C-18"));
  expect(edgePath("C-6", "C-18")).toHaveAttribute("data-lit", "true");
  expect(labels()).toEqual([]);
  fireEvent.mouseLeave(node("C-18"));
  // 線に重ねれば札が出る
  fireEvent.mouseEnter(edgeButton("C-6", "C-18"));
  expect(labels()).toEqual(["C-6>C-18"]);
});

it("札の置き場所: 同じ後続へ合流する 2 本の線の札は、中ほどが近くても重ならない", () => {
  const alpha = new MockStore().workspaces.alpha;
  if (!alpha) throw new Error("alpha が無い");
  const rels = seedGraphs().alpha.relations;
  const parents = new Set(
    rels.filter((r) => r.kind === "parent_of").map((r) => r.from),
  );
  const l = layoutGraph(
    alpha.items.map((m) => m.c.id),
    rels,
    WIDE,
    parents,
  );
  const placed = placeLabels(
    l.edges.filter((e) => e.to === "C-18"),
    WIDE.edgeChars,
  );
  expect(placed.map((q) => q.e.from).sort()).toEqual(["C-6", "C-8"]);
  const [a, b] = placed;
  if (!a || !b) throw new Error("札が 2 つ無い");
  const overlap = (h: number) =>
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + h && b.y < a.y + h;
  expect(overlap(18)).toBe(false);
});

it("理由: 畳んだ親へつなぎ替えた線は、中の課題どうしの依存の理由をまとめて持つ", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // C-15 を畳んだまま、C-19 → C-2 は C-15 → C-2 の線になる
  const b = edgeButton("C-15", "C-2");
  expect(b.getAttribute("aria-label")).toContain(
    "C-19 → C-2: ページングの配布は新しい手順で出す",
  );
  // 押すと、中の課題どうしの依存の理由を札に出す
  fireEvent.click(b);
  expect(graphOf().querySelector(".graph-edge-label")?.textContent).toContain(
    "ページングの配布は新しい手順",
  );
});
