import type { Notice } from "./api/types.gen.ts";
import {
  CYCLE_BUDGET_GUIDE_TEXT,
  CYCLE_END_REASON_LABELS,
  label,
} from "./labels.ts";
import { hrefOf } from "./shell/route.ts";

// 知らせ（GET …/notices）の文の部品。種類・件数・理由は core の値で、ここは文に直して行き先を付けるだけ。
// 終わった理由の言葉は labels.ts から読むだけ。知らせの言葉はこのファイルに置く。

export const NOTICE_SECTION_TITLE = "最近起きたこと";
export const NOTICE_BADGE = "知らせ";
const UNKNOWN_CHALLENGE_TITLE = "題名を読めない課題";
const UNKNOWN_TASK_TITLE = "題名を読めないタスク";

// 終わった理由ごとの、続け方の案内。費用の上限は labels.ts の文、ほかはここ。
// 進められるものが無くなった（settled）・止めた（stopped）には付けない。
const CYCLE_END_GUIDES: Record<string, string> = {
  budget: CYCLE_BUDGET_GUIDE_TEXT,
  rate_limited:
    "利用上限が戻ってから「サイクルを回す」を押すと、続きから進みます。",
  stalled: "進まなかった理由をサイクルの画面で確かめてから、続けてください。",
  error:
    "原因をサイクルの画面で確かめてから、「サイクルを回す」を押してください。",
  interrupted: "「サイクルを回す」を押すと、続きから進みます。",
};

// タスクを足した出どころ（core の値）。知らない値は言わない。
const ORIGIN_TEXTS: Record<string, string> = {
  expand: "計画の拡張",
  followup: "フォローアップから",
};

export interface NoticeLine {
  key: string;
  at: string;
  text: string;
  href: string | null;
  guide: string | null;
}

export function noticeLine(n: Notice, agentName: string): NoticeLine {
  const key = `${n.kind}:${n.at}:${n.challenge_id ?? ""}:${n.task_id ?? ""}:${n.cycle_id ?? ""}`;
  const challengeTitle = n.challenge_title || UNKNOWN_CHALLENGE_TITLE;
  const challengeHref =
    n.challenge_id !== null
      ? hrefOf({
          kind: "challenge",
          name: agentName,
          id: n.challenge_id,
          view: "tree",
        })
      : null;
  switch (n.kind) {
    case "task_added":
      return {
        key,
        at: n.at,
        text: `「${challengeTitle}」にタスクを ${n.added_count ?? 0} 件足しました${
          n.origin !== null && ORIGIN_TEXTS[n.origin]
            ? `（${ORIGIN_TEXTS[n.origin]}）`
            : ""
        }。`,
        href: challengeHref,
        guide: null,
      };
    case "released":
      return {
        key,
        at: n.at,
        text: `「${challengeTitle}」が反映先へ入りました。`,
        href: challengeHref,
        guide: null,
      };
    case "cycle_ended": {
      const reason = n.end_reason ?? "";
      return {
        key,
        at: n.at,
        text:
          reason === ""
            ? "サイクルが終わりました。"
            : `サイクルが終わりました（${label(CYCLE_END_REASON_LABELS, reason)}）。`,
        href: hrefOf({ kind: "agent", name: agentName, screen: "cycle" }),
        guide: CYCLE_END_GUIDES[reason] ?? null,
      };
    }
    case "supervisor_escalated": {
      const taskTitle = n.task_title || UNKNOWN_TASK_TITLE;
      const href =
        n.challenge_id !== null && n.task_id !== null
          ? hrefOf({
              kind: "task",
              name: agentName,
              id: n.challenge_id,
              taskId: n.task_id,
            })
          : challengeHref;
      return {
        key,
        at: n.at,
        text: `スーパーバイザーがあなたへ上げました（${challengeTitle}／${taskTitle}）。`,
        href,
        guide: null,
      };
    }
    default:
      return { key, at: n.at, text: n.kind, href: challengeHref, guide: null };
  }
}
