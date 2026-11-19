import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type { ChallengeCard, Followup } from "../api/types.gen.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

// フォローアップの画面の受入基準を 1 つずつ直接確かめる。Issue #812。

const URL_ALL = "/api/v1/workspaces/alpha/followups?status=all";

const fu = (id: string, p: Partial<Followup> = {}): Followup => ({
  challenge_id: null,
  created_at: "2026-11-10T01:00:00Z",
  detail: "",
  id,
  note: null,
  promoted_challenge_id: null,
  reason: null,
  source: "サイクルの振り返り",
  status: "open",
  title: `題名 ${id}`,
  updated_at: "2026-11-10T01:00:00Z",
  ...p,
});

const card = (id: string, title: string, label: string): ChallengeCard => ({
  card: {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: [],
    slot_ids: [],
    state: "in_progress",
  },
  context: null,
  created_at: "2026-11-01T00:00:00Z",
  description: "",
  done_criteria: "",
  headline: null,
  id,
  priority: null,
  reporter: "x",
  status: "in_progress",
  status_label: label,
  title,
  updated_at: "2026-11-01T00:00:00Z",
  urgency: null,
  version: 1,
});

// 未 3・済み 2・捨てた 1。課題 C-1・C-2 に属するものと、属さないもの。
const DEFAULT: Followup[] = [
  fu("F-1", { challenge_id: "C-1" }),
  fu("F-2", { challenge_id: "C-2" }),
  fu("F-3"),
  fu("F-4", { status: "done", challenge_id: "C-1", note: "確かめた: 4" }),
  fu("F-5", {
    status: "done",
    note: "確かめた: 5",
    promoted_challenge_id: "C-2",
  }),
  fu("F-6", { status: "dropped", reason: "理由 6", detail: "説明 6" }),
];

let requests: ReturnType<typeof installFakes>["requests"];

async function open(
  followups: Followup[] = DEFAULT,
  hash = "#/a/alpha/followups",
) {
  window.location.hash = hash;
  const f = installFakes({
    ...fixtures(),
    "/api/v1/workspaces/alpha/challenges": {
      challenges: [
        card("C-1", "課題の一", "着手中"),
        card("C-2", "課題の二", "完了"),
      ],
    },
    [URL_ALL]: { followups },
  });
  requests = f.requests;
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByRole("heading", { name: "フォローアップ" });
  await screen.findByRole("group", { name: "フォローアップの状態" });
}

const titles = () =>
  screen.queryAllByTestId("followup-row").map((r) => r.textContent ?? "");
const hasTitle = (id: string) => titles().some((t) => t.includes(`題名 ${id}`));

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => {
  vi.unstubAllGlobals();
});

it("AC-353 #812: 見出し「フォローアップ」が出る", async () => {
  await open();
  expect(
    screen.getByRole("heading", { level: 2, name: "フォローアップ" }),
  ).toBeInTheDocument();
});

it("AC-354 #812: 開くと、一覧の API が status=all を付けて 1 回だけ呼ばれる", async () => {
  await open();
  const calls = requests.filter(
    (r) => r.url.includes("/alpha/followups") && r.method === "GET",
  );
  expect(calls.map((r) => r.url)).toEqual([URL_ALL]);
  // 切り替えても読み直さない
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  expect(
    requests.filter((r) => r.url.includes("/alpha/followups")),
  ).toHaveLength(1);
});

it("AC-355 #812: 未 3・済み 2・捨てた 1 のとき、切り替えに「未（3）」「済み（2）」「捨てた（1）」が出る", async () => {
  await open();
  const g = screen.getByRole("group", { name: "フォローアップの状態" });
  expect(
    within(g)
      .getAllByRole("button")
      .map((b) => b.textContent),
  ).toEqual(["未（3）", "済み（2）", "捨てた（1）"]);
});

it("AC-356 #812: 開いた直後は、未のフォローアップだけが出る", async () => {
  await open();
  expect(["F-1", "F-2", "F-3"].every(hasTitle)).toBe(true);
  expect(["F-4", "F-5", "F-6"].some(hasTitle)).toBe(false);
});

