import {
  type MouseEvent,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { api } from "../../api/client.ts";
import type { ChallengeGraphResponse } from "../../api/proposed.ts";
import { StaleLabelContext } from "../../board/AgentColumn.tsx";
import { Board } from "../../board/Board.tsx";
import {
  ACTION_LABELS,
  MODIFIER_LABELS,
  NO_ACTION_LABEL,
  STATE_LABELS,
  label,
} from "../../labels.ts";
import type { LiveState } from "../../live/useLive.ts";
import { OpenDetailContext } from "../../nav.tsx";
import {
  AgentDock,
  AgentPicker,
  AgentRail,
  type DockPosition,
  type DockTab,
  summariesOf,
  useAgentIcons,
} from "./AgentLayout.tsx";
import {
  type Dimensions,
  GraphCanvas,
  type Inside,
  type NodeInfo,
} from "./GraphCanvas.tsx";
import {
  AUTO_CHECK_TEXT,
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

export const WIDE: Dimensions = {
  nodeW: 168,
  nodeH: 60,
  gapX: 20,
  gapY: 40,
  pad: 12,
  labelH: 28,
  headH: 30,
  inset: 10,
  maxW: 860,
  titleChars: 12,
  metaChars: 22,
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
  titleChars: 7,
  metaChars: 9,
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

// 詳細を開くと画面が外れて戻ると作り直されるため、表示・選んだワークスペースと課題・開いた親をここに覚えておく。
// 選んだエージェント・引き出しの開閉もここに持つ（サーバには持たない）。
const memory: {
  view: View;
  ws: string | null;
  node: string | null;
  open: Record<string, string[]>;
  dock: boolean;
  dockPos: DockPosition;
  dockTab: DockTab;
} = {
  view: "graph",
  ws: null,
  node: null,
  open: {},
  dock: false,
  dockPos: "bottom",
  dockTab: "terminal",
};
export function resetGraphMemory(): void {
  memory.view = "graph";
  memory.ws = null;
  memory.node = null;
  memory.open = {};
  memory.dock = false;
  memory.dockPos = "bottom";
  memory.dockTab = "terminal";
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
    memory.node = null;
    setChosen(name);
  };
  const [dock, setDock] = useState(memory.dock);
  const toggleDock = () => {
    memory.dock = !dock;
    setDock(!dock);
  };
  // 欄の位置の 2 案（試作だけの切り替え。2026-10-14 オーナー: どちらのパターンも見たい）
  const [dockPos, setDockPos] = useState<DockPosition>(memory.dockPos);
  const chooseDockPos = (p: DockPosition) => {
    memory.dockPos = p;
    setDockPos(p);
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
        <fieldset
          aria-label="欄の位置（案）"
          className="graph-seg graph-seg-small"
        >
          <legend>欄の位置（案）</legend>
          <button
            type="button"
            aria-pressed={dockPos === "bottom"}
            onClick={() => chooseDockPos("bottom")}
          >
            下の引き出し
          </button>
          <button
            type="button"
            aria-pressed={dockPos === "right"}
            onClick={() => chooseDockPos("right")}
          >
            右の分割
          </button>
        </fieldset>
      </div>
      <div
        className="agent-shell"
        data-dock-pos={dockPos}
        data-dock-open={dock || undefined}
      >
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
          position={dockPos}
          tab={dockTab}
          onToggle={toggleDock}
          onChoose={chooseWs}
          onTab={chooseDockTab}
        />
      </div>
    </section>
  );
}

// 選んだエージェント 1 つ分のグラフ。エージェントを切り替えると作り直す（key）ので、選択と絞り込みは持ち越さない。
function GraphView({ live, ws }: { live: LiveState; ws: string | null }) {
  const open = useContext(OpenDetailContext);
  const narrow = useNarrow();
  const dims = narrow ? NARROW : WIDE;
  const [selected, setSelected] = useState<string | null>(memory.node);
  const [openParents, setOpenParents] = useState(memory.open);
  const [catFilter, setCatFilter] = useState<Category | null>(null);
  const [repoFilter, setRepoFilter] = useState<string | null>(null);
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
  // 選ぶと、畳んだ大枠の中にあっても見えるように祖先を開く。
  const select = (id: string | null) => {
    memory.node = id;
    setSelected(id);
    if (id) setOpen((s) => new Set([...s, ...ancestorsOf(id, infos.pm)]));
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
  const layout = layoutGraph(shownIds, infos.rels, dims, expanded);
  const pm = layout.parents;
  const kids = childrenMap(pm);
  const shownRels = validRelations(new Set(shownIds), infos.rels);
  const sel = selected && shownIds.includes(selected) ? selected : null;
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

  const relevant = (id: string) =>
    sel === null ||
    id === sel ||
    up.has(id) ||
    down.has(id) ||
    ancestors.has(id) ||
    inner.has(id);
  const matches = (id: string) => {
    const i = infos.map.get(id);
    if (!i) return false;
    if (catFilter && i.category !== catFilter) return false;
    if (repoFilter && !i.repos.includes(repoFilter)) return false;
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
            selected={sel}
            relationOf={relationOf}
            dimmed={dimmed}
            edgeLit={edgeLit}
            onSelect={(id) => select(id === sel ? null : id)}
            onOpen={(id) => open?.(ws, id)}
            onToggle={toggle}
          />
        </div>
        <SelectedPanel
          ws={ws}
          id={sel}
          infos={infos.map}
          relations={shownRels}
          parent={sel ? (pm.get(sel) ?? null) : null}
          childIds={sel ? (kids.get(sel) ?? []) : []}
          onSelect={select}
          onOpen={open}
        />
      </div>
    );
  }

  const parents = [...kids.keys()];
  // いま効いている絞り込みの数（区分・リポジトリ・完了を隠す）。畳んでいても数で分かるようにする
  const filtering =
    (catFilter ? 1 : 0) + (repoFilter ? 1 : 0) + (hideDone ? 1 : 0);
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
                    <b>{k.label}</b>: {k.hint}
                  </li>
                ))}
                <li className="graph-legend-stale">
                  <b>{STALE_LABEL}</b>: 赤い箱。{STALE_HINT}
                </li>
                <li>
                  <b>実線の矢印</b>: 先行 → 後続
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
                aria-pressed={c.id === sel}
                onClick={() => select(c.id)}
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
                    title={k.hint}
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
              <fieldset aria-label="リポジトリ" className="graph-repos">
                <legend>リポジトリ</legend>
                {repos.map((r) => (
                  <button
                    key={r}
                    type="button"
                    className="graph-repo-chip"
                    aria-label={r}
                    title={r}
                    aria-pressed={repoFilter === r}
                    onClick={() => setRepoFilter(repoFilter === r ? null : r)}
                  >
                    {r.split("/").pop()}
                  </button>
                ))}
              </fieldset>
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
                  setRepoFilter(null);
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
    </>
  );
}

