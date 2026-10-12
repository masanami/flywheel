// 関係グラフの案（flywheel#176）の導出と配置。画面から切り離した純粋な関数だけを置く。
// 区分は仕様 m4-ui-server.md「状況の表示とライブ更新」のカードの区分の印と同じ値から導く（API に区分を足さない）。

import type { ChallengeRelation, RelationKind } from "../../api/proposed.ts";
import type { ChallengeCard, StatusResponse } from "../../api/types.gen.ts";

// ノードの色の区分。1 つの課題は 1 つの区分だけに入り、上から順に先に当たったものを採る。
export type Category =
  | "human"
  | "external"
  | "running"
  | "system"
  | "done"
  | "other";

export const CATEGORIES: { key: Category; label: string; hint: string }[] = [
  {
    key: "human",
    label: "人間の操作待ち",
    hint: "次に人間がすることがある（card.next_human_actions が空でない）",
  },
  {
    key: "external",
    label: "外部待ち",
    hint: "CI などの外部の完了を待っている（waiting_ci）",
  },
  {
    key: "running",
    label: "システムが実行中",
    hint: "run が動いている（running・unresponsive）",
  },
  {
    key: "system",
    label: "システムが次に進めるもの",
    hint: "次の周でシステムが進められる（status.actionable）",
  },
  { key: "done", label: "完了", hint: "完了した課題" },
  { key: "other", label: "その他", hint: "上のいずれにも当たらない" },
];

export function categoryOf(
  c: ChallengeCard,
  actionable: Set<string>,
): Category {
  if (c.card.next_human_actions.length > 0) return "human";
  if (c.card.modifiers.includes("waiting_ci")) return "external";
  if (
    c.card.modifiers.includes("running") ||
    c.card.modifiers.includes("unresponsive")
  )
    return "running";
  if (c.card.state === "done") return "done";
  if (actionable.has(c.id)) return "system";
  return "other";
}

export function actionableIds(status: StatusResponse | undefined): Set<string> {
  return new Set((status?.actionable.challenges ?? []).map((c) => c.id));
}

// 課題の ID（C-12 など）を数の順に並べる。数が無ければ文字列の順。
export function compareIds(a: string, b: string): number {
  const na = Number(a.replace(/^\D+/, ""));
  const nb = Number(b.replace(/^\D+/, ""));
  if (Number.isFinite(na) && Number.isFinite(nb) && na !== nb) return na - nb;
  return a < b ? -1 : a > b ? 1 : 0;
}

// 両端がいま描く課題にある関係だけを残し、自己参照と重複を除く。
export function validRelations(
  ids: Set<string>,
  relations: ChallengeRelation[],
): ChallengeRelation[] {
  const seen = new Set<string>();
  return relations.filter((r) => {
    const key = `${r.kind}:${r.from}:${r.to}`;
    if (r.from === r.to || !ids.has(r.from) || !ids.has(r.to) || seen.has(key))
      return false;
    seen.add(key);
    return true;
  });
}

function walk(
  start: string,
  relations: ChallengeRelation[],
  kind: RelationKind,
  dir: "down" | "up",
): string[] {
  const out: string[] = [];
  const seen = new Set([start]);
  const queue = [start];
  while (queue.length > 0) {
    const cur = queue.shift() as string;
    for (const r of relations) {
      if (r.kind !== kind) continue;
      const [a, b] = dir === "down" ? [r.from, r.to] : [r.to, r.from];
      if (a === cur && !seen.has(b)) {
        seen.add(b);
        out.push(b);
        queue.push(b);
      }
    }
  }
  return out.sort(compareIds);
}

// この課題が終わると（連鎖して）進められるようになる課題。依存だけをたどる。
export const downstreamOf = (id: string, relations: ChallengeRelation[]) =>
  walk(id, relations, "depends_on", "down");

// この課題の前に終わっている必要がある課題。依存だけをたどる。
export const upstreamOf = (id: string, relations: ChallengeRelation[]) =>
  walk(id, relations, "depends_on", "up");

// 直接の先行課題・後続課題・親・子。
export function neighborsOf(id: string, relations: ChallengeRelation[]) {
  const pick = (kind: RelationKind, end: "from" | "to") =>
    relations
      .filter(
        (r) => r.kind === kind && r[end === "from" ? "to" : "from"] === id,
      )
      .map((r) => r[end])
      .sort(compareIds);
  return {
    predecessors: pick("depends_on", "from"),
    successors: pick("depends_on", "to"),
    parents: pick("parent_of", "from"),
    children: pick("parent_of", "to"),
  };
}

// --- 配置（上から下へ層を重ねる。先行・親が上、後続・子が下） ---

export interface Dims {
  nodeW: number;
  nodeH: number;
  gapX: number;
  gapY: number;
  pad: number;
  // 関係の無い課題の段の見出しの高さ
  labelH: number;
}

export interface LayoutNode {
  id: string;
  x: number;
  y: number;
  layer: number;
}

export interface LayoutEdge {
  from: string;
  to: string;
  kind: RelationKind;
  path: string;
}

export interface Layout {
  width: number;
  height: number;
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  // 関係を 1 本も持たない課題（グラフの下にまとめて並べる）と、その段の見出しの y
  isolated: string[];
  isolatedLabelY: number | null;
  // 依存・親子に循環があった（本物の core は循環を作らせない想定。偽のデータの誤りを見える形にする）
  hasCycle: boolean;
}

