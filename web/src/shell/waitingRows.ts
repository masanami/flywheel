import type {
  Question,
  StatusNeedsHuman,
  Workspace,
} from "../api/types.gen.ts";
import { DISCREPANCY_LABELS, label } from "../labels.ts";
import { type Route, hrefOf } from "./route.ts";
import { WAITING_KINDS, type WaitingKind } from "./waiting.ts";

// 「あなたを待っている」の 1 行。種類の並びは WAITING_KINDS、同じ種類の中は API の並び。
export interface WaitingRow {
  kind: WaitingKind;
  key: string;
  // 行の主な文（課題の題名・問いの文・スロットの ID）
  title: string;
  // 行き先（ハッシュ）。スロットの片付けは行き先が無い。
  href: string | null;
  // 補足（課題の題名・操作の要約・理由など）
  hint?: string;
  // スロットの片付けの行だけ持つ。端末のコマンドの案内の差し込み口を埋める値。
  slotId?: string;
}

const challengeHref = (name: string, id: string) =>
  hrefOf({ kind: "challenge", name, id, view: "tree" });

// 状況の API の needs_human と問いの一覧から、行を作る。件数・区分は core の値で、ここは並べるだけ
// （食い違いは課題ごとに 1 行。循環だけの要素は、いまの人間待ちの数え方と同じく行にしない）。
export function buildWaitingRows(
  agent: Pick<Workspace, "name">,
  nh: StatusNeedsHuman,
  questions: readonly Question[],
  titleOf: (challengeId: string) => string,
): WaitingRow[] {
  const name = agent.name;
  const inState = (s: string) => nh.challenges.filter((c) => c.status === s);
  const rows: Record<WaitingKind, WaitingRow[]> = {
    plan_approval: inState("awaiting_plan_approval").map((c) => ({
      kind: "plan_approval",
      key: `plan:${c.id}`,
      title: c.title,
      href: hrefOf({ kind: "approve_plan", name, id: c.id }),
    })),
    completion_approval: inState("awaiting_completion_approval").map((c) => ({
      kind: "completion_approval",
      key: `completion:${c.id}`,
      title: c.title,
      href: challengeHref(name, c.id),
    })),
    operation: nh.operations.map((o) => ({
      kind: "operation",
      key: `operation:${o.id}`,
      title: o.summary,
      href: challengeHref(name, o.challenge_id),
      hint: titleOf(o.challenge_id),
    })),
    answer: inState("awaiting_human").flatMap((c): WaitingRow[] => {
      const qs = questions.filter(
        (q) => q.workspace === name && q.challenge_id === c.id,
      );
      if (qs.length === 0) {
        return [
          {
            kind: "answer",
            key: `answer:${c.id}`,
            title: c.title,
            href: challengeHref(name, c.id),
          },
        ];
      }
      // 数え方の 1 件が 1 行: 問いが複数あっても、課題ごとに 1 行（先頭の問いの行き先。残りは補足に並べる）。
      const first = qs[0] as Question;
      const route: Route =
        first.task_id !== null
          ? { kind: "task", name, id: c.id, taskId: first.task_id }
          : { kind: "challenge", name, id: c.id, view: "tree" };
      return [
        {
          kind: "answer",
          key: `answer:${c.id}`,
          title: first.question,
          href: hrefOf(route),
          hint: [c.title, ...qs.slice(1).map((q) => q.question)].join(" / "),
        },
      ];
    }),
    discrepancy: uniqueByChallenge(
      nh.discrepancies.filter((d) => d.cycle_only !== true),
    ).map((d) => ({
      kind: "discrepancy",
      key: `discrepancy:${d.challenge_id}`,
      title: titleOf(d.challenge_id),
      href: challengeHref(name, d.challenge_id),
      // 同じ課題の要素が複数あれば、種類を重複なく合わせる。
      hint: [
        ...new Set(
          nh.discrepancies
            .filter(
              (x) => x.cycle_only !== true && x.challenge_id === d.challenge_id,
            )
            .flatMap((x) => x.kinds),
        ),
      ]
        .map((k) => label(DISCREPANCY_LABELS, k))
        .join("・"),
    })),
    budget: nh.budget_exhausted.map((b) => ({
      kind: "budget",
      key: `budget:${b.challenge_id}`,
      title: titleOf(b.challenge_id),
      href: challengeHref(name, b.challenge_id),
    })),
    triage: nh.triage.map((t) => ({
      kind: "triage",
      key: `triage:${t.challenge_id}:${t.run_id}`,
      title: titleOf(t.challenge_id),
      href: challengeHref(name, t.challenge_id),
      hint: t.reason,
    })),
    slot: nh.slots.map((s) => ({
      kind: "slot",
      key: `slot:${s.slot_id}`,
      title: s.slot_id,
      href: null,
      hint: s.repo,
      slotId: s.slot_id,
    })),
  };
  return WAITING_KINDS.flatMap((k) => rows[k]);
}

function uniqueByChallenge<T extends { challenge_id: string }>(
  xs: readonly T[],
): T[] {
  const seen = new Set<string>();
  return xs.filter((x) => {
    if (seen.has(x.challenge_id)) return false;
    seen.add(x.challenge_id);
    return true;
  });
}
