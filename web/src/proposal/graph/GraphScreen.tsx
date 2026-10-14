import {
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
import { CycleHeaders } from "../CycleHeaders.tsx";
import { NeedsHumanStrip } from "../NeedsHumanStrip.tsx";
import {
  type Dimensions,
  GraphCanvas,
  type Inside,
  type NodeInfo,
} from "./GraphCanvas.tsx";
import {
  CATEGORIES,
  CI_HINT,
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
const memory: {
  view: View;
  ws: string | null;
  node: string | null;
  open: Record<string, string[]>;
} = { view: "graph", ws: null, node: null, open: {} };
export function resetGraphMemory(): void {
  memory.view = "graph";
  memory.ws = null;
  memory.node = null;
  memory.open = {};
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
export function GraphScreen({ live }: { live: LiveState }) {
  const [view, setView] = useState<View>(memory.view);
  const choose = (v: View) => {
    memory.view = v;
    setView(v);
  };
  return (
    <section aria-label={GRAPH_TITLE} className="graph-screen">
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
      {view === "graph" ? (
        <GraphView live={live} />
      ) : (
        <div className="graph-board-view board-screen">
          <NeedsHumanStrip live={live} />
          <CycleHeaders
            workspaces={live.workspaces}
            refreshKey={live.lastUpdated}
          />
          <StaleLabelContext.Provider value={STALE_LONG}>
            <Board live={live} />
          </StaleLabelContext.Provider>
        </div>
      )}
    </section>
  );
}

function GraphView({ live }: { live: LiveState }) {
  const open = useContext(OpenDetailContext);
  const narrow = useNarrow();
  const dims = narrow ? NARROW : WIDE;
  const readable = live.workspaces.filter((w) => w.state === "ok");
  const unreadable = live.workspaces.filter((w) => w.state !== "ok");
  const [chosen, setChosen] = useState<string | null>(memory.ws);
  const ws =
    readable.find((w) => w.name === chosen)?.name ?? readable[0]?.name ?? null;
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
      const ci = c.card.modifiers.includes("waiting_ci");
      const marks: string[] = [];
      if (c.card.modifiers.includes("unresponsive")) marks.push(STALE_LABEL);
      if (ci) marks.push("CI 待ち");
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
        ci,
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
  const chooseWs = (name: string) => {
    memory.ws = name;
    memory.node = null;
    setChosen(name);
    setSelected(null);
    setCatFilter(null);
    setRepoFilter(null);
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
  return (
    <>
      <div className="graph-toolbar">
        <fieldset aria-label="ワークスペース" className="graph-chips">
          {readable.map((w) => (
            <button
              key={w.name}
              type="button"
              className="filter-chip"
              aria-pressed={w.name === ws}
              onClick={() => chooseWs(w.name)}
            >
              {w.name}
            </button>
          ))}
          {unreadable.map((w) => (
            <button
              key={w.name}
              type="button"
              className="filter-chip"
              disabled
              title={w.error ?? w.state}
            >
              {w.name}（読めない: {w.state}）
            </button>
          ))}
        </fieldset>
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
        {/* 狭い画面ではグラフが下へ押し出されないよう、凡例と絞り込みを畳んで始める */}
        <details className="graph-filters" open={!narrow || undefined}>
          <summary>凡例と絞り込み</summary>
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
                  {k.label} {counts.get(k.key) ?? 0}
                </button>
              </li>
            ))}
            <li
              className="graph-legend-edge graph-legend-stale"
              title={STALE_HINT}
            >
              {STALE_LABEL}: 赤い箱。{STALE_HINT}
            </li>
            <li className="graph-legend-edge graph-legend-ci" title={CI_HINT}>
              CI の札: CI の完了待ち（実行中の区分に数える。数分で自動で解ける）
            </li>
            <li className="graph-legend-edge">
              外部待ち:
              自動では解けない待ち（他チームへの相談・外部のレビュー・ベンダーの回答など）
            </li>
            <li className="graph-legend-edge">実線の矢印: 先行 → 後続</li>
            <li className="graph-legend-edge">
              大枠: 親課題（中に子課題）。重ねた箱は畳んだ親で、▶
              は中の実施中・! は中の人間の操作待ちの件数
            </li>
          </ul>
          <div className="graph-chips">
            {repos.length > 0 ? (
              <fieldset aria-label="リポジトリ" className="graph-chips">
                {repos.map((r) => (
                  <button
                    key={r}
                    type="button"
                    className="filter-chip"
                    aria-pressed={repoFilter === r}
                    onClick={() => setRepoFilter(repoFilter === r ? null : r)}
                  >
                    {r}
                  </button>
                ))}
              </fieldset>
            ) : null}
            <label className="graph-toggle">
              <input
                type="checkbox"
                checked={hideDone}
                onChange={(e) => setHideDone(e.target.checked)}
              />
              完了を隠す
            </label>
          </div>
        </details>
        {parents.length > 0 ? (
          <div className="graph-chips">
            <button
              type="button"
              className="filter-chip"
              onClick={() => setOpen(() => new Set(parents))}
            >
              親をすべて開く
            </button>
            <button
              type="button"
              className="filter-chip"
              onClick={() => setOpen(() => new Set())}
            >
              親をすべて畳む
            </button>
          </div>
        ) : null}
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
            {i?.ci ? "（CI 待ち）" : ""}
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
          m === "unresponsive" ? null : ` [${label(MODIFIER_LABELS, m)}]`,
        )}
      </p>
      {c.card.modifiers.includes("unresponsive") ? (
        <p className="graph-panel-stale" title={STALE_HINT}>
          {STALE_LONG}
        </p>
      ) : null}
      {i.ci ? (
        <p className="graph-panel-ci" title={CI_HINT}>
          CI の完了待ち（数分で自動で解ける。外部待ちには数えない）
        </p>
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