export function layoutGraph(
  ids: string[],
  relations: ChallengeRelation[],
  d: Dims,
): Layout {
  const order = [...ids].sort(compareIds);
  const rels = validRelations(new Set(order), relations);
  const linked = new Set(rels.flatMap((r) => [r.from, r.to]));
  const graphIds = order.filter((id) => linked.has(id));
  const isolated = order.filter((id) => !linked.has(id));

  // 最長経路で層を決める（Kahn の方法）。親子も「親が上」として層に数える。
  const preds = new Map<string, string[]>(graphIds.map((id) => [id, []]));
  for (const r of rels) preds.get(r.to)?.push(r.from);
  const indeg = new Map(graphIds.map((id) => [id, preds.get(id)?.length ?? 0]));
  const layer = new Map<string, number>();
  const queue = graphIds.filter((id) => indeg.get(id) === 0);
  while (queue.length > 0) {
    const cur = queue.shift() as string;
    const ps = preds.get(cur) ?? [];
    layer.set(
      cur,
      ps.length === 0 ? 0 : Math.max(...ps.map((p) => (layer.get(p) ?? 0) + 1)),
    );
    for (const r of rels) {
      if (r.from !== cur) continue;
      const n = (indeg.get(r.to) ?? 0) - 1;
      indeg.set(r.to, n);
      if (n === 0) queue.push(r.to);
    }
  }
  const hasCycle = layer.size < graphIds.length;
  if (hasCycle) {
    const last = Math.max(-1, ...layer.values()) + 1;
    for (const id of graphIds) if (!layer.has(id)) layer.set(id, last);
  }

  // 層の中の並びは、上の層の先行の位置の平均（重心）で決め、交差を減らす。
  const layers: string[][] = [];
  for (const id of graphIds) {
    const l = layer.get(id) ?? 0;
    layers[l] = [...(layers[l] ?? []), id];
  }
  const pos = new Map<string, number>();
  layers.forEach((row, l) => {
    if (l > 0) {
      const bary = (id: string) => {
        const ps = (preds.get(id) ?? []).filter((p) => pos.has(p));
        return ps.length === 0
          ? Number.POSITIVE_INFINITY
          : ps.reduce((s, p) => s + (pos.get(p) ?? 0), 0) / ps.length;
      };
      row.sort((a, b) => bary(a) - bary(b) || compareIds(a, b));
    }
    row.forEach((id, i) => pos.set(id, (i + 0.5) / row.length));
  });

  const widest = Math.max(1, ...layers.map((r) => r.length));
  const isoCols = Math.max(widest, Math.min(isolated.length, 3));
  const cols = Math.max(widest, isolated.length > 0 ? isoCols : 1);
  const width = d.pad * 2 + cols * d.nodeW + (cols - 1) * d.gapX;
  const rowX = (count: number, i: number) =>
    (width - (count * d.nodeW + (count - 1) * d.gapX)) / 2 +
    i * (d.nodeW + d.gapX);

  const nodes: LayoutNode[] = [];
  layers.forEach((row, l) => {
    row.forEach((id, i) => {
      nodes.push({
        id,
        layer: l,
        x: rowX(row.length, i),
        y: d.pad + l * (d.nodeH + d.gapY),
      });
    });
  });
  let y = d.pad + layers.length * (d.nodeH + d.gapY);
  let isolatedLabelY: number | null = null;
  if (isolated.length > 0) {
    if (layers.length === 0) y = d.pad;
    isolatedLabelY = y;
    y += d.labelH;
    isolated.forEach((id, i) => {
      const r = Math.floor(i / isoCols);
      const inRow = Math.min(isoCols, isolated.length - r * isoCols);
      nodes.push({
        id,
        layer: -1,
        x: rowX(inRow, i % isoCols),
        y: y + r * (d.nodeH + d.gapX),
      });
    });
    const rows = Math.ceil(isolated.length / isoCols);
    y += rows * d.nodeH + (rows - 1) * d.gapX + d.pad;
  } else {
    y = y - d.gapY + d.pad;
  }

  const at = new Map(nodes.map((n) => [n.id, n]));
  const edges: LayoutEdge[] = rels.map((r) => {
    const a = at.get(r.from) as LayoutNode;
    const b = at.get(r.to) as LayoutNode;
    const x1 = a.x + d.nodeW / 2;
    const y1 = a.y + d.nodeH;
    const x2 = b.x + d.nodeW / 2;
    const y2 = b.y;
    const bend = Math.max(d.gapY / 2, Math.abs(y2 - y1) / 2);
    return {
      from: r.from,
      to: r.to,
      kind: r.kind,
      path: `M ${x1} ${y1} C ${x1} ${y1 + bend}, ${x2} ${y2 - bend}, ${x2} ${y2}`,
    };
  });

  return {
    width,
    height: Math.max(y, d.pad * 2 + d.nodeH),
    nodes,
    edges,
    isolated,
    isolatedLabelY,
    hasCycle,
  };
}

// 文字数で切り詰める（全角も 1 文字と数える。SVG の text は折り返さないため）。
export function clip(s: string, n: number): string {
  const chars = Array.from(s);
  return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
}
