// 関係グラフから開いた詳細のモーダルに差し込む「待っている理由」。その課題が直接待っている先行課題（前提）と、
// 衝突のため後回しにした相手を、別の行で並べる。表示の部品は、ボードから開いた詳細と共通
// （board/WaitingReasonsSection）。ボードは API の waiting を、ここはグラフの辺を出どころにする。

import {
  PREREQUISITE_LABEL,
  WAITING_TITLE,
  WaitingReasonsSection,
  type WaitingRow,
  conflictKindLabel,
} from "../board/WaitingReasonsSection.tsx";
import type { NodeInfo } from "./GraphCanvas.tsx";
import {
  type ChallengeRelation,
  basisOfRelation,
  categoryLabel,
  compareIds,
  reasonDetail,
} from "./model.ts";

export { WAITING_TITLE };

// 課題 id が直接待っている前提（先行 → id）を、先行の ID の順に並べる。衝突は前提ではないので含めない。
export function waitingOn(
  id: string,
  relations: ChallengeRelation[],
): ChallengeRelation[] {
  return relations
    .filter((r) => r.kind === "depends_on" && r.to === id)
    .sort((a, b) => compareIds(a.from, b.from));
}

// 課題 id が衝突のため後回しにした相手（向きのある衝突で、id が後回しの側）を、相手の ID の順に並べる。
// 先に委譲した側・向きの無い組には、待っている側が無いので含めない。
export function yieldingTo(
  id: string,
  relations: ChallengeRelation[],
): ChallengeRelation[] {
  return relations
    .filter(
      (r) =>
        r.kind === "conflicts_with" &&
        r.from === id &&
        r.edge?.directed !== false,
    )
    .sort((a, b) => compareIds(a.to, b.to));
}

export function waitingRowsOfGraph(
  id: string,
  relations: ChallengeRelation[],
  infos: Map<string, NodeInfo>,
): WaitingRow[] {
  const row = (other: string, r: ChallengeRelation): WaitingRow => {
    const i = infos.get(other);
    const basis = basisOfRelation(r);
    return {
      key: `${basis}:${other}`,
      id: other,
      title: i?.node.title ?? "",
      marks: `${i?.category === "done" ? "（完了）" : ""}${i?.active ? "（実施中）" : ""}`,
      basis,
      kindLabel:
        basis === "conflict" ? conflictKindLabel(other) : PREREQUISITE_LABEL,
      detail: reasonDetail(r),
      category: i?.category,
      swatchTitle: i ? categoryLabel(i.category) : "",
    };
  };
  return [
    ...waitingOn(id, relations).map((r) => row(r.from, r)),
    ...yieldingTo(id, relations).map((r) => row(r.to, r)),
  ];
}

export function WaitingReasons({
  id,
  relations,
  infos,
}: {
  id: string;
  relations: ChallengeRelation[];
  infos: Map<string, NodeInfo>;
}) {
  return (
    <WaitingReasonsSection rows={waitingRowsOfGraph(id, relations, infos)} />
  );
}
