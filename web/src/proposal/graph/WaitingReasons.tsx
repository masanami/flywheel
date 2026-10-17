// 詳細のモーダルに差し込む「待っている理由（直前の先行）」（2026-10-17 オーナー: 以前の右のパネルにあった一覧を、
// 課題を押したときに開く詳細へ足す）。その課題が直接待っている先行課題と、依存の理由・種類を並べる。
// モーダルは本番の部品なので、関係グラフの画面からだけ差し込み口（CardDetailModal の supplement）で渡す。

import type { ChallengeRelation } from "../../api/proposed.ts";
import type { NodeInfo } from "./GraphCanvas.tsx";
import { BASIS_LABELS, CATEGORIES, compareIds } from "./model.ts";

// 課題 id が直接待っている依存（先行 → id）を、先行の ID の順に並べる。
export function waitingOn(
  id: string,
  relations: ChallengeRelation[],
): ChallengeRelation[] {
  return relations
    .filter((r) => r.kind === "depends_on" && r.to === id)
    .sort((a, b) => compareIds(a.from, b.from));
}

export const WAITING_TITLE = "待っている理由（直前の先行）";

export function WaitingReasons({
  id,
  relations,
  infos,
}: {
  id: string;
  relations: ChallengeRelation[];
  infos: Map<string, NodeInfo>;
}) {
  const rels = waitingOn(id, relations);
  // 待っている先行が無ければ節ごと出さない（以前の右のパネルと同じ）
  if (rels.length === 0) return null;
  return (
    <section aria-label={WAITING_TITLE} className="graph-waiting">
      <h3>{WAITING_TITLE}</h3>
      <ul>
        {rels.map((r) => {
          const i = infos.get(r.from);
          const basis = r.basis ?? "prerequisite";
          return (
            <li
              key={r.from}
              data-from={r.from}
              data-category={i?.category}
              data-basis={basis}
            >
              <div className="graph-waiting-head">
                <span
                  className="graph-swatch"
                  title={
                    CATEGORIES.find((k) => k.key === i?.category)?.label ?? ""
                  }
                  aria-hidden="true"
                />
                <b>{r.from}</b> {i?.card.title ?? ""}
                {i?.category === "done" ? "（完了）" : ""}
                {i?.active ? "（実施中）" : ""}
              </div>
              <div className="graph-waiting-reason">
                <span className="graph-waiting-kind">
                  {BASIS_LABELS[basis]}
                </span>
                {r.reason ? <span>{r.reason}</span> : null}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
