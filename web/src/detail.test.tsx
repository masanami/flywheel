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
    "/api/v1/workspaces/alpha/challenges": {
      challenges: [{ ...detail.challenge, card: detail.card }],
    },
    "/api/v1/workspaces/alpha/challenges/C-1": detail,
    "/api/v1/workspaces/alpha/log?challenge=C-1": { activities: [] },
    ...extra,
  };
}

async function openDetail() {
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const card = await screen.findByTestId("card-C-1");
  fireEvent.click(within(card).getByRole("button"));
  return await screen.findByTestId("card-detail-content");
}

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
        run_id: null,
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
  const log = within(
    within(detail).getByRole("region", { name: "作業ログ", hidden: true }),
  );
  const lis = log
    .getAllByRole("listitem", { hidden: true })
    .map((l) => l.textContent ?? "");
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
        run_id: null,
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

it("AC-105/106/107: 詳細に answer・budget のコマンドが差し込み口のまま出て、端末で実行する旨が添えられる", async () => {
  const cases = [
    {
      d: show({
        card: {
          ...show().card,
          next_human_actions: ["answer_question"],
          state: "awaiting_human",
        },
        challenge: { ...show().challenge, status: "awaiting_human" },
      }),
      command: "flywheel answer C-1 --workspace '/w/alpha' --answer <回答>",
      guide: "answer",
    },
    {
      d: show({
        card: {
          ...show().card,
          modifiers: ["budget_exhausted"],
          next_human_actions: ["increase_budget"],
        },
      }),
      command: "flywheel budget C-1 --workspace '/w/alpha' --impl-usd <額>",
      guide: "budget",
    },
  ];
  for (const c of cases) {
    installFakes(bodies(c.d));
    const detail = await openDetail();
    const g = await within(detail).findByRole("region", {
      name: "承認方法の案内",
      hidden: true,
    });
    const pre = g.querySelector(`[data-guide="${c.guide}"]`);
    expect(pre?.textContent).toBe(c.command);
    expect(g.textContent).toContain("Claude Code の外の端末");
    expect(
      within(detail).queryByRole("button", { name: /回答|予算|額|承認/ }),
    ).toBeNull();
    cleanup();
  }
});

it("AC-113: 承認と差し戻しのそれぞれの記録が詳細に出る", async () => {
  const approval = (decision: string, reason: string | null, at: string) => ({
    actor: "me",
    channel: "tty",
    decided_at: at,
    decision,
    kind: "plan",
    operation_id: null,
    reason,
    target_version: 1,
    verification: "tty",
  });
  installFakes(
    bodies(
      show({
        approvals: [
          approval("rejected", "差し戻しの理由", "2026-01-02T00:00:00Z"),
          approval("approved", null, "2026-01-03T00:00:00Z"),
        ],
      }),
    ),
  );
  const detail = await openDetail();
  const rec = await within(detail).findByRole("region", {
    name: "承認と差し戻しの記録",
    hidden: true,
  });
  const items = within(rec)
    .getAllByRole("listitem", { hidden: true })
    .map((l) => l.textContent ?? "");
  expect(items).toHaveLength(2);
  expect(items[0]).toContain("rejected");
  expect(items[0]).toContain("差し戻しの理由");
  expect(items[1]).toContain("approved");
});

it("AC-105/106: answer・budget の案内は差し込み口 <回答>・<額> のまま、コードで決まる", () => {
  const a = guidesFor("C-1", "/w/a", ["answer_question"]);
  expect(a.map((g) => g.command)).toEqual([
    "flywheel answer C-1 --workspace '/w/a' --answer <回答>",
  ]);
  const b = guidesFor("C-1", "/w/a", ["increase_budget"]);
  expect(b.map((g) => g.command)).toEqual([
    "flywheel budget C-1 --workspace '/w/a' --impl-usd <額>",
  ]);
  expect(guidesFor("C-1", "/w/a", [])).toEqual([]);
});

it("AC-108: <パス> は単一引用符で囲み、中の単一引用符は '\\'' にする", () => {
  expect(shellQuote("/a b/c")).toBe("'/a b/c'");
  expect(shellQuote('/it\'s "x"')).toBe("'/it'\\''s \"x\"'");
  const [d] = guidesFor("C-1", "/tmp/a$$b$&", ["approve_completion"]);
  expect(d?.command).toBe(
    "flywheel reject C-1 --workspace '/tmp/a$$b$&' --reason <理由>",
  );
  const [g] = guidesFor("C-1", "/it's here", ["approve_plan"]);
  expect(g?.command).toBe(
    "flywheel reject C-1 --workspace '/it'\\''s here' --reason <理由>",
  );
});

it("T12: 詳細を開いても fetch は GET だけ、form 要素なし", async () => {
  const { requests } = installFakes(bodies(show()));
  const detail = await openDetail();
  await within(detail).findByText("計画（0）");
  expect(requests.length).toBeGreaterThan(0);
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(document.querySelector("form")).toBeNull();
});
