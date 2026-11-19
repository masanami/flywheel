import type { ShowCardResponse } from "../api/types.gen.ts";
import { usd } from "../usd.ts";

// 課題の項目。状態・取り込み元・統合先・タスクの進み・費用・予算の上限。
// 進み・費用・予算・統合先は core が返した tree の値をそのまま出す（画面では導かない）。
// 統合先と予算の上限は、値が null なら項目ごと出さない。
export function Items({ show }: { show: ShowCardResponse }) {
  const { tree, source_binding: binding, source_children: children } = show;
  return (
    <dl className="detail-items" data-testid="detail-items">
      <div>
        <dt>状態</dt>
        <dd data-testid="detail-status">{show.challenge.status_label}</dd>
      </div>
      {binding !== null ? (
        <div>
          <dt>取り込み元</dt>
          <dd>
            <a
              href={safeHref(binding.url)}
              target="_blank"
              rel="noopener noreferrer"
            >
              上流を開く
            </a>{" "}
            <span>
              取り込み元の子{" "}
              <b data-testid="source-children-count">{children.length}</b> 件
            </span>
          </dd>
        </div>
      ) : null}
      {tree.uses_integration !== null ? (
        <div>
          <dt>統合先</dt>
          <dd data-testid="detail-integration">
            {tree.uses_integration ? "使う" : "使わない"}
          </dd>
        </div>
      ) : null}
      <div>
        <dt>タスクの進み</dt>
        <dd>
          <b data-testid="tasks-done">{tree.tasks_done}</b> /{" "}
          <b data-testid="tasks-total">{tree.tasks_total}</b>
        </dd>
      </div>
      <div>
        <dt>費用</dt>
        <dd>
          <b data-testid="cost">{usd(tree.cost_usd)}</b> USD
        </dd>
      </div>
      {tree.budget_usd !== null ? (
        <div>
          <dt>予算の上限</dt>
          <dd>
            <b data-testid="budget">{usd(tree.budget_usd)}</b> USD
          </dd>
        </div>
      ) : null}
    </dl>
  );
}

// 外部由来の行き先は http(s) だけをリンクにする。それ以外は行き先を持たない（# のまま）。
export function safeHref(url: string): string {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? url : "#";
  } catch {
    return "#";
  }
}
