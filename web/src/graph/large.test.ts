import { expect, it } from "vitest";
import { childId, looseId } from "../mock/large.ts";
import { graphOf, seedLarge } from "../mock/store.ts";
import { WIDE } from "./GraphView.tsx";
import {
  OTHERS_ID,
  childrenMap,
  focusOf,
  isFinished,
  layoutGraph,
  othersOf,
  parentMap,
  relationsOf,
  validRelations,
  visibleIds,
} from "./model.ts";

// flywheel#470: 課題 100 件がほぼ 1 つにつながる偽のデータ（mock/large.ts）で、注目のまとまり・「ほかの課題」・配置を固定する。
const load = (name: "large" | "large-flat") => {
  const w = seedLarge()[name];
  if (!w) throw new Error(`${name} が無い`);
  const g = graphOf(w);
  const all = new Set(g.nodes.map((n) => n.id));
  return { g, rels: validRelations(all, relationsOf(g)) };
};

it("大きなまとまり: 課題は 100 件で、つながる形は「ほかの課題」が空になる（終わった課題を隠しても全部を出しても）", () => {
  const { g, rels } = load("large");
  expect(g.nodes).toHaveLength(100);
  for (const showAll of [false, true]) {
    const ids = visibleIds(g.nodes, rels, showAll);
    expect(othersOf(ids, rels, g.nodes)).toEqual([]);
    // 表示中の動いている課題は、どれも注目に入る（起点か、起点とつながる linked）
    const focus = focusOf(ids, rels, g.nodes);
    const live = g.nodes.filter((n) => ids.includes(n.id) && !isFinished(n));
    expect(live.filter((n) => !focus.has(n.id)).map((n) => n.id)).toEqual([]);
  }
});

it("大きなまとまり: 起点から何段も先の、起点でない課題も linked で前に出る（推移的な範囲の違いが出る）", () => {
  const { g, rels } = load("large");
  const ids = visibleIds(g.nodes, rels, false);
  const focus = focusOf(ids, rels, g.nodes);
  // 最後の親の連鎖の末尾・入れ子の孫。起点（親 4・5 の着手中など）から依存と親子で何段も先
  expect(focus.get(childId(8, 6))).toBe("linked");
  expect(focus.get(childId(8, 8))).toBe("linked");
  expect([...focus.values()].filter((r) => r === "linked").length).toBe(57);
});

it("同じ 100 件から線を抜いた形では、起点でない動いている課題が「ほかの課題」に入り、linked は出ない", () => {
  const { g, rels } = load("large-flat");
  expect(rels).toEqual([]);
  const ids = visibleIds(g.nodes, rels, false);
  const focus = focusOf(ids, rels, g.nodes);
  expect([...focus.values()]).not.toContain("linked");
  const others = othersOf(ids, rels, g.nodes);
  expect(others).toHaveLength(57);
  expect(others.filter((id) => focus.has(id))).toEqual([]);
  // 関係を持たない課題のうち起点のもの（P0 の分類待ち）は前に出る
  expect(focus.get(looseId(6))).toBe("urgent");
});

// 同じ大枠の直接の中身どうしが重ならず、どの箱も外の大枠からはみ出さない
const overlaps = (
  a: { x: number; y: number; w: number; h: number },
  b: { x: number; y: number; w: number; h: number },
) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

it("大きなまとまりの配置: 親を全部開いても畳んでも、箱が重ならず、大枠からはみ出さない", () => {
  const { g, rels } = load("large");
  for (const showAll of [false, true]) {
    const ids = visibleIds(g.nodes, rels, showAll);
    const pm = parentMap(new Set(ids), rels);
    const kids = childrenMap(pm);
    for (const expanded of [
      new Set<string>(),
      new Set([...kids.keys(), OTHERS_ID]),
    ]) {
      const l = layoutGraph(ids, rels, WIDE, expanded);
      const boxes = [...l.groups, ...l.nodes];
      expect(l.width).toBeLessThanOrEqual(WIDE.maxW + 2 * WIDE.pad);
      const outer = (id: string) => {
        const p = l.parents.get(id);
        return p && expanded.has(p) ? p : null;
      };
      for (const b of boxes) {
        const p = outer(b.id);
        const box = p ? boxes.find((x) => x.id === p) : undefined;
        if (box) {
          expect(b.x).toBeGreaterThanOrEqual(box.x);
          expect(b.x + b.w).toBeLessThanOrEqual(box.x + box.w);
          expect(b.y).toBeGreaterThanOrEqual(box.y + box.headH);
          expect(b.y + b.h).toBeLessThanOrEqual(box.y + box.h);
        }
        for (const c of boxes)
          if (c !== b && outer(c.id) === p)
            expect(overlaps(b, c), `${b.id} と ${c.id}`).toBe(false);
      }
    }
  }
});
