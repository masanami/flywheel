import { useState } from "react";
import { api } from "../api/client.ts";
import type { TreeChallenge } from "../api/types.gen.ts";
import {
  LEGEND_EDGE_HINT,
  PHASE_LEGEND_LABELS,
  PHASE_LEGEND_ORDER,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { WAITING_LABELS, type WaitingKind } from "../shell/waiting.ts";
import { TaskTree } from "../tree/TaskTree.tsx";

export const NO_CHALLENGES_TEXT = "開いている課題はありません。";
const LOADING = "読み込んでいます";

// next_human_actions のコードを、「いま進められること」と同じ語（WAITING_LABELS）の種類へ写す。
const ACTION_KIND: Record<string, WaitingKind> = {
  approve_plan: "plan_approval",
  approve_completion: "completion_approval",
  approve_operation: "operation",
  answer_question: "answer",
  review_discrepancy: "discrepancy",
  increase_budget: "budget",
  triage: "triage",
  clear_slot: "slot",
};
const waitingKinds = (actions: string[]): WaitingKind[] => [
  ...new Set(actions.flatMap((a) => (ACTION_KIND[a] ? [ACTION_KIND[a]] : []))),
];

// 費用は小数 2 桁までで、末尾の 0 を落とす（30 は「30」、11.2 は「11.2」）。
const usd = (n: number) => String(Number(n.toFixed(2)));

// 課題の画面。課題のツリーの API を、この画面を開いている間だけ読み、そのエージェントの変化の通し番号が進んだら読み直す。
// 区分・進み・費用・件数は API の値をそのまま出す（画面では導かない）。
export function ChallengesScreen({
  live,
  agentName,
}: {
  live: LiveState;
  agentName: string;
}) {
  const { result } = useScreenData(live, agentName, () => api.tree(agentName));
  const [showDone, setShowDone] = useState(false);
  const home = hrefOf({ kind: "agent", name: agentName, screen: "now" });

  return (
    <section aria-labelledby="screen-title">
      <h2 id="screen-title">課題</h2>
      <Legend />
      {result.state === "loading" ? <p>{LOADING}</p> : null}
      {result.state === "error" ? (
        <p role="alert">{String(result.error)}</p>
      ) : null}
      {result.state === "ready" ? (
        <>
          {result.data.challenges.length === 0 ? (
            <p>
              {NO_CHALLENGES_TEXT} <a href={home}>「いま進められること」へ</a>
            </p>
          ) : (
            result.data.challenges.map((c) => (
              <ChallengeFrame key={c.id} agentName={agentName} c={c} />
            ))
          )}
          {result.data.recent_done.length > 0 ? (
            <div className="done-section">
              <button
                type="button"
                className="done-toggle"
                aria-expanded={showDone}
                onClick={() => setShowDone((v) => !v)}
              >
                完了した課題を開く（最近の {result.data.recent_done.length} 件）
              </button>
              {showDone
                ? result.data.recent_done.map((c) => (
                    <ChallengeFrame key={c.id} agentName={agentName} c={c} />
                  ))
                : null}
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function Legend() {
  return (
    <div className="legend" data-testid="legend">
      {PHASE_LEGEND_ORDER.map((p) => (
        <span key={p} className="phase-pill" data-phase={p}>
          {PHASE_LEGEND_LABELS[p]}
        </span>
      ))}
      <span className="legend-hint">{LEGEND_EDGE_HINT}</span>
    </div>
  );
}

function ChallengeFrame({
  agentName,
  c,
}: {
  agentName: string;
  c: TreeChallenge;
}) {
  const detail = hrefOf({
    kind: "challenge",
    name: agentName,
    id: c.id,
    view: "tree",
  });
  const needsPlan = c.next_human_actions.includes("approve_plan");
  return (
    <article className="challenge-frame" data-testid="challenge-frame">
      <h3>
        <a href={detail}>{c.title}</a>{" "}
        <span className="status-pill">{c.status_label}</span>
        {waitingKinds(c.next_human_actions).map((k) => (
          <span key={k}>
            {" "}
            <span className="action-pill">{WAITING_LABELS[k]}</span>
          </span>
        ))}
        {needsPlan ? (
          <>
            {" "}
            <a
              href={hrefOf({ kind: "approve_plan", name: agentName, id: c.id })}
            >
              計画の承認の画面へ
            </a>
          </>
        ) : null}
      </h3>
      <div className="frame-meta">
        <span>
          タスク <b data-testid="tasks-done">{c.tasks_done}</b> /{" "}
          <b data-testid="tasks-total">{c.tasks_total}</b>
        </span>
        <span>
          費用 <b data-testid="cost">{usd(c.cost_usd)}</b>
          {c.budget_usd !== null ? (
            <>
              {" "}
              / <b data-testid="budget">{usd(c.budget_usd)}</b>
            </>
          ) : null}{" "}
          USD
        </span>
        <span>
          未のフォローアップ{" "}
          <b data-testid="followups-open">{c.followups_open}</b>
        </span>
        {c.uses_integration !== null ? (
          <span>
            統合先{" "}
            <b data-testid="integration">
              {c.uses_integration ? "使う" : "使わない"}
            </b>
          </span>
        ) : null}
      </div>
      {c.dependencies.length > 0 ? (
        <p className="frame-deps" data-testid="dependencies">
          前提:{" "}
          {c.dependencies.map((d) => (
            <span key={d.challenge_id} className="dep">
              <a
                href={hrefOf({
                  kind: "challenge",
                  name: agentName,
                  id: d.challenge_id,
                  view: "tree",
                })}
              >
                {d.title}
              </a>{" "}
              <span className="status-pill">{d.status_label}</span>
              {d.satisfied ? null : (
                <span className="dep-unmet"> 未完了</span>
              )}{" "}
            </span>
          ))}
        </p>
      ) : null}
      <TaskTree
        tasks={c.tasks}
        taskHref={(taskId) =>
          hrefOf({ kind: "task", name: agentName, id: c.id, taskId })
        }
      />
    </article>
  );
}
