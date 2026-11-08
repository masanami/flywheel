// 大きなまとまりの偽のデータ（flywheel#470）。課題 100 件が依存と親子の線でほぼ 1 つにつながる形と、
// 同じ 100 件から線をすべて抜いた形を、決定的に作る。mock モードで `?mock=large` を付けたときだけ読み込む
// （既定の偽のデータ alpha・beta・gamma と、それを前提にしたテストは変えない）。
//
// 形:
// - 親 9 件（C-1〜C-9）。親ごとに子 9 件（C-10〜C-90）。子の 1 件目は、子の 8・9 件目の親（入れ子の 2 段）。
// - 親の中の依存: 子の 2 → 3 → 4 → 5 の連鎖（前提）、2 → 6（衝突）、4 → 7（前提）。
// - 親をまたぐ依存: 親 g の子 5 → 親 g+1 の子 2（前提）。9 つの親が 1 本の長い連鎖でつながり、何段もの依存になる。
// - 関係を持たない課題 10 件（C-91〜C-100）。完了・取り下げ・起点（人間の操作待ち・着手中・P0 の分類待ち）を混ぜ、
//   起点でない動いている課題を置かない（「ほかの課題」が空になる形を確かめるため）。
// - 状態: 連鎖の前のほう（親 1〜3）は終わった課題が多く、親 4・5 に着手中・実行中、親 6〜9 は分類済・計画承認待ちが多い。
//   親 6〜9 の多くは起点ではなく、起点から何段も先で推移的に注目に入る。
//   先行が完了していない依存の後続は、分類済か計画承認待ちに直す（mock/graph.test.ts の規則）。

import type { MockDep } from "./graph.ts";
import type { MockChallenge, MockWorkspace } from "./store.ts";

export const LARGE_PARENTS = 9;
export const LARGE_CHILDREN = 9;
export const LARGE_LOOSE = 10;
export const LARGE_TOTAL =
  LARGE_PARENTS + LARGE_PARENTS * LARGE_CHILDREN + LARGE_LOOSE;

// 親 g（0 始まり）の子 i（0 始まり）の ID
export const childId = (g: number, i: number): string =>
  `C-${LARGE_PARENTS + 1 + g * LARGE_CHILDREN + i}`;
export const parentId = (g: number): string => `C-${g + 1}`;
export const looseId = (k: number): string =>
  `C-${LARGE_PARENTS + LARGE_PARENTS * LARGE_CHILDREN + 1 + k}`;

export interface LargeSpec {
  id: string;
  title: string;
  status: string;
  priority: string | null;
  running?: boolean;
  unresponsive?: boolean;
  operation?: boolean;
}

export interface LargeShape {
  specs: LargeSpec[];
  parents: Record<string, string>;
  deps: MockDep[];
}

const WAITING = ["classified", "awaiting_plan_approval"];

// 子の状態の下書き（先行との矛盾は後で直す）。親 g・子 i ごとに決める。
function draftChild(g: number, i: number): Omit<LargeSpec, "id" | "title"> {
  if (g < 3) {
    // 前のほう: 連鎖の部分は完了。衝突の相手は取り下げ。子 7 は完了確認待ち（人間の操作待ち）
    if (i >= 1 && i <= 4) return { status: "done", priority: "P2" };
    if (i === 5) return { status: "withdrawn", priority: null };
    if (i === 6)
      return g === 1
        ? {
            status: "awaiting_completion_approval",
            priority: "P1",
            operation: true,
          }
        : { status: "done", priority: "P2" };
    if (i === 7) return { status: "done", priority: "P3" };
    // 親 1 の孫は着手中（終わった連鎖が隠れても、親 1 のまとまりに起点が残る）
    if (i === 8)
      return g === 0
        ? { status: "in_progress", priority: "P2" }
        : { status: "unclassified", priority: null };
    return { status: "classified", priority: "P2" };
  }
  if (g === 3 || g === 4) {
    // 着手中・実行中（連鎖の先頭は前の親の完了の後ろ）
    if (i === 1)
      return {
        status: "in_progress",
        priority: "P1",
        running: true,
      };
    if (i === 2) return { status: "awaiting_plan_approval", priority: "P1" };
    // 孫は連鎖の外なので先行に縛られない。親 5 の孫は実行中で、しきい値を超えて記録が無い
    if (i === 8)
      return {
        status: "verifying",
        priority: "P1",
        running: true,
        unresponsive: g === 4,
      };
    return { status: "classified", priority: i % 2 === 0 ? "P2" : "P3" };
  }
  // 後ろのほう: 起点でない分類済・未分類が中心。ところどころ計画承認待ち（人間の操作待ち）
  if (i === 3 && g === 7)
    return { status: "awaiting_plan_approval", priority: "P2" };
  if (i === 0) return { status: "unclassified", priority: null };
  if (i === 7) return { status: "withdrawn", priority: null };
  return { status: "classified", priority: i % 3 === 0 ? "P3" : "P2" };
}

