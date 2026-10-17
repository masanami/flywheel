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
import {
  HUMAN_KINDS,
  HUMAN_KIND_LABELS,
  type HumanKind,
  challengeIdsOf,
  countsOf,
  filterByKind,
  unboundSlots,
} from "./filter.ts";

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

it("AC-184: 帯は区分ごとの件数をワークスペースごとに出し、0 件の区分は出さない", async () => {
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
  expect(within(strip).getByText("予算の使い切り 1")).toBeInTheDocument();
  expect(within(strip).getByText(/人間待ち \d+ 件/)).toBeInTheDocument();
  expect(strip.textContent).not.toMatch(/ 0(?!\d)/);
});

it("AC-185・AC-186: 7 区分それぞれを選ぶとその区分に当たるカードだけに絞られ、もう一度選ぶと外れる", async () => {
  const live = await liveState();
  const nh = alpha(live);
  const cards = toAgentBoard(
    live.workspaces.find((w) => w.name === "alpha") as never,
    live.data.alpha,
  ).challenges;
  render(<Board live={live} />);
  const column = () => screen.getByRole("region", { name: "alpha の列" });
  const all = within(column()).getAllByTestId(/^card-/).length;
  for (const kind of HUMAN_KINDS as readonly HumanKind[]) {
    const ids = challengeIdsOf(nh, kind, cards);
    const chip = document.querySelector(
      `.strip-chip[data-kind="${kind}"]`,
    ) as HTMLElement | null;
    if (countsOf(nh)[kind] === 0) {
      expect(chip, kind).toBeNull();
      continue;
    }
    if (!chip) throw new Error(`${HUMAN_KIND_LABELS[kind]} の押し子が無い`);
    fireEvent.click(chip);
    expect(chip).toHaveAttribute("aria-pressed", "true");
    const shown = within(column())
      .queryAllByTestId(/^card-/)
      .map((e) => e.getAttribute("data-testid")?.replace("card-", ""));
    expect(new Set(shown), kind).toEqual(ids);
    expect(ids.size, kind).toBeGreaterThan(0);
    expect(shown.length, kind).toBeLessThan(all);
    fireEvent.click(chip);
    expect(within(column()).getAllByTestId(/^card-/)).toHaveLength(all);
  }
  // 解除の操作
  const first = document.querySelector(".strip-chip") as HTMLElement;
  fireEvent.click(first);
  fireEvent.click(screen.getByRole("button", { name: "絞り込みを解除" }));
  expect(within(column()).getAllByTestId(/^card-/)).toHaveLength(all);
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
  expect(unboundSlots(withSlot, [])).toEqual(withSlot.slots);
  const bound = { nextHumanActions: ["clear_slot"] } as never;
  expect(
    unboundSlots({ ...nh, slots: withSlot.slots.slice(0, 1) }, [bound]),
  ).toEqual([]);
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
