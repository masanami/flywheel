import { useState } from "react";
import type { CardState } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { RefreshKeyContext } from "../nav.tsx";
import { AgentColumn } from "./AgentColumn.tsx";
import type { BoardFilter } from "./FilterBar.tsx";
import { FilterBar } from "./FilterBar.tsx";
import { NeedsHumanStrip } from "./NeedsHumanStrip.tsx";
import { toAgentBoard } from "./adapt.ts";
import { type HumanKind, filterByKind } from "./filter.ts";
import type { AgentBoard, Challenge } from "./types.ts";

// 完了と取り下げ（どちらも終端）は既定で隠す。表示の絞り込みだけで、データには影響しない。
const COMPLETED_STATES: readonly CardState[] = ["done", "withdrawn"];

function visibleChallenges(
  challenges: Challenge[],
  filter: BoardFilter,
  showCompleted: boolean,
): Challenge[] {
  let result = challenges;
  if (!showCompleted) {
    result = result.filter(
      (c) => !(COMPLETED_STATES as readonly string[]).includes(c.state),
    );
  }
  if (filter === "needsHuman") {
    result = result.filter((c) => c.needsHuman);
  }
  return result;
}

// ワークスペースを「エージェントの列」として並べる。データは useLive が API と SSE から集めたものを受け取る。
export function Board({ live }: { live: LiveState }) {
  const [filter, setFilter] = useState<BoardFilter>("all");
  const [showCompleted, setShowCompleted] = useState(false);
  const [kind, setKind] = useState<HumanKind | null>(null);

  if (live.workspaces.length === 0 && live.lastUpdated === null) {
    return <div className="board-loading">読み込み中...</div>;
  }

  const agents = new Map(
    live.workspaces.map((w) => [w.name, toAgentBoard(w, live.data[w.name])]),
  );
  const needsHumanOf = (name: string) =>
    live.needsHuman?.workspaces.find((w) => w.name === name)?.needs_human;

  return (
    <RefreshKeyContext.Provider value={live.lastUpdated}>
      <div className="board">
        <FilterBar
          value={filter}
          onChange={setFilter}
          showCompleted={showCompleted}
          onShowCompletedChange={setShowCompleted}
        />
        <NeedsHumanStrip
          live={live}
          selected={kind}
          onSelect={setKind}
          cardsOf={(name) => agents.get(name)?.challenges ?? []}
        />
        <div className="board-main-row">
          <div className="board-columns">
            {live.workspaces.map((w) => {
              const agent = agents.get(w.name) as AgentBoard;
              return (
                <AgentColumn
                  key={w.name}
                  agent={{
                    ...agent,
                    challenges: filterByKind(
                      visibleChallenges(
                        agent.challenges,
                        filter,
                        showCompleted,
                      ),
                      needsHumanOf(w.name),
                      kind,
                    ),
                    runningRuns:
                      filter === "needsHuman" || kind !== null
                        ? []
                        : agent.runningRuns,
                  }}
                />
              );
            })}
          </div>
        </div>
      </div>
    </RefreshKeyContext.Provider>
  );
}
