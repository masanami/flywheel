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
  discrepancies: "食い違い",
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

// 循環だけの食い違い（cycle_only。core が判定する）は帯に数えない（循環は status で見える）。
function realDiscrepancies(nh: StatusNeedsHuman) {
  return nh.discrepancies.filter((d) => d.cycle_only !== true);
}

// 食い違いのある課題の ID。同じ課題に要素が複数あっても 1 課題と数える（件数と絞り込みの単位）。
function discrepancyChallengeIds(nh: StatusNeedsHuman): Set<string> {
  return new Set(realDiscrepancies(nh).map((d) => d.challenge_id));
}

function challengesIn(nh: StatusNeedsHuman, states: string[]): string[] {
  return nh.challenges
    .filter((c) => states.includes(c.status))
    .map((c) => c.id);
}

// 区分ごとの件数。食い違いは課題の数、他は要素の数（絞り込みで出る課題の数と一致する）。
export function countsOf(nh: StatusNeedsHuman): HumanCounts {
  return {
    approvals: challengesIn(nh, APPROVAL_STATES).length,
    questions: challengesIn(nh, [QUESTION_STATE]).length,
    operations: nh.operations.length,
    discrepancies: discrepancyChallengeIds(nh).size,
    budget: nh.budget_exhausted.length,
    triage: nh.triage.length,
    slots: nh.slots.length,
  };
}

// 区分に当たる課題の ID。スロットは card.next_human_actions に clear_slot を持つ課題（スロットの結び付きは card.slot_ids）。
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
      return discrepancyChallengeIds(nh);
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

// 課題の run に結ばれないスロット。どのカードの card.slot_ids にも載らない needs_human.slots のスロット
// （スロットごとの直近の run の課題にだけ結ばれる）。
export function unboundSlots(nh: StatusNeedsHuman, cards: Challenge[]): Slot[] {
  const bound = new Set(cards.flatMap((c) => c.slotIds));
  return nh.slots.filter((s) => !bound.has(s.slot_id));
}
