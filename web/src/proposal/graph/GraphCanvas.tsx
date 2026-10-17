// 関係グラフの案（flywheel#176）の SVG の描画。親課題は大枠（開いたとき）か重ねた箱（畳んだとき）で示し、
// 依存だけを線で結ぶ。どの課題を選んでいるか・薄くするかは画面（GraphScreen）が決めて渡す。
// 2026-10-14 オーナー: ノードは ID と題名だけでは読めない。開いた大枠の見出しには「位置づけ」、依存の線には理由を持たせる。
// 2026-10-17 オーナー: ノードには題名を出し、カーソルを重ねたとき（キーボードで移ったとき）に「一言で」を吹き出しで出す。
// 課題を押すとボードと同じ詳細のモーダルを開く。先行・後続の強調も重ねたとき・移ったときに出す。

import type { KeyboardEvent } from "react";
import type { PartyWait } from "../../api/proposed.ts";
import type { ChallengeCard } from "../../api/types.gen.ts";
import {
  AUTO_CHECK_TEXT,
  CATEGORIES,
  type Category,
  type Dims,
  type Layout,
  type LayoutBox,
  type LayoutEdge,
  STALE_LABEL,
  STALE_LONG,
  clip,
  clipLines,
  reasonText,
  wrapByWidth,
} from "./model.ts";

export interface NodeInfo {
  card: ChallengeCard;
  category: Category;
  repos: string[];
  marks: string[];
  // run が動いている（実施中）・自動の確認待ち（waiting_ci）・自動では解けない待ちの相手
  active: boolean;
  autoCheck: boolean;
  party: PartyWait | null;
}

// 畳んだ親の中に入っている課題の数（子・孫…を含む）。
export interface Inside {
  total: number;
  active: number;
  human: number;
}

// titleLines はノードの題名の行数。ctxCharW・ctxLineH は位置づけの全角 1 文字の幅の目安と行の高さ。
// edgeChars は依存の理由の札の文字数。calloutW・calloutLines は一言での吹き出しの最小の幅と行数の上限。
export type Dimensions = Dims & {
  titleLines: number;
  metaChars: number;
  ctxCharW: number;
  ctxLineH: number;
  edgeChars: number;
  calloutW: number;
  calloutLines: number;
};

export const edgeKey = (e: { from: string; to: string }) => `${e.from}:${e.to}`;

export interface CanvasProps {
  ws: string;
  layout: Layout;
  dims: Dimensions;
  narrow: boolean;
  infos: Map<string, NodeInfo>;
  inside: (id: string) => Inside | null;
  // カーソルを重ねた・キーボードで移った課題（先行・後続の強調の起点。一言での吹き出しもこの課題に出す）
  focused: string | null;
  relationOf: (id: string) => string | undefined;
  dimmed: (id: string) => boolean;
  edgeLit: (from: string, to: string) => boolean;
  // 課題を押した（詳細のモーダルを開く）
  onOpen: (id: string) => void;
  onToggle: (id: string) => void;
  // 重ねた・移った課題の通知
  onHoverNode: (id: string | null) => void;
  // 位置づけを全文まで広げた大枠か
  contextOpen: (id: string) => boolean;
  onToggleContext: (id: string) => void;
  selectedEdge: string | null;
  // 理由の札を出す線（選んだ線・重ねた線）
  edgeLabeled: (e: LayoutEdge) => boolean;
  onSelectEdge: (key: string) => void;
  onHoverEdge: (key: string | null) => void;
}

const TOGGLE = 20;
// 位置づけの右に置く「続き」の文字の幅
const MORE_W = 40;

// 開いた大枠の見出しの欄に出す一言でと位置づけを、見出しの幅に収まるように行へ分ける（それぞれ 1 行目の頭に
// 「一言で:」「位置づけ:」を付ける）。右に「続き」の文字の幅を空ける。
export function headLines(
  headline: string | null,
  context: string | null,
  width: number,
  dims: Dimensions,
): { headline: string[]; context: string[] } {
  const wrap = (label: string, s: string | null) =>
    s
      ? wrapByWidth(
          `${label}: ${s}`,
          Math.max(4 * dims.ctxCharW, width - 16 - MORE_W),
          dims.ctxCharW,
        )
      : [];
  return {
    headline: wrap("一言で", headline),
    context: wrap("位置づけ", context),
  };
}

