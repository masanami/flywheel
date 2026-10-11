import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import type { ShowCardResponse } from "./api/types.gen.ts";
import { guidesFor, shellQuote } from "./guidance.ts";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

const XSS = "<script>alert(1)</script> <img src=x onerror=alert(1)>";

function show(over: Partial<ShowCardResponse> = {}): ShowCardResponse {
  const base: ShowCardResponse = {
    approvals: [],
    card: {
      context: null,
      headline: null,
      modifiers: [],
      next_human_actions: [],
      state: "in_progress",
    },
    challenge: {
      created_at: "2026-01-01T00:00:00Z",
      description: "説明",
      done_criteria: "完了条件",
      id: "C-1",
      priority: null,
      reporter: "me",
      status: "in_progress",
      status_label: "着手中",
      title: "タイトル",
      updated_at: "2026-01-01T00:00:00Z",
      urgency: null,
      version: 1,
    },
    holds: [],
    operations: [],
    plans: [],
    runs: [],
    source_binding: null,
  };
  return { ...base, ...over };
}

const run = (id: string, started_at: string) => ({
  challenge_id: "C-1",
  cost_source: null,
  cost_usd: null,
  cycle_budget_usd: null,
  cycle_id: null,
  ended_at: null,
  id,
  judgment: null,
  kind: "delegation",
  max_budget_usd: 1,
  rate_limited: false,
  result: null,
  session_id: null,
  started_at,
});

const act1 = (at: string, action: string) => ({
  action,
  actor: "me",
  after: null,
  at,
  before: null,
  channel: "cli",
  entity: "challenge",
  entity_id: "C-1",
  verification: "none",
});

function bodies(
  detail: ShowCardResponse,
  extra: Record<string, unknown> = {},
  wsPath = "/w/alpha",
) {
  const f = fixtures();
  const wsList = f["/api/v1/workspaces"] as {
    workspaces: { name: string; path: string }[];
  };
  for (const w of wsList.workspaces) if (w.name === "alpha") w.path = wsPath;
  return {
    ...f,
    "/api/v1/questions": {
      questions: [
        {
          challenge_id: "C-2",
          from_status: "in_progress",
          question: "二番目に来るべきでない問い",
          raised_at: "2026-02-02T00:00:00Z",
          run_id: null,
          title: "後の課題",
          workspace: "beta",
        },
        {
          challenge_id: "C-1",
          from_status: "in_progress",
          question: "最初の問い",
          raised_at: "2026-01-01T00:00:00Z",
          run_id: null,
          title: "先の課題",
          workspace: "alpha",
        },
      ],
    },
    "/api/v1/workspaces/alpha/challenges/C-1": detail,
    "/api/v1/workspaces/alpha/log?challenge=C-1": { activities: [] },
    ...extra,
  };
}

async function openDetail() {
  render(<App />);
  act(() => FakeEventSource.latest.open());
  fireEvent.click(await screen.findByRole("button", { name: "問いの一覧" }));
  const items = await screen.findAllByTestId("question");
  fireEvent.click(
    within(items[1] as HTMLElement).getByRole("button", {
      name: "課題の詳細を開く",
    }),
  );
  return await screen.findByRole("region", { name: "課題の詳細" });
}

it("AC-110/111: 問いの一覧は返ってきた順に出し、要素から詳細を開ける", async () => {
  installFakes(bodies(show()));
  const detail = await openDetail();
  const items = screen.queryAllByTestId("question");
  expect(items.length).toBe(0); // 詳細を開くと一覧から切り替わる
  await waitFor(() =>
    expect(within(detail).getByText("タイトル")).toBeTruthy(),
  );
  fireEvent.click(within(detail).getByRole("button", { name: "一覧へ戻る" }));
  const back = await screen.findAllByTestId("question");
  expect(back.map((i) => i.textContent)).toEqual([
    expect.stringContaining("二番目に来るべきでない問い"),
    expect.stringContaining("最初の問い"),
  ]);
});

