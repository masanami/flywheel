import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type {
  Hold,
  Requirement,
  ShowCardResponse,
  SliceSplit,
} from "../api/types.gen.ts";
import { bodies, show } from "../testing/detailFixture.ts";
import { FakeEventSource, installFakes } from "../testing/fakes.ts";

// 課題の詳細の、区画「要求」と、区切りを分ける保留の所（docs/features/request-to-plan-s3.md「記録と表示」）。

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

const URL41 = "https://github.com/o/app/issues/41";
const EVIDENCE = ".flywheel/requirements/split-C-9.md";

const requirement = (p: Partial<Requirement> = {}): Requirement => ({
  challenges: [
    { id: "C-1", status: "done", title: "通知の設定を保存する" },
    { id: "C-2", status: "in_progress", title: "通知をまとめて送る" },
  ],
  chat_session: null,
  created_at: "2026-11-27T10:00:00Z",
  evidence_path: EVIDENCE,
  external_key: "o/app#41",
  id: "RQ-3",
  origin: "issue",
  origin_challenge_id: "C-9",
  resolved_at: null,
  title: "通知をまとめて受け取る",
  url: URL41,
  ...p,
});

const slice = (title: string, p: Partial<SliceSplit["slices"][number]>) => ({
  context: "位置づけ",
  depends_on: [],
  headline: "一言",
  repo: "app",
  scope: "範囲",
  summary: "要旨",
  title,
  ...p,
});

const sliceSplit = (p: Partial<SliceSplit> = {}): SliceSplit => ({
  evidence_path: EVIDENCE,
  hold_id: "H-1",
  reason: "設定と送信は、それぞれ単独で出荷できる",
  slices: [
    slice("設定を保存する", { summary: "設定の要旨", repo: "app" }),
    slice("まとめて送る", { summary: "送信の要旨", repo: "mailer" }),
    slice("履歴を見る", {
      summary: "履歴の要旨",
      repo: "web-ui",
      depends_on: [1, 2],
    }),
  ],
  state: "awaiting_owner",
  ...p,
});

