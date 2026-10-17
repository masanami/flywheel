// 2026-10-13 の決定（flywheel#176）の検査: 親課題の大枠の入れ子と畳む操作・実施中の強調・グラフとボードの切り替え。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { ChallengeRelation } from "../../api/proposed.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { seedGraphs } from "../../mock/graph.ts";
import { MockStore } from "../../mock/store.ts";
import {
  GRAPH_TITLE,
  GraphScreen,
  NARROW,
  resetGraphMemory,
} from "./GraphScreen.tsx";
import {
  type Dims,
  ancestorsOf,
  layoutGraph,
  parentMap,
  visibleBoxOf,
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
const parent = (from: string, to: string): ChallengeRelation => ({
  from,
  kind: "parent_of",
  to,
});

it("入れ子: 親が複数なら ID の小さい親の大枠に置き、親子の循環は捨てる", () => {
  const ids = new Set(["C-1", "C-2", "C-3", "C-4", "C-5", "C-6"]);
  const pm = parentMap(ids, [
    parent("C-3", "C-4"),
    parent("C-2", "C-4"),
    parent("C-1", "C-2"),
    parent("C-9", "C-3"),
  ]);
  expect(pm.get("C-4")).toBe("C-2");
  expect(pm.get("C-2")).toBe("C-1");
  expect(pm.has("C-1")).toBe(false);
  expect(pm.has("C-3")).toBe(false);
  expect(ancestorsOf("C-4", pm)).toEqual(["C-2", "C-1"]);
  // 畳んだ祖先のうち一番外の箱に入る
  expect(visibleBoxOf("C-4", pm, new Set())).toBe("C-1");
  expect(visibleBoxOf("C-4", pm, new Set(["C-1"]))).toBe("C-2");
  expect(visibleBoxOf("C-4", pm, new Set(["C-1", "C-2"]))).toBe("C-4");
  // 循環（C-5 ⇄ C-6）は、子の ID の順に先に見た関係だけを採る
  const loop = parentMap(ids, [parent("C-5", "C-6"), parent("C-6", "C-5")]);
  expect([...loop]).toEqual([["C-5", "C-6"]]);
});

it("入れ子: 開いた親は大枠になり、子は大枠の内側に入る。畳むと依存の線を親の箱へつなぎ替える", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4", "C-5"];
  const rels = [
    parent("C-1", "C-2"),
    parent("C-1", "C-3"),
    dep("C-2", "C-3"),
    dep("C-3", "C-4"),
    dep("C-5", "C-2"),
  ];
  const open = layoutGraph(ids, rels, D, new Set(["C-1"]));
  const g = open.groups.find((b) => b.id === "C-1");
  if (!g) throw new Error("C-1 の大枠が無い");
  expect(open.nodes.map((n) => n.id).sort()).toEqual([
    "C-2",
    "C-3",
    "C-4",
    "C-5",
  ]);
  for (const id of ["C-2", "C-3"]) {
    const n = open.nodes.find((b) => b.id === id);
    if (!n) throw new Error(id);
    expect(n.depth).toBe(1);
    expect(n.x).toBeGreaterThanOrEqual(g.x);
    expect(n.y).toBeGreaterThanOrEqual(g.y + D.headH);
    expect(n.x + n.w).toBeLessThanOrEqual(g.x + g.w);
    expect(n.y + n.h).toBeLessThanOrEqual(g.y + g.h);
  }
  // 大枠の中の層: C-2 が上、C-3 が下
  const at = (l: typeof open, id: string) => l.nodes.find((n) => n.id === id);
  expect(at(open, "C-2")?.layer).toBe(0);
  expect(at(open, "C-3")?.layer).toBe(1);
  // 親子は線にしない。依存は大枠をまたいでも実際の課題どうしを結ぶ
  expect(open.edges.map((e) => `${e.from}>${e.to}`).sort()).toEqual([
    "C-2>C-3",
    "C-3>C-4",
    "C-5>C-2",
  ]);
  // 一番外では、大枠をまたぐ依存を持ち上げて層を決める（C-5 → C-1 → C-4）
  expect(open.groups[0]?.layer).toBe(1);
  expect(at(open, "C-4")?.layer).toBe(2);

  const shut = layoutGraph(ids, rels, D, new Set());
  expect(shut.groups).toEqual([]);
  expect(shut.nodes.map((n) => n.id).sort()).toEqual(["C-1", "C-4", "C-5"]);
  expect(shut.edges.map((e) => `${e.from}>${e.to}`).sort()).toEqual([
    "C-1>C-4",
    "C-5>C-1",
  ]);
  expect(shut.isolated).toEqual([]);
});

it("入れ子: 幅の上限を超える段は折り返す", () => {
  const ids = ["C-1", "C-2", "C-3", "C-4", "C-5"];
  const rels = ids.slice(1).map((c) => dep("C-1", c));
  const l = layoutGraph(ids, rels, { ...D, maxW: 220 }, new Set());
  expect(l.width).toBeLessThanOrEqual(220 + 2 * D.pad);
  const ys = new Set(l.nodes.filter((n) => n.layer === 1).map((n) => n.y));
  expect(ys.size).toBe(2);
});