// 開いた大枠の見出しの高さ。一言では常に全行、位置づけは畳んだら 1 行・広げたら全行の分だけ伸ばす。
export function groupHeadHeight(
  headline: string | null,
  context: string | null,
  width: number,
  dims: Dimensions,
  open: boolean,
): number {
  const l = headLines(headline, context, width, dims);
  const rows =
    l.headline.length +
    (open ? l.context.length : Math.min(1, l.context.length));
  return rows === 0 ? dims.headH : dims.headH + dims.ctxLineH * rows + 4;
}

// 10.5px の文字の幅の目安（全角 10.5px・半角 6px）。SVG の text は幅を測らずに置く
const labelW = (t: string) =>
  Array.from(t).reduce((s, ch) => s + (ch.charCodeAt(0) > 0xff ? 10.5 : 6), 0);

// 依存の線の名前と、重ねたときの吹き出し。課題のノードの名前（「C-12 …」）と前方一致しないよう「依存」で始める。
export function edgeName(e: LayoutEdge): string {
  const why = e.relations
    .map((r) =>
      e.relations.length > 1 || r.from !== e.from || r.to !== e.to
        ? `${r.from} → ${r.to}: ${reasonText(r)}`
        : reasonText(r),
    )
    .join("／");
  return `依存 ${e.from} → ${e.to}（${why}）`;
}

