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
import { App } from "../App.tsx";
import type {
  ChallengeSpecResponse,
  Hold,
  ShowCardResponse,
  SpecDraft,
  SpecDraftAnswer,
} from "../api/types.gen.ts";
import { bodies, show } from "../testing/detailFixture.ts";
import { FakeEventSource, installFakes } from "../testing/fakes.ts";

// 課題の詳細の「仕様の文書を開く」と、スーパーバイザーが答えた分（#1383）。名前の AC-<番号> が受入基準の番号。

afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = "";
  cleanup();
});

const SPEC_URL = "/api/v1/workspaces/alpha/challenges/C-1/spec";

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
  question:
    "仕様の下書きに、決めないと進まない問いが 2 件残りました。下書き: .flywheel/specs/c-1.md",
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

const QUESTIONS: SpecDraft["questions"] = [
  {
    question: "残った問いの一つめ",
    options: [{ label: "選択肢の甲" }, { label: "選択肢の乙" }],
    recommended: 1,
    reason: "推奨の理由の甲",
  },
  {
    question: "残った問いの二つめ",
    options: [{ label: "選択肢の丙" }, { label: "選択肢の丁" }],
    recommended: 2,
    reason: "推奨の理由の乙",
  },
];

const answer = (p: Partial<SpecDraftAnswer> = {}): SpecDraftAnswer => ({
  kind: "answer",
  attempt: 1,
  question: "記録はどの表に残すか",
  answer: "いまの作業ログに残す",
  by: "supervisor",
  basis: [{ path: "docs/architecture.md", locator: "作業ログの節" }],
  ...p,
});

const draft = (p: Partial<SpecDraft> = {}): SpecDraft => ({
  answers: [],
  attempts: 1,
  base_path: null,
  hold_id: "H-1",
  hold_reason: "spec_question",
  max_attempts: 3,
  path: ".flywheel/specs/c-1.md",
  questions: QUESTIONS,
  reason: "新しい機能のため",
  repo: "app",
  state: "awaiting_owner",
  ...p,
});

const held = (p: Partial<SpecDraft> = {}): ShowCardResponse =>
  show({
    actions: ["answer_question"],
    challenge: { status: "awaiting_human", spec_draft: draft(p) },
    holds: [hold("H-1")],
  });

const noHold = (
  spec_draft: SpecDraft | null,
  spec_path: string | null = null,
): ShowCardResponse => show({ challenge: { spec_draft, spec_path } });

const spec = (
  p: Partial<ChallengeSpecResponse> = {},
): ChallengeSpecResponse => ({
  body: "# 取り消しの仕様\n\n本文の段落",
  is_draft: true,
  path: ".flywheel/specs/c-1.md",
  unreadable_reason: null,
  ...p,
});

async function open(detail: ShowCardResponse, res: unknown = spec()) {
  window.location.hash = "#/a/alpha/challenges/C-1";
  const ctx = installFakes(bodies(detail, { [SPEC_URL]: res }));
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  return { root, ...ctx };
}

const specCalls = (requests: { url: string }[]) =>
  requests.filter((r) => r.url === SPEC_URL);
const press = (root: HTMLElement) =>
  fireEvent.click(within(root).getByTestId("spec-open"));
const sv = (root: HTMLElement) => within(root).getByTestId("spec-sv-answers");

it("AC-298 #1383: spec-sv-answers に、答えた問いの文が出る", async () => {
  const { root } = await open(held({ answers: [answer()] }));
  expect(sv(root).textContent).toContain("記録はどの表に残すか");
});

it("AC-299 #1383: spec-sv-answers に、スーパーバイザーの答えが出る", async () => {
  const { root } = await open(held({ answers: [answer()] }));
  expect(sv(root).textContent).toContain("いまの作業ログに残す");
});

it("AC-300 #1383: spec-sv-answers に、答えの根拠の場所が出る", async () => {
  const { root } = await open(
    held({
      answers: [
        answer({
          basis: [
            { path: "docs/architecture.md", locator: "作業ログの節" },
            { path: "docs/glossary.md", locator: "" },
          ],
        }),
      ],
    }),
  );
  expect(sv(root).textContent).toContain(
    "docs/architecture.md（作業ログの節）",
  );
  expect(sv(root).textContent).toContain("docs/glossary.md");
  expect(sv(root).textContent).not.toContain("docs/glossary.md（");
});

const twoAttempts = () =>
  held({
    attempts: 2,
    answers: [
      answer({ attempt: 1, question: "前の回の問い" }),
      answer({ attempt: 2, question: "この回の問い" }),
    ],
  });

it("AC-301 #1383: attempts が 2 の記録では、attempt が 2 の答えの問いの文が出る", async () => {
  const { root } = await open(twoAttempts());
  expect(sv(root).textContent).toContain("この回の問い");
});

