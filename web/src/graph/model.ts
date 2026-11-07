// 課題の関係グラフ（flywheel#176）の導出と配置。画面から切り離した純粋な関数だけを置く。
// 区分・running・stale・親子・依存は `GET …/graph`（core が記録だけから決定的に導く）の値をそのまま使う。

import type { GraphEdge, GraphNode, GraphResponse } from "../api/types.gen.ts";
import {
  HUMAN_KIND_LABELS,
  HUMAN_KIND_OF_ACTION,
  type HumanKind,
} from "../board/filter.ts";

// ノードの色の区分。API の区分（human・external・parent_agent・done・withdrawn・system）に、システムが進めていて run が動いている
// 課題の running を足したもの。other は畳んだ親の件数の札の色にだけ使う。
export type Category =
  | "human"
  | "external"
  | "parent_agent"
  | "running"
  | "system"
  | "done"
  | "withdrawn"
  | "other";

// 区分の名前。凡例は区分の名前と色だけにし、実装の語（フィールド名など）を人に見せない。
export const CATEGORIES: { key: Category; label: string }[] = [
  { key: "human", label: "人間の操作待ち" },
  { key: "external", label: "外部待ち" },
  { key: "parent_agent", label: "親のエージェントが動く番" },
  { key: "running", label: "システムが実行中" },
  { key: "system", label: "システムが次に進めるもの" },
  { key: "done", label: "完了" },
  // 取り下げは完了と同じ灰色で描き、名前で区別する
  { key: "withdrawn", label: "取り下げ" },
];

export const categoryLabel = (c: Category): string =>
  CATEGORIES.find((k) => k.key === c)?.label ?? c;

// 実行中で、しきい値を超えて記録が無い run（stale）の見せ方。死活の断定に読める「応答なし」を避け、
// 事実だけを言う「更新なし」とする。
export const STALE_LABEL = "⚠ 更新なし";
export const STALE_LONG = "⚠ 更新なし（要確認）";
export const STALE_HINT =
  "実行中だが、しきい値を超えて記録がありません。止まったとは限らず、長い委譲でも出ます";

// 色の区分。人間の操作待ち・外部待ち・親のエージェントが動く番・完了・取り下げは API の区分のまま、システムの課題は run が動いていれば running。
export function categoryOf(n: GraphNode): Category {
  switch (n.category) {
    case "human":
    case "external":
    case "parent_agent":
    case "done":
    case "withdrawn":
      return n.category;
    default:
      return n.running ? "running" : "system";
  }
}

// 人間の操作待ちのチップの記号。帯の区分（HumanKind）ごとに 1 つで、語の前に添える。
// 語（HUMAN_KIND_LABELS）を変えずに、題名と見分けられる印にする。
export const HUMAN_KIND_GLYPHS: Record<HumanKind, string> = {
  approvals: "✓",
  questions: "?",
  operations: "!",
  discrepancies: "≠",
  budget: "$",
  triage: "↕",
  slots: "⌫",
};

export interface HumanChip {
  kind: HumanKind;
  glyph: string;
  label: string;
}

// 人間の操作待ちのノードに付けるチップ（何をするか）。ボードの上の人間待ちの帯の区分の語を使い、
// next_human_actions の並びで、同じ区分になるもの（計画と完了の承認など）は 1 つにまとめる。
// 人間の操作待ちでなければ空。知らない行動の値は飛ばす。
export function humanChips(n: GraphNode): HumanChip[] {
  if (n.category !== "human") return [];
  const kinds = new Set<HumanKind>();
  for (const a of n.next_human_actions) {
    const k = (HUMAN_KIND_OF_ACTION as Record<string, HumanKind | undefined>)[
      a
    ];
    if (k) kinds.add(k);
  }
  return [...kinds].map((kind) => ({
    kind,
    glyph: HUMAN_KIND_GLYPHS[kind],
    label: HUMAN_KIND_LABELS[kind],
  }));
}

