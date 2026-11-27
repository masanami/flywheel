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
import { App } from "../App.tsx";
import type { Hold, ShowCardResponse, SpecDraft } from "../api/types.gen.ts";
import guidance from "../guidance.json";
import { bodies, show } from "../testing/detailFixture.ts";
import { FakeEventSource, installFakes } from "../testing/fakes.ts";

// 課題の詳細の、下書きづくりの保留の所（#1357）。
// 保留の選択肢（識別と見出し）は core が組み立てて返すので、ここでは仕様の値を持つ保留を偽の応答にする。

beforeEach(() => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok";
  document.head.append(m);
});
afterEach(() => {
  vi.unstubAllGlobals();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
});

const BASE = "/api/v1/workspaces/alpha";
const PATH = ".flywheel/specs/c-1.md";

const LABELS: Record<string, string> = {
  answer: "推奨のまま進める",
  proceed: "いまの下書きで計画へ進む",
  retry: "やり直す",
  skip: "仕様の文書なしで計画する",
};

const hold = (id: string, p: Partial<Hold> = {}): Hold => ({
  hold_id: id,
  scope: "challenge",
  answer: null,
  answer_option: null,
  answered_at: null,
  answered_by: null,
  answered_channel: null,
  answered_verification: null,
  closed_at: null,
  from_status: "classified",
  from_status_label: "分類済",
  kind: "human",
  options: [],
  question: "どちらにしますか",
  raised_at: "2026-11-01T00:00:00Z",
  relay_quote: null,
  relay_session: null,
  replan: false,
  run_id: null,
  stop_kind: null,
  supervisor_findings: null,
  supervisor_recommendation: null,
  supervisor_state: null,
  task_id: null,
  ...p,
});

const specHold = (id: string, options: string[]): Hold =>
  hold(id, {
    check: `check-${id}`,
    question: "仕様の下書きの保留です。",
    options: options.map((o) => ({
      id: o,
      label: LABELS[o] ?? o,
      description: "",
      button: true,
    })),
  });

const QUESTIONS: SpecDraft["questions"] = [
  {
    question: "取り消しは、実行中の処理にも効かせるか",
    options: [{ label: "実行中にも効かせる" }, { label: "待ちの処理だけ" }],
    recommended: 1,
    reason: "Issue の本文が「すぐ止めたい」と書いているため",
  },
  {
    question: "取り消した記録を残すか",
    options: [
      { label: "残さない" },
      { label: "残す" },
      { label: "1 日だけ残す" },
    ],
    recommended: 2,
    reason: "後から調べられるようにするため",
  },
];

const draft = (p: Partial<SpecDraft> = {}): SpecDraft => ({
  answers: [],
  attempts: 1,
  base_path: null,
  hold_id: "H-1",
  hold_reason: "spec_question",
  max_attempts: 3,
  path: PATH,
  questions: QUESTIONS,
  reason: "新しい機能のため",
  repo: "app",
  state: "awaiting_owner",
  ...p,
});

const questionDetail = (
  p: Partial<SpecDraft> = {},
  holds: Hold[] = [specHold("H-1", ["answer", "proceed", "skip"])],
): ShowCardResponse =>
  show({
    actions: ["answer_question"],
    challenge: { status: "awaiting_human", spec_draft: draft(p) },
    holds,
  });

const failedDetail = () =>
  questionDetail({ hold_reason: "spec_failed", questions: [] }, [
    specHold("H-1", ["retry", "proceed", "skip"]),
  ]);

async function open(detail: ShowCardResponse, extra = {}) {
  window.location.hash = "#/a/alpha/challenges/C-1";
  const ctx = installFakes(bodies(detail, extra));
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  return { root, ...ctx };
}

// 保留の所（hold-question は、その中の保留の問いの文の要素）
const placesOf = (root: HTMLElement) =>
  within(root)
    .getAllByTestId("hold-question")
    .map((e) => e.closest(".detail-question") as HTMLElement);
const placeOf = (root: HTMLElement) => {
  const [place, ...rest] = placesOf(root);
  if (!place || rest.length > 0) throw new Error("保留の所が 1 つでない");
  return place;
};

const list = (root: HTMLElement) => within(root).getByTestId("spec-questions");
const questionsOf = (root: HTMLElement) =>
  within(list(root)).getAllByTestId("plan-question");
const buttonIds = (root: HTMLElement) =>
  within(root)
    .getAllByTestId("hold-option-button")
    .map((b) => b.closest("li")?.getAttribute("data-option"));

