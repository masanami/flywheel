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

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";

function summary(over: Partial<ApprovalSummary>): {
  approval: ApprovalSummary;
} {
  return {
    approval: {
      challenge_id: "C-1",
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
