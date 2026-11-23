import { api } from "../api/client.ts";
import type {
  CycleCost,
  CycleScreen as CycleScreenData,
  CycleStatus,
  CycleSupervisorAction,
  CycleWatching,
  CycleWork,
  StatusNeedsHuman,
} from "../api/types.gen.ts";
import {
  CYCLE_BUDGET_GUIDE_TEXT,
  CYCLE_END_REASON_LABELS,
  CYCLE_RESULTS,
  CYCLE_RESULT_LABELS,
  CYCLE_TRIGGER_LABELS,
  CYCLE_WORK_STAGES,
  CYCLE_WORK_STAGE_LABELS,
  type CycleResult,
  JUDGMENT_LABELS,
  LOADING_TEXT,
  NO_SUPERVISOR_ACTIONS_TEXT,
  RUN_KIND_LABELS,
  SUPERVISOR_HAND_LABELS,
  SUPERVISOR_NO_HAND_TEXT,
  SUPERVISOR_REJECT_REASON_LABELS,
  SUPERVISOR_RESULT_LABELS,
  WATCH_STATE_LABELS,
  cycleStateText,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { CycleWorkPart } from "../shell/CycleWorkPart.tsx";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { waitingCounts } from "../shell/waiting.ts";
import { usd } from "../usd.ts";
import { CycleStopButton, STOP_REQUESTED_TEXT } from "./CycleStopButton.tsx";

export const NO_TIME_TEXT = "まだありません";

export function Time({ iso }: { iso: string | null }) {
  if (iso === null) return <>{NO_TIME_TEXT}</>;
  const d = new Date(iso);
  return (
    <time dateTime={iso}>
      {Number.isNaN(d.getTime()) ? iso : d.toLocaleString()}
    </time>
  );
}

export function cycleResultText(result: string | null): string {
  if (result === null) return NO_TIME_TEXT;
  return CYCLE_RESULTS.some((r) => r === result)
    ? CYCLE_RESULT_LABELS[result as CycleResult]
    : result;
}

export function cycleEndReasonText(reason: string | null): string {
  return reason === null
    ? NO_TIME_TEXT
    : label(CYCLE_END_REASON_LABELS, reason);
}

// 承認待ち（計画・完了・不可逆操作）と回答待ちの件数。共通の数え方（shell/waiting.ts）を足し合わせるだけ。
export function approvalAnswerCounts(nh: StatusNeedsHuman | undefined): {
  approval: number;
  answer: number;
} {
  if (!nh) return { approval: 0, answer: 0 };
  const c = waitingCounts(nh);
  return {
    approval: c.plan_approval + c.completion_approval + c.operation,
    answer: c.answer,
  };
}

function Dest({
  workspace,
  challengeId,
  taskId,
  children,
}: {
  workspace: string;
  challengeId: string;
  taskId: string | null;
  children: string;
}) {
  const href =
    taskId === null
      ? hrefOf({
          kind: "challenge",
          name: workspace,
          id: challengeId,
          view: "tree",
        })
      : hrefOf({ kind: "task", name: workspace, id: challengeId, taskId });
  return <a href={href}>{children}</a>;
}

function WatchingList({
  workspace,
  watching,
}: {
  workspace: string;
  watching: CycleWatching[];
}) {
  return (
    <section aria-labelledby="cycle-watching-title" className="now-block">
      <h3 id="cycle-watching-title">見ているタスク</h3>
      {watching.length === 0 ? (
        <p data-testid="cycle-watching-empty">見ているタスクはありません。</p>
      ) : (
        <ul className="now-list" data-testid="cycle-watching">
          {watching.map((w) => (
            <li
              key={`${w.challenge_id}:${w.task_id ?? ""}:${w.run_started_at}`}
              data-testid="cycle-watching-row"
            >
              <Dest
                workspace={workspace}
                challengeId={w.challenge_id}
                taskId={w.task_id}
              >
                {w.title}
              </Dest>{" "}
              <span data-testid="cycle-watching-state">
                {label(WATCH_STATE_LABELS, w.state)}
              </span>{" "}
              <span>
                実行の始まり: <Time iso={w.run_started_at} />
              </span>{" "}
              <span>費用: {usd(w.spent_usd)} USD</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function StageDiagram({
  work,
  nh,
}: {
  work: CycleWork | undefined;
  nh: StatusNeedsHuman | undefined;
}) {
  const waiting = approvalAnswerCounts(nh);
  return (
    <section aria-labelledby="cycle-structure-title" className="now-block">
      <h3 id="cycle-structure-title">サイクルの段</h3>
      <ol className="now-stages" data-testid="cycle-structure">
        {CYCLE_WORK_STAGES.map((s) => (
          <li key={s} data-stage={s}>
            {CYCLE_WORK_STAGE_LABELS[s]} <b>{work ? work.stages[s] : "—"}</b>
          </li>
        ))}
      </ol>
      <p data-testid="cycle-waiting">
        あなたを待っているもの: 承認待ち <b>{waiting.approval}</b> 件・回答待ち{" "}
        <b>{waiting.answer}</b> 件
      </p>
    </section>
  );
}

function SupervisorRecord({
  workspace,
  actions,
}: {
  workspace: string;
  actions: CycleSupervisorAction[];
}) {
  return (
    <section aria-labelledby="cycle-supervisor-title" className="now-block">
      <h3 id="cycle-supervisor-title">スーパーバイザーの記録</h3>
      {actions.length === 0 ? (
        <p data-testid="cycle-supervisor-empty">{NO_SUPERVISOR_ACTIONS_TEXT}</p>
      ) : (
        <ul className="now-list" data-testid="cycle-supervisor">
          {actions.map((a) => (
            <li key={a.id} data-testid="cycle-supervisor-row">
              <b data-testid="cycle-supervisor-hand">
                {a.hand === null
                  ? SUPERVISOR_NO_HAND_TEXT
                  : label(SUPERVISOR_HAND_LABELS, a.hand)}
              </b>{" "}
              <Dest
                workspace={workspace}
                challengeId={a.challenge_id}
                taskId={a.task_id}
              >
                {a.task_title === null
                  ? a.challenge_title
                  : `${a.challenge_title} / ${a.task_title}`}
              </Dest>{" "}
              <span data-testid="cycle-supervisor-result">
                {label(SUPERVISOR_RESULT_LABELS, a.result)}
                {a.reject_reason
                  ? `（${label(SUPERVISOR_REJECT_REASON_LABELS, a.reject_reason)}）`
                  : ""}
              </span>{" "}
              <Time iso={a.at} />
              {a.findings !== "" ? (
                <p data-testid="cycle-supervisor-findings">
                  調べたこと: {a.findings}
                </p>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function costKindText(c: CycleCost): string {
  if (c.kind === "judgment" && c.role !== null)
    return label(JUDGMENT_LABELS, c.role);
  return label(RUN_KIND_LABELS, c.kind);
}

function CostBreakdown({ costs }: { costs: CycleCost[] }) {
  return (
    <section aria-labelledby="cycle-costs-title" className="now-block">
      <h3 id="cycle-costs-title">費用の内訳</h3>
      {costs.length === 0 ? (
        <p data-testid="cycle-costs-empty">費用はまだありません。</p>
      ) : (
        <ul className="now-list" data-testid="cycle-costs">
          {costs.map((c) => (
            <li key={`${c.kind}:${c.role ?? ""}`} data-testid="cycle-cost-row">
              {costKindText(c)}: {usd(c.cost_usd)} USD
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// サイクルの画面。判定は core が持ち、ここは API の値を写す。時間の内訳・段ごとの結果・振り返りは出さない。
export function CycleScreen({
  live,
  agentName,
}: {
  live: LiveState;
  agentName: string;
}) {
  const data = live.data[agentName];
  const { result, reload } = useScreenData<CycleScreenData>(
    live,
    agentName,
    () => api.cycle(agentName),
  );
  const cycle: CycleStatus | undefined =
    result.state === "ready" ? result.data : data?.cycle;
  const running = cycle?.state === "running" ? (cycle.running ?? null) : null;
  const neverRun =
    cycle !== undefined &&
    running === null &&
    cycle.last_started_at === null &&
    cycle.state !== "stale";
  const work = data?.status?.cycle_work;
  const workspacePath = live.workspaces.find((w) => w.name === agentName)?.path;

  return (
    <section aria-labelledby="screen-title">
      <h2 id="screen-title">サイクル</h2>
      {cycle ? (
        <dl className="cycle-facts">
          <div>
            <dt>状態</dt>
            <dd data-testid="cycle-screen-state">
              {cycleStateText(cycle.state)}
            </dd>
          </div>
          {neverRun ? null : running ? (
            <>
              <div>
                <dt>始まり</dt>
                <dd data-testid="cycle-running-started">
                  <Time iso={running.started_at} />
                </dd>
              </div>
              <div>
                <dt>きっかけ</dt>
                <dd data-testid="cycle-running-trigger">
                  {label(CYCLE_TRIGGER_LABELS, running.trigger)}
                </dd>
              </div>
              <div>
                <dt>回り</dt>
                <dd data-testid="cycle-running-rounds">{running.rounds}</dd>
              </div>
              <div>
                <dt>作業中の実行</dt>
                <dd data-testid="cycle-running-runs">
                  {running.active_runs} 件（同時に {running.max_parallel_runs}{" "}
                  件まで）
                </dd>
              </div>
              <div>
                <dt>このサイクルの費用</dt>
                <dd data-testid="cycle-running-cost">
                  {usd(running.spent_usd)} USD（上限 {usd(running.budget_usd)}{" "}
                  USD）
                </dd>
              </div>
            </>
          ) : (
            <>
              <div>
                <dt>最後の始まり</dt>
                <dd data-testid="cycle-last-started">
                  <Time iso={cycle.last_started_at ?? null} />
                </dd>
              </div>
              <div>
                <dt>最後の終わり</dt>
                <dd data-testid="cycle-last-ended">
                  <Time iso={cycle.last_ended_at ?? null} />
                </dd>
              </div>
              <div>
                <dt>最後の結果</dt>
                <dd data-testid="cycle-last-result">
                  {cycleResultText(cycle.last_result ?? null)}
                </dd>
              </div>
              <div>
                <dt>終わった理由</dt>
                <dd data-testid="cycle-last-end-reason">
                  {cycleEndReasonText(cycle.last_end_reason ?? null)}
                </dd>
              </div>
            </>
          )}
        </dl>
      ) : (
        <p>{data ? "サイクルの状態を読めませんでした。" : LOADING_TEXT}</p>
      )}
      {running ? (
        running.stop_requested ? (
          <output data-testid="cycle-stop-requested">
            {STOP_REQUESTED_TEXT}
          </output>
        ) : (
          <CycleStopButton workspace={agentName} onDone={reload} />
        )
      ) : null}
      {cycle && !running && cycle.last_end_reason === "budget" ? (
        <p className="now-hint" data-testid="cycle-budget-guide">
          {CYCLE_BUDGET_GUIDE_TEXT}
        </p>
      ) : null}
      {neverRun ? (
        <CycleWorkPart
          workspace={agentName}
          workspacePath={workspacePath}
          work={work}
          cycleState={cycle?.state ?? ""}
          hideStages
        />
      ) : (
        <>
          {running ? (
            <WatchingList workspace={agentName} watching={running.watching} />
          ) : null}
          {cycle ? (
            <StageDiagram work={work} nh={data?.status?.needs_human} />
          ) : null}
          <CycleWorkPart
            workspace={agentName}
            workspacePath={workspacePath}
            work={work}
            cycleState={cycle?.state ?? ""}
            hideStages
          />
          {result.state === "ready" ? (
            <>
              <SupervisorRecord
                workspace={agentName}
                actions={result.data.supervisor_actions ?? []}
              />
              <CostBreakdown costs={result.data.costs ?? []} />
            </>
          ) : null}
        </>
      )}
    </section>
  );
}
