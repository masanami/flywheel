import { existsSync } from "node:fs";
import { join } from "node:path";
import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { resetApi, setApi } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { StatusNeedsHuman } from "../api/types.gen.ts";
import { useLive } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { FakeEventSource, installFakes } from "../testing/fakes.ts";
import {
  CYCLE_LOCKED_NOTE,
  CYCLE_NOT_STARTED_NOTE,
  CYCLE_STARTED_NOTE,
  CycleStartButton,
  canStartCycle,
} from "./CycleStartButton.tsx";
import { hrefOf, parseHash } from "./route.ts";
import { useScreenData } from "./useScreenData.ts";
import {
  WAITING_KINDS,
  WAITING_LABELS,
  waitingCounts,
  waitingKindGroup,
  waitingKindsOfActions,
  waitingTotal,
} from "./waiting.ts";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  resetApi();
});

const nh = (p: Partial<StatusNeedsHuman>): StatusNeedsHuman => ({
  budget_exhausted: [],
  challenges: [],
  discrepancies: [],
  operations: [],
  slots: [],
  triage: [],
  ...p,
});

it("#803: あなたを待っているものの種類は 8 つで、全部に語があり、並びが決まっている", () => {
  expect(WAITING_KINDS.map((k) => WAITING_LABELS[k])).toEqual([
    "計画の承認",
    "完了の承認",
    "不可逆操作の承認",
    "回答",
    "食い違い",
    "予算の増額",
    "トリアージ",
    "スロットの片付け",
  ]);
});

it("#803: 承認の 3 種（計画・完了・不可逆操作）は同じまとまり approval で、並びで隣り合う。ほかはまとまりを持たない", () => {
  expect(WAITING_KINDS.map((k) => [k, waitingKindGroup(k)])).toEqual([
    ["plan_approval", "approval"],
    ["completion_approval", "approval"],
    ["operation", "approval"],
    ["answer", undefined],
    ["discrepancy", undefined],
    ["budget", undefined],
    ["triage", undefined],
    ["slot", undefined],
  ]);
});

it("#807: next_human_actions の種類は、あなたを待っているものの並びで返り、重複と知らないコードは出ない", () => {
  expect(
    waitingKindsOfActions([
      "increase_budget",
      "answer_question",
      "unknown_code",
      "approve_operation",
      "answer_question",
      "approve_plan",
    ]),
  ).toEqual(["plan_approval", "operation", "answer", "budget"]);
  expect(waitingKindsOfActions([])).toEqual([]);
});

it("#803: 件数の数え方 — 循環だけの食い違いは数えず、食い違いは課題ごとに 1 件", () => {
  const c = waitingCounts(
    nh({
      discrepancies: [
        { challenge_id: "C-1", kinds: ["upstream_closed"] },
        { challenge_id: "C-1", kinds: ["out_of_policy"] },
        { challenge_id: "C-2", kinds: ["dependency_cycle"], cycle_only: true },
      ],
      budget_exhausted: [
        {
          challenge_id: "C-3",
          impl_remaining_usd: 0,
          plan_version: 1,
          review_remaining_usd: 0,
        },
      ],
      triage: [{ challenge_id: "C-4", reason: "r", run_id: "R-1" }],
      slots: [{ path: "/p", repo: "r", run_id: null, slot_id: "S-1" }],
    }),
  );
  expect(c).toMatchObject({ discrepancy: 1, budget: 1, triage: 1, slot: 1 });
  expect(waitingTotal(c)).toBe(4);
});

it("#803: ハッシュの読み書きが往復し、解釈できないハッシュは unknown になる", () => {
  expect(parseHash("")).toBeNull();
  expect(parseHash("#/")).toBeNull();
  expect(parseHash("#/all")).toEqual({ kind: "all" });
  for (const r of [
    { kind: "agent", name: "a b", screen: "cycle" },
    { kind: "challenge", name: "alpha", id: "C-1", view: "tree" },
    { kind: "challenge", name: "alpha", id: "C-1", view: "history" },
    { kind: "challenge", name: "alpha", id: "C-1", view: "plan" },
    { kind: "approve_plan", name: "alpha", id: "C-1" },
    { kind: "task", name: "alpha", id: "C-1", taskId: "T-1/x" },
    {
      kind: "agent",
      name: "alpha",
      screen: "followups",
      query: { status: "done", challenge: "C-1" },
    },
  ] as const) {
    expect(parseHash(hrefOf(r))).toEqual(r);
  }
  expect(parseHash("#/a/alpha/nope")).toEqual({ kind: "unknown" });
  expect(parseHash("#/zzz")).toEqual({ kind: "unknown" });
});

