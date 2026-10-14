// 関係グラフの案（flywheel#176）の SVG の描画。親課題は大枠（開いたとき）か重ねた箱（畳んだとき）で示し、
// 依存だけを線で結ぶ。どの課題を選んでいるか・薄くするかは画面（GraphScreen）が決めて渡す。

import type { KeyboardEvent } from "react";
import type { PartyWait } from "../../api/proposed.ts";
import type { ChallengeCard } from "../../api/types.gen.ts";
import {
  CATEGORIES,
  type Category,
  type Dims,
  type Layout,
  type LayoutBox,
  STALE_LABEL,
  STALE_LONG,
  clip,
} from "./model.ts";

export interface NodeInfo {
  card: ChallengeCard;
  category: Category;
  repos: string[];
  marks: string[];
  // run が動いている（実施中）・CI の完了待ち・自動では解けない待ちの相手
  active: boolean;
  ci: boolean;
  party: PartyWait | null;
}

// 畳んだ親の中に入っている課題の数（子・孫…を含む）。
export interface Inside {
  total: number;
  active: number;
  human: number;
}

export type Dimensions = Dims & { titleChars: number; metaChars: number };

export interface CanvasProps {
  ws: string;
  layout: Layout;
  dims: Dimensions;
  narrow: boolean;
  infos: Map<string, NodeInfo>;
  inside: (id: string) => Inside | null;
  selected: string | null;
  relationOf: (id: string) => string | undefined;
  dimmed: (id: string) => boolean;
  edgeLit: (from: string, to: string) => boolean;
  onSelect: (id: string) => void;
  onOpen: (id: string) => void;
  onToggle: (id: string) => void;
}

const TOGGLE = 20;

const categoryLabel = (c: Category) =>
  CATEGORIES.find((k) => k.key === c)?.label ?? c;

function insideText(n: Inside): string {
  const parts = [
    n.active > 0 ? `実施中 ${n.active}` : "",
    n.human > 0 ? `人間の操作待ち ${n.human}` : "",
  ].filter(Boolean);
  return `畳んだ子 ${n.total} 件${parts.length > 0 ? `（${parts.join("・")}）` : ""}`;
}

export function accessibleName(
  i: NodeInfo,
  extra: string | null = null,
): string {
  const c = i.card;
  const notes = [
    categoryLabel(i.category),
    ...i.marks,
    ...(extra ? [extra] : []),
  ];
  return `${c.id} ${c.title}（${notes.join("・")}）`;
}

const onActivate = (fn: () => void) => (ev: KeyboardEvent) => {
  if (ev.key === "Enter" || ev.key === " ") {
    ev.preventDefault();
    fn();
  }
};

function Toggle({
  id,
  open,
  count,
  x,
  y,
  onToggle,
}: {
  id: string;
  open: boolean;
  count: number;
  x: number;
  y: number;
  onToggle: (id: string) => void;
}) {
  const act = () => onToggle(id);
  return (
    <g
      // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
      role="button"
      tabIndex={0}
      aria-label={open ? `${id} を畳む` : `${id} を開く（子 ${count} 件）`}
      aria-expanded={open}
      className="graph-toggle-btn"
      transform={`translate(${x} ${y})`}
      onClick={(ev) => {
        ev.stopPropagation();
        act();
      }}
      onKeyDown={onActivate(act)}
    >
      <rect width={TOGGLE} height={TOGGLE} rx={4} />
      <text x={TOGGLE / 2} y={TOGGLE / 2 + 5} textAnchor="middle">
        {open ? "−" : "+"}
      </text>
    </g>
  );
}

