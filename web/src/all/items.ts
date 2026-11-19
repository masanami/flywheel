import type { Question, StatusNeedsHuman } from "../api/types.gen.ts";
import type { WaitingKind } from "../shell/waiting.ts";
import { buildWaitingRows } from "../shell/waitingRows.ts";

export interface WaitingItem {
  // 行ごとに一意な鍵（エージェントの中で）。
  key: string;
  kind: WaitingKind;
  title: string;
  // 行き先。スロットの片付けは行き先が無い。
  href: string | null;
  // 種類の下に添える文（スロットの片付けの案内）。
  note?: string;
}

// あなたを待っているものを、1 件ごとの種類・題名・行き先にする。行の組み立ては「いま進められること」と
// 同じ部品（buildWaitingRows）で、ここは「すべて」の見た目に合わせて並べ直すだけ。題名は課題の一覧から引き、無ければ課題の ID。
export function waitingItems(
  agentName: string,
  nh: StatusNeedsHuman,
  titleOf: (challengeId: string) => string | undefined,
  questions: readonly Question[] = [],
): WaitingItem[] {
  return buildWaitingRows(
    { name: agentName },
    nh,
    questions,
    (id) => titleOf(id) ?? id,
  ).map((r) =>
    r.kind === "slot"
      ? {
          key: r.key,
          kind: r.kind,
          title: `${r.title}（${r.hint ?? ""}）`,
          href: null,
          note: "片付けは端末で行います。",
        }
      : r.kind === "operation"
        ? {
            key: r.key,
            kind: r.kind,
            title: r.title,
            href: r.href,
            note: r.hint,
          }
        : { key: r.key, kind: r.kind, title: r.title, href: r.href },
  );
}
