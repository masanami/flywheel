import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import type { ApprovalSummary } from "./api/types.gen.ts";
import { CONVERSATION_NOTE } from "./guidance.ts";
import {
  FakeEventSource,
  type FakeResponder,
  type Recorded,
  fixtures,
  installFakes,
} from "./testing/fakes.ts";

const meta = () => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok-123";
  document.head.append(m);
};

beforeEach(meta);
afterEach(() => {
  vi.unstubAllGlobals();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
});

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";
const APPROVE = "POST /api/v1/workspaces/alpha/challenges/C-1/approve";

function card(state: string, actions: string[], modifiers: string[] = []) {
  return {
    context: null,
    headline: null,
    modifiers,
    next_human_actions: actions,
    state,
  };
}

function challenge(status: string, version = 3) {
  return {
    created_at: "",
    description: "説明",
    done_criteria: "完了条件",
    id: "C-1",
    priority: null,
    reporter: "me",
    status,
    status_label: "",
    title: "題名",
    updated_at: "",
    urgency: null,
    version,
  };
}

function summary(over: Partial<ApprovalSummary> = {}): {
  approval: ApprovalSummary;
} {
  return {
    approval: {
      challenge_id: "C-1",
      done_criteria: "完了条件の本文",
      kind: "completion",
      other_pending_operations: [
        { id: "O-2", kind: "delete", summary: "古いデータの削除" },
      ],
      pairs: [],
      plan: null,
      release_held: true,
      stages: {
        actual: null,
        expected: null,
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
      },
      releases: [
        {
          head_sha: "abc123def456",
          id: "O-1",
          ref: "refs/pull/9/head",
          summary: "v1.0 を出す",
        },
        { head_sha: null, id: "O-3", ref: null, summary: "別の release" },
      ],
      title: "題名",
      version: 3,
      ...over,
    },
  };
}

function setup(
  state: string,
  actions: string[],
  extra: Record<string, unknown> = {},
  modifiers: string[] = [],
) {
  const f = fixtures();
  const c = card(state, actions, modifiers);
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [{ ...challenge(state), card: c }],
  };
  f["/api/v1/workspaces/alpha/challenges/C-1"] = {
    approvals: [],
    card: c,
    challenge: challenge(state),
    holds: [],
    operations: [],
    plans: [],
    runs: [],
    source_binding: null,
  };
  f["/api/v1/workspaces/alpha/log?challenge=C-1"] = { activities: [] };
  return installFakes({ ...f, ...extra });
}

async function open() {
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const c = await screen.findByTestId("card-C-1");
  fireEvent.click(within(c).getByRole("button"));
  return await screen.findByTestId("card-detail-content");
}

const btn = (m: HTMLElement, name: string | RegExp) =>
  within(m).findByRole("button", { name, hidden: true });
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status });

it("AC-207: approve_plan を持つ課題の詳細に、開いたとき ① を呼んで計画の要約と承認のボタンが出る", async () => {
  const { requests } = setup("awaiting_plan_approval", ["approve_plan"], {
    [APPROVAL]: summary({
      kind: "plan",
      plan: { body: "承認する計画", version: 2 },
      releases: [],
      other_pending_operations: [],
      release_held: false,
    }),
  });
  const m = await open();
  await btn(m, "計画を承認する");
  expect(within(m).getByText("承認する計画")).toBeInTheDocument();
  expect(requests.some((r) => r.url === APPROVAL && r.method === "GET")).toBe(
    true,
  );
});

it("AC-208: approve_completion を持つ課題の詳細に完了の承認のボタンが出る", async () => {
  setup("awaiting_completion_approval", ["approve_completion"], {
    [APPROVAL]: summary(),
  });
  const m = await open();
  await btn(m, /完了を承認する/);
});

