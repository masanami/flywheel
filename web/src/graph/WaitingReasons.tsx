// 関係グラフから開いた詳細のモーダルに差し込む「待っている理由」。その課題が直接待っている先行課題（前提）を並べる。
// 表示の部品は、ボードから開いた詳細と共通
// （board/WaitingReasonsSection）。ボードは API の waiting を、ここはグラフの辺を出どころにする。

import {
  PREREQUISITE_LABEL,
  WAITING_TITLE,
  WaitingReasonsSection,
  type WaitingRow,
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

// 課題 id が直接待っている前提（先行 → id）を、先行の ID の順に並べる。
export function waitingOn(
  id: string,
  relations: ChallengeRelation[],
): ChallengeRelation[] {
  return relations
    .filter((r) => r.kind === "depends_on" && r.to === id)
    .sort((a, b) => compareIds(a.from, b.from));
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
      kindLabel: PREREQUISITE_LABEL,
      detail: reasonDetail(r),
      category: i?.category,
      swatchTitle: i ? categoryLabel(i.category) : "",
    };
  };
  return [...waitingOn(id, relations).map((r) => row(r.from, r))];
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
