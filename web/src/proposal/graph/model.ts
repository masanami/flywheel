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

// 直接の先行課題・後続課題・親・子（親子は記録の上の関係。入れ子の大枠は parentMap で 1 つに絞る）。
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

// --- 親子の入れ子（親課題の大枠の中に子課題を置く。親子は線で結ばない） ---

// 子ごとの親。親が複数あるときは ID の小さい親の大枠だけに置き、親子が循環する関係は捨てる。
export function parentMap(
  ids: Set<string>,
  relations: ChallengeRelation[],
): Map<string, string> {
  const pm = new Map<string, string>();
  const rels = relations
    .filter(
      (r) =>
        r.kind === "parent_of" &&
        r.from !== r.to &&
        ids.has(r.from) &&
        ids.has(r.to),
    )
    .sort((a, b) => compareIds(a.to, b.to) || compareIds(a.from, b.from));
  for (const r of rels) {
    if (pm.has(r.to)) continue;
    let loops = false;
    for (let p: string | undefined = r.from; p !== undefined; p = pm.get(p)) {
      if (p === r.to) {
        loops = true;
        break;
      }
    }
    if (!loops) pm.set(r.to, r.from);
  }
  return pm;
}

// 近い順の祖先（親・その親…）。
export function ancestorsOf(id: string, pm: Map<string, string>): string[] {
  const out: string[] = [];
  for (let p = pm.get(id); p !== undefined; p = pm.get(p)) out.push(p);
  return out;
}

export function childrenMap(pm: Map<string, string>): Map<string, string[]> {
  const m = new Map<string, string[]>();
  for (const [c, p] of pm) m.set(p, [...(m.get(p) ?? []), c]);
  for (const list of m.values()) list.sort(compareIds);
  return m;
}

// 子・孫…をすべて。
export function descendantsOf(
  id: string,
  kids: Map<string, string[]>,
): string[] {
  const out: string[] = [];
  const stack = [...(kids.get(id) ?? [])];
  while (stack.length > 0) {
    const c = stack.pop() as string;
    out.push(c);
    stack.push(...(kids.get(c) ?? []));
  }
  return out.sort(compareIds);
}

// この課題がいま見えている箱。畳んだ祖先があれば、そのうち一番外の祖先の箱に入っている。
export function visibleBoxOf(
  id: string,
  pm: Map<string, string>,
  expanded: Set<string>,
): string {
  let box = id;
  for (const a of ancestorsOf(id, pm)) if (!expanded.has(a)) box = a;
  return box;
}

// --- 配置（大枠ごとに、上から下へ層を重ねる。先行が上、後続が下） ---

export interface Dims {
  nodeW: number;
  nodeH: number;
  gapX: number;
  gapY: number;
  pad: number;
  // 関係の無い課題の段の見出しの高さ
  labelH: number;
  // 親の大枠の見出しの高さと、大枠の内側の余白
  headH: number;
  inset: number;
  // 一番外の段の幅の上限。超える段は折り返す（大枠の中は内側の余白の分だけ狭くなる）
  maxW: number;
}

export interface LayoutBox {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  // 同じ大枠の中での層（関係の無い課題の段は -1）
  layer: number;
  // 入れ子の深さ（一番外が 0）
  depth: number;
  // node は課題の箱（畳んだ親を含む）、group は開いた親の大枠
  kind: "node" | "group";
}

export interface LayoutEdge {
  from: string;
  to: string;
  path: string;
}

export interface Layout {
  width: number;
  height: number;
  nodes: LayoutBox[];
  // 開いた親の大枠。外側から内側の順
  groups: LayoutBox[];
  // 依存の線。両端は見えている箱（畳んだ親の中の課題の関係は、その親の箱へつなぎ替える）
  edges: LayoutEdge[];
  // 一番外で、依存も親子も持たない課題（グラフの下にまとめて並べる）と、その段の見出しの y
  isolated: string[];
  isolatedLabelY: number | null;
  // 依存に循環があった（本物の core は循環を作らせない想定。偽のデータの誤りを見える形にする）
  hasCycle: boolean;
  parents: Map<string, string>;
}

