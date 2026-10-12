import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../api/client.ts";
import type { ApiClient } from "../api/client.ts";
import type { LiveState } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { ApproveBatch } from "./ApproveBatch.tsx";
import { CreateForm } from "./CreateForm.tsx";
import { DecisionAction } from "./DecisionAction.tsx";
import { EditForm } from "./EditForm.tsx";
import { NeedsHumanStrip, countsOf } from "./NeedsHumanStrip.tsx";
import {
  ProposalCard,
  ProposalDetailFields,
  pointsOf,
} from "./ProposalCard.tsx";

let store: MockStore;
let api: ApiClient;
beforeEach(() => {
  store = new MockStore();
  api = createMockApi(store, { latencyMs: 0 });
  setApi(api);
});
afterEach(() => resetApi());

const slow = { timeout: 3000 };

it("起票フォーム: 空のタイトルはエラー、入力すると起票できる", async () => {
  render(<CreateForm workspace="alpha" />);
  fireEvent.click(screen.getByRole("button", { name: "起票する" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "validation_failed",
  );
  fireEvent.change(screen.getByLabelText(/タイトル/), {
    target: { value: "新規" },
  });
  fireEvent.click(screen.getByRole("button", { name: "起票する" }));
  expect(await screen.findByText(/を起票しました/)).toBeInTheDocument();
  expect((await api.challenges("alpha")).challenges.at(-1)?.title).toBe("新規");
});

it("編集フォーム: 版が先に進んでいたら conflict の表示を出し、自動では上書きしない", async () => {
  const base = (await api.challenge("alpha", "C-2")).challenge;
  render(<EditForm workspace="alpha" base={base} />);
  const c2 = store.workspaces.alpha?.items.find((i) => i.c.id === "C-2");
  if (c2) {
    c2.c.version += 1;
    c2.c.title = "他の人が直したタイトル";
  }
  fireEvent.change(screen.getByLabelText("タイトル"), {
    target: { value: "自分が直したタイトル" },
  });
  fireEvent.click(screen.getByRole("button", { name: "保存する" }));
  const panel = await screen.findByTestId("conflict-panel");
  expect(within(panel).getByText("他の人が直したタイトル")).toBeInTheDocument();
  expect(within(panel).getByText("自分が直したタイトル")).toBeInTheDocument();
  expect((await api.challenge("alpha", "C-2")).challenge.title).toBe(
    "他の人が直したタイトル",
  );
  // 入力を残して最新の版で保存し直せる
  fireEvent.click(
    screen.getByRole("button", { name: "入力を残して最新の版で保存し直す" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "保存する" }));
  expect(await screen.findByText(/保存しました/)).toBeInTheDocument();
});

it("編集フォーム: 優先度を変えると状態は変わらず、未分類では変えられない", async () => {
  const base = (await api.challenge("alpha", "C-2")).challenge;
  const { unmount } = render(<EditForm workspace="alpha" base={base} />);
  fireEvent.change(screen.getByLabelText("優先度"), {
    target: { value: "P0" },
  });
  fireEvent.click(screen.getByRole("button", { name: "優先度を変更する" }));
  expect(await screen.findByText(/優先度を P0 にしました/)).toBeInTheDocument();
  unmount();
  const c1 = (await api.challenge("alpha", "C-1")).challenge;
  render(<EditForm workspace="alpha" base={c1} />);
  expect(screen.queryByRole("button", { name: "優先度を変更する" })).toBeNull();
});

async function batchProps() {
  const cards = (await api.challenges("alpha")).challenges;
  const nh = (await api.needsHuman()).workspaces[0]?.needs_human;
  return { cards, operations: nh?.operations ?? [] };
}

it("まとめて承認: 選んだ集合を確認ダイアログで示し、パスキーの確認後に承認する", async () => {
  const props = await batchProps();
  render(<ApproveBatch workspace="alpha" {...props} />);
  fireEvent.click(screen.getByRole("checkbox", { name: /^C-3\b/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /^C-7\b/ }));
  // C-7 の完了の承認を選ぶと、その release は独立に選べない
  expect(screen.getByRole("checkbox", { name: /^O-1\b/ })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: /2 件をまとめて承認/ }));
  const dialog = await screen.findByTestId("passkey-dialog");
  expect(within(dialog).getByText("同時に承認される: O-1")).toBeInTheDocument();
  expect(within(dialog).getAllByRole("row")).toHaveLength(3);
  fireEvent.click(
    within(dialog).getByRole("button", { name: /パスキーで確認して承認/ }),
  );
  expect(
    await within(dialog).findByText(/パスキーを確認しています/),
  ).toBeTruthy();
  await waitFor(
    () => expect(screen.getByText(/2 件を承認しました/)).toBeInTheDocument(),
    slow,
  );
  expect((await api.challenge("alpha", "C-7")).challenge.status).toBe("done");
});

it("まとめて承認: 版が変わっていたら何も承認せず、読み直して再確認を求める", async () => {
  const props = await batchProps();
  render(<ApproveBatch workspace="alpha" {...props} />);
  fireEvent.click(screen.getByRole("checkbox", { name: /^C-3\b/ }));
  fireEvent.click(screen.getByRole("button", { name: /1 件をまとめて承認/ }));
  const dialog = await screen.findByTestId("passkey-dialog");
  const c3 = store.workspaces.alpha?.items.find((i) => i.c.id === "C-3");
  if (c3) c3.c.version += 1;
  fireEvent.click(
    within(dialog).getByRole("button", { name: /パスキーで確認して承認/ }),
  );
  await waitFor(
    () =>
      expect(within(dialog).getByRole("alert")).toHaveTextContent("conflict"),
    slow,
  );
  expect(
    within(dialog).getByText(`v${c3?.c.version ?? 0}`),
  ).toBeInTheDocument();
  expect((await api.challenge("alpha", "C-3")).challenge.status).toBe(
    "awaiting_plan_approval",
  );
});