it("AC-112〜118: 計画は版の順、run は新しい順、作業ログは古い順、report の一覧", async () => {
  const d = show({
    plans: [
      { body: "計画その2", created_at: "t2", spec: null, version: 2 },
      { body: "計画その1", created_at: "t1", spec: null, version: 1 },
    ],
    approvals: [
      {
        actor: "me",
        channel: "tty",
        decided_at: "2026-01-02T00:00:00Z",
        decision: "rejected",
        kind: "plan",
        operation_id: null,
        reason: "差し戻しの理由",
        target_version: 1,
        verification: "tty",
      },
    ],
    holds: [
      {
        answer: "回答の本文",
        answered_at: "t",
        answered_by: "me",
        from_status: "in_progress",
        from_status_label: "着手中",
        question: "保留の問い",
        raised_at: "t0",
      },
    ],
    operations: [
      {
        challenge_id: "C-1",
        created_at: "t",
        id: "op-1",
        kind: "merge",
        ref: null,
        state: "pending",
        summary: "不可逆の要約",
        version: 1,
      },
    ],
    runs: [
      run("run-old", "2026-01-01T00:00:00Z"),
      run("run-new", "2026-03-01T00:00:00Z"),
    ],
  });
  installFakes(
    bodies(d, {
      "/api/v1/workspaces/alpha/log?challenge=C-1": {
        activities: [act1("2026-05-02", "後"), act1("2026-05-01", "先")],
      },
      "/api/v1/workspaces/alpha/runs/run-new/report": {
        report: {
          assumptions: ["仮定A"],
          run_id: "run-new",
          unverified: ["未検証B"],
        },
      },
      "/api/v1/workspaces/alpha/runs/run-old/report": {
        report: { assumptions: [], run_id: "run-old", unverified: [] },
      },
    }),
  );
  const detail = await openDetail();
  await within(detail).findByText("仮定A");
  const text = detail.textContent ?? "";
  expect(text.indexOf("計画その1")).toBeLessThan(text.indexOf("計画その2"));
  expect(text).toContain("差し戻しの理由");
  expect(text).toContain("保留の問い");
  expect(text).toContain("回答の本文");
  expect(text).toContain("不可逆の要約");
  expect(text.indexOf("run-new")).toBeLessThan(text.indexOf("run-old"));
  expect(text).toContain("未検証B");
  const log = within(within(detail).getByRole("region", { name: "作業ログ" }));
  const lis = log.getAllByRole("listitem").map((l) => l.textContent ?? "");
  expect(lis[0]).toContain("先");
  expect(lis[1]).toContain("後");
  // 空の assumptions・unverified の run には欄を出さない
  const oldRun = detail.querySelector('[data-run="run-old"]');
  expect(oldRun?.textContent).not.toContain("仮定");
  expect(oldRun?.textContent).not.toContain("未検証");
});

it("AC-119: 外部由来の文字列は文字として出し、script 要素と onerror 属性が DOM に無い", async () => {
  const d = show({
    challenge: { ...show().challenge, description: XSS, title: XSS },
    plans: [{ body: XSS, created_at: "t", spec: null, version: 1 }],
    holds: [
      {
        answer: XSS,
        answered_at: "t",
        answered_by: "me",
        from_status: "in_progress",
        from_status_label: "",
        question: XSS,
        raised_at: "t0",
      },
    ],
  });
  installFakes(bodies(d));
  const detail = await openDetail();
  await within(detail).findByText(/保留の問い/);
  expect(detail.textContent).toContain("<script>alert(1)</script>");
  expect(detail.textContent).toContain("onerror=alert(1)");
  expect(document.body.querySelector("script")).toBeNull();
  expect(document.body.querySelector("img")).toBeNull();
  expect(document.body.querySelector("[onerror]")).toBeNull();
});

it("AC-104/107: 計画承認待ち・完了確認待ちには approve の案内があり承認のボタンは無い", async () => {
  for (const status of [
    "awaiting_plan_approval",
    "awaiting_completion_approval",
  ]) {
    const d = show({
      card: { ...show().card, state: status },
      challenge: { ...show().challenge, status },
    });
    installFakes(bodies(d));
    const detail = await openDetail();
    const g = await within(detail).findByRole("region", {
      name: "承認方法の案内",
    });
    expect(g.textContent).toContain(
      "flywheel approve C-1 --workspace '/w/alpha'",
    );
    expect(g.textContent).toContain("Claude Code の外の端末");
    expect(within(detail).queryByRole("button", { name: /承認/ })).toBeNull();
    cleanup();
  }
});

it("AC-105/106: answer・budget の案内は差し込み口 <回答>・<額> のまま", () => {
  const a = guidesFor("C-1", "/w/a", "awaiting_human", []);
  expect(a.map((g) => g.command)).toEqual([
    "flywheel answer C-1 --workspace '/w/a' --answer <回答>",
  ]);
  const b = guidesFor("C-1", "/w/a", "in_progress", ["budget_exhausted"]);
  expect(b.map((g) => g.command)).toEqual([
    "flywheel budget C-1 --workspace '/w/a' --impl-usd <額>",
  ]);
  expect(guidesFor("C-1", "/w/a", "in_progress", [])).toEqual([]);
});

it("AC-108: <パス> は単一引用符で囲み、中の単一引用符は '\\'' にする", () => {
  expect(shellQuote("/a b/c")).toBe("'/a b/c'");
  expect(shellQuote('/it\'s "x"')).toBe("'/it'\\''s \"x\"'");
  const [d] = guidesFor("C-1", "/tmp/a$$b$&", "awaiting_plan_approval", []);
  expect(d?.command).toBe("flywheel approve C-1 --workspace '/tmp/a$$b$&'");
  const [g] = guidesFor("C-1", "/it's here", "awaiting_plan_approval", []);
  expect(g?.command).toBe("flywheel approve C-1 --workspace '/it'\\''s here'");
});

it("T12: 詳細を開いても fetch は GET だけ、form 要素なし", async () => {
  const { requests } = installFakes(bodies(show()));
  const detail = await openDetail();
  await within(detail).findByText("タイトル");
  expect(requests.length).toBeGreaterThan(0);
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(document.querySelector("form")).toBeNull();
});
