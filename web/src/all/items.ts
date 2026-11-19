import type { Question, StatusNeedsHuman } from "../api/types.gen.ts";
import { hrefOf } from "../shell/route.ts";
import type { WaitingKind } from "../shell/waiting.ts";

export interface WaitingItem {
  kind: WaitingKind;
  title: string;
  // 行き先。スロットの片付けは行き先が無い。
  href: string | null;
  // 種類の下に添える文（スロットの片付けの案内）。
  note?: string;
}

// あなたを待っているものを、1 件ごとの種類・題名・行き先にする。件数の数え方は waitingCounts と同じ
// （食い違いは課題ごとに 1 件で、循環だけの要素は数えない）。題名は課題の一覧から引き、無ければ課題の ID。
export function waitingItems(
  agentName: string,
  nh: StatusNeedsHuman,
  titleOf: (challengeId: string) => string | undefined,
  questions: readonly Question[] = [],
): WaitingItem[] {
  const challenge = (id: string) =>
    hrefOf({ kind: "challenge", name: agentName, id, view: "tree" });
  const title = (id: string) => titleOf(id) ?? id;
  const out: WaitingItem[] = [];
  for (const c of nh.challenges) {
    if (c.status === "awaiting_plan_approval") {
      out.push({
        kind: "plan_approval",
        title: c.title,
        href: hrefOf({ kind: "approve_plan", name: agentName, id: c.id }),
      });
    } else if (c.status === "awaiting_completion_approval") {
      out.push({
        kind: "completion_approval",
        title: c.title,
        href: challenge(c.id),
      });
    } else if (c.status === "awaiting_human") {
      // 回答の行は問いの文を出し、保留がタスクに付いていればタスクの詳細へ。問いが一覧に無ければ課題の題名。
      const q = questions.find(
        (x) => x.workspace === agentName && x.challenge_id === c.id,
      );
      out.push({
        kind: "answer",
        title: q ? q.question : c.title,
        href:
          q?.task_id != null
            ? hrefOf({
                kind: "task",
                name: agentName,
                id: c.id,
                taskId: q.task_id,
              })
            : challenge(c.id),
      });
    }
  }
  for (const o of nh.operations) {
    out.push({
      kind: "operation",
      title: title(o.challenge_id),
      href: challenge(o.challenge_id),
    });
  }
  const seen = new Set<string>();
  for (const d of nh.discrepancies) {
    if (d.cycle_only === true || seen.has(d.challenge_id)) continue;
    seen.add(d.challenge_id);
    out.push({
      kind: "discrepancy",
      title: title(d.challenge_id),
      href: challenge(d.challenge_id),
    });
  }
  for (const b of nh.budget_exhausted) {
    out.push({
      kind: "budget",
      title: title(b.challenge_id),
      href: challenge(b.challenge_id),
    });
  }
  for (const t of nh.triage) {
    out.push({
      kind: "triage",
      title: title(t.challenge_id),
      href: challenge(t.challenge_id),
    });
  }
  for (const sl of nh.slots) {
    out.push({
      kind: "slot",
      title: `${sl.slot_id}（${sl.repo}）`,
      href: null,
      note: "片付けは端末で行います。",
    });
  }
  return out;
}