// 重ねた・移った課題の一言での吹き出し。箱の下（入らなければ上）に置き、画面の幅に収める。
// 一言では切らずに折り返し、行数の上限を超えたときだけ「…」で切る。押す操作は下へ通す。
// 自動では解けない外部待ちの相手と理由・自動の確認を待っていることもここに出す（以前は右のパネルに出していた。
// 詳細のモーダルは試作の案の印を知らず、自動の確認待ちも利用分野の語で出すため）。
function HeadlineCallout({
  id,
  p,
}: {
  id: string;
  p: CanvasProps;
}) {
  const info = p.infos.get(id);
  const c = info?.card;
  // 開いた大枠の一言では見出しの欄に出すので、吹き出しは課題のノード（畳んだ親を含む）だけに出す
  const box = p.layout.nodes.find((b) => b.id === id);
  if (!c || !box) return null;
  const { dims, layout, narrow } = p;
  const font = narrow ? 12 : 13;
  const w = Math.min(layout.width - 4, Math.max(box.w, dims.calloutW));
  const lineH = font + 5;
  const sections = [
    c.card.headline ? { label: "一言で", text: c.card.headline } : null,
    info.party
      ? {
          label: "外部待ち",
          text: `${info.party.party}（${info.party.reason}）`,
        }
      : null,
    info.autoCheck ? { label: "自動の確認", text: AUTO_CHECK_TEXT } : null,
  ]
    .filter((x) => x !== null)
    .map((x) => ({
      ...x,
      lines: clipLines(x.text, w - 20, font, dims.calloutLines),
    }));
  if (sections.length === 0) return null;
  // 区分ごとに、見出しの行（16px）と文の行を積む
  let top = 0;
  const placed = sections.map((x) => {
    const at = top;
    top += 22 + x.lines.length * lineH;
    return { ...x, at };
  });
  const h = top + 6;
  const bottom = box.y + box.h;
  const y = bottom + 6 + h <= layout.height ? bottom + 6 : box.y - 6 - h;
  const x = Math.max(2, Math.min(box.x, layout.width - 2 - w));
  return (
    <g
      className="graph-callout"
      data-callout={id}
      transform={`translate(${x} ${Math.max(2, y)})`}
    >
      <rect width={w} height={h} rx={6} />
      {placed.map((x) => (
        <g key={x.label} data-section={x.label}>
          <text x={10} y={x.at + 16} className="graph-callout-label">
            {x.label}
          </text>
          <text x={10} y={x.at + 22 + lineH - 4} className="graph-callout-text">
            {x.lines.map((line, k) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 幅で切った行の並びで、行の位置そのものが鍵
              <tspan key={k} x={10} dy={k === 0 ? 0 : lineH}>
                {line}
              </tspan>
            ))}
          </text>
        </g>
      ))}
    </g>
  );
}

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
  const c = i.card;
  const inside = p.inside(box.id);
  const selfActive = i.active;
  const active = selfActive || (inside?.active ?? 0) > 0;
  const stale = c.card.modifiers.includes("unresponsive");
  const select = () => p.onOpen(box.id);
  return (
    <g transform={`translate(${box.x} ${box.y})`}>
      <g
        // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
        role="button"
        tabIndex={0}
        aria-label={accessibleName(i, inside ? insideText(inside) : null)}
        aria-haspopup="dialog"
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
        onKeyDown={onActivate(select)}
        onMouseEnter={() => p.onHoverNode(box.id)}
        onMouseLeave={() => p.onHoverNode(null)}
        onFocus={() => p.onHoverNode(box.id)}
        onBlur={() => p.onHoverNode(null)}
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
        <NodeLines box={box} p={p} i={i} inside={inside} />
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

// ノードの 3 段（ID と優先度・題名・印かリポジトリ）。
function NodeLines({
  box,
  p,
  i,
  inside,
}: {
  box: LayoutBox;
  p: CanvasProps;
  i: NodeInfo;
  inside: Inside | null;
}) {
  const { dims, narrow } = p;
  const c = i.card;
  const selfActive = i.active;
  const stale = c.card.modifiers.includes("unresponsive");
  // 更新なしは 3 行目を札に使う（旧 board の「⚠ 更新なし（要確認）」と同じく、赤の太字で出す）
  const marks = i.marks.filter((m) => m !== STALE_LABEL);
  // 題名の行が増えた分だけ、3 段目（印・リポジトリ・中の件数の札）を下げる
  const shift = (dims.titleLines - 1) * 16;
  const lines = clipLines(
    c.title,
    box.w - 18,
    narrow ? 12 : 13,
    dims.titleLines,
  );
  const third = narrow
    ? (marks[0] ?? "")
    : [...marks, i.repos.map((r) => r.split("/").pop()).join("・")]
        .filter(Boolean)
        .join(" / ");
  return (
    <>
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
      <text x={11} y={narrow ? 31 : 35} className="graph-node-title">
        {lines.map((line, k) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 幅で切った行の並びで、行の位置そのものが鍵
          <tspan key={k} x={11} dy={k === 0 ? 0 : 16}>
            {line}
          </tspan>
        ))}
      </text>
      {inside ? (
        <InsideBadges
          n={inside}
          narrow={narrow}
          y={(narrow ? 36 : 41) + shift}
        />
      ) : stale ? (
        <text
          x={11}
          y={(narrow ? 45 : 51) + shift}
          className="graph-node-stale"
        >
          {narrow ? STALE_LABEL : STALE_LONG}
        </text>
      ) : third ? (
        <text x={11} y={(narrow ? 45 : 51) + shift} className="graph-node-meta">
          {clip(third, dims.metaChars)}
        </text>
      ) : null}
    </>
  );
}

