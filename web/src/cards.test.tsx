import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import {
  CARD_ACTION_CODES,
  CARD_MODIFIER_CODES,
  DISCREPANCY_KIND_CODES,
  STATUS_CODES,
} from "./api/types.gen.ts";
import { ChallengeCardView } from "./cards.tsx";
import {
  ACTION_LABELS,
  CARD_ACTIONS,
  CARD_MODIFIERS,
  CARD_STATES,
  DISCREPANCY_KINDS,
  DISCREPANCY_LABELS,
  MODIFIER_LABELS,
  NO_ACTION_LABEL,
  NO_PRIORITY_LABEL,
  STATE_LABELS,
} from "./labels.ts";

it("AC-102: 閉集合のすべてのコードに文言があり、core の定義と一致する", () => {
  expect([...CARD_ACTIONS]).toEqual([...CARD_ACTION_CODES]);
  expect([...CARD_MODIFIERS]).toEqual([...CARD_MODIFIER_CODES]);
  expect([...CARD_STATES]).toEqual([...STATUS_CODES]);
  expect([...DISCREPANCY_KINDS]).toEqual([...DISCREPANCY_KIND_CODES]);
  for (const [codes, table] of [
    [CARD_STATES, STATE_LABELS],
    [CARD_MODIFIERS, MODIFIER_LABELS],
    [CARD_ACTIONS, ACTION_LABELS],
    [DISCREPANCY_KINDS, DISCREPANCY_LABELS],
  ] as const) {
    for (const c of codes) {
      expect((table as Record<string, string>)[c], c).toBeTruthy();
    }
  }
});

const base = {
  id: "C-1",
  title: "題名",
  priority: null,
  status: "classified",
  status_label: "分類済",
};
const card = (o: Partial<Parameters<typeof ChallengeCardView>[0]["card"]>) => ({
  state: "classified",
  modifiers: [],
  next_human_actions: [],
  headline: null,
  context: null,
  ...o,
});

it("AC-103/101: 操作待ちが無ければ「なし（エージェントが進める）」、優先度未設定は「優先度なし」", () => {
  render(
    <ul>
      <ChallengeCardView challenge={base} card={card({})} />
    </ul>,
  );
  expect(
    screen.getByText(new RegExp(NO_ACTION_LABEL.replace(/[（）]/g, "."))),
  ).toBeInTheDocument();
  expect(screen.getByText(NO_PRIORITY_LABEL)).toBeInTheDocument();
  expect(screen.queryByText(/人間の操作待ち/)).toBeNull();
});

it("操作待ちのカードは ID・優先度・待ちの印・状態・次の操作を出す", () => {
  render(
    <ul>
      <ChallengeCardView
        challenge={{
          ...base,
          priority: "P1",
          status: "awaiting_plan_approval",
        }}
        card={card({
          state: "awaiting_plan_approval",
          next_human_actions: ["approve_plan"],
        })}
      />
    </ul>,
  );
  expect(screen.getByText("C-1")).toBeInTheDocument();
  expect(screen.getByText("P1")).toBeInTheDocument();
  expect(screen.getByText(/人間の操作待ち/)).toBeInTheDocument();
  expect(screen.getByText(/計画承認待ち/)).toBeInTheDocument();
  expect(screen.getByText(/計画を承認する/)).toBeInTheDocument();
});

it("AC-120/121: unresponsive は running だけの課題と区別できる", () => {
  const { container, rerender } = render(
    <ul>
      <ChallengeCardView
        challenge={base}
        card={card({ state: "in_progress", modifiers: ["running"] })}
      />
    </ul>,
  );
  expect(container.querySelector('[data-modifier="unresponsive"]')).toBeNull();
  expect(screen.getByText(/実行中/)).toBeInTheDocument();
  rerender(
    <ul>
      <ChallengeCardView
        challenge={base}
        card={card({
          state: "in_progress",
          modifiers: ["running", "unresponsive"],
        })}
      />
    </ul>,
  );
  expect(
    container.querySelector('[data-modifier="unresponsive"]'),
  ).toHaveTextContent("応答なし");
});

it("M4P10: タイトルは HTML として解釈されず、長い値は切り詰めて操作で全文を開ける", () => {
  const long = `<img src=x onerror=alert(1)> ${"あ".repeat(300)}`;
  const { container } = render(
    <ul>
      <ChallengeCardView challenge={{ ...base, title: long }} card={card({})} />
    </ul>,
  );
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector('[data-clamped="true"]')).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "全文を開く" }));
  expect(container.querySelector('[data-clamped="true"]')).toBeNull();
  expect(container.textContent).toContain("<img src=x");
});
