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
  screen.getByRole("region", { name: "ターミナル／チャット（案）" });
const openDock = () =>
  fireEvent.click(
    within(dock()).getByRole("button", { name: /ターミナル／チャット（案）/ }),
  );

it("アイコンの列: 名前と件数を読み上げ名とマウスを重ねたときの吹き出しに持ち、人間の操作待ちを角の数字で示す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const alpha = within(sidebar()).getByRole("button", { name: /^alpha / });
  expect(alpha).toHaveAccessibleName("alpha 人間の操作待ち 7・実施中 3");
  expect(alpha).toHaveAttribute("aria-current", "true");
  expect(alpha.querySelector(".agent-rail-badge")?.textContent).toBe("7");
  expect(alpha.querySelector(".agent-rail-active")).not.toBeNull();
  expect(alpha.querySelector(".agent-rail-tip")?.textContent).toContain(
    "alpha",
  );
  // アイコンの設定: alpha は絵文字、beta は画像、設定の無い gamma は頭文字
  expect(
    (await within(alpha).findByText("🚀")).classList.contains("agent-icon"),
  ).toBe(true);
  const beta = within(sidebar()).getByRole("button", { name: /^beta / });
  expect(beta.querySelector("img.agent-icon")).not.toBeNull();
  expect(beta.querySelector(".agent-rail-active")).toBeNull();
  const gamma = within(sidebar()).getByRole("button", { name: /^gamma / });
  expect(gamma).toBeDisabled();
  expect(gamma.querySelector(".agent-icon-initial")?.textContent).toBe("G");
  fireEvent.click(beta);
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
  expect(beta).toHaveAttribute("aria-current", "true");
  expect(
    screen.getByRole("heading", { name: "beta の関係グラフ" }),
  ).toBeInTheDocument();
});

it("欄: 既定は閉じ、開くとターミナルとチャットのタブを持ち、選んだエージェントを宛先にする", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const toggle = within(dock()).getByRole("button", {
    name: /ターミナル／チャット（案）/,
  });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(within(dock()).queryByRole("tablist")).toBeNull();
  openDock();
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  const tabs = within(dock()).getByRole("tablist", { name: "欄の中身（案）" });
  const term = within(tabs).getByRole("tab", { name: "ターミナル" });
  expect(term).toHaveAttribute("aria-selected", "true");
  expect(within(dock()).getByRole("tabpanel").textContent).toContain(
    "~/work/alpha % claude",
  );
  fireEvent.click(within(tabs).getByRole("tab", { name: "チャット" }));
  const panel = within(dock()).getByRole("tabpanel");
  expect(panel.textContent).toContain("宛先: alpha のエージェント");
  // ツール呼び出しのカード（畳める）・差分・実行の許可・入力欄
  expect(panel.querySelectorAll("details.chat-tool").length).toBe(2);
  expect(panel.querySelector(".chat-diff")).not.toBeNull();
  expect(
    within(panel).getByRole("button", { name: "許可" }),
  ).toBeInTheDocument();
  expect(
    within(panel).getByRole("textbox", { name: "alpha へのメッセージ" }),
  ).toBeInTheDocument();
  // 列で選び直すと宛先も切り替わる
  fireEvent.click(within(sidebar()).getByRole("button", { name: /^beta / }));
  expect(within(dock()).getByRole("tabpanel").textContent).toContain(
    "宛先: beta のエージェント",
  );
  // 欄の「対象」から選んでも、グラフが切り替わる
  fireEvent.change(within(dock()).getByRole("combobox", { name: "対象" }), {
    target: { value: "alpha" },
  });
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(toggle);
  expect(within(dock()).queryByRole("tabpanel")).toBeNull();
});

it("欄の位置の 2 案: 下の引き出しと右の分割を切り替えられ、グラフでもボードでも同じ欄が出る", async () => {
  const { container } = render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const shell = container.querySelector(".agent-shell");
  const pos = screen.getByRole("group", { name: "欄の位置（案）" });
  expect(shell).toHaveAttribute("data-dock-pos", "bottom");
  fireEvent.click(within(pos).getByRole("button", { name: "右の分割" }));
  expect(shell).toHaveAttribute("data-dock-pos", "right");
  expect(dock()).toHaveAttribute("data-pos", "right");
  openDock();
  fireEvent.click(screen.getByRole("button", { name: "列のボード" }));
  expect(
    await screen.findByRole("region", { name: "beta の列" }),
  ).toBeInTheDocument();
  expect(container.querySelector(".agent-shell")).toHaveAttribute(
    "data-dock-pos",
    "right",
  );
  expect(within(dock()).getByRole("tabpanel")).toBeInTheDocument();
  fireEvent.click(within(pos).getByRole("button", { name: "下の引き出し" }));
  expect(dock()).toHaveAttribute("data-pos", "bottom");
});

it("ボード: 列の見出しを押すとそのエージェントを選び、欄の対象が切り替わる", async () => {
  render(<GraphScreen live={await liveOf()} />);
  fireEvent.click(screen.getByRole("button", { name: "列のボード" }));
  openDock();
  const beta = await screen.findByRole("region", { name: "beta の列" });
  const header = beta.querySelector(".agent-column-header");
  if (!header) throw new Error("列の見出しが無い");
  fireEvent.click(header);
  expect(within(dock()).getByRole("combobox", { name: "対象" })).toHaveValue(
    "beta",
  );
  // ボードの列の並び（縦割りの列）はそのまま
  const alphaCol = screen.getByRole("region", { name: "alpha の列" });
  // 列の中身（カード）を押しても選び直さない
  const body = alphaCol.querySelector(".agent-column-body");
  if (body) fireEvent.click(body);
  expect(within(dock()).getByRole("combobox", { name: "対象" })).toHaveValue(
    "beta",
  );
  // グラフへ戻ると、ボードで選んだエージェントのグラフが出る
  fireEvent.click(screen.getByRole("button", { name: "グラフ" }));
  await screen.findByRole("group", { name: "beta の課題の関係グラフ" });
});

it("狭い画面: アイコンの列の代わりに上部の選択欄でエージェントを選ぶ", async () => {
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
