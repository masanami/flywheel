import { useState } from "react";
import type { LiveState } from "../live/useLive.ts";
import { RefreshKeyContext } from "../nav.tsx";
import { AgentColumn } from "./AgentColumn.tsx";
import type { BoardFilter } from "./FilterBar.tsx";
import { FilterBar } from "./FilterBar.tsx";
import { toAgentBoard } from "./adapt.ts";
import type { Challenge } from "./types.ts";

// 完了は既定で隠す。表示の絞り込みだけで、データには影響しない。
const COMPLETED_STATE = "done";

function visibleChallenges(
  challenges: Challenge[],
  filter: BoardFilter,
  showCompleted: boolean,
): Challenge[] {
  let result = challenges;
  if (!showCompleted) {
    result = result.filter((c) => c.state !== COMPLETED_STATE);
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

  if (live.workspaces.length === 0 && live.lastUpdated === null) {
    return <div className="board-loading">読み込み中...</div>;
  }

  const unreadable = live.needsHuman?.unreadable_workspaces ?? [];

  return (
    <RefreshKeyContext.Provider value={live.lastUpdated}>
      <div className="board">
        <FilterBar
          value={filter}
          onChange={setFilter}
          showCompleted={showCompleted}
          onShowCompletedChange={setShowCompleted}
        />
        {unreadable.length > 0 ? (
          <p role="alert" data-testid="unreadable-workspaces">
            読み取れなかったワークスペース（集約から除いています）:{" "}
            {unreadable.join("、")}
          </p>
        ) : null}
        <div className="board-main-row">
          <div className="board-columns">
            {live.workspaces.map((w) => {
              const agent = toAgentBoard(w, live.data[w.name]);
              return (
                <AgentColumn
                  key={w.name}
                  agent={{
                    ...agent,
                    challenges: visibleChallenges(
                      agent.challenges,
                      filter,
                      showCompleted,
                    ),
                    runningRuns:
                      filter === "needsHuman" ? [] : agent.runningRuns,
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