it("AC-209: 状態の文字列が承認待ちでも next_human_actions に承認のコードが無ければボタンも ① も無い", async () => {
  const { requests } = setup("awaiting_plan_approval", ["triage"]);
  const m = await open();
  await within(m).findByText("計画（0）");
  expect(
    within(m).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
  expect(requests.some((r) => r.url.endsWith("/approval"))).toBe(false);
});

it("AC-209: 承認のコードがあれば状態の文字列が何でもボタンが出る", async () => {
  setup("in_progress", ["approve_plan"], {
    [APPROVAL]: summary({ kind: "plan", plan: null }),
  });
  const m = await open();
  await btn(m, "計画を承認する");
});

it("AC-210/213: 完了の要約に完了条件・同時には承認されない不可逆操作・release の head（取れなければその旨）・保留のままを示す", async () => {
  setup("awaiting_completion_approval", ["approve_completion"], {
    [APPROVAL]: summary(),
  });
  const m = await open();
  await btn(m, /完了を承認する/);
  const s = within(m).getByTestId("approval-summary");
  expect(s.textContent).toContain("完了条件の本文");
  expect(s.textContent).toContain("O-2 / delete: 古いデータの削除");
  expect(s.querySelector('[data-release="O-1"]')?.textContent).toContain(
    "abc123def456",
  );
  expect(s.querySelector('[data-release="O-3"]')?.textContent).toContain(
    "取得できませんでした",
  );
  expect(within(m).getByTestId("release-held").textContent).toContain(
    "保留のまま残ります",
  );
  // 保留を外す選択は無い
  expect(within(m).queryByRole("checkbox", { hidden: true })).toBeNull();
});

it("AC-211: ボタンは開いたときの版を CSRF つきで ② に送り、承認後は承認済みを示す", async () => {
  const { requests } = setup("awaiting_plan_approval", ["approve_plan"], {
    [APPROVAL]: summary({ kind: "plan", version: 7 }),
    [APPROVE]: () => json(200, {}),
  });
  const m = await open();
  fireEvent.click(await btn(m, "計画を承認する"));
  await within(m).findByText("承認しました。", {}, { timeout: 2000 });
  const posts = requests.filter((r) => r.method === "POST");
  expect(posts).toHaveLength(1);
  expect(posts[0]?.url).toBe("/api/v1/workspaces/alpha/challenges/C-1/approve");
  expect(posts[0]?.body).toBe('{"version":7}');
  expect(posts[0]?.headers["X-Flywheel-CSRF"]).toBe("tok-123");
});

it("AC-212: conflict なら ① を読み直して最新の要約を示し、自動で再送せず、押し直すと新しい版を送る", async () => {
  let call = 0;
  const responder: FakeResponder = () => {
    call++;
    return call === 1
      ? json(409, { error: { code: "conflict", message: "版が違う" } })
      : json(200, {});
  };
  let reads = 0;
  const summaryResponder: FakeResponder = () => {
    reads++;
    return json(
      200,
      summary({
        kind: "plan",
        version: reads === 1 ? 3 : 4,
        plan: { body: reads === 1 ? "古い計画" : "新しい計画", version: 1 },
      }),
    );
  };
  const { requests } = setup("awaiting_plan_approval", ["approve_plan"]);
  // setup の後から応答を差し込む（GET の応答も関数で返す）
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      const rec: Recorded = {
        url: String(input),
        method,
        headers: {},
        body: typeof init?.body === "string" ? init.body : null,
      };
      requests.push(rec);
      if (rec.url === APPROVAL) return summaryResponder(rec);
      if (method === "POST") return responder(rec);
      const b = (fixturesFor() as Record<string, unknown>)[rec.url];
      return b === undefined ? json(404, {}) : json(200, b);
    }),
  );
  const m = await open();
  fireEvent.click(await btn(m, "計画を承認する"));
  await within(m).findByText(
    /最新の要約を読み直しました/,
    {},
    { timeout: 2000 },
  );
  await within(m).findByText("新しい計画");
  expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  fireEvent.click(await btn(m, "計画を承認する"));
  await within(m).findByText("承認しました。");
  const posts = requests.filter((r) => r.method === "POST");
  expect(posts.map((p) => p.body)).toEqual(['{"version":3}', '{"version":4}']);
});

// conflict のテストが GET の本文を引き直すための固定の本文。
function fixturesFor(): Record<string, unknown> {
  const f = fixtures();
  const c = card("awaiting_plan_approval", ["approve_plan"]);
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [{ ...challenge("awaiting_plan_approval"), card: c }],
  };
  f["/api/v1/workspaces/alpha/challenges/C-1"] = {
    approvals: [],
    card: c,
    challenge: challenge("awaiting_plan_approval"),
    holds: [],
    operations: [],
    plans: [],
    runs: [],
    source_binding: null,
  };
  f["/api/v1/workspaces/alpha/log?challenge=C-1"] = { activities: [] };
  return f;
}

