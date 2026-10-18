// 2026-10-13 のオーナーの指示（flywheel#176）: 実行中で更新の無い課題（unresponsive）は、旧 board と同じく
// 箱そのものを赤みにし、「⚠ 更新なし」と出す。凡例・パネル・列のボード・列の見出しの案も同じ文言にそろえる。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { GraphScreen, resetGraphMemory } from "./GraphScreen.tsx";

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

const node = (id: string) =>
  within(screen.getByRole("group", { name: /の課題の関係グラフ$/ })).getByRole(
    "button",
    { name: new RegExp(`^${id} (?!を)`) },
  );

it("更新なし: ノードを赤みの箱にして札を出し、実施中の印も残す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: /^C-14 を開く/ }));
  const c6 = node("C-6");
  expect(c6).toHaveAttribute("data-unresponsive", "true");
  expect(c6).toHaveAttribute("data-active", "true");
  expect(c6.textContent).toContain("⚠ 更新なし（要確認）");
  expect(c6.textContent).toContain("実施中");
  expect(c6.querySelector(".graph-node-stale")).not.toBeNull();
  expect(c6.querySelector(".graph-node-halo")).not.toBeNull();
  expect(c6.getAttribute("aria-label")).not.toContain("応答なし");
  // 更新の止まっていない実施中の課題には札を出さない
  fireEvent.click(screen.getByRole("button", { name: /^C-15 を開く/ }));
  expect(node("C-5")).not.toHaveAttribute("data-unresponsive");
  expect(node("C-5").textContent).not.toContain("更新なし");
  // 区分の一覧には「応答なし」を出さない
  const legend = screen.getByRole("list", { name: "区分の凡例" });
  // 凡例の説明（吹き出し）にも同じ文言で出す
  const help = screen.getByRole("list", { name: "凡例の説明" });
  expect(help.textContent).toContain("⚠ 更新なし");
  expect(help.textContent).not.toContain("応答なし");
  expect(legend.textContent).not.toContain("応答なし");
});

it("更新なし: 大枠を開くとノードに「⚠ 更新なし（要確認）」と出し、グラフに「応答なし」を出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: /^C-14 を開く/ }));
  expect(node("C-6").textContent).toContain("⚠ 更新なし（要確認）");
  const graph = screen.getByRole("group", { name: "alpha の課題の関係グラフ" });
  expect(graph.textContent).not.toContain("応答なし");
});

it("更新なし: 案の画面の列のボードは「更新なし」と出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  fireEvent.click(screen.getByRole("button", { name: "列のボード" }));
  const row = await screen.findByTestId("running-run-C-6");
  expect(row).toHaveAttribute("data-stale", "true");
  expect(row).toHaveTextContent("⚠ 更新なし（要確認）");
  expect(row).not.toHaveTextContent("応答なし");
});