// チップ（10px の太字）の寸法。SVG の text は幅を測らずに置くので、全角 10px・半角 6px の目安で並べる。
export const CHIP_H = 14;
const CHIP_PAD = 5;
const CHIP_GAP = 3;
const chipW = (t: string) =>
  Array.from(t).reduce((s, ch) => s + (ch.charCodeAt(0) > 0xff ? 10 : 6), 0);

export interface PlacedChip {
  chip: HumanChip;
  // 画面に出す語。収まらないときは「…」で切る（全部の語は読み上げの名前にある）
  label: string;
  // チップの行の左端からの位置と、チップの幅
  x: number;
  w: number;
}

// チップを幅 room（px）の 1 行に左から詰めて並べる。入らない分は、最後に入れる 1 つの語を「…」で切り（3 文字以上残るとき）、
// それも入らなければ出さない。
export function fitChips(
  chips: HumanChip[],
  room: number,
): { items: PlacedChip[]; width: number } {
  const items: PlacedChip[] = [];
  let used = 0;
  for (const chip of chips) {
    const at = used === 0 ? 0 : used + CHIP_GAP;
    const base = 2 * CHIP_PAD + chipW(chip.glyph) + CHIP_GAP;
    const full = base + chipW(chip.label);
    if (at + full <= room) {
      items.push({ chip, label: chip.label, x: at, w: full });
      used = at + full;
      continue;
    }
    const chars = Math.min(
      Array.from(chip.label).length,
      Math.floor((room - at - base) / 10),
    );
    if (chars >= 3) {
      const label = clip(chip.label, chars);
      const w = base + chipW(label);
      items.push({ chip, label, x: at, w });
      used = at + w;
    }
    break;
  }
  return { items, width: used };
}

// チップの語だけ（読み上げの名前に使う。記号は読み上げない）。
export const humanActionLabels = (n: GraphNode): string[] =>
  humanChips(n).map((c) => c.label);

export type DependencyBasis = "prerequisite" | "conflict";
export type RelationKind = "depends_on" | "parent_of";

// 課題どうしの関係 1 本。depends_on は from が先行・to が後続（edge に登録の記録を持つ）、
// parent_of は from が親・to が子。
export interface ChallengeRelation {
  from: string;
  kind: RelationKind;
  to: string;
  edge?: GraphEdge;
}