const splitHold = (p: Partial<Hold> = {}): Hold => ({
  hold_id: "H-1",
  scope: "challenge",
  answer: null,
  answer_option: null,
  answered_at: null,
  answered_by: null,
  answered_channel: null,
  answered_verification: null,
  check: "check-H-1",
  closed_at: null,
  from_status: "classified",
  from_status_label: "分類済",
  kind: "human",
  options: [
    { id: "split", label: "この案で分ける", description: "", button: true },
    {
      id: "single",
      label: "分けずに 1 つの課題で進める",
      description: "",
      button: true,
    },
  ],
  question: "この課題は、区切り 1 つに収まらないと見ました。",
  raised_at: "2026-11-27T10:00:00Z",
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

const splitDetail = (): ShowCardResponse =>
  show({
    actions: ["answer_question"],
    challenge: { status: "awaiting_human" },
    holds: [splitHold()],
    slice_split: sliceSplit(),
  });

async function open(detail: ShowCardResponse) {
  window.location.hash = "#/a/alpha/challenges/C-1";
  installFakes(bodies(detail));
  render(<App />);
  act(() => FakeEventSource.latest.open());
  return screen.findByTestId("challenge-detail");
}

const section = (root: HTMLElement) =>
  within(root).getByTestId("requirement-section");
const proposal = (root: HTMLElement) =>
  within(root).getByTestId("slice-split-proposal");
const slicesOf = (root: HTMLElement) =>
  within(proposal(root)).getAllByTestId("slice-split-slice");
const buttonNames = (root: HTMLElement) =>
  within(root)
    .queryAllByTestId("hold-option-button")
    .map((b) => b.textContent);

it("AC-224: 要求を指す課題の詳細に requirement-section が出て、元の Issue の URL へのリンクがある", async () => {
  const root = await open(show({ requirement: requirement() }));
  const link = within(section(root)).getByRole("link", { name: URL41 });
  expect(link.getAttribute("href")).toBe(URL41);
});

it("元の Issue の URL が http でなければ、行き先を持たせない", async () => {
  const bad = "javascript:alert(1)";
  const root = await open(show({ requirement: requirement({ url: bad }) }));
  expect(section(root).textContent).toContain(bad);
  expect(
    [...section(root).querySelectorAll("a")].map((a) => a.getAttribute("href")),
  ).not.toContain(bad);
});

it("AC-225: 同じ要求の課題が、課題の詳細へのリンクと状態つきで並ぶ", async () => {
  const root = await open(show({ requirement: requirement() }));
  const rows = within(section(root)).getAllByTestId("requirement-challenge");
  expect(
    rows.map((r) => [
      r.querySelector("a")?.getAttribute("href"),
      r.querySelector("a")?.textContent,
      r.querySelector(".status-pill")?.textContent,
    ]),
  ).toEqual([
    ["#/a/alpha/challenges/C-1", "通知の設定を保存する", "完了"],
    ["#/a/alpha/challenges/C-2", "通知をまとめて送る", "着手中"],
  ]);
});

it("AC-226: 出どころ chat の要求では「チャットの欄の会話」が出る", async () => {
  const root = await open(
    show({
      requirement: requirement({
        origin: "chat",
        url: null,
        external_key: null,
        origin_challenge_id: null,
        chat_session: "s",
      }),
    }),
  );
  expect(section(root).textContent).toContain("チャットの欄の会話");
});

it("出どころ manual の要求では「手で作った」が出る", async () => {
  const root = await open(
    show({ requirement: requirement({ origin: "manual", url: null }) }),
  );
  expect(section(root).textContent).toContain("手で作った");
});

it("AC-227: 要求を指さず、取り下げた元の課題でもない課題には requirement-section が出ない", async () => {
  const root = await open(show({ requirement: null }));
  expect(within(root).queryByTestId("requirement-section")).toBeNull();
});

it("AC-228: 区切りを分ける保留を持つ課題に、その保留の所に slice-split-proposal が出る", async () => {
  const root = await open(splitDetail());
  expect(proposal(root).closest(".detail-question")).not.toBeNull();
});

it("slice_split の hold_id でない保留の所には、slice-split-proposal を出さない", async () => {
  const other = splitHold({ hold_id: "H-2", check: "check-H-2" });
  const root = await open(
    show({
      actions: ["answer_question"],
      challenge: { status: "awaiting_human" },
      holds: [other],
      slice_split: sliceSplit(),
    }),
  );
  expect(within(root).queryByTestId("slice-split-proposal")).toBeNull();
});

it("AC-229: slice-split-proposal に、区切りごとの番号と題名が出る", async () => {
  const root = await open(splitDetail());
  expect(slicesOf(root).map((s) => s.querySelector("b")?.textContent)).toEqual([
    "1. 設定を保存する",
    "2. まとめて送る",
    "3. 履歴を見る",
  ]);
});

it("AC-230: slice-split-proposal に、区切りごとの要旨が出る", async () => {
  const root = await open(splitDetail());
  const texts = slicesOf(root).map((s) => s.textContent);
  expect(texts[0]).toContain("設定の要旨");
  expect(texts[1]).toContain("送信の要旨");
  expect(texts[2]).toContain("履歴の要旨");
});

it("AC-231: slice-split-proposal に、区切りごとのリポジトリが出る", async () => {
  const root = await open(splitDetail());
  const texts = slicesOf(root).map((s) => s.textContent);
  expect(texts[0]).toContain("リポジトリ: app");
  expect(texts[1]).toContain("リポジトリ: mailer");
  expect(texts[2]).toContain("リポジトリ: web-ui");
});

it("AC-232: 前に済ませる区切りを持つ区切りでは、その番号が出る", async () => {
  const root = await open(splitDetail());
  const texts = slicesOf(root).map((s) => s.textContent);
  expect(texts[2]).toContain("前に済ませる区切り: 1, 2");
  expect(texts[0]).toContain("前に済ませる区切り: なし");
});

it("AC-233: 開いている区切りを分ける保留の slice-split-proposal に、分ける理由が出る", async () => {
  const root = await open(splitDetail());
  expect(proposal(root).textContent).toContain(
    "分ける理由: 設定と送信は、それぞれ単独で出荷できる",
  );
});

it("AC-234: 同じ slice-split-proposal に、証跡の場所が文字で出る", async () => {
  const root = await open(splitDetail());
  expect(proposal(root).textContent).toContain(`証跡の場所: ${EVIDENCE}`);
  expect(proposal(root).querySelector("a")).toBeNull();
});

it("AC-235: 開いている区切りを分ける保留に、「この案で分ける」のボタンが出る", async () => {
  const root = await open(splitDetail());
  expect(buttonNames(root)).toContain("この案で分ける");
});

it("AC-236: 開いている区切りを分ける保留に、「分けずに 1 つの課題で進める」のボタンが出る", async () => {
  const root = await open(splitDetail());
  expect(buttonNames(root)).toContain("分けずに 1 つの課題で進める");
});

// 取り下げで保留は閉じる。記録の状態は awaiting_owner のまま、hold_id は空になる。
it.each([null, "H-1"])(
  "AC-237: awaiting_owner のまま取り下げた課題には、「この案で分ける」のボタンが出ない（hold_id=%s）",
  async (holdId) => {
    const root = await open(
      show({
        challenge: { status: "withdrawn" },
        holds: [splitHold({ closed_at: "2026-11-27T11:00:00Z" })],
        slice_split: sliceSplit({ hold_id: holdId }),
      }),
    );
    expect(buttonNames(root)).not.toContain("この案で分ける");
    expect(
      within(root).queryByRole("button", { name: "この案で分ける" }),
    ).toBeNull();
  },
);
