import { useContext } from "react";
import type { StatusNeedsHuman } from "../api/types.gen.ts";
import type { LiveState } from "../live/useLive.ts";
import { ScreenNavContext } from "../nav.tsx";

export interface HumanCounts {
  approvals: number;
  questions: number;
  operations: number;
  discrepancies: number;
  budget: number;
  triage: number;
  slots: number;
}

export function countsOf(nh: StatusNeedsHuman): HumanCounts {
  const n = (s: string) => nh.challenges.filter((c) => c.status === s).length;
  return {
    approvals: n("awaiting_plan_approval") + n("awaiting_completion_approval"),
    questions: n("awaiting_human"),
    operations: nh.operations.length,
    discrepancies: nh.discrepancies.length,
    budget: nh.budget_exhausted.length,
    triage: nh.triage.length,
    slots: nh.slots.length,
  };
}

const KINDS: [keyof HumanCounts, string][] = [
  ["approvals", "承認待ち"],
  ["questions", "未回答の問い"],
  ["operations", "不可逆操作の承認"],
  ["discrepancies", "上流との食い違い"],
  ["budget", "予算切れ"],
  ["triage", "トリアージ"],
  ["slots", "片づけ待ちのスロット"],
];

// 案①: ボードの上に、承認以外も含めた「人間の操作待ち」を集めて出す。0 件の区分は出さない。
export function NeedsHumanStrip({ live }: { live: LiveState }) {
  const go = useContext(ScreenNavContext);
  const rows = (live.needsHuman?.workspaces ?? []).map((w) => ({
    name: w.name,
    counts: countsOf(w.needs_human),
  }));
  const total = rows.reduce(
    (sum, r) => sum + Object.values(r.counts).reduce((a, b) => a + b, 0),
    0,
  );
  return (
    <section
      className="needs-human-strip"
      aria-label="人間の操作待ち（案）"
      data-total={total}
    >
      <strong>🔔 人間の操作待ち {total} 件</strong>
      {total === 0 ? <span> なし</span> : null}
      <ul>
        {rows.map((r) => {
          const shown = KINDS.filter(([k]) => r.counts[k] > 0);
          if (shown.length === 0) return null;
          return (
            <li key={r.name}>
              <b>{r.name}</b>
              {shown.map(([k, text]) => (
                <span key={k} className="strip-chip" data-kind={k}>
                  {text} {r.counts[k]}
                </span>
              ))}
            </li>
          );
        })}
      </ul>
      {go ? (
        <button type="button" onClick={() => go("needs-human")}>
          一覧で見る
        </button>
      ) : null}
    </section>
  );
}