it("AC-357 #812: 「済み」を押すと、済みのフォローアップだけが出る", async () => {
  await open();
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  expect(["F-4", "F-5"].every(hasTitle)).toBe(true);
  expect(["F-1", "F-2", "F-3", "F-6"].some(hasTitle)).toBe(false);
});

it("AC-358 #812: 「済み」を押すと、URL のハッシュの status が done になり、履歴は増えない", async () => {
  await open();
  const len = window.history.length;
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  expect(window.location.hash).toBe("#/a/alpha/followups?status=done");
  expect(window.history.length).toBe(len);
});

it("AC-359 #812: status が open・done・dropped のどれでもないとき、未が出る", async () => {
  await open(DEFAULT, "#/a/alpha/followups?status=bogus");
  expect(["F-1", "F-2", "F-3"].every(hasTitle)).toBe(true);
  expect(["F-4", "F-5", "F-6"].some(hasTitle)).toBe(false);
});

it("#812: ハッシュの status=dropped で捨てたが出る（開いた時点の URL を読む）", async () => {
  await open(DEFAULT, "#/a/alpha/followups?status=dropped");
  expect(hasTitle("F-6")).toBe(true);
});

it("AC-360 #812: 課題に属するフォローアップは、課題ごとにまとまって出る", async () => {
  await open([
    fu("F-1", { challenge_id: "C-1" }),
    fu("F-2", { challenge_id: "C-2" }),
    fu("F-3", { challenge_id: "C-1" }),
  ]);
  const groups = screen.getAllByTestId("followup-group");
  expect(groups).toHaveLength(2);
  const first = within(groups[0] as HTMLElement);
  expect(first.getAllByTestId("followup-row")).toHaveLength(2);
  expect(first.getByText(/題名 F-3/)).toBeInTheDocument();
  expect(
    within(groups[1] as HTMLElement).getByText(/題名 F-2/),
  ).toBeInTheDocument();
});

it("AC-361 #812: まとまりの見出しに、課題の題名（詳細へのリンク）と status_label が出る", async () => {
  await open();
  const g = screen.getAllByTestId("followup-group")[0] as HTMLElement;
  const link = within(g).getByRole("link", { name: "課題の一" });
  expect(link).toHaveAttribute("href", "#/a/alpha/challenges/C-1");
  expect(within(g).getByText("着手中")).toBeInTheDocument();
});

it("AC-362 #812: 課題に属さないものは、「課題に属さないもの」のまとまりで最後に出る", async () => {
  await open([fu("F-3"), fu("F-1", { challenge_id: "C-1" })]);
  const groups = screen.getAllByTestId("followup-group");
  expect(groups).toHaveLength(2);
  const last = groups[1] as HTMLElement;
  expect(
    within(last).getByRole("heading", { name: "課題に属さないもの" }),
  ).toBeInTheDocument();
  expect(within(last).getByText(/題名 F-3/)).toBeInTheDocument();
});

it("AC-363 #812: 行に題名が出る", async () => {
  await open([fu("F-1", { title: "固有の題名" })]);
  expect(screen.getByTestId("followup-row")).toHaveTextContent("固有の題名");
});

it("AC-364 #812: 行に出どころ（source）が出る", async () => {
  await open([fu("F-1", { source: "固有の出どころ" })]);
  expect(screen.getByTestId("followup-source")).toHaveTextContent(
    "固有の出どころ",
  );
});

it("AC-365 #812: 行にできた日時が出る", async () => {
  await open([fu("F-1", { created_at: "2026-11-10T01:00:00Z" })]);
  expect(screen.getByTestId("followup-created")).toHaveTextContent(
    new Date("2026-11-10T01:00:00Z").toLocaleString(),
  );
});

it("AC-366 #812: 説明（detail）がある行に説明が出て、無い行には出ない", async () => {
  await open([fu("F-1", { detail: "固有の説明" }), fu("F-2")]);
  const rows = screen.getAllByTestId("followup-row");
  expect(
    within(rows[0] as HTMLElement).getByTestId("followup-detail"),
  ).toHaveTextContent("固有の説明");
  expect(
    within(rows[1] as HTMLElement).queryByTestId("followup-detail"),
  ).toBeNull();
});