it("AC-374 #1357: 問いの保留では、spec-questions に残った問いの文が出る", async () => {
  const { root } = await open(questionDetail());
  const items = questionsOf(root);
  expect(items).toHaveLength(2);
  expect(items[0]?.textContent).toContain(
    "取り消しは、実行中の処理にも効かせるか",
  );
  expect(items[1]?.textContent).toContain("取り消した記録を残すか");
});

it("AC-375 #1357: spec-questions に、問いごとの選択肢の見出しが並びの順で出る", async () => {
  const { root } = await open(questionDetail());
  const labels = questionsOf(root).map((item) =>
    within(item)
      .getAllByRole("listitem")
      .map((li) => li.textContent?.replace("（推奨）", "")),
  );
  expect(labels).toEqual([
    ["実行中にも効かせる", "待ちの処理だけ"],
    ["残さない", "残す", "1 日だけ残す"],
  ]);
});

it("AC-376 #1357: 推奨の選択肢の見出しの後ろにだけ「（推奨）」が付く", async () => {
  const { root } = await open(questionDetail());
  const texts = questionsOf(root).map((item) =>
    within(item)
      .getAllByRole("listitem")
      .map((li) => li.textContent),
  );
  expect(texts).toEqual([
    ["実行中にも効かせる（推奨）", "待ちの処理だけ"],
    ["残さない", "残す（推奨）", "1 日だけ残す"],
  ]);
});

it("AC-377 #1357: spec-questions に、問いごとの推奨の理由が出る", async () => {
  const { root } = await open(questionDetail());
  const items = questionsOf(root);
  expect(items[0]?.textContent).toContain(
    "推奨の理由: Issue の本文が「すぐ止めたい」と書いているため",
  );
  expect(items[1]?.textContent).toContain(
    "推奨の理由: 後から調べられるようにするため",
  );
});

it("AC-378 #1357: spec-questions の中に、選ぶ入力（ラジオボタン・選択の欄・文の欄）が無い", async () => {
  const { root } = await open(questionDetail());
  expect(list(root).querySelectorAll("input, select, textarea")).toHaveLength(
    0,
  );
  for (const role of ["radio", "checkbox", "combobox", "listbox", "textbox"]) {
    expect(within(list(root)).queryAllByRole(role), role).toHaveLength(0);
  }
  expect(
    list(root).querySelectorAll("[contenteditable], [role=option]"),
  ).toHaveLength(0);
});

it.each([
  ["spec_question", QUESTIONS, ["answer", "proceed", "skip"]],
  ["spec_failed", [], ["retry", "proceed", "skip"]],
  ["spec_attempt_limit", QUESTIONS, ["proceed", "skip"]],
])(
  "AC-379 #1357: 下書きづくりの保留（%s）の所に、下書きの場所が出る",
  async (reason, questions, options) => {
    const { root } = await open(
      questionDetail({ hold_reason: reason, questions }, [
        specHold("H-1", options),
      ]),
    );
    expect(placeOf(root).textContent).toContain(`下書き: ${PATH}`);
  },
);

it("AC-379 #1357: 下書きづくりでない保留だけの課題では、下書きの場所を出さない", async () => {
  const { root } = await open(
    questionDetail({ hold_id: null, hold_reason: null }, [hold("H-9")]),
  );
  expect(placeOf(root).textContent).not.toContain(".flywheel/specs/");
});

it("AC-380 #1357: 問いの保留では、spec-answer-guide に --option answer --choice の案内が出る（--hold は付けない）", async () => {
  const { root } = await open(
    questionDetail({}, [
      specHold("H-1", ["answer", "proceed", "skip"]),
      hold("H-2"),
    ]),
  );
  const guide = within(root).getByTestId("spec-answer-guide");
  expect(guide.querySelector("code")?.textContent).toBe(
    "flywheel answer C-1 --workspace '/w/alpha' --option answer --choice <問いの番号>=<選択肢の番号>",
  );
  expect(guide.textContent).toContain("対話");
});

it("AC-381 #1357: 失敗の保留では、spec-answer-guide を出さない", async () => {
  const { root } = await open(failedDetail());
  expect(within(root).getByTestId("hold-question")).toBeTruthy();
  expect(within(root).queryByTestId("spec-answer-guide")).toBeNull();
  expect(within(root).queryByTestId("spec-questions")).toBeNull();
});

it("AC-381 #1357: 回数の上限の保留では、残った問いは出すが、spec-answer-guide は出さない", async () => {
  const { root } = await open(
    questionDetail({ hold_reason: "spec_attempt_limit", attempts: 3 }, [
      specHold("H-1", ["proceed", "skip"]),
    ]),
  );
  expect(questionsOf(root)).toHaveLength(2);
  expect(within(root).queryByTestId("spec-answer-guide")).toBeNull();
});

