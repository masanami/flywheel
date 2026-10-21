import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

const cc = (
  id: string,
  state: string,
  card: Record<string, unknown> = {},
  o: Record<string, unknown> = {},
) => ({
  created_at: "",
  description: `説明${id}`,
  done_criteria: `完了条件${id}`,
  id,
  priority: null,
  reporter: "",
  status: state,
  status_label: "",
  title: `題名${id}`,
  updated_at: "",
  urgency: null,
  version: 1,
  card: {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: [],
    state,
    ...card,
  },
  ...o,
});

function bodies() {
  const f = fixtures();
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [
      cc("C-1", "classified"),
      cc("C-2", "awaiting_plan_approval", {
        next_human_actions: ["approve_plan"],
      }),
      cc("C-3", "in_progress", { modifiers: ["running", "unresponsive"] }),
      cc("C-7", "in_progress", { next_human_actions: ["answer_question"] }),
      cc("C-4", "done"),
      cc(
        "C-5",
        "in_progress",
        { headline: "一言の要約", context: "位置づけの文" },
        { priority: "P0" },
      ),
      cc("C-6", "in_progress", { modifiers: ["running"] }),
    ],
  };
  f["/api/v1/workspaces/beta/challenges"] = {
    challenges: [cc("C-9", "classified")],
  };
  f["/api/v1/workspaces/alpha/challenges/C-2"] = {
    approvals: [],
    card: cc("C-2", "awaiting_plan_approval").card,
    challenge: cc("C-2", "awaiting_plan_approval"),
    holds: [],
    operations: [],
    plans: [{ body: "計画の本文", created_at: "", spec: null, version: 1 }],
    runs: [],
    pairs: [],
    source_binding: null,
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: ["やらないこと1"],
    },
  };
  f["/api/v1/workspaces/alpha/log?challenge=C-2"] = { activities: [] };
  f["/api/v1/workspaces/alpha/challenges/C-2/approval"] = {
    approval: {
      challenge_id: "C-2",
      done_criteria: "完了条件C-2",
      kind: "plan",
      other_pending_operations: [],
      pairs: [],
      plan: { body: "計画の本文", version: 1 },
      release_held: false,
      releases: [],
      stages: {
        actual: null,
        expected: null,
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
      },
      title: "題名C-2",
      version: 1,
    },
  };
  return f;
}

async function mount() {
  const { requests } = installFakes(bodies());
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText("題名C-1");
  return { requests };
}

it("既定の画面はボード: ワークスペースがエージェントの列として並び、課題がカードで出る", async () => {
  await mount();
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  expect(screen.getByRole("region", { name: "beta の列" })).toBeInTheDocument();
  expect(within(alpha).getByText("題名C-1")).toBeInTheDocument();
  expect(within(alpha).getByText("一言の要約")).toBeInTheDocument();
  expect(within(alpha).getByText("P0")).toBeInTheDocument();
  // 完了は既定で隠れ、「完了を表示」で出る
  expect(within(alpha).queryByText("題名C-4")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "完了を表示" }));
  expect(within(alpha).getByText("題名C-4")).toBeInTheDocument();
});

it("人間の操作待ちは「🔔 人間の操作待ち」の見出しの下に先頭で出て、差し戻しの端末のコマンドは詳細に出す", async () => {
  await mount();
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  expect(within(alpha).getByText("🔔 人間の操作待ち")).toBeInTheDocument();
  const card = within(alpha).getByTestId("card-C-2");
  expect(card).toHaveAttribute("data-needs-human", "true");
  // 面にはコマンドを出さない（M4H23）
  expect(card.textContent).not.toContain("flywheel ");
  fireEvent.click(within(card).getByRole("button"));
  const modal = await screen.findByTestId("card-detail-content");
  expect(
    within(modal).getByText(
      /flywheel reject C-2 --workspace '\/w\/alpha' --reason <理由>/,
    ),
  ).toBeInTheDocument();
  // 承認のボタンは詳細にだけある（面には無い）
  expect(within(card).queryByRole("button", { name: /承認する/ })).toBeNull();
  expect(
    await within(modal).findByRole("button", {
      name: "計画を承認する",
      hidden: true,
    }),
  ).toBeInTheDocument();
});

it("実行中の課題は「⚡ 実行中」に出て、更新なしは区別される", async () => {
  await mount();
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  expect(within(alpha).getByText("⚡ 実行中")).toBeInTheDocument();
  const row = within(alpha).getByTestId("running-run-C-3");
  expect(row).toHaveAttribute("data-stale", "true");
  expect(row).toHaveTextContent("更新なし");
});

