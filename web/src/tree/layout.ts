// タスクのツリーの並べ方。列は前提の深さ（そのタスクまでのいちばん長い前提の連鎖の長さ）で決める。
// 深さと線の本数は core の値ではなく、受け取った depends_on からの純粋な配置の計算だけ（区分・件数は導かない）。

import type { TreeTask } from "../api/types.gen.ts";

// ツリーの部品が使う項目だけの型。計画の承認の画面のタスク（id・費用を持たない）も、そのまま渡せる。
export type TreeNode = Pick<
  TreeTask,
  "key" | "title" | "depends_on" | "phase"
> & { id?: string | null; progress?: string | null };

export const BOX_W = 190;
export const BOX_H = 54;
export const COL_GAP = 64;
export const ROW_GAP = 14;
export const PAD = 12;

export interface PlacedTask {
  task: TreeNode;
  col: number;
  row: number;
  x: number;
  y: number;
}

export interface Edge {
  from: string;
  to: string;
  d: string;
}

export interface TreeLayout {
  boxes: PlacedTask[];
  edges: Edge[];
  width: number;
  height: number;
}

// その課題の tasks に無い key の前提は、無いものとして扱う。循環は、たどり直しで打ち切る（深さは増やさない）。
export function layoutTree(tasks: readonly TreeNode[]): TreeLayout {
  const byKey = new Map(tasks.map((t) => [t.key, t] as const));
  const depth = new Map<string, number>();
  const visiting = new Set<string>();
  const depthOf = (t: TreeNode): number => {
    const known = depth.get(t.key);
    if (known !== undefined) return known;
    if (visiting.has(t.key)) return 0;
    visiting.add(t.key);
    let d = 0;
    for (const k of t.depends_on) {
      const p = byKey.get(k);
      if (p) d = Math.max(d, depthOf(p) + 1);
    }
    visiting.delete(t.key);
    depth.set(t.key, d);
    return d;
  };

  const rowsInCol: number[] = [];
  const boxes: PlacedTask[] = tasks.map((task) => {
    const col = depthOf(task);
    const row = rowsInCol[col] ?? 0;
    rowsInCol[col] = row + 1;
    return {
      task,
      col,
      row,
      x: PAD + col * (BOX_W + COL_GAP),
      y: PAD + row * (BOX_H + ROW_GAP),
    };
  });

  const at = new Map(boxes.map((b) => [b.task.key, b] as const));
  const edges: Edge[] = [];
  for (const b of boxes) {
    for (const k of b.task.depends_on) {
      const p = at.get(k);
      if (!p) continue;
      const x1 = p.x + BOX_W;
      const y1 = p.y + BOX_H / 2;
      const x2 = b.x;
      const y2 = b.y + BOX_H / 2;
      const mid = (x1 + x2) / 2;
      edges.push({
        from: p.task.key,
        to: b.task.key,
        d: `M${x1},${y1} C${mid},${y1} ${mid},${y2} ${x2},${y2}`,
      });
    }
  }

  // 循環があると、使われない列ができる。空の列も 1 列として数え、高さは空を 0 件として測る。
  const cols = rowsInCol.length;
  const maxRows = Math.max(0, ...Array.from(rowsInCol, (n) => n ?? 0));
  return {
    boxes,
    edges,
    width: cols === 0 ? 0 : PAD * 2 + cols * BOX_W + (cols - 1) * COL_GAP,
    height:
      maxRows === 0 ? 0 : PAD * 2 + maxRows * BOX_H + (maxRows - 1) * ROW_GAP,
  };
}