it("#803: 「サイクルを回す」は、進むものが 1 件以上で idle のときだけ押せる", () => {
  expect(canStartCycle(1, "idle")).toBe(true);
  expect(canStartCycle(0, "idle")).toBe(false);
  expect(canStartCycle(3, "running")).toBe(false);
  expect(canStartCycle(3, "stale")).toBe(false);
});

it("#803: 「サイクルを回す」を押した後の表示（始めた・始めなかった・ほかが走っている）", async () => {
  const startCycle = vi.fn();
  setApi({ startCycle } as never);
  render(<CycleStartButton workspace="alpha" total={2} cycleState="idle" />);
  const btn = screen.getByRole("button", { name: "サイクルを回す" });
  startCycle.mockResolvedValueOnce({ started: true });
  fireEvent.click(btn);
  expect(await screen.findByText(CYCLE_STARTED_NOTE)).toBeInTheDocument();
  startCycle.mockResolvedValueOnce({ started: false });
  fireEvent.click(btn);
  expect(await screen.findByText(CYCLE_NOT_STARTED_NOTE)).toBeInTheDocument();
  startCycle.mockRejectedValueOnce(new ApiError("locked", "locked", 409));
  fireEvent.click(btn);
  expect(await screen.findByText(CYCLE_LOCKED_NOTE)).toBeInTheDocument();
  expect(startCycle).toHaveBeenCalledTimes(3);
  for (const args of startCycle.mock.calls) expect(args).toEqual(["alpha"]);
});

it("#803: 押せない条件のとき、ボタンは無効", () => {
  render(<CycleStartButton workspace="alpha" total={0} cycleState="idle" />);
  expect(screen.getByRole("button", { name: "サイクルを回す" })).toBeDisabled();
});

it("#803: 変化の通し番号は、合図のあったエージェントだけ進む。問いの一覧も読む", async () => {
  const store = new MockStore();
  setApi(createMockApi(store, { latencyMs: 0 }));
  vi.stubGlobal("EventSource", FakeEventSource);
  FakeEventSource.instances = [];
  const { result } = renderHook(() => useLive());
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await waitFor(() => expect(result.current.revision.alpha).toBeGreaterThan(0));
  expect(result.current.questions).not.toBeNull();
  const before = { ...result.current.revision };
  await act(async () => {
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    });
  });
  await waitFor(() =>
    expect(result.current.revision.alpha).toBeGreaterThan(before.alpha ?? 0),
  );
  expect(result.current.revision.beta).toBe(before.beta);
});

it("#803: 画面ごとの読み込みは、そのエージェントの通し番号が進んだときだけ読み直す", async () => {
  const load = vi.fn(async () => "x");
  const { result, rerender } = renderHook(
    ({ rev }: { rev: Record<string, number> }) =>
      useScreenData({ revision: rev }, "alpha", load),
    { initialProps: { rev: { alpha: 1, beta: 1 } } },
  );
  await waitFor(() => expect(result.current.result.state).toBe("ready"));
  expect(load).toHaveBeenCalledTimes(1);
  rerender({ rev: { alpha: 1, beta: 2 } });
  expect(load).toHaveBeenCalledTimes(1);
  rerender({ rev: { alpha: 2, beta: 2 } });
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
});

it("#803: 画面が裏に回っている間は読み直さず、表に戻ると読む", async () => {
  const load = vi.fn(async () => "x");
  const { rerender } = renderHook(
    ({ rev }: { rev: Record<string, number> }) =>
      useScreenData({ revision: rev }, "alpha", load),
    { initialProps: { rev: { alpha: 1 } } },
  );
  await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
  const vis = vi.spyOn(document, "visibilityState", "get");
  vis.mockReturnValue("hidden");
  rerender({ rev: { alpha: 2 } });
  expect(load).toHaveBeenCalledTimes(1);
  vis.mockReturnValue("visible");
  document.dispatchEvent(new Event("visibilitychange"));
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  vis.mockRestore();
});

it("AC-417 #803: web/src/graph/ のディレクトリは無い", () => {
  expect(existsSync(join(import.meta.dirname, "..", "graph"))).toBe(false);
});

it("AC-421 #803: 偽のデータは 4 つの状態の課題をそれぞれ 1 件以上持つ", () => {
  const store = new MockStore();
  const statuses = new Set(
    Object.values(store.workspaces).flatMap((w) =>
      w.items.map((m) => m.c.status),
    ),
  );
  for (const s of [
    "awaiting_plan_approval",
    "in_progress",
    "awaiting_human",
    "done",
  ]) {
    expect(statuses.has(s), s).toBe(true);
  }
  expect(Object.keys(store.workspaces)).toHaveLength(3);
});