it("AC-120: running だけを持つ課題も、そのワークスペースの列で実行中の印が出る（更新なしではない）", async () => {
  await mount();
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  const row = within(alpha).getByTestId("running-run-C-6");
  expect(row).toHaveTextContent("題名C-6");
  expect(row).not.toHaveAttribute("data-stale", "true");
  expect(row).not.toHaveTextContent("更新なし");
  // 実行中の印は running を持つ課題だけ（C-1 などには出ない）
  expect(within(alpha).queryByTestId("running-run-C-1")).toBeNull();
  // 他のワークスペースの列には出ない
  const beta = screen.getByRole("region", { name: "beta の列" });
  expect(within(beta).queryByText("⚡ 実行中")).toBeNull();
});

it("「🔔 人間の操作待ち」で絞り込むと、人間の操作待ちだけが残る", async () => {
  await mount();
  fireEvent.click(screen.getByRole("button", { name: "🔔 人間の操作待ち" }));
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  expect(within(alpha).getByText("題名C-2")).toBeInTheDocument();
  // 承認以外の操作待ち（answer_question）も集まる（AC-214b）
  expect(within(alpha).getByText("題名C-7")).toBeInTheDocument();
  expect(within(alpha).queryByText("題名C-1")).toBeNull();
  expect(within(alpha).queryByText("⚡ 実行中")).toBeNull();
});

it("API に対応する機能が無い操作（アーカイブ表示・エージェント追加）は押せない", async () => {
  const { requests } = await mount();
  const before = requests.length;
  for (const name of ["🗄 アーカイブ表示", "＋ エージェント追加"]) {
    const b = screen.getByRole("button", { name });
    expect(b).toBeDisabled();
    fireEvent.click(b);
  }
  expect(requests.length).toBe(before);
  expect(document.querySelector("form")).toBeNull();
});

it("カードを開くと詳細のモーダルに、API から読んだ計画と承認方法の案内が出る", async () => {
  const { requests } = await mount();
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  fireEvent.click(within(alpha).getByText("題名C-2"));
  const modal = await screen.findByTestId("card-detail-content");
  await within(modal).findAllByText("計画の本文");
  expect(within(modal).getAllByText("完了条件C-2")[0]).toBeInTheDocument();
  expect(within(modal).getByText("説明C-2")).toBeInTheDocument();
  expect(within(modal).getByText("やらないこと")).toBeInTheDocument();
  expect(within(modal).getByText("やらないこと1")).toBeInTheDocument();
  expect(within(modal).getByLabelText("承認方法の案内")).toBeInTheDocument();
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  fireEvent.click(within(modal).getByText("閉じる"));
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
});

it("ok でないワークスペースの列には、状態とエラーが出る", async () => {
  const f = bodies();
  f["/api/v1/workspaces"] = {
    workspaces: [
      {
        error: "壊れている",
        name: "alpha",
        path: "/w/alpha",
        state: "store_error",
      },
    ],
  };
  installFakes(f);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const alpha = await screen.findByRole("region", { name: "alpha の列" });
  expect(alpha).toHaveTextContent("store_error");
  expect(alpha).toHaveTextContent("壊れている");
});

it("ok でなくなったワークスペースの列は、前に読めた課題を残さない", async () => {
  const f = bodies();
  f["/api/v1/workspaces"] = {
    workspaces: [
      {
        error: "壊れている",
        name: "alpha",
        path: "/w/alpha",
        state: "store_error",
      },
    ],
  };
  installFakes(f);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const alpha = await screen.findByRole("region", { name: "alpha の列" });
  expect(within(alpha).queryByText("題名C-1")).toBeNull();
});

it("モーダルは <dialog> の showModal で開き、close イベントで閉じる（実ブラウザの経路）", async () => {
  const proto = HTMLDialogElement.prototype as unknown as Record<
    string,
    unknown
  >;
  const orig = { showModal: proto.showModal, close: proto.close };
  proto.showModal = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  proto.close = function (this: HTMLDialogElement) {
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
  try {
    await mount();
    const alpha = screen.getByRole("region", { name: "alpha の列" });
    fireEvent.click(within(alpha).getByText("題名C-2"));
    const modal = await screen.findByTestId("card-detail-content");
    expect(modal.closest("dialog")).toHaveAttribute("open");
    fireEvent.click(within(modal).getByText("閉じる"));
    expect(screen.queryByTestId("card-detail-content")).toBeNull();
  } finally {
    proto.showModal = orig.showModal;
    proto.close = orig.close;
  }
});
