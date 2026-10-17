import {
  type MouseEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { api } from "../../api/client.ts";
import type { ChallengeGraphResponse } from "../../api/proposed.ts";
import { StaleLabelContext } from "../../board/AgentColumn.tsx";
import { Board } from "../../board/Board.tsx";
import { CardDetailModal } from "../../board/CardDetailModal.tsx";
import { toAgentBoard } from "../../board/adapt.ts";
import type { LiveState } from "../../live/useLive.ts";
import { RefreshKeyContext } from "../../nav.tsx";
import {
  AgentDock,
  AgentPicker,
  AgentRail,
  type DockTab,
  summariesOf,
  useAgentIcons,
} from "./AgentLayout.tsx";
import {
  type Dimensions,
  GraphCanvas,
  type Inside,
  type NodeInfo,
  edgeKey,
  groupHeadHeight,
} from "./GraphCanvas.tsx";
import { RepoFilter } from "./RepoFilter.tsx";
import {
  BASIS_LABELS,
  CATEGORIES,
  type Category,
  STALE_HINT,
  STALE_LABEL,
  STALE_LONG,
  actionableIds,
  ancestorsOf,
  categoryOf,
  childrenMap,
  clip,
  compareIds,
  descendantsOf,
  downstreamOf,
  isActive,
  layoutGraph,
  neighborsOf,
  parentMap,
  upstreamOf,
  validRelations,
} from "./model.ts";
import "./graph.css";

export const GRAPH_TITLE = "関係グラフ（案）";

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
  metaChars: 22,
  ctxCharW: 11,
  ctxLineH: 16,
  edgeChars: 16,
  calloutW: 280,
  calloutLines: 4,
};
// 幅 360px の端末で、一番外の段に 3 つ並んでも縮めずに読める大きさ。大枠の中は 2 つで折り返す。
export const NARROW: Dimensions = {
  nodeW: 104,
  nodeH: 52,
  gapX: 8,
  gapY: 30,
  pad: 4,
  labelH: 24,
  headH: 28,
  inset: 6,
  maxW: 328,
  titleLines: 1,
  metaChars: 9,
  ctxCharW: 10,
  ctxLineH: 15,
  edgeChars: 9,
  calloutW: 220,
  calloutLines: 5,
};
const NARROW_QUERY = "(max-width: 480px)";

function useNarrow(): boolean {
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

type View = "graph" | "board";

// 画面を外れて戻ると作り直されるため、表示・選んだワークスペース・開いた親をここに覚えておく。
// 欄の開閉もここに持つ（サーバには持たない）。
const memory: {
  view: View;
  ws: string | null;
  open: Record<string, string[]>;
  dock: boolean;
  dockTab: DockTab;
  // 位置づけを広げた大枠
  context: Record<string, string[]>;
} = {
  view: "graph",
  ws: null,
  open: {},
  // 2026-10-17 オーナー: 欄は右に固定し、以前の右のパネルの場所を欄に充てる。既定で開いておく
  dock: true,
  dockTab: "terminal",
  context: {},
};
export function resetGraphMemory(): void {
  memory.view = "graph";
  memory.ws = null;
  memory.open = {};
  memory.dock = true;
  memory.dockTab = "terminal";
  memory.context = {};
}

type GraphState =
  | { ws: string; data: ChallengeGraphResponse; error?: undefined }
  | { ws: string; data?: undefined; error: string };

function useGraph(ws: string | null, refreshKey: unknown) {
  const [state, setState] = useState<GraphState | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    if (!ws) return;
    let live = true;
    api.challengeGraph(ws).then(
      (data) => live && setState({ ws, data }),
      (e) => live && setState({ ws, error: String(e) }),
    );
    return () => {
      live = false;
    };
  }, [ws, refreshKey]);
  return state?.ws === ws ? state : null;
}

