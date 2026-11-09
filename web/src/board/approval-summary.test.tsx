import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { ApprovalSummary } from "../api/types.gen.ts";
import { installFakes } from "../testing/fakes.ts";
import { ApprovalPanel } from "./ApprovalPanel.tsx";
import { ACTUAL_MISSING_MARK, EXPECTED_MISSING_MARK } from "./stage-marks.ts";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// サーバが返す見出し（CLI の「--hold-release」と同じ文言。UI は本番反映を承認しない）
const HELD_HEADING =
  "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:";

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";

function summary(over: Partial<ApprovalSummary>): {
  approval: ApprovalSummary;
} {
  return {
    approval: {
      challenge_id: "C-1",
      conditions_after_release: [],
      conditions_after_release_heading: HELD_HEADING,
      direct_completion: null,
      done_criteria: "完了条件",
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
      title: "題名",
      version: 3,
      ...over,
    },
  };
}

async function show(s: { approval: ApprovalSummary }) {
  installFakes({ [APPROVAL]: s });
  render(
    <ApprovalPanel
      workspace="alpha"
      challengeId="C-1"
      kind={s.approval.kind as "plan" | "completion"}
      onApproved={() => {}}
    />,
  );
  return await screen.findByTestId("approval-summary");
}

it("計画の承認の要約に、論点と回答・予想・やらないことを示す（AC-386）", async () => {
  const el = await show(
    summary({
      stages: {
        actual: null,
        figure: null,
        issues: [
          {
            question: "どちらにする？",
            recommended: 2,
            options: [
              { label: "案A", example: null },
              { label: "案B", example: null },
            ],
          },
        ],
        issues_answer: "案Bで決定",
        expected: [
          { kind: "ui", scene: "画面1", before: "前", after: "予想後" },
        ],
        non_goals: ["やらない1"],
      },
    }),
  );
  const w = within(el);
  expect(w.getByText("どちらにする？")).toBeInTheDocument();
  expect(w.getByText("案Bで決定")).toBeInTheDocument();
  expect(w.getByText("予想後")).toBeInTheDocument();
  // 変更前と種類も出す（端末の要約と同じ）
  expect(w.getByText("前")).toBeInTheDocument();
  expect(w.getByText(/予想（ui・画面1）/)).toBeInTheDocument();
  expect(w.getByText("やらない1")).toBeInTheDocument();
  expect(w.queryByTestId("approval-pairs")).toBeNull();
});

it("段階の欄が全部 null の計画の要約は欄の見出しを出さない", async () => {
  const el = await show(summary({}));
  expect(within(el).queryByText("論点")).toBeNull();
  expect(within(el).queryByText("やらないこと")).toBeNull();
});

it("完了の承認の要約に、pairs の各要素を 1 行として予想と実物を並べ、片側だけの行に印を出す（AC-387・AC-388・AC-389）", async () => {
  const el = await show(
    summary({
      kind: "completion",
      plan: null,
      pairs: [
        {
          expected: { kind: "ui", scene: "s1", before: "b", after: "予想1" },
          actual: { scene: "s1", after: "実物1" },
        },
        {
          expected: { kind: "ui", scene: "s2", before: "b", after: "予想2" },
          actual: null,
        },
        { expected: null, actual: { scene: "s3", after: "実物3" } },
      ],
    }),
  );
  const rows = el.querySelectorAll("[data-pair]");
  expect(rows).toHaveLength(3);
  expect(rows[0]?.textContent).toContain("予想1");
  expect(rows[0]?.textContent).toContain("実物1");
  expect(rows[0]?.textContent).not.toContain(ACTUAL_MISSING_MARK);
  expect(rows[1]?.textContent).toContain("予想2");
  expect(rows[1]?.textContent).toContain(ACTUAL_MISSING_MARK);
  expect(rows[2]?.textContent).toContain("実物3");
  expect(rows[2]?.textContent).toContain(EXPECTED_MISSING_MARK);
  await waitFor(() => expect(screen.getByTestId("release-held")).toBeTruthy());
});

it("完了の承認の要約は stages の予想を位置で対にしない（pairs の結果だけを表示する）", async () => {
  const el = await show(
    summary({
      kind: "completion",
      plan: null,
      pairs: [],
      stages: {
        actual: [{ scene: "s", after: "実物だけ" }],
        expected: [{ kind: "ui", scene: "s", before: "b", after: "予想だけ" }],
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
      },
    }),
  );
  expect(el.querySelectorAll("[data-pair]")).toHaveLength(0);
  expect(el.textContent).not.toContain("実物だけ");
});

