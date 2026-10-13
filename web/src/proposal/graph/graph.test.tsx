import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { ChallengeRelation } from "../../api/proposed.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { OpenDetailContext } from "../../nav.tsx";
import { GRAPH_TITLE, GraphScreen, resetGraphMemory } from "./GraphScreen.tsx";
import { type Dims, downstreamOf, layoutGraph, upstreamOf } from "./model.ts";

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
  // 最長経路: C-1 → C-4 の直接の関係があっても C-4 は 2 段目のまま
  const l2 = layoutGraph(
    ["C-1", "C-2", "C-4"],
    [
      dep("C-1", "C-2"),
      dep("C-2", "C-4"),
      dep("C-1", "C-4"),
      dep("C-1", "C-404"),
    ],
    D,
  );
  expect(l2.nodes.find((n) => n.id === "C-4")?.layer).toBe(2);
  expect(l2.edges).toHaveLength(3);
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

it("たどり: 後続は依存だけを連鎖してたどり、親子はたどらない", () => {
  const rels: ChallengeRelation[] = [
    dep("C-4", "C-1"),
    dep("C-4", "C-10"),
    dep("C-1", "C-9"),
    dep("C-10", "C-9"),
    { from: "C-2", kind: "parent_of", to: "C-4" },
  ];
  expect(downstreamOf("C-4", rels)).toEqual(["C-1", "C-9", "C-10"]);
  expect(upstreamOf("C-9", rels)).toEqual(["C-1", "C-4", "C-10"]);
  expect(upstreamOf("C-4", rels)).toEqual([]);
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
    needsHuman: null,
    questions: null,
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

// グラフの中の課題の箱（畳む・開くのボタンと、いま実施中の行のボタンは除く）
const node = (id: string) =>
  within(screen.getByRole("group", { name: /の課題の関係グラフ$/ })).getByRole(
    "button",
    { name: new RegExp(`^${id} (?!を)`) },
  );

it("関係グラフ: 区分で色分けし、凡例に件数を出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  const region = screen.getByRole("region", { name: GRAPH_TITLE });
  await within(region).findByRole("group", {
    name: "alpha の課題の関係グラフ",
  });
  const legend = within(region).getByRole("list", { name: "区分の凡例" });
  for (const text of [
    "人間の操作待ち 7",
    "外部待ち 1",
    "システムが実行中 3",
    "システムが次に進めるもの 7",
    "完了 1",
  ]) {
    expect(within(legend).getByText(text)).toBeInTheDocument();
  }
  // 既定では親を畳むので、一番外の課題だけが箱になる
  expect(node("C-3")).toHaveAttribute("data-category", "human");
  expect(node("C-7")).toHaveAttribute("data-category", "human");
  expect(node("C-2")).toHaveAttribute("data-category", "system");
  expect(screen.queryByRole("button", { name: /^C-4 (?!を)/ })).toBeNull();
  // 開くと中の区分が見える
  fireEvent.click(screen.getByRole("button", { name: /^C-15 を開く/ }));
  expect(node("C-11")).toHaveAttribute("data-category", "external");
  expect(node("C-5")).toHaveAttribute("data-category", "running");
  fireEvent.click(screen.getByRole("button", { name: /^C-14 を開く/ }));
  expect(node("C-12")).toHaveAttribute("data-category", "done");
  expect(node("C-6").getAttribute("aria-label")).toContain("⚠ 更新なし");
  // 先行（実施中の C-6 など）が未完了の C-18 は、システムが進められる区分のまま印を付ける（先行待ちの区分は足さない）
  expect(node("C-18")).toHaveAttribute("data-category", "system");
  expect(node("C-18").getAttribute("aria-label")).toContain("先行が未完了");
  // 関係の無い C-3 は下の段にまとめる
  expect(within(region).getByText("関係の無い課題")).toBeInTheDocument();
  // 読めないワークスペースは選べない
  expect(
    within(region).getByRole("button", { name: /gamma（読めない/ }),
  ).toBeDisabled();
});

it("関係グラフ: 課題を選ぶと後続・先行を示し、詳細へ移れる", async () => {
  const open = vi.fn();
  render(
    <OpenDetailContext.Provider value={open}>
      <GraphScreen live={await liveOf()} />
    </OpenDetailContext.Provider>,
  );
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: /^C-14 を開く/ }));
  fireEvent.click(node("C-12"));
  const panel = screen.getByRole("complementary", { name: "選んだ課題" });
  const after = within(panel).getByText("これが終わると進められる課題")
    .nextElementSibling as HTMLElement;
  expect(
    within(after)
      .getAllByRole("button")
      .map((b) => b.textContent),
  ).toEqual(["C-6", "C-8", "C-13", "C-18"]);
  // 後続の後続（C-6 → C-18）も連鎖してたどる
  expect(node("C-18")).toHaveAttribute("data-relation", "downstream");
  // 親（大枠の見出し）は家族として薄くしない。関係の無い大枠（畳んだ C-2）は薄くする
  expect(node("C-14")).toHaveAttribute("data-relation", "family");
  expect(node("C-2")).toHaveAttribute("data-dim", "true");
  // パネルの関係から別の課題へ移る
  fireEvent.click(within(after).getByRole("button", { name: "C-18" }));
  expect(
    within(panel).getByRole("heading", { level: 3 }).textContent,
  ).toContain("C-18");
  fireEvent.click(within(panel).getByRole("button", { name: "詳細を開く" }));
  expect(open).toHaveBeenCalledWith("alpha", "C-18");
  // キーボードでも選べる
  fireEvent.keyDown(node("C-7"), { key: "Enter" });
  expect(
    within(panel).getByRole("heading", { level: 3 }).textContent,
  ).toContain("C-7");
});

it("関係グラフ: 区分・リポジトリで絞り、ワークスペースを切り替える", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // 畳んだ大枠は、中に当たる課題があれば薄くしない
  fireEvent.click(screen.getByRole("button", { name: /^外部待ち/ }));
  expect(node("C-15")).not.toHaveAttribute("data-dim");
  expect(node("C-3")).toHaveAttribute("data-dim", "true");
  fireEvent.click(screen.getByRole("button", { name: /^外部待ち/ }));
  fireEvent.click(screen.getByRole("button", { name: "mock-org/infra" }));
  expect(node("C-15")).not.toHaveAttribute("data-dim");
  expect(node("C-3")).toHaveAttribute("data-dim", "true");
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(node("C-1")).toHaveAttribute("data-dim", "true");
  expect(node("C-12")).not.toHaveAttribute("data-dim");
  fireEvent.click(screen.getByRole("checkbox", { name: "完了を隠す" }));
  expect(screen.queryByRole("button", { name: /^C-12 (?!を)/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "beta" }));
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
  expect(node("C-2").getAttribute("aria-label")).toContain(
    "デプロイ手順の整理",
  );
});

it("関係グラフ: 関係の読み込みに失敗したら示す", async () => {
  const live = await liveOf();
  setApi({
    ...api,
    challengeGraph: () => Promise.reject(new Error("boom")),
  });
  render(<GraphScreen live={live} />);
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "関係の読み込みに失敗",
  );
});
