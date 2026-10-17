// 2026-10-14 のオーナーの決定（flywheel#176）: 自動の確認待ち（いまの印は waiting_ci）は外部待ちに入れず、実行中の側に寄せ、
// 区分の色だけで示す（札を付けず、利用分野に固有の語〔CI など〕を人に見せない。flywheel は汎用の道具）。
// 外部待ちは自動では解けない待ち（他チームへの相談など。案の印 waiting_party）に取っておく。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import type { ChallengeCard } from "../../api/types.gen.ts";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { GRAPH_TITLE, GraphScreen, resetGraphMemory } from "./GraphScreen.tsx";
import { categoryOf, isActive } from "./model.ts";

const card = (modifiers: string[], actions: string[] = []): ChallengeCard =>
  ({
    id: "C-1",
    card: {
      context: null,
      headline: null,
      modifiers,
      next_human_actions: actions,
      state: "verifying",
    },
  }) as unknown as ChallengeCard;

it("区分: 自動の確認待ちは実行中、自動では解けない待ちは外部待ち。人間の操作待ちが先に当たる", () => {
  const none = new Set<string>();
  expect(categoryOf(card(["waiting_ci"]), none)).toBe("running");
  expect(isActive(card(["waiting_ci"]))).toBe(false);
  expect(isActive(card(["running"]))).toBe(true);
  expect(categoryOf(card([]), none, true)).toBe("external");
  expect(categoryOf(card(["waiting_ci"]), none, true)).toBe("external");
  expect(categoryOf(card([], ["approve_plan"]), none, true)).toBe("human");
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

it("自動の確認待ち: 実行中の色だけで示し、札も光の輪も付けず、いま実施中にも数えない。画面に「CI」の語を出さない", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  // C-15 の中の自動の確認待ち（C-11）は、畳んだ親の「実施中」の件数に入らない（C-5・C-17 の 2 件のまま）
  expect(node("C-15").getAttribute("aria-label")).toContain("実施中 2");
  fireEvent.click(screen.getByRole("button", { name: /^C-15 を開く/ }));
  const c11 = node("C-11");
  expect(c11).toHaveAttribute("data-category", "running");
  expect(c11).not.toHaveAttribute("data-active");
  expect(c11.querySelector(".graph-node-halo")).toBeNull();
  expect(c11.querySelector(".graph-ci-badge")).toBeNull();
  expect(c11.getAttribute("aria-label")).toBe(
    "C-11 決済画面の改修（システムが実行中）",
  );
  const row = screen.getByRole("region", { name: "いま実施中" });
  expect(within(row).queryByRole("button", { name: /^C-11 / })).toBeNull();
  fireEvent.click(c11);
  const panel = screen.getByRole("complementary", { name: "選んだ課題" });
  expect(panel.textContent).toContain("検証中（自動の確認を待っている");
  const region = screen.getByRole("region", { name: GRAPH_TITLE });
  expect(region.textContent).not.toMatch(/CI/);
  for (const el of region.querySelectorAll("[title]"))
    expect(el.getAttribute("title")).not.toMatch(/CI/);
});

it("外部待ち: 他チームへの相談待ちを外部待ちの区分で示し、相手と理由をパネルに出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  const legend = screen.getByRole("list", { name: "区分の凡例" });
  expect(
    within(legend).getByRole("button", { name: "外部待ち 1" }),
  ).toBeInTheDocument();
  expect(
    within(legend).getByRole("button", { name: /^外部待ち/ }),
  ).toHaveAttribute("title", expect.stringContaining("自動では解けない待ち"));
  const c20 = node("C-20");
  expect(c20).toHaveAttribute("data-category", "external");
  expect(c20.getAttribute("aria-label")).toContain("決済チームの回答待ち");
  fireEvent.click(c20);
  const panel = screen.getByRole("complementary", { name: "選んだ課題" });
  expect(panel.textContent).toContain(
    "外部待ち: 決済チーム（返金 API の 1 日あたりの上限を問い合わせ中）",
  );
});
