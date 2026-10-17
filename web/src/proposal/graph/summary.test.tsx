// 3 巡目（flywheel#176・2026-10-14 オーナー）の検査: ノードに「一言で」を出し、題名は重ねたときとパネルへ回す。縮小は ID と色だけ。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { GraphScreen, WIDE, resetGraphMemory } from "./GraphScreen.tsx";
import { clipLines } from "./model.ts";

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
const variant = (group: string, name: string) =>
  fireEvent.click(
    within(screen.getByRole("group", { name: group })).getByRole("button", {
      name,
    }),
  );

const titleLine = (id: string) =>
  node(id).querySelector(".graph-node-title") as SVGTextElement | null;
// 標準の大きさのノードは、文を幅で 2 行まで出す
const face = (s: string) =>
  clipLines(s, WIDE.nodeW - 18, 13, WIDE.titleLines).join("");

it("一言で: ノードの 2 行目は一言でで、無ければ題名に戻る。題名は重ねたときの吹き出しに残す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // C-4 と C-1 は親 C-2 の大枠の中にある
  fireEvent.click(
    screen.getByRole("button", { name: "C-2 を開く（子 4 件）" }),
  );
  const c4 = titleLine("C-4");
  expect(c4?.getAttribute("data-text")).toBe("headline");
  expect(c4?.textContent).toBe(
    face("閲覧者が編集ボタンを押せてしまう穴をふさぐ"),
  );
  // 題名はノードの面から外れ、重ねたときの吹き出し（SVG の title）に出る
  const shown = [...node("C-4").querySelectorAll("text")].map(
    (t) => t.textContent,
  );
  expect(shown.join("")).not.toContain("管理画面の権限チェック");
  expect(node("C-4").querySelector("title")?.textContent).toContain(
    "管理画面の権限チェックを直す",
  );
  // 一言でが無い課題（C-1）は題名を出す
  expect(titleLine("C-1")?.getAttribute("data-text")).toBe("title");
  expect(titleLine("C-1")?.textContent).toBe(face("ログイン画面の文言を直す"));
});

it("一言で: 選んだときのパネルには題名と一言での両方を出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(node("C-7"));
  const panel = screen.getByRole("complementary", { name: "選んだ課題" });
  expect(within(panel).getByRole("heading", { level: 3 }).textContent).toBe(
    "C-7 料金表の計算を直す",
  );
  expect(
    within(panel).getByText("一言で").nextElementSibling?.textContent,
  ).toBe("料金表の丸めを請求書とそろえる");
  expect(
    within(panel).getByText("位置づけ").nextElementSibling?.textContent,
  ).toBe("月次請求の締め前に必要。経理の突合作業が減る。");
  fireEvent.click(
    screen.getByRole("button", { name: "C-2 を開く（子 4 件）" }),
  );
  fireEvent.click(node("C-1"));
  expect(
    within(panel).getByText("一言で").nextElementSibling?.textContent,
  ).toContain("未設定");
});

it("一言で: 見せ方の切り替えで、ノードの文を題名に戻せる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  expect(titleLine("C-7")?.getAttribute("data-text")).toBe("headline");
  variant("ノードの文", "題名");
  expect(titleLine("C-7")?.getAttribute("data-text")).toBe("title");
  expect(titleLine("C-7")?.textContent).toBe(face("料金表の計算を直す"));
});

it("縮小: ノードは ID と色（区分・光の輪・更新なし）だけになり、文を出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  variant("大きさ", "縮小（ID と色）");
  for (const id of ["C-3", "C-7", "C-14"]) {
    const n = node(id);
    expect(n).toHaveAttribute("data-compact", "true");
    expect(n.querySelector(".graph-node-title")).toBeNull();
    expect(n.querySelector(".graph-node-meta")).toBeNull();
    // 面に出る文字は ID だけ（吹き出しの title を除く）
    const shown = [...n.querySelectorAll("text")].map((t) => t.textContent);
    expect(shown).toEqual([id]);
    expect(n.getAttribute("data-category")).not.toBeNull();
  }
  // 実施中の光の輪は縮小でも残す（C-5 は実施中）
  expect(node("C-15")).toHaveAttribute("data-active-inside", "true");
  expect(node("C-15").querySelector(".graph-node-halo")).not.toBeNull();
  variant("大きさ", "標準");
  expect(node("C-7")).not.toHaveAttribute("data-compact");
});
