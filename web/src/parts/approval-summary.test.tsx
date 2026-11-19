import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  type ApprovalSummary,
  CONDITIONS_AFTER_RELEASE_HEADINGS,
} from "../api/types.gen.ts";
import { installFakes } from "../testing/fakes.ts";
import { ApprovalPanel } from "./ApprovalPanel.tsx";
import { ACTUAL_MISSING_MARK, EXPECTED_MISSING_MARK } from "./stage-marks.ts";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// サーバが返す見出し（正本は Go。生成物から引く。UI は本番反映を承認しない側）
const [HELD_HEADING] = CONDITIONS_AFTER_RELEASE_HEADINGS;

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
      kind: "completion",
      mergeability_note: "",
      other_pending_operations: [],
      pairs: [],
      plan: null,
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
    <ApprovalPanel workspace="alpha" challengeId="C-1" onApproved={() => {}} />,
  );
  return await screen.findByTestId("approval-summary");
}

it("完了の承認の要約に、pairs の各要素を 1 行として予想と実物を並べ、片側だけの行に印を出す（AC-387・AC-388・AC-389）", async () => {
  const el = await show(
    summary({
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

it("親が直接行った完了の承認の要約: 完了条件の次に成果物の参照をリンクで示し、完了の検証を通っていないことを添える", async () => {
  const el = await show(
    summary({
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
  const el = await show(summary({}));
  expect(within(el).queryByTestId("direct-completion")).toBeNull();
});

it("完了の承認の要約に、本番反映で満たされる見込みの条件を CLI と同じ見出しの一覧で示す（#466）", async () => {
  const el = await show(
    summary({
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
  const el = await show(summary({}));
  expect(
    within(el).queryByTestId("approval-conditions-after-release"),
  ).toBeNull();
  expect(within(el).queryByText(HELD_HEADING)).toBeNull();
});

it("完了の承認の要約に、マージ可否が未確認であることを CLI と同じ文で示す（#629）", async () => {
  const el = await show(
    summary({
      mergeability_note: "マージ可否は未確認",
    }),
  );
  const box = within(el).getByTestId("approval-mergeability");
  expect(box.textContent).toBe("反映の可否: マージ可否は未確認");
});

it("マージ可否の注記が無ければ、その行を出さない（#629）", async () => {
  const el = await show(summary({}));
  expect(within(el).queryByTestId("approval-mergeability")).toBeNull();
});

// AC-459 #814: 完了の承認の要約の枠組みの文言（見出しと注記の前置き）に、開発に固有の語が無い。
it("AC-459 #814: 完了の承認の要約の見出しと「反映の可否」の前置きに、PR・CI・ブランチ・マージ・Issue の語が無い", async () => {
  const el = await show(summary({ mergeability_note: "確認できません" }));
  const frame = [
    ...[...el.querySelectorAll("h4")].map((h) => h.textContent ?? ""),
    within(el).getByTestId("approval-mergeability").textContent ?? "",
  ].join("\n");
  expect(frame).not.toMatch(/(\bPR\b|\bCI\b|\bIssue\b|ブランチ|マージ)/);
});