it("AC-382 #1357: guidance.json に、識別 answer-spec-choice・場面の名前 spec_question の案内がある", () => {
  const found = guidance.templates.filter((t) => t.id === "answer-spec-choice");
  expect(found).toHaveLength(1);
  expect(found[0]?.when).toEqual({ context: "spec_question" });
});

it("AC-384 #1357: 問いの保留の answer のボタンの見出しが「推奨のまま進める」になる", async () => {
  const { root } = await open(questionDetail());
  const button = within(root)
    .getByTestId("hold-options")
    .querySelector('li[data-option="answer"] button');
  expect(button?.textContent).toBe("推奨のまま進める");
});

it("AC-385 #1357: answer のボタンを押して確かめると、保留の回答の口へ option が answer の本文が送られる", async () => {
  const { root, requests } = await open(questionDetail(), {
    [`POST ${BASE}/challenges/C-1/holds/H-1/answer`]: {
      cycle: { started: true, reason: "started" },
      hold: specHold("H-1", ["answer"]),
    },
  });
  fireEvent.click(
    within(root).getByRole("button", { name: "推奨のまま進める" }),
  );
  fireEvent.click(within(root).getByTestId("hold-confirm-send"));
  await waitFor(() =>
    expect(requests.some((r) => r.method === "POST")).toBe(true),
  );
  const post = requests.find((r) => r.method === "POST");
  expect(post?.url).toBe(`${BASE}/challenges/C-1/holds/H-1/answer`);
  expect(JSON.parse(post?.body ?? "{}")).toEqual({
    check: "check-H-1",
    option: "answer",
  });
});

it("AC-386 #1357: spec_question の保留では、answer・proceed・skip のボタンが出る", async () => {
  const { root } = await open(questionDetail());
  expect(buttonIds(root)).toEqual(["answer", "proceed", "skip"]);
});

it("AC-387 #1357: attempts が 1 の spec_failed の保留では、retry・proceed・skip のボタンが出る", async () => {
  const { root } = await open(failedDetail());
  expect(buttonIds(root)).toEqual(["retry", "proceed", "skip"]);
});

it("AC-388 #1357: spec_attempt_limit の保留では、proceed・skip のボタンが出る", async () => {
  const { root } = await open(
    questionDetail({ hold_reason: "spec_attempt_limit", attempts: 3 }, [
      specHold("H-1", ["proceed", "skip"]),
    ]),
  );
  expect(buttonIds(root)).toEqual(["proceed", "skip"]);
});

it("AC-389 #1357: spec-questions は、spec_draft.hold_id と同じ ID の保留の所にだけ出る", async () => {
  const { root } = await open(
    questionDetail({ hold_id: "H-2" }, [
      hold("H-1"),
      specHold("H-2", ["answer", "proceed", "skip"]),
      hold("H-3"),
    ]),
  );
  const frames = placesOf(root);
  expect(frames).toHaveLength(3);
  expect(
    frames.map((f) => within(f).queryAllByTestId("spec-questions").length),
  ).toEqual([0, 1, 0]);
  expect(
    frames.map((f) => within(f).queryAllByTestId("spec-answer-guide").length),
  ).toEqual([0, 1, 0]);
  expect(
    frames.map((f) => f.textContent?.includes(".flywheel/specs/")),
  ).toEqual([false, true, false]);
  expect(within(root).getAllByTestId("spec-questions")).toHaveLength(1);
});

it("#1357: 下書きづくりの記録が無い課題の保留には、下書きの問いも場所も案内も出ない", async () => {
  const { root } = await open(
    show({ actions: ["answer_question"], holds: [hold("H-1")] }),
  );
  expect(within(root).getByTestId("hold-question")).toBeTruthy();
  expect(within(root).queryByTestId("spec-questions")).toBeNull();
  expect(within(root).queryByTestId("spec-answer-guide")).toBeNull();
  expect(root.textContent).not.toContain(".flywheel/specs/");
});

it("#1357: 答えの by に知らない値を持つ記録でも、画面は誤りにならない", async () => {
  const { root } = await open(
    questionDetail({
      answers: [
        {
          kind: "answer",
          attempt: 1,
          question: "前の問い",
          answer: "前の答え",
          by: "supervisor",
          basis: [{ path: "docs/a.md", locator: "L1" }],
        },
      ],
    }),
  );
  expect(questionsOf(root)).toHaveLength(2);
});