it.each([
  ["403 forbidden_origin", 403, "forbidden_origin"],
  ["403 だけ", 403, "something"],
  ["forbidden_origin だけ", 400, "forbidden_origin"],
])(
  "403・forbidden_origin（%s）では再読み込みを促し、再送しない",
  async (_n, status, code) => {
    const { requests } = setup("awaiting_plan_approval", ["approve_plan"], {
      [APPROVAL]: summary({ kind: "plan" }),
      [APPROVE]: () => json(status, { error: { code, message: "拒否" } }),
    });
    const m = await open();
    fireEvent.click(await btn(m, "計画を承認する"));
    await within(m).findByText(/ページを再読み込みして/, {}, { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 50));
    expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  },
);

it("UI が送る要求は GET と …/approve の POST だけ", async () => {
  const { requests } = setup("awaiting_plan_approval", ["approve_plan"], {
    [APPROVAL]: summary({ kind: "plan" }),
    [APPROVE]: () => json(200, {}),
  });
  const m = await open();
  fireEvent.click(await btn(m, "計画を承認する"));
  await within(m).findByText("承認しました。");
  await waitFor(() => expect(requests.length).toBeGreaterThan(0));
  for (const r of requests) {
    if (r.method === "GET") continue;
    expect(r.method).toBe("POST");
    expect(r.url).toMatch(/\/challenges\/C-1\/approve$/);
  }
});

const guideText = async (m: HTMLElement) =>
  (
    await within(m).findByRole("region", {
      name: "承認方法の案内",
      hidden: true,
    })
  ).textContent ?? "";

it("AC-214: approve_plan には reject の端末のコマンドと対話の案内", async () => {
  setup("awaiting_plan_approval", ["approve_plan"], {
    [APPROVAL]: summary({ kind: "plan" }),
  });
  const t = await guideText(await open());
  expect(t).toContain(
    "flywheel reject C-1 --workspace '/w/alpha' --reason <理由>",
  );
  expect(t).toContain(CONVERSATION_NOTE);
  // AC-214a: 計画の承認は画面のボタンで行うので approve のコマンドは出さない
  expect(t).not.toContain("flywheel approve");
});

it("AC-215: approve_completion には本番反映を同時に承認する approve のコマンドと対話の案内", async () => {
  setup("awaiting_completion_approval", ["approve_completion"], {
    [APPROVAL]: summary(),
  });
  const t = await guideText(await open());
  expect(t).toContain("flywheel approve C-1 --workspace '/w/alpha'");
  expect(t).toContain("本番反映");
  expect(t).toContain(CONVERSATION_NOTE);
});

it("AC-216: approve_operation には approve <OP-ID> のコマンドと対話の案内（ボタンは無い）", async () => {
  setup("in_progress", ["approve_operation"]);
  const m = await open();
  const t = await guideText(m);
  expect(t).toContain("flywheel approve <OP-ID> --workspace '/w/alpha'");
  expect(t).toContain(CONVERSATION_NOTE);
  expect(
    within(m).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
});

// 案内の文言は UI の定数（CONVERSATION_NOTE）。テストは文言を直書きせず、定数の表示で確かめる。
it("AC-217: 案内の文言の定数は空でなく、5 つの操作それぞれの詳細に定数の文言がそのまま出る", async () => {
  expect(CONVERSATION_NOTE.trim()).not.toBe("");
  const cases: [string, string][] = [
    ["awaiting_plan_approval", "approve_plan"],
    ["awaiting_completion_approval", "approve_completion"],
    ["awaiting_human", "answer_question"],
    ["in_progress", "approve_operation"],
    ["in_progress", "increase_budget"],
  ];
  for (const [state, code] of cases) {
    cleanup();
    setup(
      state,
      [code],
      state === "awaiting_plan_approval"
        ? { [APPROVAL]: summary({ kind: "plan" }) }
        : state === "awaiting_completion_approval"
          ? { [APPROVAL]: summary() }
          : {},
    );
    const m = await open();
    const note = await within(m).findByTestId(
      "conversation-note",
      {},
      {
        timeout: 3000,
      },
    );
    expect(note.textContent, code).toBe(CONVERSATION_NOTE);
  }
});

it.each(["triage", "review_discrepancy", "clear_slot"])(
  "AC-218: %s だけなら対話の案内は出ない",
  async (code) => {
    setup("in_progress", [code]);
    const m = await open();
    await within(m).findByText("計画（0）");
    expect(m.textContent).not.toContain(CONVERSATION_NOTE);
    expect(within(m).queryByTestId("conversation-note")).toBeNull();
  },
);
