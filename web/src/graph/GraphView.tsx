import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api/client.ts";
import type { GraphResponse } from "../api/types.gen.ts";
import { CardDetailModal } from "../board/CardDetailModal.tsx";
import { toAgentBoard } from "../board/adapt.ts";
import type { LiveState } from "../live/useLive.ts";
import { RefreshKeyContext } from "../nav.tsx";
import {
  type Dimensions,
  GraphCanvas,
  type Inside,
  type NodeInfo,
  edgeKey,
  groupHeadHeight,
} from "./GraphCanvas.tsx";
import { NarrowList } from "./NarrowList.tsx";
import { WaitingReasons } from "./WaitingReasons.tsx";
import {
  BASIS_LABELS,
  CATEGORIES,
  type Category,
  OTHERS_ID,
  STALE_HINT,
  STALE_LABEL,
  ancestorsOf,
  categoryOf,
  childrenMap,
  clip,
  compareIds,
  conflictNotesOf,
  descendantsOf,
  downstreamOf,
  layoutGraph,
  othersOf,
  parentMap,
  relationsOf,
  upstreamOf,
  validRelations,
  visibleIds,
} from "./model.ts";
import "./graph.css";

// 2026-10-17 オーナー: カードはもう少し大きくてよい。題名を 2 行まで出せる幅にする。
export const WIDE: Dimensions = {
  nodeW: 200,
  nodeH: 76,
  gapX: 20,
  gapY: 40,
  pad: 12,
  labelH: 28,
  headH: 30,
  inset: 10,
  maxW: 860,
  titleLines: 2,
  ctxCharW: 11,
  ctxLineH: 16,
  edgeChars: 30,
  calloutW: 280,
  calloutLines: 4,
};

// 幅がこの値以下では、SVG のグラフの代わりに親ごとに畳んだ一覧を出す。
export const NARROW_QUERY = "(max-width: 480px)";

export function useNarrow(): boolean {
  const supported = typeof window.matchMedia === "function";
  const [narrow, setNarrow] = useState(
    () => supported && window.matchMedia(NARROW_QUERY).matches,
  );
  useEffect(() => {
    if (!supported) return;
    const m = window.matchMedia(NARROW_QUERY);
    const on = () => setNarrow(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [supported]);
  return narrow;
}

type GraphState =
  | { ws: string; data: GraphResponse; error?: undefined }
  | { ws: string; data?: undefined; error: string };

// 選んだワークスペースの関係グラフ（GET …/graph）を読む。ストアの変更の通知と 60 秒ごとの取り直し
// （refreshKey の変化）で読み直す。
function useGraph(ws: string | null, refreshKey: unknown) {
  const [state, setState] = useState<GraphState | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    if (!ws) return;
    let live = true;
    api.challengeGraph(ws).then(
      (data) => live && setState({ ws, data }),
      (e) =>
        // 取り直しの失敗では、読めていたグラフを消さない
        live &&
        setState((prev) =>
          prev?.ws === ws && prev.data ? prev : { ws, error: String(e) },
        ),
    );
    return () => {
      live = false;
    };
  }, [ws, refreshKey]);
  return state?.ws === ws ? state : null;
}

// 開いたときに開いておく親: 実施中か人間の操作待ちの子孫（孫を含む）を持つ親。
// 「ほかの課題」の大枠は常に畳んで始める（中に何があっても開かない）。
export function defaultOpen(
  infos: Map<string, NodeInfo>,
  kids: Map<string, string[]>,
): string[] {
  return [...kids.keys()]
    .filter((p) =>
      descendantsOf(p, kids).some((d) => {
        const i = infos.get(d);
        return (
          i &&
          (i.active || i.category === "human" || i.category === "parent_agent")
        );
      }),
    )
    .sort(compareIds);
}

