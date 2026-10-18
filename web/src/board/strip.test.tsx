import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../api/client.ts";
import type { ApiClient } from "../api/client.ts";
import type { StatusNeedsHuman } from "../api/types.gen.ts";
import type { LiveState, WorkspaceData } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { Board } from "./Board.tsx";
import { toAgentBoard } from "./adapt.ts";
import { countsOf, filterByKind, unboundSlots } from "./filter.ts";

let api: ApiClient;
beforeEach(() => {
  api = createMockApi(new MockStore(), { latencyMs: 0 });
  setApi(api);
});
afterEach(() => resetApi());

async function liveState(): Promise<LiveState> {
  const { workspaces } = await api.workspaces();
  const data: Record<string, WorkspaceData> = {};
  for (const w of workspaces) {
    if (w.state !== "ok") continue;
    data[w.name] = {
      status: await api.status(w.name),
      challenges: await api.challenges(w.name),
    };
  }
  return {
    connected: true,
    lastUpdated: new Date(),
    workspaces,
    data,
    needsHuman: await api.needsHuman(),
    loadError: null,
    fleetError: null,
  };
}

const alpha = (live: LiveState): StatusNeedsHuman => {
  const nh = live.needsHuman?.workspaces.find((w) => w.name === "alpha");
  if (!nh) throw new Error("alpha が無い");
  return nh.needs_human;
};

it("AC-184: 帯は区分ごとの件数をワークスペースごとの行に出し、0 件の区分は出さない", async () => {
  const live = await liveState();
  expect(countsOf(alpha(live))).toMatchObject({
    approvals: 3,
    questions: 1,
    operations: 1,
    discrepancies: 1,
    budget: 1,
    triage: 1,
  });
  render(<Board live={live} />);
  const strip = screen.getByRole("region", { name: "人間待ちの帯" });
  // 全体の合計は、ワークスペースごとの件数の和（alpha 6 区分の計 8 + beta の 承認待ち 2・不可逆操作 2）
  expect(within(strip).getByText("🔔 人間待ち 12 件")).toBeInTheDocument();
  const rows = within(strip).getAllByRole("listitem");
  const rowOf = (name: string) => {
    const row = rows.find((r) => r.querySelector("b")?.textContent === name);
    if (!row) throw new Error(`${name} の行が無い`);
    return row;
  };
  const chipsOf = (name: string) =>
    Array.from(rowOf(name).querySelectorAll("button")).map(
      (b) => b.textContent,
    );
  // 行はワークスペースごとにあり、それぞれ自分の件数だけを持つ。0 件の区分の押し子は無い。
  expect(chipsOf("alpha")).toEqual([
    "承認待ち 3",
    "未回答の問い 1",
    "不可逆操作の承認 1",
    "上流との食い違い 1",
    "予算の使い切り 1",
    "トリアージ 1",
  ]);
  expect(chipsOf("beta")).toEqual(["承認待ち 2", "不可逆操作の承認 2"]);
  // 状態が読み取れないワークスペース（gamma）と、人間待ちの無いワークスペースの行は出ない
  expect(rows.map((r) => r.querySelector("b")?.textContent)).toEqual([
    "alpha",
    "beta",
  ]);
  expect(strip.textContent).not.toMatch(/ 0(?!\d)/);
});

// 期待する課題の集合は、モックの固定の状態から手で書いた値（本番の challengeIdsOf では導かない）。
const EXPECTED_IDS: Record<string, { alpha: string[]; beta: string[] }> = {
  approvals: { alpha: ["C-3", "C-4", "C-7"], beta: ["C-2", "C-3"] },
  questions: { alpha: ["C-8"], beta: [] },
  operations: { alpha: ["C-7"], beta: ["C-3"] },
  discrepancies: { alpha: ["C-10"], beta: [] },
  budget: { alpha: ["C-9"], beta: [] },
  triage: { alpha: ["C-13"], beta: [] },
};