// 畳んだ親の箱の 3 行目に置く、中の件数の札。実施中と人間の操作待ちだけを色つきで出す。
function InsideBadges({
  n,
  narrow,
  y,
}: {
  n: Inside;
  narrow: boolean;
  y: number;
}) {
  const badges: { key: string; cat: Category; text: string }[] = [
    { key: "all", cat: "other", text: `子${n.total}` },
  ];
  if (n.active > 0)
    badges.push({
      key: "active",
      cat: "running",
      text: narrow ? `▶${n.active}` : `▶実施中${n.active}`,
    });
  if (n.human > 0)
    badges.push({
      key: "human",
      cat: "human",
      text: narrow ? `!${n.human}` : `!人間待ち${n.human}`,
    });
  // 10px の文字の幅の目安（全角 10px・半角 6px）。SVG の text は幅を測らずに置く
  const textW = (t: string) =>
    Array.from(t).reduce((s, ch) => s + (ch.charCodeAt(0) > 0xff ? 10 : 6), 0);
  let x = 11;
  return (
    <g className="graph-badges">
      {badges.map((b) => {
        const w = textW(b.text) + 10;
        const at = x;
        x += w + 4;
        return (
          <g
            key={b.key}
            className="graph-badge"
            data-category={b.cat}
            data-badge={b.key}
            transform={`translate(${at} ${y})`}
          >
            <rect width={w} height={narrow ? 13 : 14} rx={7} />
            <text x={5} y={narrow ? 10 : 10.5}>
              {b.text}
            </text>
          </g>
        );
      })}
    </g>
  );
}

function NodeBox({
  box,
  p,
}: {
  box: LayoutBox;
  p: CanvasProps;
}) {
  const i = p.infos.get(box.id);
  if (!i) return null;
  const { dims, narrow } = p;
  const c = i.card;
  const inside = p.inside(box.id);
  const selfActive = i.active;
  const active = selfActive || (inside?.active ?? 0) > 0;
  const stale = c.card.modifiers.includes("unresponsive");
  // 更新なしは 3 行目を札に使う（旧 board の「⚠ 更新なし（要確認）」と同じく、赤の太字で出す）
  const marks = i.marks.filter((m) => m !== STALE_LABEL);
  const third = narrow
    ? (marks[0] ?? "")
    : [...marks, i.repos.map((r) => r.split("/").pop()).join("・")]
        .filter(Boolean)
        .join(" / ");
  const select = () => p.onSelect(box.id);
  return (
    <g transform={`translate(${box.x} ${box.y})`}>
      <g
        // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
        role="button"
        tabIndex={0}
        aria-label={accessibleName(i, inside ? insideText(inside) : null)}
        aria-pressed={box.id === p.selected}
        className="graph-node"
        data-category={i.category}
        data-relation={p.relationOf(box.id)}
        data-dim={p.dimmed(box.id) || undefined}
        data-active={selfActive || undefined}
        data-active-inside={(inside?.active ?? 0) > 0 || undefined}
        data-human-inside={(inside?.human ?? 0) > 0 || undefined}
        data-collapsed={inside ? true : undefined}
        data-unresponsive={stale || undefined}
        onClick={select}
        onDoubleClick={() => p.onOpen(box.id)}
        onKeyDown={onActivate(select)}
      >
        <title>{accessibleName(i, inside ? insideText(inside) : null)}</title>
        {active ? (
          <rect
            className="graph-node-halo"
            x={-4}
            y={-4}
            width={box.w + 8}
            height={box.h + 8}
            rx={9}
          />
        ) : null}
        {inside ? (
          <rect
            className="graph-node-stack"
            x={4}
            y={4}
            width={box.w}
            height={box.h}
            rx={6}
          />
        ) : null}
        <rect className="graph-node-box" width={box.w} height={box.h} rx={6} />
        <rect className="graph-node-stripe" width={5} height={box.h} rx={2} />
        <text x={11} y={narrow ? 16 : 18} className="graph-node-id">
          {c.id}
          {c.priority ? ` ${c.priority}` : ""}
        </text>
        {selfActive ? (
          <text
            x={box.w - (inside ? TOGGLE + 10 : 8)}
            y={narrow ? 16 : 18}
            textAnchor="end"
            className="graph-node-active-tag"
          >
            {narrow ? "▶" : "▶ 実施中"}
          </text>
        ) : null}
        {i.ci ? (
          // CI 待ちの小さな札。実行中の区分の色のまま、光の輪は付けない（run は動いていない）
          <g
            className="graph-ci-badge"
            transform={`translate(${box.w - (inside ? TOGGLE + 10 : 8) - 22} ${narrow ? 5 : 7})`}
          >
            <rect width={22} height={14} rx={4} />
            <text x={11} y={10.5} textAnchor="middle">
              CI
            </text>
          </g>
        ) : null}
        <text x={11} y={narrow ? 31 : 35} className="graph-node-title">
          {clip(c.title, dims.titleChars)}
        </text>
        {inside ? (
          <InsideBadges n={inside} narrow={narrow} y={narrow ? 36 : 41} />
        ) : stale ? (
          <text x={11} y={narrow ? 45 : 51} className="graph-node-stale">
            {narrow ? STALE_LABEL : STALE_LONG}
          </text>
        ) : third ? (
          <text x={11} y={narrow ? 45 : 51} className="graph-node-meta">
            {clip(third, dims.metaChars)}
          </text>
        ) : null}
      </g>
      {inside ? (
        <Toggle
          id={box.id}
          open={false}
          count={inside.total}
          x={box.w - TOGGLE - 4}
          y={4}
          onToggle={p.onToggle}
        />
      ) : null}
    </g>
  );
}