// API の応答を関係の並びにする。親は node.parent_id から、依存は edges から。
export function relationsOf(g: GraphResponse): ChallengeRelation[] {
  const rels: ChallengeRelation[] = [];
  for (const n of g.nodes)
    if (n.parent_id !== null)
      rels.push({ from: n.parent_id, kind: "parent_of", to: n.id });
  for (const e of g.edges)
    rels.push({ from: e.from, kind: "depends_on", to: e.to, edge: e });
  return rels;
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

// --- 終わった課題（完了・取り下げ）の既定の隠し方と、「ほかの課題」のまとめ方 ---

export const isFinished = (c: { category: string }): boolean =>
  c.category === "done" || c.category === "withdrawn";

// 「ほかの課題」の大枠は課題ではない疑似の大枠。開閉の集合（expanded）と配置では、この名前で親と同じに扱う。
export const OTHERS_ID = "others";
export const OTHERS_LABEL = "ほかの課題";

// グラフと一覧に出す課題の ID（入力の並びのまま）。既定では終わった課題を出さないが、動いている課題の前提
// （その課題へ向かう依存の先行）と、動いている課題の親（祖先を含む）になっている終わった課題だけは残す。
// 残した終わった前提が親の中にあるときも、入れ子を保つために祖先を残す。showAll なら全部を出す。
export function visibleIds(
  nodes: GraphNode[],
  relations: ChallengeRelation[],
  showAll: boolean,
): string[] {
  if (showAll) return nodes.map((n) => n.id);
  const all = new Set(nodes.map((n) => n.id));
  const rels = validRelations(all, relations);
  const live = new Set(nodes.filter((n) => !isFinished(n)).map((n) => n.id));
  const keep = new Set(live);
  for (const r of rels)
    if (r.kind === "depends_on" && live.has(r.to)) keep.add(r.from);
  const pm = parentMap(all, rels);
  for (const id of [...keep]) for (const a of ancestorsOf(id, pm)) keep.add(a);
  return nodes.filter((n) => keep.has(n.id)).map((n) => n.id);
}

// --- 注目の課題と「ほかの課題」 ---

// ③で前に出す優先度。どの優先度から前に出すかは、Tom#2 の見直しの後に決める仮定（いまは P0 だけ）。
export const FOCUS_PRIORITIES: readonly string[] = ["P0"];

// 注目に入った理由。human・working・urgent は課題そのものが起点に当たったもの（①②③）、
// linked は起点とつながりのまとまりが同じもの（④）。
export type FocusReason = "human" | "working" | "urgent" | "linked";

// 課題そのものが注目の起点に当たるか（①②③の順で最初に当たったもの）。終わった課題（完了・取り下げ）は当たらない。
export function seedReason(n: GraphNode): FocusReason | null {
  if (isFinished(n)) return null;
  // ① 人間の操作待ち
  if (n.next_human_actions.length > 0) return "human";
  // ② 着手中・検証中・実行中・親のエージェントが動く番（親が直接行っている課題を「ほかの課題」に隠さない）
  if (
    n.state === "in_progress" ||
    n.state === "verifying" ||
    n.running ||
    n.category === "parent_agent"
  )
    return "working";
  // ③ 分類待ち・計画待ちのうち、前に出す優先度
  if (
    (n.state === "unclassified" || n.state === "classified") &&
    n.priority !== null &&
    FOCUS_PRIORITIES.includes(n.priority)
  )
    return "urgent";
  return null;
}

// 表示中の課題を、依存の線（前提・後続・衝突）と親子の線（親と子の両方向）でつながるまとまりに分ける。
// 表示している終わった課題を通る線もつながりに数える（画面に描く線と同じ）。隠れた課題につながる関係は数えない。
// 返すのは課題の ID → まとまりの代表の ID。
function componentsOf(
  ids: string[],
  relations: ChallengeRelation[],
): Map<string, string> {
  const set = new Set(ids);
  const rels = validRelations(set, relations);
  const adj = new Map<string, string[]>(ids.map((id) => [id, []]));
  const link = (a: string, b: string) => {
    adj.get(a)?.push(b);
    adj.get(b)?.push(a);
  };
  for (const r of rels) link(r.from, r.to);
  const pm = parentMap(set, rels);
  for (const [child, parent] of pm) link(child, parent);
  const comp = new Map<string, string>();
  for (const id of [...ids].sort(compareIds)) {
    if (comp.has(id)) continue;
    const stack = [id];
    comp.set(id, id);
    while (stack.length > 0) {
      const cur = stack.pop() as string;
      for (const nb of adj.get(cur) ?? [])
        if (!comp.has(nb)) {
          comp.set(nb, id);
          stack.push(nb);
        }
    }
  }
  return comp;
}

// 前に出す課題（注目）と、その理由。ids は表示中の課題で、隠れた課題につながる関係は数えない。
// ①②③の起点（seedReason）と、④として、起点と依存の線・親子の線で（段数に関係なく）つながる動いている課題。
// つながりのまとまりごと前に出す。終わった課題は注目に入らない（まとまりの中で前に残る。othersOf）。
export function focusOf(
  ids: string[],
  relations: ChallengeRelation[],
  nodes: GraphNode[],
): Map<string, FocusReason> {
  const set = new Set(ids);
  const comp = componentsOf(ids, relations);
  const live = new Map<string, GraphNode>();
  for (const n of nodes) if (set.has(n.id) && !isFinished(n)) live.set(n.id, n);
  const out = new Map<string, FocusReason>();
  const hot = new Set<string>();
  for (const n of live.values()) {
    const why = seedReason(n);
    if (why) {
      out.set(n.id, why);
      hot.add(comp.get(n.id) as string);
    }
  }
  for (const id of live.keys())
    if (!out.has(id) && hot.has(comp.get(id) as string)) out.set(id, "linked");
  return out;
}

// 「ほかの課題」の大枠に入れる課題（ID の数の昇順）: どの線でも注目の課題とつながらない、注目でない課題。
// つながりのまとまりのうち、起点を含まず、動いている課題を 1 つ以上含むものを、まとまりごと入れる
// （残した終わった課題も、属するまとまりが大枠なら大枠の中。注目のまとまりなら前）。
// 終わった課題だけのまとまり（全部を見る切り替えのとき）は大枠に入れない。
export function othersOf(
  ids: string[],
  relations: ChallengeRelation[],
  nodes: GraphNode[],
): string[] {
  const set = new Set(ids);
  const comp = componentsOf(ids, relations);
  const focus = focusOf(ids, relations, nodes);
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const hot = new Set<string>();
  const hasLive = new Set<string>();
  for (const id of ids) {
    const n = byId.get(id);
    if (!n || !set.has(id)) continue;
    const c = comp.get(id) as string;
    if (focus.has(id)) hot.add(c);
    if (!isFinished(n)) hasLive.add(c);
  }
  return ids
    .filter((id) => {
      const c = comp.get(id) as string;
      return byId.has(id) && hasLive.has(c) && !hot.has(c);
    })
    .sort(compareIds);
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
  // 開いた親の大枠の見出しの高さ（位置づけを広げると伸びる）。node は d.headH のまま
  headH: number;
}

export interface LayoutEdge {
  from: string;
  to: string;
  path: string;
  // 線の中ほど（理由の札を置く位置）
  mid: { x: number; y: number };
  // この線が表す依存（畳んだ親へつなぎ替えると複数になる）。理由と種類はここから引く
  relations: ChallengeRelation[];
  basis: DependencyBasis;
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
// headOf は開いた親ごとの見出しの高さ（無ければ d.headH）。大枠の幅は見出しの高さに左右されない。
export function layoutGraph(
  ids: string[],
  relations: ChallengeRelation[],
  d: Dims,
  expanded: Set<string> = new Set(),
  headOf: (id: string) => number = () => d.headH,
  // 「ほかの課題」の大枠へまとめる課題（大枠の中でも、仲間どうしの親子の入れ子と依存の線は保つ）。
  // 大枠の開閉は expanded の OTHERS_ID。大枠の外の親の子は、親の入れ子から外して大枠へ移す。
  isOther: (id: string) => boolean = () => false,
): Layout {
  const order = [...ids].sort(compareIds);
  const otherSet = new Set(order.filter(isOther));
  const rels = validRelations(new Set(order), relations).filter(
    // 大枠の内と外をまたぐ親子は入れ子にしない（依存の線はまたいでも描く）
    (r) =>
      r.kind !== "parent_of" || otherSet.has(r.from) === otherSet.has(r.to),
  );
  const pm = parentMap(new Set(order), rels);
  const kids = childrenMap(pm);
  const deps = rels.filter((r) => r.kind === "depends_on");
  let hasCycle = false;

  // 大枠 container（null は一番外）の直接の中身のうち、課題 id を含むもの。
  // 「ほかの課題」の大枠の直接の中身は、親を持たない大枠の仲間。
  const containerOf = (x: string): string | null =>
    pm.get(x) ?? (otherSet.has(x) ? OTHERS_ID : null);
  const memberIn = (id: string, container: string | null): string | null => {
    for (const x of [id, ...ancestorsOf(id, pm)])
      if (containerOf(x) === container) return x;
    return null;
  };
  const otherRoots = order.filter((id) => otherSet.has(id) && !pm.has(id));

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
              h: headOf(m) + inner.h + d.inset,
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
    // 「ほかの課題」の大枠との間だけに依存を持つ課題も、関係の無い課題ではない（線を持つ）
    if (container === null)
      for (const r of deps) {
        const a = memberIn(r.from, null);
        const b = memberIn(r.to, null);
        if (a && !b && otherSet.has(r.to)) linked.add(a);
        if (b && !a && otherSet.has(r.from)) linked.add(b);
      }
    const isolatedAll =
      container === null
        ? members.filter((m) => !linked.has(m) && !kids.has(m))
        : [];
    const others = container === null ? otherRoots : [];
    const isolated = isolatedAll;
    const graphIds = members.filter((m) => !isolatedAll.includes(m));

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
    // 「ほかの課題」: 開いていれば大枠（中に課題を並べる）、畳んでいれば 1 つの箱
    let otherBox: {
      w: number;
      h: number;
      inner: Arranged | null;
    } | null = null;
    if (others.length > 0) {
      const inner = expanded.has(OTHERS_ID)
        ? arrange(others, OTHERS_ID, limit - 2 * d.inset)
        : null;
      otherBox = inner
        ? {
            w: Math.max(d.nodeW, inner.w + 2 * d.inset),
            h: headOf(OTHERS_ID) + inner.h + d.inset,
            inner,
          }
        : { w: d.nodeW, h: d.nodeH, inner: null };
    }
    const w = Math.max(
      d.nodeW,
      otherBox?.w ?? 0,
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
    if (otherBox) {
      if (rows.length > 0) y += d.gapY;
      items.push({
        id: OTHERS_ID,
        x: (w - otherBox.w) / 2,
        y,
        w: otherBox.w,
        h: otherBox.h,
        layer: -1,
        inner: otherBox.inner,
      });
      y += otherBox.h;
    }
    let labelY: number | null = null;
    if (isoRows.length > 0) {
      if (rows.length > 0 || otherBox) y += d.gapY;
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
    order.filter((id) => !pm.has(id) && !otherSet.has(id)),
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
        headH: p.inner ? headOf(p.id) : d.headH,
      };
      if (p.inner) {
        groups.push(box);
        flat(
          p.inner,
          box.x + (p.w - p.inner.w) / 2,
          box.y + box.headH,
          depth + 1,
        );
      } else nodes.push(box);
    }
  };
  flat(root, d.pad, d.pad, 0);
  groups.sort((a, b) => a.depth - b.depth);

  const at = new Map([...groups, ...nodes].map((b) => [b.id, b]));
  // 線の端の箱。畳んだ「ほかの課題」の中の課題は大枠の箱、畳んだ親の中の課題は一番外の畳んだ親の箱
  const boxOf = (id: string) =>
    otherSet.has(id) && !expanded.has(OTHERS_ID)
      ? OTHERS_ID
      : visibleBoxOf(id, pm, expanded);
  const seen = new Map<string, LayoutEdge>();
  const edges: LayoutEdge[] = [];
  for (const r of deps) {
    const a = boxOf(r.from);
    const b = boxOf(r.to);
    // 同じ箱の中・自分の大枠との依存は線にしない（入れ子の位置で読める）
    if (
      a === b ||
      ancestorsOf(a, pm).includes(b) ||
      ancestorsOf(b, pm).includes(a)
    )
      continue;
    const key = `${a}:${b}`;
    const merged = seen.get(key);
    if (merged) {
      merged.relations.push(r);
      merged.basis = basisOf(merged.relations);
      continue;
    }
    const ba = at.get(a);
    const bb = at.get(b);
    if (!ba || !bb) continue;
    const x1 = ba.x + ba.w / 2;
    const y1 = ba.y + ba.h;
    const x2 = bb.x + bb.w / 2;
    const y2 = bb.y;
    const bend = Math.max(d.gapY / 2, Math.abs(y2 - y1) / 2);
    const edge: LayoutEdge = {
      from: a,
      to: b,
      path: `M ${x1} ${y1} C ${x1} ${y1 + bend}, ${x2} ${y2 - bend}, ${x2} ${y2}`,
      // 制御点が両端から対称なので、t = 0.5 の点は両端の中点
      mid: { x: (x1 + x2) / 2, y: (y1 + y2) / 2 },
      relations: [r],
      basis: basisOf([r]),
    };
    seen.set(key, edge);
    edges.push(edge);
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

// 依存 1 つの種類。API の kind が conflict のときだけ衝突、それ以外は先行の成果を使う。
export const basisOfRelation = (r: ChallengeRelation): DependencyBasis =>
  r.edge?.kind === "conflict" ? "conflict" : "prerequisite";

// 線が表す依存の種類。1 つでも先行の成果を使う依存があれば、終わらないと始められない線として描く。
// すべてが衝突のための順番なら、衝突の線として描く。
export function basisOf(rels: ChallengeRelation[]): DependencyBasis {
  return rels.length > 0 && rels.every((r) => basisOfRelation(r) === "conflict")
    ? "conflict"
    : "prerequisite";
}

export const BASIS_LABELS: Record<DependencyBasis, string> = {
  prerequisite: "終わらないと始められない",
  conflict: "同時に走らせると衝突する",
};

const VIA_LABELS: Record<string, string> = {
  plan: "計画の判断で登録",
  manual: "端末のコマンドで登録",
  unknown: "登録の経路は不明",
};
const VIA_SHORT: Record<string, string> = {
  plan: "計画の判断",
  manual: "端末のコマンド",
  unknown: "経路不明",
};

const pad2 = (n: number) => String(n).padStart(2, "0");
// 登録の日時を「月/日 時:分」にする。読めない値はそのまま返す。
export function shortAt(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return `${pad2(d.getMonth() + 1)}/${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// 依存 1 つの登録の経路と日時・満たされているか（種類は含めない）。理由の文は core の記録から UI が組み立てる。
export function reasonDetail(r: ChallengeRelation): string {
  const e = r.edge;
  if (!e) return "";
  return `${VIA_LABELS[e.registered_via] ?? VIA_LABELS.unknown}（${shortAt(e.registered_at)}）。${e.satisfied ? "満たされている" : "まだ満たされていない"}`;
}

// 依存 1 つを人が読む一文にする（種類・登録の経路と日時・満たされているか）。
export function reasonText(r: ChallengeRelation): string {
  const detail = reasonDetail(r);
  const kind = BASIS_LABELS[basisOfRelation(r)];
  return detail === "" ? kind : `${kind}。${detail}`;
}

// 線の札に出す 2 行（1 行目: 種類、2 行目: 登録の経路・日時・満たされているか）。
export function chipLines(r: ChallengeRelation): [string, string] {
  const e = r.edge;
  const kind = BASIS_LABELS[basisOfRelation(r)];
  if (!e) return [kind, ""];
  return [
    kind,
    `${VIA_SHORT[e.registered_via] ?? VIA_SHORT.unknown}・${shortAt(e.registered_at)}・${e.satisfied ? "満たされている" : "満たされていない"}`,
  ];
}

// 線の札の文字（読み上げ・テスト用に 1 行へつないだもの）。
export const chipText = (r: ChallengeRelation): string =>
  chipLines(r)
    .filter((l) => l !== "")
    .join("・");

// 幅 maxPx に収まるように行へ分ける（SVG の text は折り返さないため）。1 文字の幅は目安で、全角を full、
// 半角をその 0.55 倍と見る（幅を測らずに置く）。
export function wrapByWidth(s: string, maxPx: number, full: number): string[] {
  const out: string[] = [];
  let cur = "";
  let w = 0;
  for (const ch of Array.from(s)) {
    const cw = ch.charCodeAt(0) > 0xff ? full : full * 0.55;
    if (cur !== "" && w + cw > maxPx) {
      out.push(cur);
      cur = "";
      w = 0;
    }
    cur += ch;
    w += cw;
  }
  if (cur !== "") out.push(cur);
  return out;
}

// wrapByWidth の先頭 n 行。続きがあれば最後の行の最後の 1 文字を「…」に替える。
export function clipLines(
  s: string,
  maxPx: number,
  full: number,
  n: number,
): string[] {
  const lines = wrapByWidth(s, maxPx, full);
  if (lines.length <= n) return lines;
  const kept = lines.slice(0, n);
  kept[n - 1] = `${Array.from(kept[n - 1] ?? "")
    .slice(0, -1)
    .join("")}…`;
  return kept;
}