// 決定 1（flywheel#176 の 2026-10-13）: グラフと列のボードを同じ画面に置き、既定はグラフ。
// 2026-10-14 オーナー: グラフはエージェントごとに出し、エージェントはサイドバーで選ぶ。ターミナル／対話の欄の置き場所も取る。
export function GraphScreen({ live }: { live: LiveState }) {
  const [view, setView] = useState<View>(memory.view);
  const choose = (v: View) => {
    memory.view = v;
    setView(v);
  };
  const narrow = useNarrow();
  const agents = summariesOf(live);
  const [chosen, setChosen] = useState<string | null>(memory.ws);
  const ws =
    agents.find((a) => a.readable && a.name === chosen)?.name ??
    agents.find((a) => a.readable)?.name ??
    null;
  const chooseWs = (name: string) => {
    if (name === ws) return;
    memory.ws = name;
    setChosen(name);
  };
  const [dock, setDock] = useState(memory.dock);
  const toggleDock = () => {
    memory.dock = !dock;
    setDock(!dock);
  };
  const [dockTab, setDockTab] = useState<DockTab>(memory.dockTab);
  const chooseDockTab = (t: DockTab) => {
    memory.dockTab = t;
    setDockTab(t);
  };
  const icons = useAgentIcons(live.lastUpdated);
  // ボードの列の見出しを押すと、そのエージェントを選ぶ（ターミナルの対象を切り替える想定の最小限）。
  // 列は本番の部品のまま使うので、外側で押された列の見出しを拾う。キーボードでは引き出しの「対象」で選べる。
  const pickColumn = (e: MouseEvent<HTMLDivElement>) => {
    const column = (e.target as Element)
      .closest(".agent-column-header")
      ?.closest(".agent-column");
    const name = column?.getAttribute("aria-label")?.replace(/ の列$/, "");
    if (name && agents.some((a) => a.name === name && a.readable))
      chooseWs(name);
  };
  return (
    <section aria-label={GRAPH_TITLE} className="graph-screen">
      <div className="graph-topbar">
        <fieldset aria-label="表示" className="graph-view-switch">
          <button
            type="button"
            aria-pressed={view === "graph"}
            onClick={() => choose("graph")}
          >
            グラフ
          </button>
          <button
            type="button"
            aria-pressed={view === "board"}
            onClick={() => choose("board")}
          >
            列のボード
          </button>
        </fieldset>
      </div>
      <div className="agent-shell" data-dock-open={dock || undefined}>
        <div className="agent-main">
          {view === "graph" ? (
            <div className="agent-layout">
              {narrow ? null : (
                <AgentRail
                  agents={agents}
                  icons={icons}
                  ws={ws}
                  onChoose={chooseWs}
                />
              )}
              <div className="agent-content">
                {narrow ? (
                  <AgentPicker agents={agents} ws={ws} onChoose={chooseWs} />
                ) : null}
                <GraphView key={ws ?? ""} live={live} ws={ws} />
              </div>
            </div>
          ) : (
            // biome-ignore lint/a11y/useKeyWithClickEvents: 列の見出しを押す操作の補助。キーボードでは引き出しの「対象」で選べる
            <div
              className="graph-board-view board-screen"
              data-selected-agent={ws ?? undefined}
              onClick={pickColumn}
            >
              {ws ? (
                <style>{`.graph-board-view .agent-column[aria-label=${JSON.stringify(`${ws} の列`)}] { outline: 2px solid var(--graph-running); outline-offset: 2px; }`}</style>
              ) : null}
              <StaleLabelContext.Provider value={STALE_LONG}>
                <Board live={live} />
              </StaleLabelContext.Provider>
            </div>
          )}
        </div>
        <AgentDock
          agents={agents}
          ws={ws}
          open={dock}
          tab={dockTab}
          onToggle={toggleDock}
          onChoose={chooseWs}
          onTab={chooseDockTab}
        />
      </div>
    </section>
  );
}