it("#803: 偽の API は課題のツリー・フォローアップ・ingestable 付きの状況を返す", async () => {
  const api = createMockApi(new MockStore(), { latencyMs: 0 });
  expect((await api.tree("alpha")).challenges.length).toBeGreaterThan(0);
  const open = (await api.followups("alpha")).followups;
  expect(open.length).toBeGreaterThan(0);
  expect(open.every((f) => f.status === "open")).toBe(true);
  const ing = (await api.statusIngestable("alpha")).ingestable;
  expect(ing?.error).toBeNull();
  expect(ing?.items.length).toBeGreaterThan(0);
});

it("AC-423 #805: 偽のデータの状況は、ingestable=true を付けた問い合わせにだけ、取り込めるものを返す", async () => {
  const api = createMockApi(new MockStore(), { latencyMs: 0 });
  expect((await api.status("alpha")).ingestable).toBeNull();
  const ing = (await api.statusIngestable("alpha")).ingestable;
  expect(ing?.items.length).toBeGreaterThan(0);
  // 子のある親と、既存の課題の下に入るものの両方の形を持つ
  expect(ing?.items.some((i) => i.children.length > 0)).toBe(true);
  expect(ing?.items.some((i) => i.parent_challenge_id !== null)).toBe(true);
});

it("#803: 実 API の口は ingestable=true・tree・followups の URL を呼ぶ", async () => {
  const { requests } = installFakes({
    "/api/v1/workspaces/a%20b/status?ingestable=true": {},
    "/api/v1/workspaces/a%20b/tree": {},
    "/api/v1/workspaces/a%20b/followups?status=all": {},
  });
  const { api } = await import("../api/client.ts");
  await api.statusIngestable("a b");
  await api.tree("a b");
  await api.followups("a b", "all");
  expect(requests.map((r) => `${r.method} ${r.url}`)).toEqual([
    "GET /api/v1/workspaces/a%20b/status?ingestable=true",
    "GET /api/v1/workspaces/a%20b/tree",
    "GET /api/v1/workspaces/a%20b/followups?status=all",
  ]);
});

it("#803: URL の形は仕様の表の文字列そのもの（approve-plan・history・plan・tasks・?query）", () => {
  const h = (r: Parameters<typeof hrefOf>[0]) => hrefOf(r);
  expect(h({ kind: "challenge", name: "alpha", id: "C-1", view: "tree" })).toBe(
    "#/a/alpha/challenges/C-1",
  );
  expect(
    h({ kind: "challenge", name: "alpha", id: "C-1", view: "history" }),
  ).toBe("#/a/alpha/challenges/C-1/history");
  expect(h({ kind: "challenge", name: "alpha", id: "C-1", view: "plan" })).toBe(
    "#/a/alpha/challenges/C-1/plan",
  );
  expect(h({ kind: "approve_plan", name: "alpha", id: "C-1" })).toBe(
    "#/a/alpha/challenges/C-1/approve-plan",
  );
  expect(h({ kind: "task", name: "alpha", id: "C-1", taskId: "T-2" })).toBe(
    "#/a/alpha/challenges/C-1/tasks/T-2",
  );
  expect(
    h({
      kind: "agent",
      name: "alpha",
      screen: "followups",
      query: { status: "done", challenge: "C-1" },
    }),
  ).toBe("#/a/alpha/followups?status=done&challenge=C-1");
  expect(parseHash("#/a/alpha/followups?status=done")).toEqual({
    kind: "agent",
    name: "alpha",
    screen: "followups",
    query: { status: "done" },
  });
});

it("#803: 読み込みの対象（エージェント）が変わった直後の描画は、前のデータを見せず読み込み中", async () => {
  const load = vi.fn(async () => "x");
  const { result, rerender } = renderHook(
    ({ ws }: { ws: string }) =>
      useScreenData({ revision: { alpha: 1, beta: 1 } }, ws, load),
    { initialProps: { ws: "alpha" } },
  );
  await waitFor(() => expect(result.current.result.state).toBe("ready"));
  const seen: string[] = [];
  const { result: r2, rerender: rerender2 } = renderHook(
    ({ ws }: { ws: string }) => {
      const r = useScreenData({ revision: { alpha: 1, beta: 1 } }, ws, load);
      seen.push(r.result.state);
      return r;
    },
    { initialProps: { ws: "alpha" } },
  );
  await waitFor(() => expect(r2.current.result.state).toBe("ready"));
  seen.length = 0;
  rerender2({ ws: "beta" });
  expect(seen[0]).toBe("loading");
  await waitFor(() => expect(r2.current.result.state).toBe("ready"));
  rerender({ ws: "beta" });
});
