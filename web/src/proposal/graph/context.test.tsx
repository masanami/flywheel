// 3 巡目（flywheel#176・2026-10-14 オーナー）の検査: 開いた親の大枠の見出しに位置づけを出し、長い位置づけは畳む。
// 2026-10-17 オーナー: 「続き」はボタンの見た目でなく文字だけにし、キーボードで押せることは保つ。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { contextLines, groupHeadHeight } from "./GraphCanvas.tsx";
import { GraphScreen, WIDE, resetGraphMemory } from "./GraphScreen.tsx";
import { clipLines, wrapByWidth } from "./model.ts";

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

const group = (id: string) => node(id).closest(".graph-group") as SVGGElement;
const contextTexts = (id: string) =>
  [...group(id).querySelectorAll(".graph-group-context")].map(
    (t) => t.textContent ?? "",
  );

it("位置づけの行: 見出しの幅で行に分け、畳んだら 1 行、広げたら全行の高さを取る。位置づけ無しは伸ばさない", () => {
  // 全角は 1 文字 10px、半角はその 0.55 倍で数える
  expect(wrapByWidth("あいうえおか", 40, 10)).toEqual(["あいうえ", "おか"]);
  expect(wrapByWidth("abcdefgh", 40, 10)).toEqual(["abcdefg", "h"]);
  expect(clipLines("あいうえおかきく", 40, 10, 1)).toEqual(["あいう…"]);
  expect(clipLines("あいうえ", 40, 10, 1)).toEqual(["あいうえ"]);
  const long = "あ".repeat(80);
  const lines = contextLines(long, 400, WIDE);
  expect(lines.length).toBeGreaterThan(1);
  expect(lines[0]?.startsWith("位置づけ: ")).toBe(true);
  expect(groupHeadHeight(null, 400, WIDE, true)).toBe(WIDE.headH);
  // 1 行に収まる位置づけは、畳んでも広げても 1 行の高さ
  expect(contextLines("短い", 400, WIDE)).toEqual(["位置づけ: 短い"]);
  expect(groupHeadHeight("短い", 400, WIDE, true)).toBe(
    WIDE.headH + WIDE.ctxLineH + 4,
  );
  expect(groupHeadHeight(long, 400, WIDE, false)).toBe(
    WIDE.headH + WIDE.ctxLineH + 4,
  );
  expect(groupHeadHeight(long, 400, WIDE, true)).toBe(
    WIDE.headH + WIDE.ctxLineH * lines.length + 4,
  );
});

it("位置づけ: 開いた大枠の見出しに題名と位置づけを出し、畳んだ親の箱には位置づけを出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // 畳んだ親は箱のまま
  expect(node("C-14").querySelector(".graph-group-context")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "C-14 を開く（子 5 件）" }),
  );
  const head = group("C-14").querySelector(".graph-group-title");
  expect(head?.textContent).toContain("C-14");
  expect(head?.textContent).toContain("旧基盤の撤去");
  // 長い位置づけは 1 行に畳み、末尾を「…」にして「続き」を置く
  const folded = contextTexts("C-14");
  expect(folded).toHaveLength(1);
  expect(folded[0]?.startsWith("位置づけ: 全体計画「運用費の削減」")).toBe(
    true,
  );
  expect(folded[0]?.endsWith("…")).toBe(true);
  const more = screen.getByRole("button", {
    name: "位置づけの続きを読む（C-14）",
  });
  expect(more).toHaveAttribute("aria-expanded", "false");
  // 「続き」はボタンの枠を持たない文字だけ（押せる範囲の透明な当たり判定と文字）
  expect(more.querySelector(".graph-more-hit")).not.toBeNull();
  expect(more.querySelectorAll("rect")).toHaveLength(1);
  expect(more.textContent).toBe("▾ 続き");
});

it("位置づけ: 「続き」はキーボード（Enter・Space）でも広げ・畳める", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(
    screen.getByRole("button", { name: "C-14 を開く（子 5 件）" }),
  );
  const more = screen.getByRole("button", {
    name: "位置づけの続きを読む（C-14）",
  });
  expect(more).toHaveAttribute("tabindex", "0");
  fireEvent.keyDown(more, { key: "Enter" });
  expect(contextTexts("C-14").length).toBeGreaterThan(1);
  fireEvent.keyDown(
    screen.getByRole("button", { name: "位置づけを畳む（C-14）" }),
    { key: " " },
  );
  expect(contextTexts("C-14")).toHaveLength(1);
});

it("位置づけ: 「続き」で全文まで広げると見出しが伸び、中の課題がその分下がる。もう一度押すと畳む", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(
    screen.getByRole("button", { name: "C-14 を開く（子 5 件）" }),
  );
  const headH = () =>
    Number(
      group("C-14")
        .querySelector(".graph-group-head-box")
        ?.getAttribute("height"),
    );
  const yOf = (id: string) => {
    const t = node(id).parentElement?.getAttribute("transform") ?? "";
    return Number(/translate\(\S+ (\S+)\)/.exec(t)?.[1]);
  };
  const h0 = headH();
  const y0 = yOf("C-12");
  fireEvent.click(
    screen.getByRole("button", { name: "位置づけの続きを読む（C-14）" }),
  );
  const lines = contextTexts("C-14");
  expect(lines.length).toBeGreaterThan(1);
  // 広げた行をつなぐと位置づけの全文になる
  expect(lines.join("")).toBe(
    `位置づけ: ${new MockStore().workspaces.alpha?.items.find((m) => m.c.id === "C-14")?.context}`,
  );
  expect(headH() - h0).toBe(WIDE.ctxLineH * (lines.length - 1));
  expect(yOf("C-12") - y0).toBe(WIDE.ctxLineH * (lines.length - 1));
  fireEvent.click(
    screen.getByRole("button", { name: "位置づけを畳む（C-14）" }),
  );
  expect(contextTexts("C-14")).toHaveLength(1);
  expect(headH()).toBe(h0);
});
