// 3 巡目（flywheel#176）の検査。2026-10-17 オーナー: ノードには常に題名を出し、選ぶかカーソルを重ねると「一言で」を
// 吹き出しで出す。一言では切れすぎないようにし、見せ方の切り替え（ノードの文・依存の線・大きさ）は置かない。
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

const titleLine = (id: string) =>
  node(id).querySelector(".graph-node-title") as SVGTextElement | null;
// ノードは題名を幅で 2 行まで出す
const face = (s: string) =>
  clipLines(s, WIDE.nodeW - 18, 13, WIDE.titleLines).join("");
const callout = () => graphOf().querySelector(".graph-callout");
const calloutText = () =>
  callout()?.querySelector(".graph-callout-text")?.textContent ?? null;

it("題名: ノードには常に題名を出し、一言では面に出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  expect(titleLine("C-7")?.textContent).toBe(face("料金表の計算を直す"));
  const shown = [...node("C-7").querySelectorAll("text")]
    .map((t) => t.textContent)
    .join("");
  expect(shown).not.toContain("料金表の丸め");
  expect(callout()).toBeNull();
  // 見せ方の切り替えの欄は置かない
  expect(screen.queryByRole("group", { name: "見せ方（案）" })).toBeNull();
  expect(screen.queryByRole("group", { name: "ノードの文" })).toBeNull();
  expect(screen.queryByRole("group", { name: "大きさ" })).toBeNull();
});

it("一言で: カーソルを重ねるかフォーカスすると吹き出しに出し、離れると消す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.mouseEnter(node("C-7"));
  expect(callout()?.getAttribute("data-callout")).toBe("C-7");
  expect(calloutText()).toBe("料金表の丸めを請求書とそろえる");
  fireEvent.mouseLeave(node("C-7"));
  expect(callout()).toBeNull();
  fireEvent.focus(node("C-3"));
  expect(calloutText()).toBe("通知メールの二重送信を止める");
  fireEvent.blur(node("C-3"));
  expect(callout()).toBeNull();
});

it("一言で: 吹き出しは重ねている間だけ出し、課題を押すとボードと同じ詳細のモーダルに題名・一言で・位置づけを出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.mouseEnter(node("C-7"));
  expect(callout()?.getAttribute("data-callout")).toBe("C-7");
  fireEvent.mouseEnter(node("C-3"));
  expect(callout()?.getAttribute("data-callout")).toBe("C-3");
  fireEvent.mouseLeave(node("C-3"));
  expect(callout()).toBeNull();
  // 押しても吹き出しを出したままにはしない（選ぶ状態を持たない）。詳細はボードと同じモーダルで読む
  fireEvent.click(node("C-7"));
  const modal = await screen.findByTestId("card-detail-content");
  expect(modal.textContent).toContain("料金表の計算を直す");
  expect(modal.textContent).toContain("料金表の丸めを請求書とそろえる");
  expect(modal.textContent).toContain(
    "月次請求の締め前に必要。経理の突合作業が減る。",
  );
  fireEvent.click(within(modal).getByText("閉じる"));
  expect(callout()).toBeNull();
});

it("一言で: 吹き出しでは一言でを切らずに折り返す。一言でが無い課題は吹き出しを出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // C-15 の一言では 25 文字あり、ノードの幅の 1 行には入らない
  const long = "ビルドを速くし、配布を手順書なしで回せるようにする";
  fireEvent.mouseEnter(node("C-15"));
  const tspans = callout()?.querySelectorAll(".graph-callout-text tspan");
  expect(tspans?.length).toBeGreaterThan(1);
  expect(calloutText()).toBe(long);
  fireEvent.mouseLeave(node("C-15"));
  // 畳んだ親の箱は子課題のノードと同じく吹き出しを出す。開いた大枠は見出しの欄に出すので吹き出しを出さない
  fireEvent.mouseEnter(node("C-2"));
  expect(calloutText()).toBe("検索 API をページングで返せるようにする");
  fireEvent.mouseLeave(node("C-2"));
  fireEvent.click(
    screen.getByRole("button", { name: "C-2 を開く（子 4 件）" }),
  );
  fireEvent.mouseEnter(node("C-2"));
  expect(callout()).toBeNull();
  // C-1 には一言でが無い（外部待ち・自動の確認待ちでもない）ので、吹き出しを出さない
  fireEvent.mouseEnter(node("C-1"));
  expect(callout()).toBeNull();
});
