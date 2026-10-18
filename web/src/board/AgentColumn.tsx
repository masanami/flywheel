import { createContext, useContext } from "react";
import { CycleHeader } from "./CycleHeader.tsx";
import { ErrorCard } from "./ErrorCard.tsx";
import { TaskCard } from "./TaskCard.tsx";
import type { AgentBoard, RunningChallenge } from "./types.ts";

// 実行中で応答のない run の行に出す文言。本番は仕様（M4H24）の「応答なし」のまま。
// 開発時の mock モードの案の画面だけが、旧 board と同じ「更新なし」に差し替えて見せる（flywheel#176）。
export const StaleLabelContext = createContext("⚠ 応答なし（要確認）");

function RunningRow({ run }: { run: RunningChallenge }) {
  const staleLabel = useContext(StaleLabelContext);
  return (
    <div
      className="agent-column-running-run"
      data-testid={`running-run-${run.challengeId}`}
      data-stale={run.unresponsive || undefined}
    >
      <div className="agent-column-running-run-subject">
        <span className="agent-column-running-run-challenge">
          {run.challengeId}
        </span>
        <span className="agent-column-running-run-ledger-title">
          {run.title}
        </span>
      </div>
      {run.unresponsive && (
        <div className="agent-column-running-run-stale-warning">
          {staleLabel}
        </div>
      )}
    </div>
  );
}

// ワークスペース 1 つ分の列。差し込み・並べ替え・優先度方針のバッジ・再開コマンドは持たない。
export function AgentColumn({ agent }: { agent: AgentBoard }) {
  const firstNeedsHumanIndex = agent.challenges.findIndex((c) => c.needsHuman);
  return (
    <section className="agent-column" aria-label={`${agent.name} の列`}>
      <div className="agent-column-header">
        <h2 className="agent-column-title">{agent.name}</h2>
        {agent.cycle && (
          <CycleHeader cycle={agent.cycle} workspace={agent.name} />
        )}
      </div>
      <div className="agent-column-body">
        {agent.runningRuns.length > 0 && (
          <section className="agent-column-running-section">
            <h3 className="agent-column-running-heading">⚡ 実行中</h3>
            {agent.runningRuns.map((run) => (
              <RunningRow key={run.challengeId} run={run} />
            ))}
          </section>
        )}
        {agent.challenges.map((challenge, index) => (
          <div key={challenge.id} className="agent-column-row-group">
            {index === firstNeedsHumanIndex && (
              <h3 className="agent-column-needs-human-heading">🔔 承認待ち</h3>
            )}
            <div
              className="agent-column-row"
              data-testid={`agent-column-row-${challenge.id}`}
            >
              <TaskCard
                challenge={challenge}
                agentName={agent.name}
                workspacePath={agent.path}
              />
            </div>
          </div>
        ))}
        {agent.parseErrors.map((error) => (
          <ErrorCard
            key={`${error.file}:${error.line ?? "?"}:${error.raw}`}
            error={error}
          />
        ))}
      </div>
    </section>
  );
}
