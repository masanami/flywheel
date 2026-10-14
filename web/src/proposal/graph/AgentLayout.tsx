// 関係グラフの画面の配置の案（flywheel#176・2026-10-14 オーナー）: 左のサイドバーでエージェント（ワークスペース）を選び、
// 中央に選んだエージェントの関係グラフを描く。下の引き出しに、選んだエージェントのターミナル／対話の置き場所を取る
// （中身は作らない）。狭い画面ではサイドバーの代わりに上部の選択欄で選ぶ。

import type { LiveState } from "../../live/useLive.ts";
import { isActive } from "./model.ts";

export interface AgentSummary {
  name: string;
  readable: boolean;
  state: string;
  human: number;
  active: number;
}

// サイドバーに添える小さな要約。人間の操作待ちと、いま run が動いている（実施中）の件数だけを数える。
export function summariesOf(live: LiveState): AgentSummary[] {
  return live.workspaces.map((w) => {
    const cards = live.data[w.name]?.challenges?.challenges ?? [];
    return {
      name: w.name,
      readable: w.state === "ok",
      state: w.state,
      human: cards.filter((c) => c.card.next_human_actions.length > 0).length,
      active: cards.filter(isActive).length,
    };
  });
}

const summaryText = (s: AgentSummary) =>
  s.readable
    ? `人間の操作待ち ${s.human}・実施中 ${s.active}`
    : `読めない: ${s.state}`;

export function AgentSidebar({
  agents,
  ws,
  onChoose,
}: {
  agents: AgentSummary[];
  ws: string | null;
  onChoose: (name: string) => void;
}) {
  return (
    <nav aria-label="エージェント" className="agent-sidebar">
      <h3>エージェント</h3>
      <ul>
        {agents.map((a) => (
          <li key={a.name}>
            <button
              type="button"
              aria-label={`${a.name} ${summaryText(a)}`}
              aria-current={a.name === ws ? "true" : undefined}
              disabled={!a.readable}
              onClick={() => onChoose(a.name)}
            >
              <span className="agent-sidebar-name">{a.name}</span>
              <span className="agent-sidebar-summary">
                {a.readable ? (
                  <>
                    <span data-kind="human">! {a.human}</span>
                    <span data-kind="active">▶ {a.active}</span>
                  </>
                ) : (
                  summaryText(a)
                )}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}

// 幅の狭い画面での選び方。サイドバーで横幅を取らず、上部の選択欄にする。
export function AgentPicker({
  agents,
  ws,
  onChoose,
  label = "エージェント",
}: {
  agents: AgentSummary[];
  ws: string | null;
  onChoose: (name: string) => void;
  label?: string;
}) {
  return (
    <label className="agent-picker">
      {label}
      <select value={ws ?? ""} onChange={(e) => onChoose(e.target.value)}>
        {agents.map((a) => (
          <option key={a.name} value={a.name} disabled={!a.readable}>
            {a.name}（{summaryText(a)}）
          </option>
        ))}
      </select>
    </label>
  );
}

// 下の引き出し: 選んだエージェントのターミナル／対話の置き場所（試作では中身を作らない）。
// グラフでもボードでも同じ位置に出し、選んだエージェントを対象にする。
export function AgentDock({
  agents,
  ws,
  open,
  onToggle,
  onChoose,
}: {
  agents: AgentSummary[];
  ws: string | null;
  open: boolean;
  onToggle: () => void;
  onChoose: (name: string) => void;
}) {
  return (
    <section
      aria-label="ターミナル／対話（案）"
      className="agent-dock"
      data-open={open || undefined}
    >
      <div className="agent-dock-bar">
        <button
          type="button"
          aria-expanded={open}
          aria-controls="agent-dock-body"
          onClick={onToggle}
        >
          {open ? "▾" : "▴"} ターミナル／対話（案）
        </button>
        <AgentPicker agents={agents} ws={ws} onChoose={onChoose} label="対象" />
      </div>
      {open ? (
        <div id="agent-dock-body" className="agent-dock-body">
          <p>
            ここに {ws ?? "（エージェント未選択）"}{" "}
            のターミナル／エージェントとの対話が入る。
          </p>
          <p className="graph-muted">
            試作では中身を作らず、置き場所だけを取っている。エージェントを切り替えると、この欄の対象も切り替わる。
          </p>
        </div>
      ) : null}
    </section>
  );
}
