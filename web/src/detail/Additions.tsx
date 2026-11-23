import type { ShowCardResponse } from "../api/types.gen.ts";
import { TASK_ADDITION_STATE_LABELS, label } from "../labels.ts";
import { usd } from "../usd.ts";
import { shortTime } from "../when.ts";

// 課題の詳細の「タスクを足す依頼」の枠。依頼ごとに、状態・足すタスクの題名・足した後の見込みと上限を出す。
// 依頼が 0 件なら枠ごと出さない。
export function Additions({ show }: { show: ShowCardResponse }) {
  const items = [...(show.additions ?? [])].sort((a, b) =>
    a.created_at.localeCompare(b.created_at),
  );
  if (items.length === 0) return null;
  return (
    <section aria-label="タスクを足す依頼" data-testid="additions">
      <h3>タスクを足す依頼</h3>
      <ul className="additions-list">
        {items.map((a) => (
          <li key={a.id} data-testid="addition">
            <time dateTime={a.created_at}>{shortTime(a.created_at)}</time>{" "}
            <b data-testid="addition-state">
              {label(TASK_ADDITION_STATE_LABELS, a.state)}
            </b>
            {a.task_titles.length > 0 ? (
              <ul data-testid="addition-titles">
                {a.task_titles.map((t, i) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: 同じ題名が並びうる
                  <li key={i}>{t}</li>
                ))}
              </ul>
            ) : null}
            {a.total_after_usd !== null || a.limit_usd !== null ? (
              <p data-testid="addition-estimate">
                足した後の見込み:{" "}
                {a.total_after_usd === null
                  ? "見込みなし"
                  : `${usd(a.total_after_usd)} USD`}
                {" ・ 上限: "}
                {a.limit_usd === null ? "上限なし" : `${usd(a.limit_usd)} USD`}
              </p>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}