interface Placed {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  layer: number;
  inner: Arranged | null;
}

interface Arranged {
  w: number;
  h: number;
  items: Placed[];
  isolated: string[];
  labelY: number | null;
}

// expanded は開いている親の集合。ここに無い親は畳んで 1 つの箱にする。
export function layoutGraph(
  ids: string[],
  relations: ChallengeRelation[],
  d: Dims,
  expanded: Set<string> = new Set(),
): Layout {
  const order = [...ids].sort(compareIds);
  const rels = validRelations(new Set(order), relations);
  const pm = parentMap(new Set(order), rels);
  const kids = childrenMap(pm);
  const deps = rels.filter((r) => r.kind === "depends_on");
  let hasCycle = false;

  // 大枠 container（null は一番外）の直接の中身のうち、課題 id を含むもの。
  const memberIn = (id: string, container: string | null): string | null => {
    for (const x of [id, ...ancestorsOf(id, pm)])
      if ((pm.get(x) ?? null) === container) return x;
    return null;
  };

  const arrange = (
    members: string[],
    container: string | null,
    limit: number,
  ): Arranged => {
    const size = new Map<
      string,
      { w: number; h: number; inner: Arranged | null }
    >();
    for (const m of members) {
      const inner =
        kids.has(m) && expanded.has(m)
          ? arrange(kids.get(m) ?? [], m, limit - 2 * d.inset)
          : null;
      size.set(
        m,
        inner
          ? {
              w: Math.max(d.nodeW, inner.w + 2 * d.inset),
              h: d.headH + inner.h + d.inset,
              inner,
            }
          : { w: d.nodeW, h: d.nodeH, inner: null },
      );
    }
    // 中の課題どうしの依存を、この大枠の直接の中身どうしの関係へ持ち上げて層を決める。
    const preds = new Map(members.map((m) => [m, new Set<string>()]));
    for (const r of deps) {
      const a = memberIn(r.from, container);
      const b = memberIn(r.to, container);
      if (a && b && a !== b) preds.get(b)?.add(a);
    }
    const linked = new Set<string>();
    for (const [b, ps] of preds) {
      if (ps.size === 0) continue;
      linked.add(b);
      for (const a of ps) linked.add(a);
    }
    const isolated =
      container === null
        ? members.filter((m) => !linked.has(m) && !kids.has(m))
        : [];
    const graphIds = members.filter((m) => !isolated.includes(m));

    // 最長経路で層を決める（Kahn の方法）。
    const indeg = new Map(graphIds.map((id) => [id, preds.get(id)?.size ?? 0]));
    const layer = new Map<string, number>();
    const queue = graphIds.filter((id) => indeg.get(id) === 0);
    while (queue.length > 0) {
      const cur = queue.shift() as string;
      const ps = [...(preds.get(cur) ?? [])];
      layer.set(
        cur,
        ps.length === 0
          ? 0
          : Math.max(...ps.map((p) => (layer.get(p) ?? 0) + 1)),
      );
      for (const id of graphIds) {
        if (!preds.get(id)?.has(cur)) continue;
        const n = (indeg.get(id) ?? 0) - 1;
        indeg.set(id, n);
        if (n === 0) queue.push(id);
      }
    }
    if (layer.size < graphIds.length) {
      hasCycle = true;
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
          const ps = [...(preds.get(id) ?? [])].filter((p) => pos.has(p));
          return ps.length === 0
            ? Number.POSITIVE_INFINITY
            : ps.reduce((s, p) => s + (pos.get(p) ?? 0), 0) / ps.length;
        };
        row.sort((a, b) => bary(a) - bary(b) || compareIds(a, b));
      }
      row.forEach((id, i) => pos.set(id, (i + 0.5) / row.length));
    });

    // 幅の上限を超える層は折り返す。
    const wOf = (id: string) => size.get(id)?.w ?? d.nodeW;
    const rowW = (row: string[]) =>
      row.reduce((s, id) => s + wOf(id), 0) + (row.length - 1) * d.gapX;
    const wrap = (row: string[]): string[][] => {
      const out: string[][] = [];
      for (const id of row) {
        const cur = out[out.length - 1];
        if (cur && rowW([...cur, id]) <= limit) cur.push(id);
        else out.push([id]);
      }
      return out;
    };
    const rows = layers.flatMap((row, l) =>
      wrap(row).map((ids) => ({ ids, layer: l })),
    );
    const isoRows = wrap(isolated);
    const w = Math.max(
      d.nodeW,
      ...rows.map((r) => rowW(r.ids)),
      ...isoRows.map(rowW),
    );

    const items: Placed[] = [];
    let y = 0;
    const put = (row: string[], l: number) => {
      let x = (w - rowW(row)) / 2;
      for (const id of row) {
        const s = size.get(id) ?? { w: d.nodeW, h: d.nodeH, inner: null };
        items.push({ id, x, y, w: s.w, h: s.h, layer: l, inner: s.inner });
        x += s.w + d.gapX;
      }
      y += Math.max(...row.map((id) => size.get(id)?.h ?? d.nodeH));
    };
    rows.forEach((r, i) => {
      if (i > 0) y += d.gapY;
      put(r.ids, r.layer);
    });
    let labelY: number | null = null;
    if (isoRows.length > 0) {
      if (rows.length > 0) y += d.gapY;
      labelY = y;
      y += d.labelH;
      isoRows.forEach((r, i) => {
        if (i > 0) y += d.gapX;
        put(r, -1);
      });
    }
    return { w, h: Math.max(y, d.nodeH), items, isolated, labelY };
  };

  const root = arrange(
    order.filter((id) => !pm.has(id)),
    null,
    d.maxW,
  );
  const nodes: LayoutBox[] = [];
  const groups: LayoutBox[] = [];
  const flat = (a: Arranged, ox: number, oy: number, depth: number) => {
    for (const p of a.items) {
      const box: LayoutBox = {
        id: p.id,
        x: ox + p.x,
        y: oy + p.y,
        w: p.w,
        h: p.h,
        layer: p.layer,
        depth,
        kind: p.inner ? "group" : "node",
      };
      if (p.inner) {
        groups.push(box);
        flat(
          p.inner,
          box.x + (p.w - p.inner.w) / 2,
          box.y + d.headH,
          depth + 1,
        );
      } else nodes.push(box);
    }
  };
  flat(root, d.pad, d.pad, 0);
  groups.sort((a, b) => a.depth - b.depth);

  const at = new Map([...groups, ...nodes].map((b) => [b.id, b]));
  const seen = new Set<string>();
  const edges: LayoutEdge[] = [];
  for (const r of deps) {
    const a = visibleBoxOf(r.from, pm, expanded);
    const b = visibleBoxOf(r.to, pm, expanded);
    // 同じ箱の中・自分の大枠との依存は線にしない（入れ子の位置で読める）
    if (
      a === b ||
      ancestorsOf(a, pm).includes(b) ||
      ancestorsOf(b, pm).includes(a)
    )
      continue;
    const key = `${a}:${b}`;
    const ba = at.get(a);
    const bb = at.get(b);
    if (seen.has(key) || !ba || !bb) continue;
    seen.add(key);
    const x1 = ba.x + ba.w / 2;
    const y1 = ba.y + ba.h;
    const x2 = bb.x + bb.w / 2;
    const y2 = bb.y;
    const bend = Math.max(d.gapY / 2, Math.abs(y2 - y1) / 2);
    edges.push({
      from: a,
      to: b,
      path: `M ${x1} ${y1} C ${x1} ${y1 + bend}, ${x2} ${y2 - bend}, ${x2} ${y2}`,
    });
  }

  return {
    width: root.w + 2 * d.pad,
    height: root.h + 2 * d.pad,
    nodes,
    groups,
    edges,
    isolated: root.isolated,
    isolatedLabelY: root.labelY === null ? null : root.labelY + d.pad,
    hasCycle,
    parents: pm,
  };
}

// 文字数で切り詰める（全角も 1 文字と数える。SVG の text は折り返さないため）。
export function clip(s: string, n: number): string {
  const chars = Array.from(s);
  return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
}