it("AC-367 #812: 済みの行に、確かめた結果（note）が出る", async () => {
  await open();
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  const notes = screen
    .getAllByTestId("followup-note")
    .map((n) => n.textContent);
  expect(notes).toEqual([
    "確かめた結果: 確かめた: 4",
    "確かめた結果: 確かめた: 5",
  ]);
});

it("AC-368 #812: 捨てたの行に、理由（reason）が出る", async () => {
  await open();
  fireEvent.click(screen.getByRole("button", { name: /捨てた/ }));
  expect(screen.getByTestId("followup-reason")).toHaveTextContent(
    "理由: 理由 6",
  );
});

it("AC-369 #812: promoted_challenge_id がある行に、上げた先の課題の題名が詳細へのリンクで出る", async () => {
  await open();
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  const p = screen.getByTestId("followup-promoted");
  expect(within(p).getByRole("link", { name: "課題の二" })).toHaveAttribute(
    "href",
    "#/a/alpha/challenges/C-2",
  );
});

it("AC-370 #812: ?challenge=<C> のとき、その課題のフォローアップだけが出る", async () => {
  await open(DEFAULT, "#/a/alpha/followups?challenge=C-1");
  expect(hasTitle("F-1")).toBe(true);
  expect(["F-2", "F-3"].some(hasTitle)).toBe(false);
});

it("AC-371 #812: 課題で絞っているとき、絞っている旨と、絞りを外すリンクが出る", async () => {
  await open(DEFAULT, "#/a/alpha/followups?challenge=C-1");
  expect(screen.getByTestId("followup-filter")).toHaveTextContent("課題の一");
  expect(screen.getByTestId("followup-unfilter")).toHaveAttribute(
    "href",
    "#/a/alpha/followups",
  );
});

it("AC-372 #812: 未が 0 件のとき、「未のフォローアップはありません。」が出る", async () => {
  await open([fu("F-4", { status: "done" })]);
  expect(
    screen.getByText("未のフォローアップはありません。"),
  ).toBeInTheDocument();
});

it("AC-373 #812: 切り替えを除くボタンと、チェックの欄・入力欄は無い", async () => {
  await open();
  for (const s of [/未/, /済み/, /捨てた/]) {
    fireEvent.click(screen.getByRole("button", { name: s }));
    const main = screen
      .getByRole("heading", { name: "フォローアップ" })
      .closest("section") as HTMLElement;
    const buttons = within(main).getAllByRole("button");
    expect(buttons.map((b) => b.textContent)).toEqual([
      "未（3）",
      "済み（2）",
      "捨てた（1）",
    ]);
    expect(
      main.querySelector('input, textarea, select, [role="checkbox"]'),
    ).toBeNull();
  }
});

it("AC-374 #812: 未の一覧の下に、flywheel followup done・drop・promote の案内が出る", async () => {
  await open();
  const guide = screen.getByTestId("followup-guide");
  const text = guide.textContent ?? "";
  expect(text).toContain("flywheel followup done");
  expect(text).toContain("--note");
  expect(text).toContain("flywheel followup drop");
  expect(text).toContain("--reason");
  expect(text).toContain("flywheel followup promote");
  expect(text).toContain("--workspace '/w/alpha'");
  // 一覧より後ろ
  const list = screen.getAllByTestId("followup-group").at(-1) as HTMLElement;
  expect(
    list.compareDocumentPosition(guide) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  // 済みでは出さない
  fireEvent.click(screen.getByRole("button", { name: /済み/ }));
  expect(screen.queryByTestId("followup-guide")).toBeNull();
});

// AC-375（案内の雛形のコマンドとフラグが flywheel に実在する）は Go のテスト internal/cli/ui_guidance_test.go が確かめる。
// AC-422（偽のデータが未・済み・捨てたを持つ）は web/src/mock/api.test.ts、AC-428・AC-434 は web/e2e/followups.spec.ts が確かめる。
