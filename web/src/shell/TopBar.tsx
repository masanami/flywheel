import type { Workspace } from "../api/types.gen.ts";
import {
  CYCLE_STATES,
  CYCLE_STATE_LABELS,
  type CycleState,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { type AgentScreen, type Route, hrefOf } from "./route.ts";

export const TOP_ITEMS: { screen: AgentScreen; label: string }[] = [
  { screen: "now", label: "いま進められること" },
  { screen: "challenges", label: "課題" },
  { screen: "followups", label: "フォローアップ" },
  { screen: "cycle", label: "サイクル" },
];

// 課題の詳細・タスクの詳細・計画の承認の画面では「課題」を選んでいることにする。
function selectedScreen(r: Route): AgentScreen | null {
  switch (r.kind) {
    case "agent":
      return r.screen;
    case "challenge":
    case "approve_plan":
    case "task":
      return "challenges";
    default:
      return null;
  }
}

export function TopBar({
  live,
  current,
  agent,
}: {
  live: LiveState;
  current: Route | null;
  agent: Workspace | undefined;
}) {
  const all = current?.kind === "all";
  const readable = agent !== undefined && agent.state === "ok";
  const sel = current ? selectedScreen(current) : null;
  const cycleState = agent ? live.data[agent.name]?.cycle?.state : undefined;
  const cycleText =
    cycleState && CYCLE_STATES.some((c) => c === cycleState)
      ? CYCLE_STATE_LABELS[cycleState as CycleState]
      : undefined;
  return (
    <header className="top-bar">
      <h1 className="top-bar-title">{all ? "すべて" : (agent?.name ?? "")}</h1>
      {readable && agent ? (
        <nav aria-label="画面">
          <ul className="top-bar-items">
            {TOP_ITEMS.map((it) => (
              <li key={it.screen}>
                <a
                  href={hrefOf({
                    kind: "agent",
                    name: agent.name,
                    screen: it.screen,
                  })}
                  aria-current={sel === it.screen ? "page" : undefined}
                >
                  {it.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      ) : null}
      <span className="top-bar-status" data-testid="connection">
        {readable && cycleText ? (
          <span data-testid="cycle-state">{cycleText}</span>
        ) : null}
        {live.connected ? null : (
          <>
            <output>切断中</output>
            {/* 最後に読めた時刻（切れた時刻ではない）。見直し前と同じ。 */}
            <span className="top-bar-updated">
              最終更新:{" "}
              {live.lastUpdated ? live.lastUpdated.toLocaleString() : "未取得"}
            </span>
          </>
        )}
      </span>
    </header>
  );
}
