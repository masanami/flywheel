import type { LiveState } from "../live/useLive.ts";
import {
  HUMAN_KINDS,
  HUMAN_KIND_LABELS,
  type HumanKind,
  countsOf,
  unboundSlots,
} from "./filter.ts";
import type { Challenge } from "./types.ts";

// ボードの上の人間待ちの帯。fleet 全体の needs-human をワークスペースごとに数え、0 件の区分は出さない。
// 区分を押すとボードがその区分に絞られ、もう一度押すか解除で外れる。
export function NeedsHumanStrip({
  live,
  selected,
  onSelect,
  cardsOf,
}: {
  live: LiveState;
  selected: HumanKind | null;
  onSelect: (kind: HumanKind | null) => void;
  cardsOf: (workspace: string) => Challenge[];
}) {
  const rows = (live.needsHuman?.workspaces ?? []).map((w) => ({
    name: w.name,
    nh: w.needs_human,
    counts: countsOf(w.needs_human),
  }));
  const unreadable = live.needsHuman?.unreadable_workspaces ?? [];
  const total = rows.reduce(
    (sum, r) => sum + HUMAN_KINDS.reduce((a, k) => a + r.counts[k], 0),
    0,
  );
  const unbound =
    selected === "slots"
      ? rows.flatMap((r) =>
          unboundSlots(r.nh, cardsOf(r.name)).map((s) => ({
            workspace: r.name,
            slot: s,
          })),
        )
      : [];
  return (
    <section
      className="needs-human-strip"
      aria-label="人間待ちの帯"
      data-total={total}
    >
      <strong>🔔 人間待ち {total} 件</strong>
      {total === 0 ? <span> なし</span> : null}
      {selected !== null ? (
        <button type="button" onClick={() => onSelect(null)}>
          絞り込みを解除
        </button>
      ) : null}
      <ul>
        {rows.map((r) => {
          const shown = HUMAN_KINDS.filter((k) => r.counts[k] > 0);
          if (shown.length === 0) return null;
          return (
            <li key={r.name}>
              <b>{r.name}</b>
              {shown.map((k) => (
                <button
                  key={k}
                  type="button"
                  className="strip-chip"
                  data-kind={k}
                  aria-pressed={selected === k}
                  onClick={() => onSelect(selected === k ? null : k)}
                >
                  {HUMAN_KIND_LABELS[k]} {r.counts[k]}
                </button>
              ))}
            </li>
          );
        })}
      </ul>
      {unbound.length > 0 ? (
        <ul
          data-testid="unbound-slots"
          aria-label="課題の run に結ばれないスロット"
        >
          {unbound.map(({ workspace, slot }) => (
            <li key={`${workspace}:${slot.slot_id}`}>
              {workspace}: {slot.repo} {slot.slot_id}（{slot.path}）
            </li>
          ))}
        </ul>
      ) : null}
      {unreadable.length > 0 ? (
        <p role="alert" data-testid="unreadable-workspaces">
          読み取れなかったワークスペース（集約から除いています）:{" "}
          {unreadable.join("、")}
        </p>
      ) : null}
    </section>
  );
}