// 承認の画面は計画の本文（core の formatJ2PlanBody が組み立てる）を加工せずに示す。下の本文はその形を写した
// 手書きの値で、節の有無を決めるのは core（judgment_j2_noissues_test.go が固定）。ここでは画面が節と引用の文を
// 落とさず示すこと・画面の側で節を足さないことを固定する（#469）。
const PLAN_HEAD =
  "# 計画\n\n## 対象\n\n- リポジトリ: flywheel\n- 操作: impl\n- サイズ: M\n- 複数リポジトリにまたがる: いいえ\n\n## 予算\n\n- 実装枠: 30 USD\n- レビュー対応枠: 25 USD\n\n## 完了条件\n\n完了条件の文\n";
const NO_ISSUES_SECTION =
  "\n## 論点を不要とした根拠\n\n1. DECISION-ONE 方式は A に決まっている\n   - 引用元: 上流のコメント\n   - 引用:\n     > QUOTE-ONE A 方式で進める\n";

it("計画に論点を不要とした根拠の引用があれば、その節と引用の文を示す（#469）", async () => {
  const el = await show(
    summary({ plan: { body: PLAN_HEAD + NO_ISSUES_SECTION, version: 2 } }),
  );
  const plan = within(el).getByText(/## 論点を不要とした根拠/);
  expect(plan.tagName).toBe("PRE");
  expect(plan.textContent).toContain("## 論点を不要とした根拠");
  expect(plan.textContent).toContain("DECISION-ONE 方式は A に決まっている");
  expect(plan.textContent).toContain("引用元: 上流のコメント");
  expect(plan.textContent).toContain("> QUOTE-ONE A 方式で進める");
});

it("計画に論点を不要とした根拠の引用が無ければ、その節を出さない（#469）", async () => {
  const el = await show(summary({ plan: { body: PLAN_HEAD, version: 2 } }));
  expect(within(el).getByText(/## 完了条件/).tagName).toBe("PRE");
  expect(el.textContent).not.toContain("論点を不要とした根拠");
  expect(el.textContent).not.toContain("QUOTE-ONE");
});

it("親が直接行った完了の承認の要約: 完了条件の次に成果物の参照をリンクで示し、完了の検証を通っていないことを添える", async () => {
  const el = await show(
    summary({
      kind: "completion",
      plan: null,
      done_criteria: "結果が Issue に書かれている",
      direct_completion: {
        artifacts: [
          "https://github.com/o/r/issues/337#issuecomment-1",
          "o/r#12",
          "javascript:alert(1)",
        ],
        created_at: "2026-11-05T01:02:03.004Z",
        note: "案 B を動かして確かめた",
      },
    }),
  );
  const block = within(el).getByTestId("direct-completion");
  const link = within(block).getByRole("link", {
    name: "https://github.com/o/r/issues/337#issuecomment-1",
  });
  expect(link).toHaveAttribute(
    "href",
    "https://github.com/o/r/issues/337#issuecomment-1",
  );
  expect(link).toHaveAttribute("target", "_blank");
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  // URL でない参照と javascript: はリンクにしない
  expect(within(block).getAllByRole("link")).toHaveLength(1);
  expect(within(block).getByText("o/r#12")).toBeInTheDocument();
  expect(within(block).getByText("javascript:alert(1)")).toBeInTheDocument();
  expect(block.textContent).toContain("案 B を動かして確かめた");
  expect(block.textContent).toContain("完了の検証");
  // 完了条件の後ろに並ぶ
  const criteria = within(el).getByText("結果が Issue に書かれている");
  expect(
    criteria.compareDocumentPosition(block) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(within(el).getByRole("heading", { name: "完了条件" })).toBeTruthy();
});

it("通常の完了の承認の要約には、親が直接行った成果物の欄を出さない", async () => {
  const el = await show(summary({ kind: "completion", plan: null }));
  expect(within(el).queryByTestId("direct-completion")).toBeNull();
});

it("完了の承認の要約に、本番反映で満たされる見込みの条件を CLI と同じ見出しの一覧で示す（#466）", async () => {
  const el = await show(
    summary({
      kind: "completion",
      plan: null,
      conditions_after_release: ["本番で画面に出る", "通知が届く"],
    }),
  );
  const box = within(el).getByTestId("approval-conditions-after-release");
  expect(within(box).getByText(HELD_HEADING)).toBeTruthy();
  const items = within(box)
    .getAllByRole("listitem")
    .map((li) => li.textContent);
  expect(items).toEqual(["本番で画面に出る", "通知が届く"]);
});

it("本番反映で満たされる見込みの条件が無ければ、見出しごと出さない（#466）", async () => {
  const el = await show(summary({ kind: "completion", plan: null }));
  expect(
    within(el).queryByTestId("approval-conditions-after-release"),
  ).toBeNull();
  expect(within(el).queryByText(HELD_HEADING)).toBeNull();
});