// 開いた親の大枠。見出し（親課題そのもの）を押すと親を選び、右上のボタンで畳む。
function GroupFrame({ box, p }: { box: LayoutBox; p: CanvasProps }) {
  const i = p.infos.get(box.id);
  if (!i) return null;
  const { dims, narrow } = p;
  const c = i.card;
  const selfActive = i.active;
  const select = () => p.onSelect(box.id);
  const chars = Math.max(4, Math.floor((box.w - 90) / (narrow ? 12 : 13)));
  return (
    <g
      transform={`translate(${box.x} ${box.y})`}
      className="graph-group"
      data-category={i.category}
      data-depth={box.depth}
      data-dim={p.dimmed(box.id) || undefined}
    >
      <rect className="graph-group-box" width={box.w} height={box.h} rx={8} />
      <g
        // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
        role="button"
        tabIndex={0}
        aria-label={accessibleName(i, "開いた親")}
        aria-pressed={box.id === p.selected}
        className="graph-group-head"
        data-category={i.category}
        data-relation={p.relationOf(box.id)}
        data-active={selfActive || undefined}
        onClick={select}
        onDoubleClick={() => p.onOpen(box.id)}
        onKeyDown={onActivate(select)}
      >
        <title>{accessibleName(i, "開いた親")}</title>
        <rect
          className="graph-group-head-box"
          width={box.w}
          height={dims.headH}
          rx={8}
        />
        <rect
          className="graph-node-stripe"
          width={5}
          height={dims.headH}
          rx={2}
        />
        <text x={11} y={dims.headH / 2 + 4.5} className="graph-group-title">
          <tspan className="graph-node-id">{c.id}</tspan> {clip(c.title, chars)}
          {selfActive ? " ▶ 実施中" : ""}
        </text>
      </g>
      <Toggle
        id={box.id}
        open
        count={0}
        x={box.w - TOGGLE - 5}
        y={(dims.headH - TOGGLE) / 2}
        onToggle={p.onToggle}
      />
    </g>
  );
}

export function GraphCanvas(p: CanvasProps) {
  const { layout, dims, narrow, ws } = p;
  return (
    <svg
      // biome-ignore lint/a11y/useSemanticElements: 課題のノード（ボタン）を束ねる SVG で、fieldset に置き換えられない
      role="group"
      aria-label={`${ws} の課題の関係グラフ`}
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      width={layout.width}
      className={narrow ? "graph-svg is-narrow" : "graph-svg"}
    >
      <defs>
        <marker
          id="graph-arrow"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" className="graph-arrow" />
        </marker>
      </defs>
      {layout.groups.map((g) => (
        <GroupFrame key={g.id} box={g} p={p} />
      ))}
      {layout.edges.map((e) => {
        const lit = p.selected !== null && p.edgeLit(e.from, e.to);
        return (
          <path
            key={`${e.from}:${e.to}`}
            d={e.path}
            className="graph-edge"
            data-from={e.from}
            data-to={e.to}
            data-lit={lit || undefined}
            data-dim={
              (p.selected !== null && !lit) ||
              p.dimmed(e.from) ||
              p.dimmed(e.to) ||
              undefined
            }
            markerEnd="url(#graph-arrow)"
          />
        );
      })}
      {layout.isolatedLabelY !== null ? (
        <text
          x={dims.pad}
          y={layout.isolatedLabelY + dims.labelH * 0.65}
          className="graph-section-label"
        >
          関係の無い課題
        </text>
      ) : null}
      {layout.nodes.map((n) => (
        <NodeBox key={n.id} box={n} p={p} />
      ))}
    </svg>
  );
}