it("AC-302 #1383: 同じ記録で、attempt が 1 の答えの問いの文は出ない", async () => {
  const { root } = await open(twoAttempts());
  expect(sv(root).textContent).not.toContain("前の回の問い");
});

it("AC-303 #1383: by が owner の要素は、spec-sv-answers に出ない", async () => {
  const { root } = await open(
    held({
      answers: [
        answer(),
        answer({ by: "owner", question: "オーナーが答えた問い" }),
      ],
    }),
  );
  expect(sv(root).textContent).not.toContain("オーナーが答えた問い");
});

it("AC-304 #1383: kind が note の要素は、spec-sv-answers に出ない", async () => {
  const { root } = await open(
    held({
      answers: [
        answer(),
        // by が supervisor でも、種類が添え書きなら出さない（kind だけで外れることを確かめる）
        answer({
          kind: "note",
          question: "",
          answer: "添え書きの文",
          basis: [],
        }),
      ],
    }),
  );
  expect(sv(root).textContent).not.toContain("添え書きの文");
});

it("AC-305 #1383: 前の回の実行の答えだけの保留では、spec-sv-answers が出ない", async () => {
  const { root } = await open(
    held({ attempts: 2, answers: [answer({ attempt: 1 })] }),
  );
  expect(within(root).getByTestId("spec-questions")).toBeTruthy();
  expect(within(root).queryByTestId("spec-sv-answers")).toBeNull();
});

it("AC-306 #1383: スーパーバイザーの答えを持たない保留では、spec-sv-answers が出ない", async () => {
  const { root } = await open(held({ answers: [answer({ by: "owner" })] }));
  expect(within(root).getByTestId("spec-questions")).toBeTruthy();
  expect(within(root).queryByTestId("spec-sv-answers")).toBeNull();
});

it("AC-307 #1383: spec-sv-answers の中に、選ぶ入力が無い", async () => {
  const { root } = await open(held({ answers: [answer()] }));
  expect(sv(root).querySelectorAll("input, select, textarea")).toHaveLength(0);
  expect(
    sv(root).querySelectorAll("[contenteditable], [role=option]"),
  ).toHaveLength(0);
  for (const role of ["radio", "checkbox", "combobox", "listbox", "textbox"]) {
    expect(within(sv(root)).queryAllByRole(role), role).toHaveLength(0);
  }
});

it("AC-308・AC-314 #1383: 下書きづくりの保留を持つ課題では、保留の所に spec-open が 1 つだけ出る", async () => {
  const detail = held();
  detail.challenge.spec_path = "docs/features/x.md";
  const { root } = await open(detail);
  const place = within(root)
    .getByTestId("hold-question")
    .closest(".detail-question") as HTMLElement;
  expect(within(place).getByTestId("spec-open").textContent).toBe(
    "仕様の文書を開く",
  );
  expect(within(root).getAllByTestId("spec-open")).toHaveLength(1);
});

it("AC-309・AC-314 #1383: 仕様の文書の場所を持つ課題では、spec-open が 1 つ出る", async () => {
  const { root } = await open(noHold(null, "docs/features/x.md"));
  expect(within(root).getAllByTestId("spec-open")).toHaveLength(1);
});

it.each([
  ["AC-310", "pending"],
  ["AC-311", "running"],
])(
  "%s・AC-314 #1383: 場所が無く記録が %s の課題では、spec-open が 1 つ出る",
  async (_, state) => {
    const { root } = await open(noHold(draft({ state, hold_id: null })));
    expect(within(root).getAllByTestId("spec-open")).toHaveLength(1);
  },
);

it("AC-314 #1383: 下書きづくりの保留の ID の保留が画面に無いときも、途中の記録なら spec-open が 1 つ出る", async () => {
  const { root } = await open(noHold(draft()));
  expect(within(root).getAllByTestId("spec-open")).toHaveLength(1);
});

it("AC-312 #1383: 場所も記録も持たない課題では、spec-open を出さない", async () => {
  const { root } = await open(noHold(null));
  expect(within(root).queryByTestId("spec-open")).toBeNull();
});

it.each([
  ["AC-313", "skipped"],
  ["AC-313（done も同じ）", "done"],
])(
  "%s #1383: 場所が無く記録が %s の課題では、spec-open を出さない",
  async (_, state) => {
    const { root } = await open(noHold(draft({ state, hold_id: null })));
    expect(within(root).queryByTestId("spec-open")).toBeNull();
  },
);

