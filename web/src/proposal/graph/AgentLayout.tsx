// 関係グラフの画面の配置の案（flywheel#176・2026-10-14 オーナー）: 左端のアイコンの細い列で
// エージェント（ワークスペース）を選び、中央に選んだエージェントの関係グラフを描く。選んだエージェントのターミナル／チャットの欄は
// 下の引き出しか右の分割に置き（試作では見比べられるよう切り替える）、中身は偽の見た目だけを作る。
// 狭い画面では列の代わりに上部の選択欄で選ぶ。

import { useEffect, useState } from "react";
import { api } from "../../api/client.ts";
import type { LiveState } from "../../live/useLive.ts";
import { ChatMock, TerminalMock } from "./DockMocks.tsx";
import { isActive } from "./model.ts";

export interface AgentSummary {
  name: string;
  readable: boolean;
  state: string;
  human: number;
  active: number;
}

// 列に添える小さな要約。人間の操作待ちと、いま run が動いている（実施中）の件数だけを数える。
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

// エージェントごとのアイコンの設定（案の口 agentProfiles）。読めなければ設定なしとして頭文字で作る。
export function useAgentIcons(refreshKey: unknown): Map<string, string | null> {
  const [icons, setIcons] = useState(new Map<string, string | null>());
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    let live = true;
    api.agentProfiles().then(
      (r) => live && setIcons(new Map(r.agents.map((a) => [a.name, a.icon]))),
      () => live && setIcons(new Map()),
    );
    return () => {
      live = false;
    };
  }, [refreshKey]);
  return icons;
}

// 頭文字のアイコンの色。Slack の色にせず、ボードの印（出典の札など）と同じ落ち着いた系統から採る（2026-10-17 オーナー）
const ICON_COLORS = ["#2f5fa8", "#2f8a55", "#7a5bbf", "#b5651d", "#55555b"];

function colorOf(name: string): string {
  let h = 0;
  for (const ch of name) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  return ICON_COLORS[h % ICON_COLORS.length] ?? "#55555b";
}

export function AgentIcon({
  name,
  icon,
}: {
  name: string;
  icon: string | null | undefined;
}) {
  if (icon && /^(https?:|data:image\/)/.test(icon))
    return <img className="agent-icon" src={icon} alt="" />;
  if (icon)
    return (
      <span className="agent-icon agent-icon-emoji" aria-hidden="true">
        {icon}
      </span>
    );
  return (
    <span
      className="agent-icon agent-icon-initial"
      style={{ background: colorOf(name) }}
      aria-hidden="true"
    >
      {Array.from(name)[0]?.toUpperCase()}
    </span>
  );
}

// 左端のアイコンの列。アイコンの下に名前を出し（長い名前は 2 行で切る）、全文と件数はマウスを重ねたとき・
// キーボードの焦点で出す。
export function AgentRail({
  agents,
  icons,
  ws,
  onChoose,
}: {
  agents: AgentSummary[];
  icons: Map<string, string | null>;
  ws: string | null;
  onChoose: (name: string) => void;
}) {
  return (
    <nav aria-label="エージェント" className="agent-rail">
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
              <span className="agent-rail-icon">
                <AgentIcon name={a.name} icon={icons.get(a.name)} />
                {a.readable && a.human > 0 ? (
                  <span className="agent-rail-badge" aria-hidden="true">
                    {a.human}
                  </span>
                ) : null}
                {a.readable && a.active > 0 ? (
                  <span className="agent-rail-active" aria-hidden="true" />
                ) : null}
              </span>
              <span className="agent-rail-name" aria-hidden="true">
                {a.name}
              </span>
              <span className="agent-rail-tip" aria-hidden="true">
                <b>{a.name}</b>
                <span>{summaryText(a)}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}

// 幅の狭い画面での選び方。列で横幅を取らず、上部の選択欄にする。
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

export type DockPosition = "bottom" | "right";
export type DockTab = "terminal" | "chat";

const TABS: { key: DockTab; label: string }[] = [
  { key: "terminal", label: "ターミナル" },
  { key: "chat", label: "チャット" },
];

// 選んだエージェントのターミナル／チャットの欄。下の引き出しでも右の分割でも同じ部品で、置き場所は外側の配置が決める。
export function AgentDock({
  agents,
  ws,
  open,
  position,
  tab,
  onToggle,
  onChoose,
  onTab,
}: {
  agents: AgentSummary[];
  ws: string | null;
  open: boolean;
  position: DockPosition;
  tab: DockTab;
  onToggle: () => void;
  onChoose: (name: string) => void;
  onTab: (t: DockTab) => void;
}) {
  const arrow = position === "bottom" ? (open ? "▾" : "▴") : open ? "▸" : "◂";
  return (
    <section
      aria-label="ターミナル／チャット（案）"
      className="agent-dock"
      data-pos={position}
      data-open={open || undefined}
    >
      <div className="agent-dock-bar">
        <button
          type="button"
          className="agent-dock-toggle"
          aria-expanded={open}
          aria-controls="agent-dock-body"
          title={open ? "欄を閉じる" : "欄を開く"}
          onClick={onToggle}
        >
          <span aria-hidden="true">{arrow}</span>
          <span className="agent-dock-toggle-label">
            ターミナル／チャット（案）
          </span>
        </button>
        {open ? (
          <>
            <div
              role="tablist"
              aria-label="欄の中身（案）"
              className="agent-dock-tabs"
            >
              {TABS.map((t) => (
                <button
                  key={t.key}
                  type="button"
                  role="tab"
                  id={`agent-dock-tab-${t.key}`}
                  aria-selected={tab === t.key}
                  aria-controls="agent-dock-body"
                  onClick={() => onTab(t.key)}
                >
                  {t.label}
                </button>
              ))}
            </div>
            <AgentPicker
              agents={agents}
              ws={ws}
              onChoose={onChoose}
              label="対象"
            />
          </>
        ) : null}
      </div>
      {open ? (
        <div
          id="agent-dock-body"
          role="tabpanel"
          aria-labelledby={`agent-dock-tab-${tab}`}
          className="agent-dock-body"
          data-tab={tab}
        >
          {tab === "terminal" ? (
            <TerminalMock ws={ws ?? "（未選択）"} />
          ) : (
            <ChatMock ws={ws ?? "（未選択）"} />
          )}
        </div>
      ) : null}
    </section>
  );
}
