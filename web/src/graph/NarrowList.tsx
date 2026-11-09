// 幅 480px 以下の関係グラフの代わりの一覧。SVG のグラフは狭い画面で読めないので、親ごとに畳んだ一覧にし、
// 各課題に区分と、先行・後続を文字で付ける。親は押すと開閉でき、課題を押すと詳細のモーダルを開く（グラフと同じ）。

import {
  type Inside,
  type NodeInfo,
  accessibleName,
  insideText,
} from "./GraphCanvas.tsx";
import {
  type ChallengeRelation,
  OTHERS_ID,
  OTHERS_LABEL,
  categoryLabel,
  compareIds,
  conflictNotesOf,
  humanChips,
  neighborsOf,
} from "./model.ts";

export interface NarrowListProps {
  // 表示する課題の ID（絞り込み後）
  ids: string[];
  infos: Map<string, NodeInfo>;
  // 親ごとの子（絞り込み後の課題だけ）
  kids: Map<string, string[]>;
  // 親を持たない課題（一番外）の ID（「ほかの課題」にまとめる課題は含めない）
  roots: string[];
  // 「ほかの課題」にまとめる課題の ID（注目でない動いている課題。判定は model.ts の othersOf）
  others: string[];
  relations: ChallengeRelation[];
  expanded: Set<string>;
  inside: (id: string) => Inside | null;
  onToggle: (id: string) => void;
  onOpen: (id: string) => void;
}

const idsText = (ids: string[]) => (ids.length > 0 ? ids.join("、") : "なし");

// 衝突の相手を、この課題から見た役割で文字にする。向きの無い組は「衝突の可能性」（どちらを先にするかが未定）。
const CONFLICT_LABELS = {
  yields: "衝突のため後回し",
  preferred: "衝突で先に委譲",
  undirected: "衝突の可能性（順序は未定）",
} as const;

function Row({ id, p }: { id: string; p: NarrowListProps }) {
  const info = p.infos.get(id);
  if (!info) return null;
  const children = p.kids.get(id) ?? [];
  const isParent = children.length > 0;
  const open = isParent && p.expanded.has(id);
  const near = neighborsOf(id, p.relations);
  const folded = isParent && !open ? p.inside(id) : null;
  const chips = humanChips(info.node);
  const conflicts = conflictNotesOf(id, p.relations);
  return (
    <li
      className="graph-list-item"
      data-category={info.category}
      data-unresponsive={info.stale || undefined}
      data-active={info.active || undefined}
    >
      <div className="graph-list-row">
        {isParent ? (
          <button
            type="button"
            className="graph-list-toggle"
            aria-expanded={open}
            aria-label={
              open ? `${id} を畳む` : `${id} を開く（子 ${children.length} 件）`
            }
            onClick={() => p.onToggle(id)}
          >
            {open ? "−" : "+"}
          </button>
        ) : null}
        <button
          type="button"
          className="graph-list-open"
          aria-haspopup="dialog"
          aria-label={accessibleName(info, isParent ? "親" : null)}
          onClick={() => p.onOpen(id)}
        >
          <span className="graph-swatch" aria-hidden="true" />
          <b>{id}</b> {info.node.title}
        </button>
        {chips.length > 0 ? (
          // 人間の操作待ちのチップ（記号と語）。行の右上に右寄せで並べ、入らないチップは次の行へ折り返す。
          // それでも語が入らないほど狭いときに切り詰めても、語は title（ホバーで出る）に残る
          <span className="graph-list-human" data-category="human">
            {chips.map((c) => (
              <span
                key={c.kind}
                className="graph-human-chip"
                data-kind={c.kind}
                title={c.label}
              >
                <span className="graph-human-chip-glyph" aria-hidden="true">
                  {c.glyph}
                </span>
                <span className="graph-human-chip-label">{c.label}</span>
              </span>
            ))}
          </span>
        ) : null}
      </div>
      <div className="graph-list-meta">
        <span className="graph-list-cat">{categoryLabel(info.category)}</span>
        {info.active ? <span>▶ 実施中</span> : null}
        {info.stale ? (
          <span className="graph-list-stale">⚠ 更新なし（要確認）</span>
        ) : null}
        {folded ? <span>{insideText(folded)}</span> : null}
      </div>
      <div className="graph-list-rel">
        <span>先行: {idsText(near.predecessors)}</span>
        <span>後続: {idsText(near.successors)}</span>
        {conflicts.map((c) => (
          <span key={`${c.kind}:${c.id}`} data-conflict={c.kind}>
            {CONFLICT_LABELS[c.kind]}: {c.id}
          </span>
        ))}
      </div>
      {open ? (
        <ul className="graph-list-children">
          {[...children].sort(compareIds).map((c) => (
            <Row key={c} id={c} p={p} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

// 「ほかの課題」の見出し。課題ではない疑似の大枠で、押すと開閉するだけ（詳細のモーダルは開かない）。
function OthersGroup({ p }: { p: NarrowListProps }) {
  const open = p.expanded.has(OTHERS_ID);
  // 大枠の直下に並べるのは親を持たない仲間だけ（子は親の下に入れ子で出る）
  const childIds = new Set([...p.kids.values()].flat());
  const ds = p.others.map((id) => p.infos.get(id));
  const folded = {
    total: ds.length,
    active: ds.filter((d) => d?.active).length,
    human: ds.filter((d) => d?.category === "human").length,
  };
  return (
    <li className="graph-list-item graph-list-others" data-category="other">
      <div className="graph-list-row">
        <button
          type="button"
          className="graph-list-toggle"
          aria-expanded={open}
          aria-label={
            open
              ? `${OTHERS_LABEL}を畳む（${folded.total} 件）`
              : `${OTHERS_LABEL}を開く（${folded.total} 件）`
          }
          onClick={() => p.onToggle(OTHERS_ID)}
        >
          {open ? "−" : "+"}
        </button>
        <b className="graph-list-others-title">
          {OTHERS_LABEL} {folded.total}件
        </b>
      </div>
      {open ? (
        <ul className="graph-list-children">
          {[...p.others]
            .filter((c) => !childIds.has(c))
            .sort(compareIds)
            .map((c) => (
              <Row key={c} id={c} p={p} />
            ))}
        </ul>
      ) : (
        <div className="graph-list-meta">
          <span>{insideText(folded, "畳んだ課題")}</span>
        </div>
      )}
    </li>
  );
}

export function NarrowList(p: NarrowListProps) {
  return (
    <ul aria-label="課題の一覧（親ごと）" className="graph-list">
      {[...p.roots].sort(compareIds).map((id) => (
        <Row key={id} id={id} p={p} />
      ))}
      {p.others.length > 0 ? <OthersGroup p={p} /> : null}
    </ul>
  );
}