it("AC-185・AC-186: 6 区分それぞれを選ぶとその区分に当たるカードだけに絞られ、もう一度選ぶと外れる", async () => {
  const live = await liveState();
  render(<Board live={live} />);
  const column = (ws: string) =>
    screen.getByRole("region", { name: `${ws} の列` });
  const shownIn = (ws: string) =>
    within(column(ws))
      .queryAllByTestId(/^card-/)
      .map((e) => e.getAttribute("data-testid")?.replace("card-", ""))
      .sort();
  const allAlpha = shownIn("alpha");
  const allBeta = shownIn("beta");
  expect(allAlpha.length).toBeGreaterThan(10);
  for (const [kind, want] of Object.entries(EXPECTED_IDS)) {
    const chip = document.querySelector(
      `.strip-chip[data-kind="${kind}"]`,
    ) as HTMLElement | null;
    if (!chip) throw new Error(`${kind} の押し子が無い`);
    fireEvent.click(chip);
    expect(chip, kind).toHaveAttribute("aria-pressed", "true");
    expect(shownIn("alpha"), `${kind} alpha`).toEqual(want.alpha);
    expect(shownIn("beta"), `${kind} beta`).toEqual(want.beta);
    fireEvent.click(chip);
    expect(shownIn("alpha"), `${kind} 解除後 alpha`).toEqual(allAlpha);
    expect(shownIn("beta"), `${kind} 解除後 beta`).toEqual(allBeta);
  }
  // 解除の操作
  const first = document.querySelector(".strip-chip") as HTMLElement;
  fireEvent.click(first);
  expect(shownIn("alpha")).not.toEqual(allAlpha);
  fireEvent.click(screen.getByRole("button", { name: "絞り込みを解除" }));
  expect(shownIn("alpha")).toEqual(allAlpha);
});

it("filterByKind: kind が null なら絞らず、needs-human が無ければ空", async () => {
  const live = await liveState();
  const cards = toAgentBoard(
    live.workspaces.find((w) => w.name === "alpha") as never,
    live.data.alpha,
  ).challenges;
  expect(filterByKind(cards, alpha(live), null)).toBe(cards);
  expect(filterByKind(cards, undefined, "approvals")).toEqual([]);
});

it("AC-187: 片づけ待ちのスロットを選ぶと、課題の run に結ばれないスロットの一覧が出る", async () => {
  const live = await liveState();
  const nh = alpha(live);
  const withSlot: StatusNeedsHuman = {
    ...nh,
    slots: [
      ...nh.slots,
      { path: "/w/orphan", repo: "r/orphan", run_id: null, slot_id: "S-9" },
    ],
  };
  live.needsHuman = {
    unreadable_workspaces: [],
    workspaces: [{ name: "alpha", needs_human: withSlot }],
  };
  // どのカードにも載らないスロットだけが出る（カードの slot_ids に載るスロットは出ない）
  expect(unboundSlots(withSlot, [])).toEqual(withSlot.slots);
  // 結ばれたスロットと結ばれないスロットが同時にあるとき、結ばれないものだけが残る
  withSlot.slots.push({
    path: "/w/bound",
    repo: "r/bound",
    run_id: null,
    slot_id: "S-8",
  });
  const ids = withSlot.slots.map((s) => s.slot_id);
  const bound = {
    nextHumanActions: ["clear_slot"],
    slotIds: ["S-8"],
  } as never;
  expect(unboundSlots(withSlot, [bound]).map((s) => s.slot_id)).toEqual(
    ids.filter((id) => id !== "S-8"),
  );
  const all = { slotIds: ids } as never;
  expect(unboundSlots(withSlot, [all])).toEqual([]);
  // 絞り込みは clear_slot を持つ課題だけを残す
  const cards = [
    { id: "A", nextHumanActions: ["clear_slot"] },
    { id: "B", nextHumanActions: [] },
  ] as never;
  expect(
    filterByKind(cards, withSlot, "slots").map((c: { id: string }) => c.id),
  ).toEqual(["A"]);
  render(<Board live={live} />);
  expect(screen.queryByTestId("unbound-slots")).toBeNull();
  fireEvent.click(
    document.querySelector('.strip-chip[data-kind="slots"]') as HTMLElement,
  );
  expect(screen.getByTestId("unbound-slots")).toHaveTextContent("S-9");
});

it("AC-201: 読み取れなかったワークスペースは帯に示す", async () => {
  const live = await liveState();
  live.needsHuman = { unreadable_workspaces: ["gamma"], workspaces: [] };
  render(<Board live={live} />);
  const strip = screen.getByRole("region", { name: "人間待ちの帯" });
  expect(within(strip).getByTestId("unreadable-workspaces")).toHaveTextContent(
    "gamma",
  );
});