const LOOSE: Omit<LargeSpec, "id" | "title">[] = [
  { status: "done", priority: "P2" },
  { status: "done", priority: "P3" },
  { status: "done", priority: null },
  { status: "done", priority: "P1" },
  { status: "withdrawn", priority: null },
  { status: "withdrawn", priority: "P2" },
  { status: "unclassified", priority: "P0" },
  { status: "awaiting_human", priority: "P1" },
  { status: "in_progress", priority: "P1", running: true },
  { status: "awaiting_plan_approval", priority: "P2" },
];

const AREAS = [
  "認証",
  "検索",
  "決済",
  "通知",
  "監視",
  "レポート",
  "デプロイ",
  "権限",
  "課金",
];
const TASKS = [
  "の土台を整える",
  "のスキーマを変える",
  "の API を足す",
  "の画面を直す",
  "の移行を流す",
  "の負荷を測る",
  "の文言をそろえる",
  "の古い経路を消す",
  "の手順書を書く",
];

// 線でつながる形。connected が false なら、同じ課題から親子と依存をすべて抜く。
export function largeShape(connected: boolean): LargeShape {
  const specs: LargeSpec[] = [];
  const parents: Record<string, string> = {};
  const deps: MockDep[] = [];
  let n = 0;
  const dep = (from: string, to: string, kind: MockDep["kind"]) => {
    const vias: MockDep["via"][] = ["plan", "manual", "unknown"];
    deps.push({
      from,
      to,
      kind,
      via: vias[n % 3] ?? "plan",
      minutes: 60 * ++n,
    });
  };
  for (let g = 0; g < LARGE_PARENTS; g++) {
    specs.push({
      id: parentId(g),
      title: `${AREAS[g]}の改修（まとめ）`,
      // 親 5 だけ質問への回答待ち（人間の操作待ち）
      status: g === 5 ? "awaiting_human" : "classified",
      priority: "P1",
    });
    for (let i = 0; i < LARGE_CHILDREN; i++) {
      specs.push({
        id: childId(g, i),
        title: `${AREAS[g]}${TASKS[i]}`,
        ...draftChild(g, i),
      });
      parents[childId(g, i)] = i >= 7 ? childId(g, 0) : parentId(g);
    }
    dep(childId(g, 1), childId(g, 2), "prerequisite");
    dep(childId(g, 2), childId(g, 3), "prerequisite");
    dep(childId(g, 3), childId(g, 4), "prerequisite");
    dep(childId(g, 1), childId(g, 5), "conflict");
    dep(childId(g, 3), childId(g, 6), "prerequisite");
    if (g + 1 < LARGE_PARENTS)
      dep(childId(g, 4), childId(g + 1, 1), "prerequisite");
  }
  LOOSE.forEach((s, k) =>
    specs.push({ id: looseId(k), title: `単発の作業 ${k + 1}`, ...s }),
  );

  // 先行が完了していない依存の後続は、着手できないので分類済か計画承認待ちに直す。
  // ID の順に直すと、直した課題が後続の先行になる場合も先に確定している（依存はどれも ID の小さい方から大きい方）。
  const byId = new Map(specs.map((s) => [s.id, s]));
  const idNum = (id: string) => Number(id.slice(2));
  for (const d of [...deps].sort((a, b) => idNum(a.to) - idNum(b.to))) {
    const from = byId.get(d.from);
    const to = byId.get(d.to);
    if (!from || !to || from.status === "done" || WAITING.includes(to.status))
      continue;
    to.status =
      idNum(to.id) % 4 === 0 ? "awaiting_plan_approval" : "classified";
    to.running = false;
    to.unresponsive = false;
    to.operation = false;
  }
  return connected
    ? { specs, parents, deps }
    : { specs, parents: {}, deps: [] };
}

// mock の課題の作り方（store.ts の mk 等）を受け取り、ワークスペースを組み立てる（循環 import を避ける）。
export function largeWorkspace(
  name: string,
  connected: boolean,
  build: (s: LargeSpec) => MockChallenge,
  base: Omit<MockWorkspace, "items" | "graph" | "meta">,
): MockWorkspace {
  const shape = largeShape(connected);
  return {
    ...base,
    meta: { error: null, name, path: `/mock/${name}`, state: "ok" },
    items: shape.specs.map(build),
    graph: { parents: shape.parents, deps: shape.deps },
  };
}
