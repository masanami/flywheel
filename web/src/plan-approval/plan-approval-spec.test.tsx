import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type { ApprovalSummary, SpecDraftAnswer } from "../api/types.gen.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

// 計画の承認の画面の「仕様の文書を開く」と、下書きづくりの答えの一覧（#1383）。名前の AC-<番号> が受入基準の番号。

afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = "";
  cleanup();
});

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";
const SPEC_URL = "/api/v1/workspaces/alpha/challenges/C-1/spec";
const SPEC_PATH = ".flywheel/specs/c-1.md";

const summary = (over: Partial<ApprovalSummary> = {}) => ({
  approval: {
    challenge_id: "C-1",
    conditions_after_release: [],
    conditions_after_release_heading: "",
    direct_completion: null,
    done_criteria: "完了条件",
    kind: "plan",
    mergeability_note: "",
    other_pending_operations: [],
    pairs: [],
    plan: {
      body: "本文",
      budget_limit_reason: null,
      budget_limit_usd: 30,
      budget_usd: 30,
      summary: "方針の本文",
      tasks: [],
      topics: [],
      uses_integration: null,
      version: 1,
    },
    release_held: false,
    releases: [],
    spec_draft_answers: [],
    spec_is_draft: true,
    spec_path: SPEC_PATH,
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
    title: "通知の再送",
    version: 7,
    ...over,
  } as ApprovalSummary,
});

async function open(approval: unknown) {
  window.location.hash = "#/a/alpha/challenges/C-1/approve-plan";
  const r = installFakes({
    ...fixtures(),
    // 画面の枠は、一覧に無い課題を「見つかりません」にする
    "/api/v1/workspaces/alpha/challenges": {
      challenges: [
        {
          id: "C-1",
          title: "通知の再送",
          status: "awaiting_plan_approval",
          card: {
            context: null,
            headline: null,
            modifiers: [],
            next_human_actions: ["approve_plan"],
            state: "awaiting_plan_approval",
          },
        },
      ],
    },
    [APPROVAL]: approval,
    [SPEC_URL]: {
      body: "# 再送の仕様",
      is_draft: true,
      path: SPEC_PATH,
      unreadable_reason: null,
    },
  });
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  const root = await screen.findByTestId("plan-approval");
  return { root, ...r };
}

const answer = (p: Partial<SpecDraftAnswer> = {}): SpecDraftAnswer => ({
  kind: "answer",
  attempt: 1,
  question: "記録はどの表に残すか",
  answer: "いまの作業ログに残す",
  by: "supervisor",
  basis: [{ path: "docs/architecture.md", locator: "作業ログの節" }],
  ...p,
});

const itemsOf = (root: HTMLElement) =>
  within(within(root).getByTestId("spec-answers")).getAllByRole("listitem");

it("AC-315・AC-314 #1383: spec_path が null でない課題では、spec-open が 1 つ出る", async () => {
  const { root } = await open(summary());
  expect(within(root).getAllByTestId("spec-open")).toHaveLength(1);
});

it("AC-316 #1383: spec_path が null の課題では、spec-open を出さない", async () => {
  const { root } = await open(
    summary({ spec_path: null, spec_is_draft: false }),
  );
  expect(within(root).queryByTestId("spec-open")).toBeNull();
});

it("AC-317・AC-318 #1383: 計画の承認の画面を開いた時点では、口は呼ばれず、spec-body も無い", async () => {
  const { root, requests } = await open(summary());
  expect(requests.some((r) => r.url === SPEC_URL)).toBe(false);
  expect(within(root).queryByTestId("spec-body")).toBeNull();
});

it("AC-319・AC-320・AC-321 #1383: 押すと口が 1 回呼ばれて本文が Markdown で出て、もう一度押すと畳む", async () => {
  const { root, requests } = await open(summary());
  fireEvent.click(within(root).getByTestId("spec-open"));
  const body = await within(root).findByTestId("spec-body");
  expect(
    within(body).getByRole("heading", { name: "再送の仕様" }),
  ).toBeTruthy();
  expect(requests.filter((r) => r.url === SPEC_URL)).toHaveLength(1);
  fireEvent.click(within(root).getByTestId("spec-open"));
  expect(within(root).queryByTestId("spec-body")).toBeNull();
});

it("AC-328 #1383: 答えが 1 件以上の課題では、spec-answers に問いの文と答えが出る", async () => {
  const { root } = await open(summary({ spec_draft_answers: [answer()] }));
  const text = within(root).getByTestId("spec-answers").textContent;
  expect(text).toContain("記録はどの表に残すか");
  expect(text).toContain("いまの作業ログに残す");
});

const mixed = () =>
  summary({
    spec_draft_answers: [
      answer(),
      answer({ by: "owner", question: "二つめの問い", basis: [] }),
      answer({
        kind: "note",
        by: "owner",
        question: "",
        answer: "確認の文は短くしてほしい",
        basis: [],
      }),
      answer({ by: "robot", question: "知らない値の問い", basis: [] }),
    ],
  });

it("AC-329 #1383: by が supervisor の答えに「スーパーバイザー」が出る", async () => {
  const { root } = await open(mixed());
  expect(itemsOf(root)[0]?.textContent).toContain("答えた者: スーパーバイザー");
});

it("AC-330 #1383: by が owner の答えに「オーナー」が出る", async () => {
  const { root } = await open(mixed());
  expect(itemsOf(root)[1]?.textContent).toContain("答えた者: オーナー");
});

it("#1383: 知らない by の値は、値のまま出す", async () => {
  const { root } = await open(mixed());
  expect(itemsOf(root)[3]?.textContent).toContain("答えた者: robot");
});

it("AC-331 #1383: 根拠を持つ答えに根拠の場所が出て、持たない答えには根拠の行が無い", async () => {
  const { root } = await open(mixed());
  expect(itemsOf(root)[0]?.textContent).toContain(
    "docs/architecture.md（作業ログの節）",
  );
  expect(itemsOf(root)[1]?.textContent).not.toContain("根拠");
});

it("AC-332・AC-333 #1383: kind が note の要素は、「添え書き」の語と文が出る", async () => {
  const { root } = await open(mixed());
  const note = itemsOf(root)[2];
  expect(note?.textContent).toContain("添え書き");
  expect(note?.textContent).toContain("確認の文は短くしてほしい");
  expect(note?.textContent).not.toContain("問い:");
});

it("AC-334 #1383: 答えが空の課題では、spec-answers を出さない", async () => {
  const { root } = await open(summary());
  expect(within(root).queryByTestId("spec-answers")).toBeNull();
});