// 選んだワークスペース 1 つ分のグラフ。ワークスペースを切り替えると作り直す（key）ので、絞り込みは持ち越さない。
// 課題を押すと、ボードのカードと同じ詳細のモーダルを開く。
export function GraphView({
  live,
  ws,
  openParents,
  onOpenParents,
}: {
  live: LiveState;
  ws: string | null;
  // 開いている親。undefined は「まだ決めていない」（最初に読めたときの既定を使う）。サーバには持たない
  openParents: string[] | undefined;
  onOpenParents: (ids: string[]) => void;
}) {
  const narrow = useNarrow();
  const dims = WIDE;
  // 押した依存の線（`from:to`。見えている箱どうし。理由の札を出したままにする）と、マウスを重ねた・フォーカスした線
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);
  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  // カーソルを重ねた・キーボードで移った課題（先行・後続の強調と一言での吹き出しの起点）
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  // 詳細のモーダルを開いている課題と、閉じたときにフォーカスを戻す要素（ボードのカードと同じ）
  const [detailId, setDetailId] = useState<string | null>(null);
  const trigger = useRef<Element | null>(null);
  const [contextOpen, setContextOpen] = useState<Set<string>>(new Set());
  const [catFilter, setCatFilter] = useState<Category | null>(null);
  // 終わった課題（完了・取り下げ）は既定で隠し、これを入れると全部を出す（サーバには持たない）
  const [showAll, setShowAll] = useState(false);
  const graph = useGraph(ws, live.lastUpdated);
  const wsData = ws ? live.data[ws] : undefined;

  const infos = useMemo(() => {
    const nodes = graph?.data?.nodes ?? [];
    const all = new Set(nodes.map((n) => n.id));
    const rels = validRelations(
      all,
      graph?.data ? relationsOf(graph.data) : [],
    );
    const m = new Map<string, NodeInfo>();
    for (const n of nodes)
      m.set(n.id, {
        node: n,
        category: categoryOf(n),
        active: n.running,
        stale: n.stale,
      });
    const pm = parentMap(all, rels);
    return { map: m, rels, pm, kids: childrenMap(pm) };
  }, [graph]);

  // 表示する課題（既定では終わった課題を隠す）と、そのうち「ほかの課題」にまとめる課題
  // （注目でない動いている課題。判定は model.ts の focusOf・othersOf）
  const view = useMemo(() => {
    const nodes = graph?.data?.nodes ?? [];
    const ids = visibleIds(nodes, infos.rels, showAll);
    return { ids, others: othersOf(ids, infos.rels, nodes) };
  }, [graph, infos, showAll]);
  const shownIds = view.ids;
  const othersIds = view.others;

  // 最初に読めたときの既定の開き方（「ほかの課題」の大枠は含めない）
  const initialOpen = useMemo(
    () => defaultOpen(infos.map, infos.kids),
    [infos],
  );
  // 最初に読めたとき、既定の開き方を覚える（以後の取り直しで開閉が勝手に変わらないように）
  useEffect(() => {
    if (graph?.data && openParents === undefined) onOpenParents(initialOpen);
  }, [graph, openParents, initialOpen, onOpenParents]);
  const expanded = new Set(openParents ?? initialOpen);

  const setOpen = (fn: (s: Set<string>) => Set<string>) =>
    onOpenParents([...fn(new Set(expanded))].sort(compareIds));
  const toggle = (id: string) =>
    setOpen((s) => {
      if (s.has(id)) s.delete(id);
      else s.add(id);
      return s;
    });
  const openDetail = (id: string) => {
    trigger.current = document.activeElement;
    setDetailId(id);
  };
  const closeDetail = () => {
    setDetailId(null);
    const t = trigger.current;
    if (t instanceof HTMLElement || t instanceof SVGElement) t.focus();
  };
  const toggleContext = (id: string) =>
    setContextOpen((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // 開いた大枠の見出しは一言でと位置づけの分だけ伸びる。行数は大枠の幅で決まり、幅は見出しの高さに左右されないので、
  // 一度配置して大枠の幅を得てから、見出しの高さを決めて配置し直す。
  const nodeOf = (id: string) => infos.map.get(id)?.node;
  const isOther = (id: string) => othersIds.includes(id);
  const headOf = (width: (id: string) => number) => (id: string) =>
    groupHeadHeight(
      nodeOf(id)?.headline ?? null,
      nodeOf(id)?.context ?? null,
      width(id),
      dims,
      contextOpen.has(id),
    );
  const first = layoutGraph(
    shownIds,
    infos.rels,
    dims,
    expanded,
    headOf(() => dims.maxW),
    isOther,
  );
  const widths = new Map(first.groups.map((g) => [g.id, g.w]));
  const layout = layoutGraph(
    shownIds,
    infos.rels,
    dims,
    expanded,
    headOf((id) => widths.get(id) ?? dims.maxW),
    isOther,
  );
  const pm = layout.parents;
  const kids = childrenMap(pm);
  const shownRels = validRelations(new Set(shownIds), infos.rels);
  const sel = hoverNode && shownIds.includes(hoverNode) ? hoverNode : null;
  const edgeOf = (key: string | null) =>
    key ? (layout.edges.find((e) => edgeKey(e) === key) ?? null) : null;
  const selEdge = edgeOf(selectedEdge);
  const up = new Set(sel ? upstreamOf(sel, shownRels) : []);
  const down = new Set(sel ? downstreamOf(sel, shownRels) : []);
  // 選んだ課題と衝突する課題（後回し・先に委譲・向きの無い組のすべて）。衝突の線もいっしょに濃くする
  const peers = new Set(
    sel ? conflictNotesOf(sel, shownRels).map((n) => n.id) : [],
  );
  const ancestors = new Set(sel ? ancestorsOf(sel, pm) : []);
  const inner = new Set(sel ? descendantsOf(sel, kids) : []);

  // 区分の件数は、いま出している課題の数（隠した終わった課題は数えない）
  const counts = new Map<Category, number>();
  for (const id of shownIds) {
    const c = infos.map.get(id)?.category;
    if (c) counts.set(c, (counts.get(c) ?? 0) + 1);
  }
  const running = [...infos.map.values()].filter((i) => i.active);

  // 箱が表す課題: 自分と、大枠（開いていても畳んでいても）の中の課題すべて。
  const membersOf = (id: string) =>
    id === OTHERS_ID ? othersIds : [id, ...descendantsOf(id, kids)];
  // 線を選んだときは、線の両端の箱（とその中の課題）だけを濃く残す
  const edgeEnds = new Set(
    selEdge ? [selEdge.from, selEdge.to].flatMap(membersOf) : [],
  );
  const relevant = (id: string) =>
    selEdge
      ? edgeEnds.has(id)
      : sel === null ||
        id === sel ||
        up.has(id) ||
        down.has(id) ||
        peers.has(id) ||
        ancestors.has(id) ||
        inner.has(id);
  const matches = (id: string) => {
    const i = infos.map.get(id);
    if (!i) return false;
    return !(catFilter && i.category !== catFilter);
  };
  const dimmed = (id: string) =>
    !membersOf(id).some((m) => matches(m) && relevant(m));
  const edgeLit = (from: string, to: string) =>
    membersOf(from).some((m) => sel !== null && relevant(m)) &&
    membersOf(to).some((m) => sel !== null && relevant(m));
  const relationOf = (id: string): string | undefined => {
    if (!sel) return undefined;
    if (id === sel) return "selected";
    if (down.has(id)) return "downstream";
    if (up.has(id)) return "upstream";
    if (ancestors.has(id) || inner.has(id)) return "family";
    if (peers.has(id)) return "conflict";
    return undefined;
  };
  const inside = (id: string): Inside | null => {
    // 「ほかの課題」は開いていても件数を返す（大枠の見出しの件数に使う）
    if (id === OTHERS_ID) {
      const ds = othersIds.map((d) => infos.map.get(d));
      return {
        total: ds.length,
        active: ds.filter((d) => d?.active).length,
        human: ds.filter((d) => d?.category === "human").length,
      };
    }
    if (!kids.has(id) || expanded.has(id)) return null;
    const ds = descendantsOf(id, kids).map((d) => infos.map.get(d));
    return {
      total: ds.length,
      active: ds.filter((d) => d?.active).length,
      human: ds.filter((d) => d?.category === "human").length,
    };
  };

  const workspace = live.workspaces.find((w) => w.name === ws);
  const detailChallenge =
    detailId && workspace
      ? toAgentBoard(workspace, wsData).challenges.find(
          (c) => c.id === detailId,
        )
      : undefined;

  let body: ReactNode;
  if (!ws) {
    body = <p>読めるワークスペースがありません。</p>;
  } else if (graph?.error) {
    body = <p role="alert">関係の読み込みに失敗: {graph.error}</p>;
  } else if (!graph) {
    body = <p className="board-loading">読み込み中…</p>;
  } else if (shownIds.length === 0) {
    body = <p>表示する課題がありません。</p>;
  } else if (narrow) {
    const innerOf = (id: string) => {
      const ds = descendantsOf(id, kids).map((d) => infos.map.get(d));
      return {
        total: ds.length,
        active: ds.filter((d) => d?.active).length,
        human: ds.filter((d) => d?.category === "human").length,
      };
    };
    body = (
      <NarrowList
        ids={shownIds}
        infos={infos.map}
        kids={kids}
        roots={shownIds.filter((id) => !pm.has(id) && !isOther(id))}
        others={othersIds}
        relations={shownRels}
        expanded={expanded}
        inside={innerOf}
        onToggle={toggle}
        onOpen={openDetail}
      />
    );
  } else {
    body = (
      <div className="graph-main">
        <div className="graph-canvas">
          {layout.hasCycle ? (
            <p role="alert">
              関係に循環があります。循環に入った課題は最下段に並べています。
            </p>
          ) : null}
          <GraphCanvas
            ws={ws}
            layout={layout}
            dims={dims}
            infos={infos.map}
            inside={inside}
            focused={sel}
            relationOf={relationOf}
            dimmed={dimmed}
            edgeLit={edgeLit}
            onOpen={openDetail}
            onToggle={toggle}
            onHoverNode={setHoverNode}
            contextOpen={(id) => contextOpen.has(id)}
            onToggleContext={toggleContext}
            selectedEdge={selEdge ? selectedEdge : null}
            // 札は線を押したときと重ねたときだけ（課題に重ねても出さない）
            edgeLabeled={(e) => {
              const key = edgeKey(e);
              return key === selectedEdge || key === hoverEdge;
            }}
            onSelectEdge={(key) =>
              setSelectedEdge(key === selectedEdge ? null : key)
            }
            onHoverEdge={setHoverEdge}
          />
        </div>
      </div>
    );
  }

  // 開閉できる大枠（親と「ほかの課題」）
  const parents = [
    ...kids.keys(),
    ...(othersIds.length > 0 ? [OTHERS_ID] : []),
  ];
  // いま効いている絞り込みの数（区分）。畳んでいても数で分かるようにする
  const filtering = catFilter ? 1 : 0;
  return (
    <>
      <div className="graph-toolbar">
        <div className="graph-titlebar">
          <h3 className="graph-agent-title">{ws} の関係グラフ</h3>
          <div className="graph-titlebar-tools">
            {parents.length > 0 ? (
              <>
                <button
                  type="button"
                  className="graph-tool-btn"
                  onClick={() => setOpen(() => new Set(parents))}
                >
                  親をすべて開く
                </button>
                <button
                  type="button"
                  className="graph-tool-btn"
                  onClick={() => setOpen(() => new Set())}
                >
                  親をすべて畳む
                </button>
              </>
            ) : null}
            {/* 説明の文は常に並べず、必要なときに開く小さな吹き出しへ移した */}
            <details className="graph-help">
              <summary className="graph-tool-btn" title="凡例の説明">
                ? 凡例
              </summary>
              <ul aria-label="凡例の説明" className="graph-help-body">
                {CATEGORIES.map((k) => (
                  <li key={k.key} data-category={k.key}>
                    <span className="graph-swatch" aria-hidden="true" />
                    <b>{k.label}</b>
                  </li>
                ))}
                <li className="graph-legend-stale">
                  <b>{STALE_LABEL}</b>: 赤い箱。{STALE_HINT}
                </li>
                <li>
                  <b>実線の矢印</b>: 先行 → 後続。
                  {BASIS_LABELS.prerequisite}（先行の成果を使う）
                </li>
                <li>
                  <b>破線の矢印</b>: 先行 → 後続。
                  {BASIS_LABELS.conflict}（同じものを触るので順に走らせる）
                </li>
                <li>
                  <b>矢印の理由</b>: 線を押すか重ねると、なぜ待つのかを出す
                </li>
                <li>
                  <b>一言で</b>:
                  課題にカーソルを重ねると、課題の一言でを吹き出しで出す。開いた大枠では見出しの欄に位置づけと並べて出す。課題を押すと詳細を開く
                </li>
                <li>
                  <b>大枠</b>: 親課題（中に子課題）。重ねた箱は畳んだ親で、▶
                  は中の実施中・! は中の人間の操作待ちの件数
                </li>
              </ul>
            </details>
          </div>
        </div>
        <section aria-label="いま実施中" className="graph-active-row">
          <span className="graph-active-label">▶ いま実施中</span>
          {running.length === 0 ? (
            <span className="graph-muted">なし</span>
          ) : (
            running.map((i) => (
              <button
                key={i.node.id}
                type="button"
                className="graph-active-chip"
                aria-haspopup="dialog"
                onClick={() => {
                  // 畳んだ大枠の中にあっても、閉じたあと見えるように祖先を開いてから詳細を開く
                  setOpen(
                    (s) =>
                      new Set([
                        ...s,
                        ...ancestorsOf(i.node.id, pm),
                        ...(isOther(i.node.id) ? [OTHERS_ID] : []),
                      ]),
                  );
                  openDetail(i.node.id);
                }}
              >
                {i.node.id} {clip(i.node.title, narrow ? 8 : 16)}
              </button>
            ))
          )}
        </section>
        <div
          className="graph-filterbar"
          data-filtering={filtering > 0 || undefined}
        >
          <ul aria-label="区分の凡例" className="graph-legend">
            {CATEGORIES.map((k) => (
              <li key={k.key}>
                <button
                  type="button"
                  className="graph-legend-item"
                  data-category={k.key}
                  aria-pressed={catFilter === k.key}
                  onClick={() =>
                    setCatFilter(catFilter === k.key ? null : k.key)
                  }
                >
                  <span className="graph-swatch" aria-hidden="true" />
                  {k.label}{" "}
                  <span className="graph-count">{counts.get(k.key) ?? 0}</span>
                </button>
              </li>
            ))}
          </ul>
          <label className="graph-switch">
            <input
              type="checkbox"
              checked={showAll}
              onChange={(e) => setShowAll(e.target.checked)}
            />
            <span className="graph-switch-track" aria-hidden="true" />
            終わった課題もすべて表示
          </label>
          {filtering > 0 ? (
            <button
              type="button"
              className="graph-clear"
              onClick={() => {
                setCatFilter(null);
              }}
            >
              絞り込みを解除
            </button>
          ) : null}
        </div>
      </div>
      {body}
      {detailChallenge && workspace ? (
        // ボードのカード（TaskCard）と同じ部品を同じ渡し方で開く
        <RefreshKeyContext.Provider value={live.lastUpdated}>
          <CardDetailModal
            challenge={detailChallenge}
            agentName={workspace.name}
            workspacePath={workspace.path}
            onClose={closeDetail}
            // 本番の部品に、関係グラフの画面からだけ「待っている理由」を差し込む（ボードは渡さない）
            supplement={
              <WaitingReasons
                id={detailChallenge.id}
                relations={infos.rels}
                infos={infos.map}
              />
            }
          />
        </RefreshKeyContext.Provider>
      ) : null}
    </>
  );
}
