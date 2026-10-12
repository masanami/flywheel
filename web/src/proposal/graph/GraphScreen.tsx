import {
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { api } from "../../api/client.ts";
import type { ChallengeGraphResponse } from "../../api/proposed.ts";
import type { ChallengeCard } from "../../api/types.gen.ts";
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
  CATEGORIES,
  type Category,
  type Dims,
  actionableIds,
  categoryOf,
  clip,
  downstreamOf,
  layoutGraph,
  neighborsOf,
  upstreamOf,
  validRelations,
} from "./model.ts";
import "./graph.css";

export const GRAPH_TITLE = "関係グラフ（案）";

const WIDE: Dims & { titleChars: number } = {
  nodeW: 168,
  nodeH: 60,
  gapX: 20,
  gapY: 44,
  pad: 12,
  labelH: 28,
  titleChars: 12,
};
// 幅 360px の端末で、層に 3 つ並んでも縮めずに読める大きさ。
const NARROW: Dims & { titleChars: number } = {
  nodeW: 104,
  nodeH: 52,
  gapX: 8,
  gapY: 36,
  pad: 4,
  labelH: 24,
  titleChars: 7,
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

// 詳細を開くと画面が外れて戻ると作り直されるため、選んだワークスペースと課題をここに覚えておく。
const memory: { ws: string | null; node: string | null } = {
  ws: null,
  node: null,
};
export function resetGraphMemory(): void {
  memory.ws = null;
  memory.node = null;
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

interface NodeInfo {
  card: ChallengeCard;
  category: Category;
  repos: string[];
  marks: string[];
}

export function GraphScreen({ live }: { live: LiveState }) {
  const open = useContext(OpenDetailContext);
  const narrow = useNarrow();
  const dims = narrow ? NARROW : WIDE;
  const readable = live.workspaces.filter((w) => w.state === "ok");
  const unreadable = live.workspaces.filter((w) => w.state !== "ok");
  const [chosen, setChosen] = useState<string | null>(memory.ws);
  const ws =
    readable.find((w) => w.name === chosen)?.name ?? readable[0]?.name ?? null;
  const [selected, setSelected] = useState<string | null>(memory.node);
  const [catFilter, setCatFilter] = useState<Category | null>(null);
  const [repoFilter, setRepoFilter] = useState<string | null>(null);
  const [hideDone, setHideDone] = useState(false);
  const graph = useGraph(ws, live.lastUpdated);
  const wsData = ws ? live.data[ws] : undefined;

  const chooseWs = (name: string) => {
    memory.ws = name;
    memory.node = null;
    setChosen(name);
    setSelected(null);
    setCatFilter(null);
    setRepoFilter(null);
  };
  const select = (id: string | null) => {
    memory.node = id;
    setSelected(id);
  };

  const infos = useMemo(() => {
    const cards = wsData?.challenges?.challenges ?? [];
    const actionable = actionableIds(wsData?.status);
    const approvedOps = new Set(
      (wsData?.status?.approved.operations ?? []).map((o) => o.challenge_id),
    );
    const repoMap = new Map(
      (graph?.data?.repos ?? []).map((r) => [r.challenge_id, r.repos]),
    );
    const all = new Set(cards.map((c) => c.id));
    const rels = validRelations(all, graph?.data?.relations ?? []);
    const doneIds = new Set(
      cards.filter((c) => c.card.state === "done").map((c) => c.id),
    );
    const m = new Map<string, NodeInfo>();
    for (const c of cards) {
      const category = categoryOf(c, actionable);
      const marks: string[] = [];
      if (c.card.modifiers.includes("unresponsive")) marks.push("応答なし");
      const waitingOn = neighborsOf(c.id, rels).predecessors.filter(
        (p) => !doneIds.has(p),
      );
      if (category === "system" && waitingOn.length > 0)
        marks.push("先行が未完了");
      if (approvedOps.has(c.id)) marks.push("承認済みの操作が未実行");
      m.set(c.id, { card: c, category, repos: repoMap.get(c.id) ?? [], marks });
    }
    return { map: m, rels };
  }, [wsData, graph]);

  const shownIds = [...infos.map.values()]
    .filter((i) => !(hideDone && i.category === "done"))
    .map((i) => i.card.id);
  const layout = layoutGraph(shownIds, infos.rels, dims);
  const shownRels = validRelations(new Set(shownIds), infos.rels);
  const sel = selected && infos.map.has(selected) ? selected : null;
  const up = new Set(sel ? upstreamOf(sel, shownRels) : []);
  const down = new Set(sel ? downstreamOf(sel, shownRels) : []);
  const near = sel ? neighborsOf(sel, shownRels) : null;
  const family = new Set([...(near?.parents ?? []), ...(near?.children ?? [])]);
  const repos = [
    ...new Set([...infos.map.values()].flatMap((i) => i.repos)),
  ].sort();

  const counts = new Map<Category, number>();
  for (const i of infos.map.values())
    counts.set(i.category, (counts.get(i.category) ?? 0) + 1);

  const dimmed = (id: string): boolean => {
    const i = infos.map.get(id);
    if (!i) return true;
    if (catFilter && i.category !== catFilter) return true;
    if (repoFilter && !i.repos.includes(repoFilter)) return true;
    if (sel && id !== sel && !up.has(id) && !down.has(id) && !family.has(id))
      return true;
    return false;
  };
  // 選んだ課題・その先行と後続・親子。グラフの線はこの中どうしを結ぶものだけを強く出す。
  const inChain = (id: string): boolean =>
    sel !== null &&
    (id === sel || up.has(id) || down.has(id) || family.has(id));
  const relationOf = (id: string): string | undefined => {
    if (!sel) return undefined;
    if (id === sel) return "selected";
    if (down.has(id)) return "downstream";
    if (up.has(id)) return "upstream";
    if (family.has(id)) return "family";
    return undefined;
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
            {layout.edges.map((e) => {
              const lit = inChain(e.from) && inChain(e.to);
              return (
                <path
                  key={`${e.kind}:${e.from}:${e.to}`}
                  d={e.path}
                  className="graph-edge"
                  data-kind={e.kind}
                  data-lit={lit || undefined}
                  data-dim={
                    (sel !== null && !lit) ||
                    dimmed(e.from) ||
                    dimmed(e.to) ||
                    undefined
                  }
                  markerEnd={
                    e.kind === "depends_on" ? "url(#graph-arrow)" : undefined
                  }
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
            {layout.nodes.map((n) => {
              const i = infos.map.get(n.id);
              if (!i) return null;
              const c = i.card;
              const third = narrow
                ? (i.marks[0] ?? "")
                : [
                    ...i.marks,
                    i.repos.map((r) => r.split("/").pop()).join("・"),
                  ]
                    .filter(Boolean)
                    .join(" / ");
              const name = `${c.id} ${c.title}（${CATEGORIES.find((k) => k.key === i.category)?.label}${i.marks.length > 0 ? `・${i.marks.join("・")}` : ""}）`;
              return (
                <g
                  key={n.id}
                  // biome-ignore lint/a11y/useSemanticElements: SVG の中に button 要素は置けない
                  role="button"
                  tabIndex={0}
                  aria-label={name}
                  aria-pressed={n.id === sel}
                  className="graph-node"
                  data-category={i.category}
                  data-relation={relationOf(n.id)}
                  data-dim={dimmed(n.id) || undefined}
                  data-unresponsive={
                    c.card.modifiers.includes("unresponsive") || undefined
                  }
                  transform={`translate(${n.x} ${n.y})`}
                  onClick={() => select(n.id === sel ? null : n.id)}
                  onDoubleClick={() => open?.(ws, n.id)}
                  onKeyDown={(ev) => {
                    if (ev.key === "Enter" || ev.key === " ") {
                      ev.preventDefault();
                      select(n.id === sel ? null : n.id);
                    }
                  }}
                >
                  <title>{name}</title>
                  <rect
                    className="graph-node-box"
                    width={dims.nodeW}
                    height={dims.nodeH}
                    rx={6}
                  />
                  <rect
                    className="graph-node-stripe"
                    width={5}
                    height={dims.nodeH}
                    rx={2}
                  />
                  <text x={11} y={narrow ? 16 : 18} className="graph-node-id">
                    {c.id}
                    {c.priority ? ` ${c.priority}` : ""}
                  </text>
                  <text
                    x={11}
                    y={narrow ? 31 : 35}
                    className="graph-node-title"
                  >
                    {clip(c.title, dims.titleChars)}
                  </text>
                  {third ? (
                    <text
                      x={11}
                      y={narrow ? 45 : 51}
                      className="graph-node-meta"
                    >
                      {clip(third, narrow ? 9 : 22)}
                    </text>
                  ) : null}
                </g>
              );
            })}
          </svg>
        </div>
        <SelectedPanel
          ws={ws}
          id={sel}
          infos={infos.map}
          relations={shownRels}
          onSelect={select}
          onOpen={open}
        />
      </div>
    );
  }

  return (
    <section aria-label={GRAPH_TITLE} className="graph-screen">
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
        <ul aria-label="区分の凡例" className="graph-legend">
          {CATEGORIES.map((k) => (
            <li key={k.key}>
              <button
                type="button"
                className="graph-legend-item"
                data-category={k.key}
                aria-pressed={catFilter === k.key}
                title={k.hint}
                onClick={() => setCatFilter(catFilter === k.key ? null : k.key)}
              >
                <span className="graph-swatch" aria-hidden="true" />
                {k.label} {counts.get(k.key) ?? 0}
              </button>
            </li>
          ))}
          <li className="graph-legend-edge" data-kind="depends_on">
            実線の矢印: 先行 → 後続
          </li>
          <li className="graph-legend-edge" data-kind="parent_of">
            点線: 親 → 子
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
      </div>
      {body}
    </section>
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
  onSelect,
  onOpen,
}: {
  ws: string;
  id: string | null;
  infos: Map<string, NodeInfo>;
  relations: Parameters<typeof downstreamOf>[1];
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
  const near = neighborsOf(id, relations);
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
        {c.card.modifiers.map((m) => ` [${label(MODIFIER_LABELS, m)}]`)}
      </p>
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
      {near.parents.length > 0 || near.children.length > 0 ? (
        <>
          <h4>親・子</h4>
          <IdList
            ids={[...near.parents, ...near.children]}
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
