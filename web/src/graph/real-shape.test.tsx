import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { type ApiClient, resetApi, setApi } from "../api/client.ts";
import type { GraphResponse, Waiting } from "../api/types.gen.ts";
import type { LiveState, WorkspaceData } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import golden from "../testing/conflict-api.golden.json";
import { RelationScreen } from "./RelationScreen.tsx";
import { WAITING_TITLE } from "./WaitingReasons.tsx";

// 実データの形での画面の確かめ。手書きの偽の応答（mock）ではなく、ストアに入れた衝突の行から本物の API が返した値
// （internal/server の TestGraphAPI_RealShapeGoldenForTheWebScreen が同じファイルと照合する）を、関係グラフとボードの
// 詳細に通す。graph の C-4 は前提の C-5 を待ち、C-6 に譲って後回しにした。C-2・C-3 は向きの無い衝突。C-3 と C-7（取り下げ）の
// 衝突は API が返さない。

const graph = golden.graph as GraphResponse;
const waiting = golden.waiting as Record<string, Waiting>;

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
  const mock = createMockApi(new MockStore(), { latencyMs: 0 });
  api = {
    ...mock,
    challengeGraph: async () => graph,
    // 詳細の API の waiting は、本物の API が返した値にする
    challenge: async (ws, id) => ({
      ...(await mock.challenge(ws, id)),
      waiting: waiting[id] ?? { conflicts: [], prerequisites: [] },
    }),
  };
  setApi(api);
});
afterEach(() => {
  resetApi();
  vi.unstubAllGlobals();
});

const box = (group: HTMLElement, id: string) =>
  within(group).getByRole("button", { name: new RegExp(`^${id} (?!を)`) });

async function mountGraph() {
  render(<RelationScreen live={await liveOf()} />);
  return await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
}

it("実データの形: 衝突の辺が描かれる（向きありは中抜きの矢じり、向きなしは矢じりなし）。取り下げの相手との衝突は出ない", async () => {
  const group = await mountGraph();
  const conflicts = [
    ...group.querySelectorAll<SVGPathElement>(
      'path.graph-edge[data-basis="conflict"]',
    ),
  ];
  expect(
    conflicts.map((e) => [e.dataset.from, e.dataset.to, e.dataset.directed]),
  ).toEqual([
    ["C-2", "C-3", "false"],
    ["C-4", "C-6", "true"],
  ]);
  expect(conflicts[0]?.hasAttribute("marker-end")).toBe(false);
  expect(conflicts[1]?.getAttribute("marker-end")).toBe(
    "url(#graph-arrow-conflict)",
  );
  // 前提の線は変わらず実線の矢じり（C-5 → C-4）
  const prereq = group.querySelector<SVGPathElement>(
    'path.graph-edge[data-basis="prerequisite"]',
  );
  expect([prereq?.dataset.from, prereq?.dataset.to]).toEqual(["C-5", "C-4"]);
  expect(prereq?.getAttribute("marker-end")).toBe("url(#graph-arrow)");
});

it("実データの形: グラフから開いた C-4 の詳細に、前提 C-5 と、衝突のため後回し C-6 が別の行で出る", async () => {
  const group = await mountGraph();
  fireEvent.click(box(group, "C-4"));
  const content = await screen.findByTestId("card-detail-content");
  const sec = within(content).getByRole("region", {
    name: WAITING_TITLE,
    hidden: true,
  });
  const rows = [...sec.querySelectorAll("li")];
  expect(rows.map((li) => [li.dataset.basis, li.dataset.from])).toEqual([
    ["prerequisite", "C-5"],
    ["conflict", "C-6"],
  ]);
  expect(rows[1]?.textContent).toContain("衝突のため後回し: C-6");
  const dialog = content.closest("dialog") as HTMLDialogElement;
  act(() => {
    dialog.dispatchEvent(new Event("close"));
  });
  await waitFor(() =>
    expect(screen.queryByTestId("card-detail-content")).toBeNull(),
  );
});

it("実データの形: ボードから開いた C-4 の詳細も、同じ 2 行（前提と衝突のため後回し）を出す。先に委譲した C-6・向きの無い C-2 には出ない", async () => {
  await mountGraph();
  fireEvent.click(screen.getByRole("button", { name: "ボード" }));
  const open = async (id: string) => {
    // 列は alpha が先（beta にも同じ ID の課題がある）
    const [card] = await screen.findAllByTestId(`card-${id}`);
    fireEvent.click(within(card as HTMLElement).getByRole("button"));
    const content = await screen.findByTestId("card-detail-content");
    await within(content).findByText(/^計画（/);
    return content;
  };
  const close = async (content: HTMLElement) => {
    const dialog = content.closest("dialog") as HTMLDialogElement;
    act(() => {
      dialog.dispatchEvent(new Event("close"));
    });
    await waitFor(() =>
      expect(screen.queryByTestId("card-detail-content")).toBeNull(),
    );
  };
  const c4 = await open("C-4");
  const sec = within(c4).getByRole("region", {
    name: WAITING_TITLE,
    hidden: true,
  });
  expect(
    [...sec.querySelectorAll("li")].map((li) => [
      li.dataset.basis,
      li.dataset.from,
    ]),
  ).toEqual([
    ["prerequisite", "C-5"],
    ["conflict", "C-6"],
  ]);
  expect(sec.textContent).toContain("衝突のため後回し: C-6");
  await close(c4);
  for (const id of ["C-6", "C-2"]) {
    const content = await open(id);
    expect(within(content).queryByText(WAITING_TITLE)).toBeNull();
    await close(content);
  }
});
