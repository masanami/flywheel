// 課題の詳細の「待っている理由」。まだ満たされていない前提の課題を並べる（API の waiting）。

import type { Waiting } from "../api/types.gen.ts";
import { STATE_LABELS, label } from "../labels.ts";

export const WAITING_TITLE = "待っている理由";

export type WaitingBasis = "prerequisite";

// 待っている理由の 1 行。id は待っている相手の課題。
export interface WaitingRow {
  key: string;
  id: string;
  title: string;
  // 題名の後ろに付く印（「（完了）」「（実施中）」「（着手中）」など。無ければ空）
  marks: string;
  basis: WaitingBasis;
  // 理由の見出し（前提は種類の文）
  kindLabel: string;
  detail: string;
  // 区分の色（グラフから開いたときだけ。無ければ色見本を出さない）
  category?: string;
  swatchTitle?: string;
}

// 前提の行の見出し。
export const PREREQUISITE_LABEL = "終わらないと始められない";

export function WaitingReasonsSection({ rows }: { rows: WaitingRow[] }) {
  // 待っている理由が無ければ節ごと出さない
  if (rows.length === 0) return null;
  return (
    <section aria-label={WAITING_TITLE} className="waiting-reasons">
      <h3>{WAITING_TITLE}</h3>
      <ul>
        {rows.map((r) => (
          <li
            key={r.key}
            data-from={r.id}
            data-category={r.category}
            data-basis={r.basis}
          >
            <div className="waiting-reasons-head">
              {r.category !== undefined ? (
                <span
                  className="graph-swatch"
                  title={r.swatchTitle ?? ""}
                  aria-hidden="true"
                />
              ) : null}
              <b>{r.id}</b> {r.title}
              {r.marks}
            </div>
            <div className="waiting-reasons-reason">
              <span className="waiting-reasons-kind">{r.kindLabel}</span>
              <span>{r.detail}</span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

// API（`GET …/challenges/{id}` の waiting）の行。印は相手の課題の状態の名前。
export function waitingRowsOfShow(w: Waiting): WaitingRow[] {
  const mark = (status: string) => `（${label(STATE_LABELS, status)}）`;
  return [
    ...w.prerequisites.map(
      (p): WaitingRow => ({
        key: `prerequisite:${p.challenge_id}`,
        id: p.challenge_id,
        title: p.title,
        marks: mark(p.status),
        basis: "prerequisite",
        kindLabel: PREREQUISITE_LABEL,
        detail: "まだ満たされていない前提の課題",
      }),
    ),
  ];
}

export function ShowWaitingReasons({ waiting }: { waiting: Waiting }) {
  return <WaitingReasonsSection rows={waitingRowsOfShow(waiting)} />;
}