// 開いた親の大枠。見出し（親課題そのもの）を押すと親を選び、右上のボタンで畳む。
// 見出しは題名と、その下の欄に一言で・位置づけの順に出す（2026-10-17 オーナー: 大枠の一言では吹き出しでなくこの欄）。
// 一言では常に全文を出し、位置づけは 1 行に収まらなければ畳んで始め、「続き」で広げる。
function GroupFrame({ box, p }: { box: LayoutBox; p: CanvasProps }) {
  const i = p.infos.get(box.id);
  if (!i) return null;
  const { dims, narrow } = p;
  const c = i.card;
  const selfActive = i.active;
  const select = () => p.onOpen(box.id);
  const chars = Math.max(4, Math.floor((box.w - 90) / (narrow ? 12 : 13)));
  const { headline: hl, context: lines } = headLines(
    c.card.headline,
    c.card.context,
    box.w,
    dims,
  );
  const ctxOpen = p.contextOpen(box.id);
  const shown = ctxOpen ? lines : lines.slice(0, 1);
  const foldable = lines.length > 1;
  // 位置づけは一言での行の下から始める
  const ctxTop = dims.headH + dims.ctxLineH * hl.length;
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
        aria-haspopup="dialog"
        className="graph-group-head"
        data-category={i.category}
        data-relation={p.relationOf(box.id)}
        data-active={selfActive || undefined}
        onClick={select}
        onKeyDown={onActivate(select)}
        onMouseEnter={() => p.onHoverNode(box.id)}
        onMouseLeave={() => p.onHoverNode(null)}
        onFocus={() => p.onHoverNode(box.id)}
        onBlur={() => p.onHoverNode(null)}
      >
        <title>{accessibleName(i, "開いた親")}</title>
        <rect
          className="graph-group-head-box"
          width={box.w}
          height={box.headH}
          rx={8}
        />
        <rect
          className="graph-node-stripe"
          width={5}
          height={box.headH}
          rx={2}
        />
        <text x={11} y={dims.headH / 2 + 4.5} className="graph-group-title">
          <tspan className="graph-node-id">{c.id}</tspan>
          {` ${clip(c.title, chars)}`}
          {selfActive ? " ▶ 実施中" : ""}
        </text>
        {hl.map((line, k) => (
          <text
            // biome-ignore lint/suspicious/noArrayIndexKey: 幅で切った行の並びで、行の位置そのものが鍵
            key={k}
            x={11}
            y={dims.headH + dims.ctxLineH * (k + 1) - 4}
            className="graph-group-headline"
          >
            {line}
          </text>
        ))}
        {shown.map((line, k) => (
          <text
            // biome-ignore lint/suspicious/noArrayIndexKey: 文字数で切った行の並びで、行の位置そのものが鍵
            key={k}
            x={11}
            y={ctxTop + dims.ctxLineH * (k + 1) - 4}
            className="graph-group-context"
          >
            {/* 畳んだときは 1 行目の最後の 1 文字を「…」に替え、続きがあることを示す */}
            {!ctxOpen && foldable
              ? clip(`${line}…`, Array.from(line).length)
              : line}
          </text>
        ))}
      </g>
      {foldable ? (
        <MoreButton
          id={box.id}
          open={ctxOpen}
          x={box.w - MORE_W - 6}
          y={ctxTop}
          h={dims.ctxLineH}
          onToggle={p.onToggleContext}
        />
      ) : null}
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

// 大枠の位置づけを広げる・畳む操作。見た目はボタンでなく文字だけ（リンクのような下線）で、キーボードでも押せる。
// 名前は課題のノード（「C-14 …」）と前方一致しないよう、ID を後ろに置く。
function MoreButton({
  id,
  open,
  x,
  y,
  h,
  onToggle,
}: {
  id: string;
  open: boolean;
  x: number;
  y: number;
  h: number;
  onToggle: (id: string) => void;
}) {
  const act = () => onToggle(id);
  return (
    <g
      // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
      role="button"
      tabIndex={0}
      aria-label={
        open ? `位置づけを畳む（${id}）` : `位置づけの続きを読む（${id}）`
      }
      aria-expanded={open}
      className="graph-more-btn"
      transform={`translate(${x} ${y})`}
      onClick={(ev) => {
        ev.stopPropagation();
        act();
      }}
      onKeyDown={onActivate(act)}
    >
      {/* 文字だけでは押せる範囲が狭いので、透明な当たり判定を敷く */}
      <rect className="graph-more-hit" width={MORE_W} height={h} />
      <text x={MORE_W} y={h - 4} textAnchor="end">
        {open ? "▴ 畳む" : "▾ 続き"}
      </text>
    </g>
  );
}

interface PlacedLabel {
  e: LayoutEdge;
  text: string;
  x: number;
  y: number;
  w: number;
}

const LABEL_H = 18;

// 理由の札を線の中ほどに置く。同じ後続へ合流する線は中ほどが近く札が重なるので、重なる札は下へずらす。
export function placeLabels(edges: LayoutEdge[], chars: number): PlacedLabel[] {
  const placed: PlacedLabel[] = [];
  const sorted = [...edges].sort(
    (a, b) => a.mid.y - b.mid.y || a.mid.x - b.mid.x,
  );
  for (const e of sorted) {
    const first = e.relations[0];
    if (!first) continue;
    const more = e.relations.length - 1;
    const text = `${clip(reasonText(first), chars)}${more > 0 ? ` ほか${more}件` : ""}`;
    const w = labelW(text) + 12;
    const x = e.mid.x - w / 2;
    let y = e.mid.y - LABEL_H / 2;
    const hits = (q: PlacedLabel) =>
      x < q.x + q.w && q.x < x + w && y < q.y + LABEL_H && q.y < y + LABEL_H;
    while (placed.some(hits)) y += LABEL_H + 2;
    placed.push({ e, text, x, y, w });
  }
  return placed;
}

// 依存の線の理由の札。押せない（線の当たり判定の上に重ねるが、押す操作は下へ通す）。
function EdgeLabel({ l }: { l: PlacedLabel }) {
  const { e, text, x, y, w } = l;
  return (
    <g
      className="graph-edge-label"
      data-from={e.from}
      data-to={e.to}
      data-basis={e.basis}
      transform={`translate(${x} ${y})`}
    >
      <rect width={w} height={LABEL_H} rx={LABEL_H / 2} />
      <text x={6} y={12.5}>
        {text}
      </text>
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
        {/* 衝突のための順番の線は、先端を中抜きの矢じりにして実線の矢印と見分ける */}
        <marker
          id="graph-arrow-conflict"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path
            d="M 1 1 L 9 5 L 1 9 z"
            className="graph-arrow graph-arrow-open"
          />
        </marker>
      </defs>
      {layout.groups.map((g) => (
        <GroupFrame key={g.id} box={g} p={p} />
      ))}
      {layout.edges.map((e) => {
        const key = edgeKey(e);
        const picked = p.selectedEdge === key;
        const lit = picked || (p.focused !== null && p.edgeLit(e.from, e.to));
        const focus = p.focused !== null || p.selectedEdge !== null;
        const conflict = e.basis === "conflict";
        const pick = () => p.onSelectEdge(key);
        const name = edgeName(e);
        return (
          <g key={key} className="graph-edge-wrap">
            <path
              d={e.path}
              className="graph-edge"
              data-from={e.from}
              data-to={e.to}
              data-basis={e.basis}
              data-lit={lit || undefined}
              data-dim={
                (focus && !lit) ||
                p.dimmed(e.from) ||
                p.dimmed(e.to) ||
                undefined
              }
              markerEnd={
                conflict ? "url(#graph-arrow-conflict)" : "url(#graph-arrow)"
              }
            />
            {/* 細い線は押しにくいので、太い透明な線を重ねて当たり判定にする */}
            <path
              // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
              role="button"
              tabIndex={0}
              aria-label={name}
              aria-pressed={picked}
              d={e.path}
              className="graph-edge-hit"
              onClick={pick}
              onKeyDown={onActivate(pick)}
              onMouseEnter={() => p.onHoverEdge(key)}
              onMouseLeave={() => p.onHoverEdge(null)}
              onFocus={() => p.onHoverEdge(key)}
              onBlur={() => p.onHoverEdge(null)}
            >
              <title>{name}</title>
            </path>
          </g>
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
      {placeLabels(layout.edges.filter(p.edgeLabeled), dims.edgeChars).map(
        (l) => (
          <EdgeLabel key={edgeKey(l.e)} l={l} />
        ),
      )}
      {p.focused ? <HeadlineCallout id={p.focused} p={p} /> : null}
    </svg>
  );
}
