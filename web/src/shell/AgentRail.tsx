import type { LiveState } from "../live/useLive.ts";
import { type Route, hrefOf } from "./route.ts";
import { waitingCounts, waitingTotal } from "./waiting.ts";

// 頭文字は名前の先頭の 1 字（英字は大文字）。
export function initialOf(name: string): string {
  const ch = Array.from(name)[0] ?? "";
  return ch.toLocaleUpperCase();
}

// 左の列。エージェントを足す口は置かない。「すべて」はエージェントが 2 つ以上のときだけ一番上に置く。
export function AgentRail({
  live,
  current,
}: {
  live: LiveState;
  current: Route | null;
}) {
  const selected =
    current && current.kind !== "all" && current.kind !== "unknown"
      ? current.name
      : null;
  const agents = live.workspaces;
  const selectedAll = current?.kind === "all";
  return (
    <nav className="agent-rail" aria-label="エージェント">
      <span className="agent-rail-brand">flywheel</span>
      <ul>
        {agents.length >= 2 ? (
          <li>
            <a
              href={hrefOf({ kind: "all" })}
              className="agent-item"
              aria-current={selectedAll ? "page" : undefined}
            >
              <span className="agent-initial" aria-hidden="true">
                ★
              </span>
              <span className="agent-name">すべて</span>
            </a>
          </li>
        ) : null}
        {agents.map((w) => {
          const nh = live.needsHuman?.workspaces.find((x) => x.name === w.name);
          const count = nh ? waitingTotal(waitingCounts(nh.needs_human)) : 0;
          const unreadable = w.state !== "ok";
          return (
            <li key={w.name}>
              <a
                href={hrefOf({ kind: "agent", name: w.name, screen: "now" })}
                className="agent-item"
                data-unreadable={unreadable ? "true" : undefined}
                aria-current={selected === w.name ? "page" : undefined}
              >
                <span className="agent-initial" aria-hidden="true">
                  {initialOf(w.name)}
                </span>
                <span className="agent-name">{w.name}</span>
                {unreadable ? (
                  <span className="agent-unreadable">（読めません）</span>
                ) : null}
                {count > 0 ? (
                  <span className="agent-count" data-testid="waiting-count">
                    {count}
                  </span>
                ) : null}
              </a>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