it("AC-317・AC-318 #1383: 画面を開いた時点では、本文の口は呼ばれず、spec-body も無い", async () => {
  const { root, requests } = await open(held());
  await within(root).findByTestId("spec-open");
  expect(specCalls(requests)).toHaveLength(0);
  expect(within(root).queryByTestId("spec-body")).toBeNull();
  expect(
    within(root).getByTestId("spec-open").getAttribute("aria-expanded"),
  ).toBe("false");
});

it("AC-319・AC-320 #1383: 押すと口が 1 回呼ばれ、本文が Markdown として出る", async () => {
  const { root, requests } = await open(held());
  press(root);
  const body = await within(root).findByTestId("spec-body");
  expect(
    within(body).getByRole("heading", { name: "取り消しの仕様" }),
  ).toBeTruthy();
  expect(specCalls(requests)).toHaveLength(1);
  expect(specCalls(requests)[0]).toMatchObject({ method: "GET" });
});

it("AC-321 #1383: もう一度押すと、spec-body が無くなる（口は呼び直さない）", async () => {
  const { root, requests } = await open(held());
  press(root);
  await within(root).findByTestId("spec-body");
  press(root);
  expect(within(root).queryByTestId("spec-body")).toBeNull();
  expect(specCalls(requests)).toHaveLength(1);
});

it.each([
  ["AC-322", "missing", "ファイルがまだありません"],
  ["AC-323", "invalid_path", "場所が正しくないため開けません"],
  ["AC-324", "not_regular_file", "通常のファイルでないため開けません"],
  ["AC-325", "too_large", "2 MiB を超えているため開けません"],
  ["AC-326", "unreadable", "読み取れませんでした"],
])(
  "%s・AC-327 #1383: %s の応答では、訳の文が出て、spec-body は出ない",
  async (_, reason, text) => {
    const { root } = await open(
      held(),
      spec({ body: null, unreadable_reason: reason }),
    );
    press(root);
    const el = await within(root).findByTestId("spec-unreadable");
    expect(el.textContent).toBe(text);
    expect(el.getAttribute("role")).not.toBe("alert");
    expect(within(root).queryByTestId("spec-body")).toBeNull();
    press(root);
    expect(within(root).queryByTestId("spec-unreadable")).toBeNull();
  },
);

it("#1383: 口が失敗したときは、失敗を知らせ、spec-body は出ない", async () => {
  const { root } = await open(
    held(),
    () =>
      new Response(
        JSON.stringify({
          error: { code: "not_found", message: "仕様の文書がありません" },
        }),
        { status: 404 },
      ),
  );
  press(root);
  await waitFor(() =>
    expect(within(root).getByRole("alert").textContent).toContain(
      "仕様の文書がありません",
    ),
  );
  expect(within(root).queryByTestId("spec-body")).toBeNull();
});

it("#1383: 返る前に畳んで開き直しても、前の呼び出しの遅れた応答で上書きしない", async () => {
  const resolvers: ((r: Response) => void)[] = [];
  window.location.hash = "#/a/alpha/challenges/C-1";
  installFakes(bodies(held(), {}));
  const base = globalThis.fetch;
  vi.stubGlobal("fetch", (input: string, init?: RequestInit) =>
    String(input) === SPEC_URL
      ? new Promise<Response>((r) => resolvers.push(r))
      : base(input, init),
  );
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  press(root);
  press(root);
  press(root);
  const json = (body: string) => new Response(JSON.stringify(spec({ body })));
  await act(async () => resolvers[1]?.(json("# 新しい本文")));
  await act(async () => resolvers[0]?.(json("# 古い本文")));
  expect(within(root).getByTestId("spec-body").textContent).toBe("新しい本文");
});

const frameOf = (root: HTMLElement) =>
  within(root).getByTestId("hold-question");

it("AC-335 #1383: hold-question の中に、残った問いの文が出ない", async () => {
  const { root } = await open(held());
  for (const q of QUESTIONS) {
    expect(frameOf(root).textContent).not.toContain(q.question);
  }
  expect(frameOf(root).textContent).toContain("問いが 2 件残りました");
});

it("AC-336 #1383: hold-question の中に、選択肢の見出しが出ない", async () => {
  const { root } = await open(held());
  for (const o of QUESTIONS.flatMap((q) => q.options)) {
    expect(frameOf(root).textContent).not.toContain(o.label);
  }
});

it("AC-337 #1383: hold-question の中に、推奨の理由が出ない", async () => {
  const { root } = await open(held());
  for (const q of QUESTIONS) {
    expect(frameOf(root).textContent).not.toContain(q.reason);
  }
});

it("AC-338 #1383: spec-questions の中に、問いの文がそれぞれ 1 回ずつ出る", async () => {
  const { root } = await open(held());
  const text = within(root).getByTestId("spec-questions").textContent ?? "";
  for (const q of QUESTIONS) {
    expect(text.split(q.question)).toHaveLength(2);
  }
});
