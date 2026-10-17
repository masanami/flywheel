// 2026-10-17 オーナー: リポジトリの絞り込みはキーワードを入れて候補から選ぶ形にし（多数・長い名前に耐える）、
// 凡例は区分の名前と色だけにして実装の語（フィールド名など）を人に見せない。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { seedGraphs } from "../../mock/graph.ts";
import { MockStore } from "../../mock/store.ts";
import { GraphScreen, resetGraphMemory } from "./GraphScreen.tsx";
import { MAX_CANDIDATES, repoCandidates } from "./RepoFilter.tsx";

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
const input = () => screen.getByRole("combobox", { name: "リポジトリ" });
// 欄の「対象」の選択欄（select）の option と混ざらないよう、候補の並びの中だけを数える
const optionEls = () => {
  const list = screen.queryByRole("listbox", { name: "リポジトリの候補" });
  return list ? within(list).queryAllByRole("option") : [];
};
const options = () => optionEls().map((o) => o.textContent);
const chosen = () =>
  [
    ...(screen
      .queryByRole("list", { name: "絞り込むリポジトリ" })
      ?.querySelectorAll(".graph-repo-chosen-name") ?? []),
  ].map((n) => n.textContent);
const LONG = "mock-org/payments-reconciliation-batch-service-legacy-v2";

it("偽のデータ: alpha は 10 件以上のリポジトリを触り、名前の長いリポジトリを含む", () => {
  const repos = new Set(seedGraphs().alpha.repos.flatMap((r) => r.repos));
  expect(repos.size).toBeGreaterThanOrEqual(10);
  expect(repos).toContain(LONG);
});

it("候補: 入力した文字を含むリポジトリを大文字小文字を区別せずに出し、選んだものは除く", () => {
  const repos = ["org/web", "org/Web-Admin", "org/api", LONG];
  expect(repoCandidates(repos, "WEB", [])).toEqual([
    "org/Web-Admin",
    "org/web",
  ]);
  expect(repoCandidates(repos, "web", ["org/web"])).toEqual(["org/Web-Admin"]);
  expect(repoCandidates(repos, "  legacy ", [])).toEqual([LONG]);
  expect(repoCandidates(repos, "", [])).toHaveLength(4);
});

it("絞り込み: 札を並べず入力欄だけを置き、入力すると候補を出して、選んだものを足す（複数選べ、×で外せる）", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // 以前の札（リポジトリごとのボタン）は置かない
  expect(screen.queryByRole("button", { name: "mock-org/infra" })).toBeNull();
  expect(input().getAttribute("placeholder")).toContain("12 件");
  // 文字を入れる前は候補を全部は出さず、上限までにして残りの件数を示す
  fireEvent.focus(input());
  expect(options()).toHaveLength(MAX_CANDIDATES);
  expect(screen.getByText(/ほか 4 件/)).toBeInTheDocument();
  // 名前の一部（長い名前の途中）で候補が出る
  fireEvent.change(input(), { target: { value: "reconcil" } });
  expect(options()).toEqual([LONG]);
  fireEvent.click(screen.getByRole("option", { name: LONG }));
  expect(chosen()).toEqual([LONG]);
  expect(input()).toHaveValue("");
  // C-6・C-8 だけが触る。親を開くと、触らない課題は薄くなる
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(node("C-6")).not.toHaveAttribute("data-dim");
  expect(node("C-12")).toHaveAttribute("data-dim", "true");
  // 2 つ目を足すと、どちらかを触る課題を残す
  fireEvent.change(input(), { target: { value: "ci-config" } });
  fireEvent.click(screen.getByRole("option", { name: "mock-org/ci-config" }));
  expect(chosen()).toEqual([LONG, "mock-org/ci-config"]);
  expect(node("C-5")).not.toHaveAttribute("data-dim");
  expect(node("C-6")).not.toHaveAttribute("data-dim");
  expect(node("C-12")).toHaveAttribute("data-dim", "true");
  // 選んだものは候補に出さない
  fireEvent.change(input(), { target: { value: "mock-org" } });
  expect(options()).not.toContain(LONG);
  // ×で外す
  fireEvent.click(
    screen.getByRole("button", { name: `${LONG} を絞り込みから外す` }),
  );
  expect(chosen()).toEqual(["mock-org/ci-config"]);
  expect(node("C-6")).toHaveAttribute("data-dim", "true");
  // 当たらない文字は、無いことを示す
  fireEvent.change(input(), { target: { value: "zzz" } });
  expect(options()).toEqual([]);
  expect(
    screen.getByText("「zzz」を含むリポジトリはありません"),
  ).toBeInTheDocument();
  // 絞り込みを解除すると、選んだものも外れる
  fireEvent.click(screen.getByRole("button", { name: "絞り込みを解除" }));
  expect(chosen()).toEqual([]);
});

it("絞り込み: キーボードで候補を移って Enter で足し、空の入力欄の Backspace で最後のものを外し、Esc で候補を閉じる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.change(input(), { target: { value: "mock-org/" } });
  expect(input()).toHaveAttribute("aria-expanded", "true");
  const first = options()[0];
  const second = options()[1];
  // 先頭の候補を指している
  expect(input().getAttribute("aria-activedescendant")).toBe(
    optionEls()[0]?.id,
  );
  fireEvent.keyDown(input(), { key: "ArrowDown" });
  expect(optionEls()[1]).toHaveAttribute("aria-selected", "true");
  fireEvent.keyDown(input(), { key: "Enter" });
  expect(chosen()).toEqual([second]);
  fireEvent.change(input(), { target: { value: "mock-org/" } });
  fireEvent.keyDown(input(), { key: "Enter" });
  expect(chosen()).toEqual([second, first]);
  fireEvent.keyDown(input(), { key: "Backspace" });
  expect(chosen()).toEqual([second]);
  fireEvent.change(input(), { target: { value: "mock" } });
  fireEvent.keyDown(input(), { key: "Escape" });
  expect(input()).toHaveAttribute("aria-expanded", "false");
  expect(options()).toEqual([]);
});

it("凡例: 区分の名前と色だけを出し、実装の語（フィールド名・API の語）を出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const help = screen.getByRole("list", { name: "凡例の説明" });
  const human = [...help.querySelectorAll("li")].find(
    (li) => li.getAttribute("data-category") === "human",
  );
  expect(human?.textContent).toBe("人間の操作待ち");
  const legend = screen.getByRole("list", { name: "区分の凡例" });
  for (const b of within(legend).getAllByRole("button"))
    expect(b).not.toHaveAttribute("title");
  // 画面に見える文と吹き出し（title）に、実装の語を出さない
  const region = screen.getByRole("region", { name: "関係グラフ（案）" });
  const words =
    /next_human_actions|waiting_party|waiting_ci|status\.actionable|card\.|run_id/;
  expect(region.textContent).not.toMatch(words);
  for (const el of region.querySelectorAll("[title]"))
    expect(el.getAttribute("title")).not.toMatch(words);
});