// 選んだエージェント 1 つ分のグラフ。エージェントを切り替えると作り直す（key）ので、絞り込みは持ち越さない。
// 2026-10-17 オーナー: 課題を押すとボードと同じ詳細のモーダルを開く（右のパネルで選ぶ流れはやめた）。
function GraphView({ live, ws }: { live: LiveState; ws: string | null }) {
  const narrow = useNarrow();
  const dims = narrow ? NARROW : WIDE;
  // 押した依存の線（`from:to`。見えている箱どうし。理由の札を出したままにする）と、マウスを重ねた・フォーカスした線
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);
  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  // カーソルを重ねた・キーボードで移った課題（先行・後続の強調と一言での吹き出しの起点）
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  // 詳細のモーダルを開いている課題と、閉じたときにフォーカスを戻す要素（ボードのカードと同じ）
  const [detailId, setDetailId] = useState<string | null>(null);
  const trigger = useRef<Element | null>(null);
  const [contextOpenMap, setContextOpenMap] = useState(memory.context);
  const [openParents, setOpenParents] = useState(memory.open);
  const [catFilter, setCatFilter] = useState<Category | null>(null);
  const [repoFilter, setRepoFilter] = useState<string[]>([]);
  const [hideDone, setHideDone] = useState(false);
  const graph = useGraph(ws, live.lastUpdated);
  const wsData = ws ? live.data[ws] : undefined;
  const expanded = new Set(ws ? (openParents[ws] ?? []) : []);

  const infos = useMemo(() => {
    const cards = wsData?.challenges?.challenges ?? [];
    const actionable = actionableIds(wsData?.status);
    const approvedOps = new Set(
      (wsData?.status?.approved.operations ?? []).map((o) => o.challenge_id),
    );
    const repoMap = new Map(
      (graph?.data?.repos ?? []).map((r) => [r.challenge_id, r.repos]),
    );
    const partyMap = new Map(
      (graph?.data?.waiting_party ?? []).map((w) => [w.challenge_id, w]),
    );
    const all = new Set(cards.map((c) => c.id));
    const rels = validRelations(all, graph?.data?.relations ?? []);
    const doneIds = new Set(
      cards.filter((c) => c.card.state === "done").map((c) => c.id),
    );
    const m = new Map<string, NodeInfo>();
    for (const c of cards) {
      const party = partyMap.get(c.id) ?? null;
      const category = categoryOf(c, actionable, party !== null);
      const autoCheck = c.card.modifiers.includes("waiting_ci");
      const marks: string[] = [];
      if (c.card.modifiers.includes("unresponsive")) marks.push(STALE_LABEL);
      if (party) marks.push(`${party.party}の回答待ち`);
      const waitingOn = neighborsOf(c.id, rels).predecessors.filter(
        (p) => !doneIds.has(p),
      );
      if (category === "system" && waitingOn.length > 0)
        marks.push("先行が未完了");
      if (approvedOps.has(c.id)) marks.push("承認済みの操作が未実行");
      m.set(c.id, {
        card: c,
        category,
        repos: repoMap.get(c.id) ?? [],
        marks,
        active: isActive(c),
        autoCheck,
        party,
      });
    }
    const pm = parentMap(all, rels);
    return { map: m, rels, pm, kids: childrenMap(pm) };
  }, [wsData, graph]);

  const setOpen = (fn: (s: Set<string>) => Set<string>) => {
    if (!ws) return;
    const next = [...fn(new Set(openParents[ws] ?? []))].sort(compareIds);
    const all = { ...openParents, [ws]: next };
    memory.open = all;
    setOpenParents(all);
  };
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
  const contextOpen = new Set(ws ? (contextOpenMap[ws] ?? []) : []);
  const toggleContext = (id: string) => {
    if (!ws) return;
    const next = new Set(contextOpen);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    const all = { ...contextOpenMap, [ws]: [...next].sort(compareIds) };
    memory.context = all;
    setContextOpenMap(all);
  };

  // 完了を隠しても、まだ終わっていない子を持つ親（大枠）は残す。
  const shownIds = [...infos.map.values()]
    .filter(
      (i) =>
        !(
          hideDone &&
          i.category === "done" &&
          descendantsOf(i.card.id, infos.kids).every(
            (d) => infos.map.get(d)?.category === "done",
          )
        ),
    )
    .map((i) => i.card.id);
  // 開いた大枠の見出しは一言でと位置づけの分だけ伸びる。行数は大枠の幅で決まり、幅は見出しの高さに左右されないので、
  // 一度配置して大枠の幅を得てから、見出しの高さを決めて配置し直す。
  const cardOf = (id: string) => infos.map.get(id)?.card.card;
  const headOf = (width: (id: string) => number) => (id: string) =>
    groupHeadHeight(
      cardOf(id)?.headline ?? null,
      cardOf(id)?.context ?? null,
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
  );
  const widths = new Map(first.groups.map((g) => [g.id, g.w]));
  const layout = layoutGraph(
    shownIds,
    infos.rels,
    dims,
    expanded,
    headOf((id) => widths.get(id) ?? dims.maxW),
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
  const ancestors = new Set(sel ? ancestorsOf(sel, pm) : []);
  const inner = new Set(sel ? descendantsOf(sel, kids) : []);
  const repos = [
    ...new Set([...infos.map.values()].flatMap((i) => i.repos)),
  ].sort();

  const counts = new Map<Category, number>();
  for (const i of infos.map.values())
    counts.set(i.category, (counts.get(i.category) ?? 0) + 1);
  const running = [...infos.map.values()]
    .filter((i) => i.active)
    .map((i) => i.card);

  // 線を選んだときは、線の両端の箱（とその中の課題）だけを濃く残す
  const edgeEnds = new Set(
    selEdge
      ? [selEdge.from, selEdge.to].flatMap((b) => [
          b,
          ...descendantsOf(b, kids),
        ])
      : [],
  );
  const relevant = (id: string) =>
    selEdge
      ? edgeEnds.has(id)
      : sel === null ||
        id === sel ||
        up.has(id) ||
        down.has(id) ||
        ancestors.has(id) ||
        inner.has(id);
  const matches = (id: string) => {
    const i = infos.map.get(id);
    if (!i) return false;
    if (catFilter && i.category !== catFilter) return false;
    // 選んだリポジトリのどれかを触る課題を残す
    if (repoFilter.length > 0 && !repoFilter.some((r) => i.repos.includes(r)))
      return false;
    return true;
  };
  // 箱が表す課題: 自分と、大枠（開いていても畳んでいても）の中の課題すべて。
  const membersOf = (id: string) => [id, ...descendantsOf(id, kids)];
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
    return undefined;
  };
  const inside = (id: string): Inside | null => {
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
  } else if (wsData?.error) {
    body = <p role="alert">課題の読み込みに失敗: {wsData.error}</p>;
  } else if (graph?.error) {
    body = <p role="alert">関係の読み込みに失敗: {graph.error}</p>;
  } else if (!graph || !wsData?.challenges) {
    body = <p className="board-loading">読み込み中…</p>;
  } else if (shownIds.length === 0) {
    body = <p>表示する課題がありません。</p>;
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
            narrow={narrow}
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
            // 2026-10-17 オーナー: 札は線を押したときと重ねたときだけ（課題に重ねても出さない）
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

  const parents = [...kids.keys()];
  // いま効いている絞り込みの数（区分・リポジトリ・完了を隠す）。畳んでいても数で分かるようにする
  const filtering =
    (catFilter ? 1 : 0) + (repoFilter.length > 0 ? 1 : 0) + (hideDone ? 1 : 0);
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
            running.map((c) => (
              <button
                key={c.id}
                type="button"
                className="graph-active-chip"
                aria-haspopup="dialog"
                onClick={() => {
                  // 畳んだ大枠の中にあっても、閉じたあと見えるように祖先を開いてから詳細を開く
                  setOpen(
                    (s) => new Set([...s, ...ancestorsOf(c.id, infos.pm)]),
                  );
                  openDetail(c.id);
                }}
              >
                {c.id} {clip(c.title, narrow ? 8 : 16)}
              </button>
            ))
          )}
        </section>
        {/* 狭い画面ではグラフが下へ押し出されないよう、絞り込みを畳んで始める */}
        <details className="graph-filters" open={!narrow || undefined}>
          <summary>
            絞り込み
            {filtering > 0 ? (
              <span className="graph-filter-count">{filtering}</span>
            ) : null}
          </summary>
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
                    <span className="graph-count">
                      {counts.get(k.key) ?? 0}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            {repos.length > 0 ? (
              <RepoFilter
                repos={repos}
                chosen={repoFilter}
                onChange={setRepoFilter}
              />
            ) : null}
            <label className="graph-switch">
              <input
                type="checkbox"
                checked={hideDone}
                onChange={(e) => setHideDone(e.target.checked)}
              />
              <span className="graph-switch-track" aria-hidden="true" />
              完了を隠す
            </label>
            {filtering > 0 ? (
              <button
                type="button"
                className="graph-clear"
                onClick={() => {
                  setCatFilter(null);
                  setRepoFilter([]);
                  setHideDone(false);
                }}
              >
                絞り込みを解除
              </button>
            ) : null}
          </div>
        </details>
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
          />
        </RefreshKeyContext.Provider>
      ) : null}
    </>
  );
}
