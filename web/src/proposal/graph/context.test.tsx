// 3 巡目（flywheel#176・2026-10-14 オーナー）の検査: 開いた親の大枠の見出しに一言でと位置づけを出し、長い位置づけは畳む。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { contextLines, groupHeadHeight } from "./GraphCanvas.tsx";
import {
  COMPACT,
  GraphScreen,
  WIDE,
  resetGraphMemory,
} from "./GraphScreen.tsx";
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
const variant = (group: string, name: string) =>
  fireEvent.click(
    within(screen.getByRole("group", { name: group })).getByRole("button", {
      name,
    }),
  );

const group = (id: string) => node(id).closest(".graph-group") as SVGGElement;
const contextTexts = (id: string) =>
  [...group(id).querySelectorAll(".graph-group-context")].map(
    (t) => t.textContent ?? "",
  );

it("位置づけの行: 見出しの幅で行に分け、畳んだら 1 行、広げたら全行の高さを取る。縮小と位置づけ無しは伸ばさない", () => {
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
  expect(groupHeadHeight(long, 400, COMPACT, true)).toBe(COMPACT.headH);
});

it("位置づけ: 開いた大枠の見出しに一言でと位置づけを出し、畳んだ親の箱には位置づけを出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // 畳んだ親は箱のまま（一言でだけ）
  expect(node("C-14").querySelector(".graph-group-context")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "C-14 を開く（子 5 件）" }),
  );
  const head = group("C-14").querySelector(".graph-group-title");
  expect(head?.textContent).toContain("C-14");
  expect(head?.textContent).toContain("旧バッチ基盤を止めて消す");
  // 長い位置づけは 1 行に畳み、末尾を「…」にして「続き」のボタンを置く
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

it("位置づけ: 縮小では位置づけを出さず、見出しは ID だけになる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  expect(contextTexts("C-16")).toHaveLength(1);
  variant("大きさ", "縮小（ID と色）");
  expect(contextTexts("C-14")).toEqual([]);
  expect(contextTexts("C-16")).toEqual([]);
  expect(screen.queryByRole("button", { name: /^位置づけ/ })).toBeNull();
  expect(group("C-14").querySelector(".graph-group-title")?.textContent).toBe(
    "C-14",
  );
});