it("入れ子: 幅 360px（狭い寸法）では、親を畳んでも開いても偽のデータが画面の幅に収まる", () => {
  const store = new MockStore();
  const alpha = store.workspaces.alpha;
  if (!alpha) throw new Error("alpha が無い");
  const ids = alpha.items.map((m) => m.c.id);
  const rels = seedGraphs().alpha.relations;
  const parents = new Set(
    rels.filter((r) => r.kind === "parent_of").map((r) => r.from),
  );
  // 画面の幅 360px から既存のページの左右の余白（12px ずつ）を引いた幅
  for (const open of [new Set<string>(), parents]) {
    const l = layoutGraph(ids, rels, NARROW, open);
    expect(l.width).toBeLessThanOrEqual(336);
  }
});

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

it("畳んだ親: 中の件数と、実施中・人間の操作待ちがあることを示し、開くと大枠になる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const c14 = node("C-14");
  expect(c14.getAttribute("aria-label")).toContain(
    "畳んだ子 5 件（実施中 1・人間の操作待ち 2）",
  );
  expect(c14).toHaveAttribute("data-active-inside", "true");
  expect(c14).toHaveAttribute("data-human-inside", "true");
  // 2 段の入れ子（C-15 の中の C-16 の中の C-17）も数える
  expect(node("C-15").getAttribute("aria-label")).toContain(
    "畳んだ子 5 件（実施中 2）",
  );
  expect(node("C-15")).not.toHaveAttribute("data-human-inside");
  expect(node("C-2").getAttribute("aria-label")).toContain(
    "畳んだ子 4 件（人間の操作待ち 3）",
  );
  expect(node("C-2")).not.toHaveAttribute("data-active-inside");
  // 畳んだ大枠の中の課題の依存は、畳んだ親の箱へつなぎ替える（C-19 → C-2 は C-15 → C-2）
  expect(
    graphOf().querySelector('path[data-from="C-15"][data-to="C-2"]'),
  ).not.toBeNull();

  fireEvent.click(
    screen.getByRole("button", { name: "C-15 を開く（子 5 件）" }),
  );
  // 開いた親は見出しつきの大枠になり、中の子が箱になる。子の大枠（C-16）は畳んだまま
  expect(node("C-15").getAttribute("aria-label")).toContain("開いた親");
  expect(node("C-5")).toBeInTheDocument();
  expect(node("C-16").getAttribute("aria-label")).toContain("畳んだ子 1 件");
  expect(
    graphOf().querySelector('path[data-from="C-11"][data-to="C-19"]'),
  ).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "C-15 を畳む" }));
  expect(
    within(graphOf()).queryByRole("button", { name: /^C-5 (?!を)/ }),
  ).toBeNull();
});

it("実施中の強調: いま実施中の課題を上に並べ、押すと大枠を開いて選ぶ", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const row = screen.getByRole("region", { name: "いま実施中" });
  expect(
    within(row)
      .getAllByRole("button")
      .map((b) => b.textContent?.split(" ")[0]),
  ).toEqual(["C-5", "C-6", "C-17"]);
  fireEvent.click(within(row).getByRole("button", { name: /^C-17 / }));
  // C-17 は C-15 → C-16 の 2 段の大枠の中にある
  expect(node("C-17")).toHaveAttribute("data-active", "true");
  expect(node("C-17")).toHaveAttribute("aria-pressed", "true");
  expect(node("C-17").textContent).toContain("実施中");
  expect(node("C-16").getAttribute("aria-label")).toContain("開いた親");
  const panel = screen.getByRole("complementary", { name: "選んだ課題" });
  const parentList = within(panel).getByText("親課題（外側の大枠）")
    .nextElementSibling as HTMLElement;
  expect(within(parentList).getByRole("button").textContent).toBe("C-16");
  // 実施中でない課題には印を付けない
  expect(node("C-7")).not.toHaveAttribute("data-active");
});

it("表示の切り替え: 既定はグラフで、列のボードへ切り替えられ、戻っても覚えている", async () => {
  const live = await liveOf();
  const { unmount } = render(<GraphScreen live={live} />);
  const region = screen.getByRole("region", { name: GRAPH_TITLE });
  const sw = within(region).getByRole("group", { name: "表示" });
  expect(within(sw).getByRole("button", { name: "グラフ" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(within(sw).getByRole("button", { name: "列のボード" }));
  expect(
    screen.queryByRole("group", { name: /の課題の関係グラフ$/ }),
  ).toBeNull();
  expect(
    screen.getByRole("region", { name: "人間待ちの帯" }),
  ).toBeInTheDocument();
  // 詳細を開いて戻ったとき（画面の作り直し）も、選んだ表示のまま
  unmount();
  render(<GraphScreen live={live} />);
  expect(screen.getByRole("button", { name: "列のボード" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
});
