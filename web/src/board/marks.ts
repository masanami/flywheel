import type { ChallengeCard, StatusResponse } from "../api/types.gen.ts";
import type { MarkKind } from "../labels.ts";

// 区分の印の導出に使う、課題の ID の集合（status から作る）。
export type MarkContext = {
  actionable: ReadonlySet<string>;
  approvedOperations: ReadonlySet<string>;
};

export const EMPTY_MARK_CONTEXT: MarkContext = {
  actionable: new Set(),
  approvedOperations: new Set(),
};

export function markContext(status: StatusResponse | undefined): MarkContext {
  if (!status) return EMPTY_MARK_CONTEXT;
  return {
    actionable: new Set(status.actionable.challenges.map((c) => c.id)),
    approvedOperations: new Set(
      status.approved.operations.map((o) => o.challenge_id),
    ),
  };
}

// API に区分を足さず、既存の値から 6 種の印を導く（M4H23）。
export function marksFor(c: ChallengeCard, ctx: MarkContext): MarkKind[] {
  const m = c.card.modifiers;
  const marks: MarkKind[] = [];
  if (c.card.next_human_actions.length > 0) marks.push("needs_human");
  if (m.includes("waiting_ci")) marks.push("external");
  if (ctx.actionable.has(c.id)) marks.push("actionable");
  if (m.includes("running") && !m.includes("unresponsive"))
    marks.push("running");
  if (m.includes("unresponsive")) marks.push("unresponsive");
  if (ctx.approvedOperations.has(c.id)) marks.push("approved_operation");
  return marks;
}

const HEADLINE_LIMIT = 60;

// 面の見出し。60 文字を超えたら先頭 60 文字と「…」、60 文字ちょうどまでは全文（60 は仮定の値）。
export function faceHeadline(text: string): string {
  const chars = Array.from(text);
  return chars.length > HEADLINE_LIMIT
    ? `${chars.slice(0, HEADLINE_LIMIT).join("")}…`
    : text;
}
