import type { Slot, StatusNeedsHuman } from "../api/types.gen.ts";
import type { Challenge } from "./types.ts";

// 帯の区分。表示の順もこの並び。
export const HUMAN_KINDS = [
  "approvals",
  "questions",
  "operations",
  "discrepancies",
  "budget",
  "triage",
  "slots",
] as const;
export type HumanKind = (typeof HUMAN_KINDS)[number];

export const HUMAN_KIND_LABELS: Record<HumanKind, string> = {
  approvals: "承認待ち",
  questions: "未回答の問い",
  operations: "不可逆操作の承認",
  discrepancies: "上流との食い違い",
  budget: "予算の使い切り",
  triage: "トリアージ",
  slots: "片づけ待ちのスロット",
};

export type HumanCounts = Record<HumanKind, number>;

const APPROVAL_STATES = [
  "awaiting_plan_approval",
  "awaiting_completion_approval",
];
const QUESTION_STATE = "awaiting_human";
const CLEAR_SLOT = "clear_slot";

function challengesIn(nh: StatusNeedsHuman, states: string[]): string[] {
  return nh.challenges
    .filter((c) => states.includes(c.status))
    .map((c) => c.id);
}

// 区分ごとの件数 = 区分に入る要素の数。
export function countsOf(nh: StatusNeedsHuman): HumanCounts {
  return {
    approvals: challengesIn(nh, APPROVAL_STATES).length,
    questions: challengesIn(nh, [QUESTION_STATE]).length,
    operations: nh.operations.length,
    discrepancies: nh.discrepancies.length,
    budget: nh.budget_exhausted.length,
    triage: nh.triage.length,
    slots: nh.slots.length,
  };
}

// 区分に当たる課題の ID。スロットは card.next_human_actions に clear_slot を持つ課題。
export function challengeIdsOf(
  nh: StatusNeedsHuman,
  kind: HumanKind,
  cards: Challenge[],
): Set<string> {
  switch (kind) {
    case "approvals":
      return new Set(challengesIn(nh, APPROVAL_STATES));
    case "questions":
      return new Set(challengesIn(nh, [QUESTION_STATE]));
    case "operations":
      return new Set(nh.operations.map((o) => o.challenge_id));
    case "discrepancies":
      return new Set(nh.discrepancies.map((d) => d.challenge_id));
    case "budget":
      return new Set(nh.budget_exhausted.map((b) => b.challenge_id));
    case "triage":
      return new Set(nh.triage.map((t) => t.challenge_id));
    case "slots":
      return new Set(
        cards
          .filter((c) => c.nextHumanActions.includes(CLEAR_SLOT))
          .map((c) => c.id),
      );
  }
}

// 区分で絞る。kind が null なら絞らない。
export function filterByKind(
  cards: Challenge[],
  nh: StatusNeedsHuman | undefined,
  kind: HumanKind | null,
): Challenge[] {
  if (kind === null) return cards;
  if (!nh) return [];
  const ids = challengeIdsOf(nh, kind, cards);
  return cards.filter((c) => ids.has(c.id));
}

// 課題の run に結ばれないスロット。needs_attention のスロットは run_id を持たず、スロットと課題の結び付きは API から導けない。
// clear_slot を持つ課題の数より多いスロットがあるときだけ、そのワークスペースのスロットを全件出す（結び付きは未判定）。
export function unboundSlots(nh: StatusNeedsHuman, cards: Challenge[]): Slot[] {
  const bound = cards.filter((c) =>
    c.nextHumanActions.includes(CLEAR_SLOT),
  ).length;
  return nh.slots.length > bound ? nh.slots : [];
}