it("回答: パスキー確認ダイアログで要約を示してから実行する。やめれば何も起きない", async () => {
  let done: string | null = null;
  render(
    <DecisionAction
      workspace="alpha"
      label="回答する…"
      request={{ action: "answer", challenge_id: "C-8", answer: "含めない" }}
      onDone={(m) => {
        done = m;
      }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "回答する…" }));
  let dialog = await screen.findByTestId("passkey-dialog");
  expect(within(dialog).getByText("回答: 含めない")).toBeInTheDocument();
  fireEvent.click(within(dialog).getByRole("button", { name: "やめる" }));
  await waitFor(() =>
    expect(screen.queryByTestId("passkey-dialog")).toBeNull(),
  );
  expect((await api.challenge("alpha", "C-8")).challenge.status).toBe(
    "awaiting_human",
  );
  fireEvent.click(screen.getByRole("button", { name: "回答する…" }));
  dialog = await screen.findByTestId("passkey-dialog");
  fireEvent.click(
    within(dialog).getByRole("button", { name: /パスキーで確認して実行/ }),
  );
  await waitFor(() => expect(done).toMatch(/保留への回答を行いました/), slow);
});

it("S4 のカード: 面には一言で（無ければタイトル）と識別子だけを出し、全体のクリックで開く。長い値は切り詰め、外部の文字列は HTML にしない", async () => {
  const cards = (await api.challenges("alpha")).challenges;
  const c10 = cards.find((c) => c.id === "C-10");
  if (!c10) throw new Error("C-10 が無い");
  let opened = 0;
  const { container } = render(
    <ProposalCard
      card={c10}
      onOpen={() => {
        opened += 1;
      }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /外部 Issue/ }));
  expect(opened).toBe(1);
  expect(container.querySelector("img")).toBeNull();
  expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  // 識別子は残り、説明的な欄は面に出さない
  expect(screen.getByText("C-10")).toBeInTheDocument();
  expect(container.querySelector("dl")).toBeNull();
  for (const t of [
    "位置づけ",
    "論点",
    "完了したら見えるもの",
    "やらないこと",
    "図",
  ])
    expect(container.textContent).not.toContain(t);
  // 長い値は切り詰めた表示から始まる
  expect(container.querySelector('[data-clamped="true"]')).not.toBeNull();
});

it("S4 のカード: 一言で があれば面にはそれだけを出し、タイトルは面に出さない", async () => {
  const cards = (await api.challenges("alpha")).challenges;
  const c3 = cards.find((c) => c.id === "C-3");
  if (!c3) throw new Error("C-3 が無い");
  const { container } = render(<ProposalCard card={c3} onOpen={() => {}} />);
  expect(screen.getByText("通知メールの二重送信を止める")).toBeInTheDocument();
  expect(container.textContent).not.toContain(c3.title);
});

it("S4 の詳細: 論点・完了したら見えるもの（予想と実物）・やらないこと・図を詳細側に出す。実物が無ければ完了後に入ると示す", async () => {
  const cards = (await api.challenges("alpha")).challenges;
  const c3 = cards.find((c) => c.id === "C-3");
  if (!c3) throw new Error("C-3 が無い");
  render(
    <ProposalDetailFields
      card={c3}
      extra={await api.cardExtra("alpha", "C-3")}
      points={pointsOf(c3, undefined, undefined)}
    />,
  );
  for (const t of [
    "論点（次に人間がすること）",
    "完了したら見えるもの",
    "やらないこと",
    "図",
  ])
    expect(screen.getByText(t)).toBeInTheDocument();
  expect(screen.getByText("（完了後に入る）")).toBeInTheDocument();
  expect(
    screen.getByText(
      "同じ通知を 2 回送っても受信箱に届くのは 1 通だけになる。",
    ),
  ).toBeInTheDocument();
  expect(screen.getByText("計画を承認する")).toBeInTheDocument();
});

it("S4 の詳細: 位置づけが未記入なら、その旨を詳細に示す", async () => {
  const cards = (await api.challenges("alpha")).challenges;
  const c = cards.find((x) => x.card.context === null);
  if (!c) throw new Error("位置づけの無いカードが無い");
  render(<ProposalDetailFields card={c} extra={null} points={[]} />);
  expect(screen.getByText(/未記入。J1 が書くか/)).toBeInTheDocument();
});

it("人間の操作待ちの帯: 承認以外の区分も数える", async () => {
  const nh = await api.needsHuman();
  const alpha = nh.workspaces.find((w) => w.name === "alpha")?.needs_human;
  if (!alpha) throw new Error("alpha が無い");
  expect(countsOf(alpha)).toMatchObject({
    approvals: 3,
    questions: 1,
    operations: 1,
    discrepancies: 1,
    budget: 1,
    triage: 1,
  });
  const live = { needsHuman: nh } as unknown as LiveState;
  render(<NeedsHumanStrip live={live} />);
  expect(screen.getByText("予算切れ 1")).toBeInTheDocument();
  expect(screen.getByText(/人間の操作待ち \d+ 件/)).toBeInTheDocument();
});