function IdList({
  ids,
  infos,
  onSelect,
  empty,
}: {
  ids: string[];
  infos: Map<string, NodeInfo>;
  onSelect: (id: string) => void;
  empty: string;
}) {
  if (ids.length === 0) return <p className="graph-muted">{empty}</p>;
  return (
    <ul className="graph-id-list">
      {ids.map((id) => {
        const i = infos.get(id);
        return (
          <li key={id} data-category={i?.category}>
            <span className="graph-swatch" aria-hidden="true" />
            <button type="button" onClick={() => onSelect(id)}>
              {id}
            </button>{" "}
            {i ? clip(i.card.title, 24) : ""}
            {i?.category === "done" ? "（完了）" : ""}
            {i?.active ? "（実施中）" : ""}
          </li>
        );
      })}
    </ul>
  );
}

function SelectedPanel({
  ws,
  id,
  infos,
  relations,
  parent,
  childIds,
  onSelect,
  onOpen,
}: {
  ws: string;
  id: string | null;
  infos: Map<string, NodeInfo>;
  relations: Parameters<typeof downstreamOf>[1];
  parent: string | null;
  childIds: string[];
  onSelect: (id: string | null) => void;
  onOpen: ((ws: string, id: string) => void) | null;
}) {
  const i = id ? infos.get(id) : undefined;
  if (!id || !i) {
    return (
      <aside aria-label="選んだ課題" className="graph-panel">
        <p className="graph-muted">
          課題を選ぶと、その先行・後続（終わると進められるようになる課題）と詳細への入口をここに出します。ダブルクリックでも詳細を開けます。
        </p>
      </aside>
    );
  }
  const c = i.card;
  const actions = c.card.next_human_actions;
  return (
    <aside aria-label="選んだ課題" className="graph-panel">
      <h3>
        {c.id} {c.title}
      </h3>
      <p data-category={i.category}>
        <span className="graph-swatch" aria-hidden="true" />
        {CATEGORIES.find((k) => k.key === i.category)?.label}
        {" ・ "}
        {label(STATE_LABELS, c.card.state)}
        {c.card.modifiers.map((m) =>
          // 更新なしは下の帯で、自動の確認待ちは利用分野の語（CI）を避けて下の行で示す
          m === "unresponsive" || m === "waiting_ci"
            ? null
            : ` [${label(MODIFIER_LABELS, m)}]`,
        )}
      </p>
      {c.card.modifiers.includes("unresponsive") ? (
        <p className="graph-panel-stale" title={STALE_HINT}>
          {STALE_LONG}
        </p>
      ) : null}
      {i.autoCheck ? (
        <p className="graph-panel-autocheck">{AUTO_CHECK_TEXT}</p>
      ) : null}
      {i.party ? (
        <p className="graph-panel-party">
          外部待ち: {i.party.party}（{i.party.reason}）
        </p>
      ) : null}
      {c.card.headline ? <p>{c.card.headline}</p> : null}
      <h4>次に人間がすること</h4>
      <p>
        {actions.length === 0
          ? NO_ACTION_LABEL
          : actions.map((a) => label(ACTION_LABELS, a)).join("、")}
      </p>
      <h4>これが終わると進められる課題</h4>
      <IdList
        ids={downstreamOf(id, relations)}
        infos={infos}
        onSelect={onSelect}
        empty="なし"
      />
      <h4>先に終わる必要がある課題</h4>
      <IdList
        ids={upstreamOf(id, relations)}
        infos={infos}
        onSelect={onSelect}
        empty="なし"
      />
      {parent ? (
        <>
          <h4>親課題（外側の大枠）</h4>
          <IdList
            ids={[parent]}
            infos={infos}
            onSelect={onSelect}
            empty="なし"
          />
        </>
      ) : null}
      {childIds.length > 0 ? (
        <>
          <h4>子課題（大枠の中）</h4>
          <IdList
            ids={childIds}
            infos={infos}
            onSelect={onSelect}
            empty="なし"
          />
        </>
      ) : null}
      <h4>触るリポジトリ</h4>
      <p>{i.repos.length === 0 ? "記録なし" : i.repos.join("、")}</p>
      <div className="graph-panel-actions">
        {onOpen ? (
          <button type="button" onClick={() => onOpen(ws, id)}>
            詳細を開く
          </button>
        ) : null}
        <button type="button" onClick={() => onSelect(null)}>
          選択を外す
        </button>
      </div>
    </aside>
  );
}
