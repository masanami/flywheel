// 2026-10-14 のオーナーの指示（flywheel#176）: 関係グラフはエージェントごとに出し、エージェントは左のサイドバーで選ぶ。
// 選んだエージェントのターミナル／対話の置き場所（下の引き出し・中身は作らない）を取り、ボードでも列の見出しで選べる。
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
    questions: null,
    loadError: null,
    fleetError: null,
  };
}

const realMatchMedia = window.matchMedia;
beforeEach(() => {
  resetGraphMemory();
  api = createMockApi(new MockStore(), { latencyMs: 0 });
  setApi(api);
});
afterEach(() => {
  resetApi();
  window.matchMedia = realMatchMedia;
});

const sidebar = () => screen.getByRole("navigation", { name: "エージェント" });
const dock = () =>
  screen.getByRole("region", { name: "ターミナル／対話（案）" });

it("サイドバー: エージェントごとに要約を添えて並べ、選んだエージェントのグラフを描く", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const alpha = within(sidebar()).getByRole("button", { name: /^alpha / });
  expect(alpha).toHaveAccessibleName("alpha 人間の操作待ち 7・実施中 3");
  expect(alpha).toHaveAttribute("aria-current", "true");
  expect(
    within(sidebar()).getByRole("button", { name: /^gamma / }),
  ).toBeDisabled();
  fireEvent.click(
    within(sidebar()).getByRole("button", {
      name: "beta 人間の操作待ち 2・実施中 0",
    }),
  );
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
  expect(
    within(sidebar()).getByRole("button", { name: /^beta / }),
  ).toHaveAttribute("aria-current", "true");
  expect(
    screen.getByRole("heading", { name: "beta の関係グラフ" }),
  ).toBeInTheDocument();
});

it("引き出し: 既定は閉じ、開くと選んだエージェントの置き場所を示し、対象の切り替えでグラフも切り替わる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const toggle = within(dock()).getByRole("button", {
    name: /ターミナル／対話（案）/,
  });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(dock().textContent).not.toContain(
    "ターミナル／エージェントとの対話が入る",
  );
  fireEvent.click(toggle);
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  expect(dock().textContent).toContain(
    "ここに alpha のターミナル／エージェントとの対話が入る",
  );
  // サイドバーで選び直すと欄の対象も切り替わる
  fireEvent.click(within(sidebar()).getByRole("button", { name: /^beta / }));
  expect(dock().textContent).toContain("ここに beta のターミナル");
  // 欄の「対象」から選んでも、グラフが切り替わる
  fireEvent.change(within(dock()).getByRole("combobox", { name: "対象" }), {
    target: { value: "alpha" },
  });
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  expect(dock().textContent).toContain("ここに alpha のターミナル");
  fireEvent.click(toggle);
  expect(dock().textContent).not.toContain("ここに alpha");
});

it("ボード: 列の見出しを押すとそのエージェントを選び、引き出しの対象が切り替わる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  fireEvent.click(screen.getByRole("button", { name: "列のボード" }));
  const beta = await screen.findByRole("region", { name: "beta の列" });
  const header = beta.querySelector(".agent-column-header");
  if (!header) throw new Error("列の見出しが無い");
  fireEvent.click(header);
  expect(within(dock()).getByRole("combobox", { name: "対象" })).toHaveValue(
    "beta",
  );
  // ボードの列の並び（縦割りの列）はそのまま
  expect(
    screen.getByRole("region", { name: "alpha の列" }),
  ).toBeInTheDocument();
  // 列の中身（カード）を押しても選び直さない
  const alphaCol = screen.getByRole("region", { name: "alpha の列" });
  const body = alphaCol.querySelector(".agent-column-body");
  if (body) fireEvent.click(body);
  expect(within(dock()).getByRole("combobox", { name: "対象" })).toHaveValue(
    "beta",
  );
  // グラフへ戻ると、ボードで選んだエージェントのグラフが出る
  fireEvent.click(screen.getByRole("button", { name: "グラフ" }));
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
});

it("狭い画面: サイドバーの代わりに上部の選択欄でエージェントを選ぶ", async () => {
  window.matchMedia = ((q: string) => ({
    matches: true,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  expect(screen.queryByRole("navigation", { name: "エージェント" })).toBeNull();
  const picker = screen.getByRole("combobox", { name: "エージェント" });
  fireEvent.change(picker, { target: { value: "beta" } });
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
});
