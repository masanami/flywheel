import { useEffect, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type {
  Hold,
  RunReport,
  ShowCardResponse,
  Task,
  TreeTask,
} from "../api/types.gen.ts";
import { ApprovalGuide } from "../board/ApprovalGuide.tsx";
import { safeHref } from "../detail/Items.tsx";
import {
  HOLD_KIND_PARENT_DIRECT,
  LOADING_TEXT,
  NO_COST_LABEL,
  PHASE_LABELS,
  RUN_KIND_LABELS,
  RUN_RESULT_LABELS,
  RUN_RUNNING_LABEL,
  UPSTREAM_STATE_LABELS,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { NotFound } from "../shell/NotFound.tsx";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { usd } from "../usd.ts";
import { shortTime } from "../when.ts";
import { whoOf } from "../who.ts";

// 委譲の実行（報告の API を呼ぶ対象）の種類。
const DELEGATION_KINDS = ["delegate", "contract"];

// タスクの詳細の画面。課題の詳細の API の応答を、タスクの ID で結んで作る（tasks = 本文、tree.tasks = 区分・費用）。
// 区分と費用は core が返した値をそのまま出す。画面のボタンは置かず、回答は端末のコマンドの案内で示す。
export function TaskDetail({
  live,
  agentName,
  workspacePath,
  id,
  taskId,
}: {
  live: LiveState;
  agentName: string;
  workspacePath: string;
  id: string;
  taskId: string;
}) {
  const { result } = useScreenData(
    live,
    agentName,
    () => api.challenge(agentName, id),
    [id],
  );
  const home = hrefOf({ kind: "agent", name: agentName, screen: "now" });
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error") {
    const e = result.error;
    if (e instanceof ApiError && e.code === "not_found") {
      return <NotFound homeHref={home} />;
    }
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const show = result.data;
  const task = show.tasks.find((t) => t.id === taskId);
  const node = show.tree.tasks.find((t) => t.id === taskId);
  if (!task || !node) return <NotFound homeHref={home} />;
  return (
    <TaskView
      key={`${id}/${taskId}`}
      show={show}
      task={task}
      node={node}
      agentName={agentName}
      workspacePath={workspacePath}
    />
  );
}

const taskHref = (agentName: string, id: string, taskId: string) =>
  hrefOf({ kind: "task", name: agentName, id, taskId });

function TaskView({
  show,
  task,
  node,
  agentName,
  workspacePath,
}: {
  show: ShowCardResponse;
  task: Task;
  node: TreeTask;
  agentName: string;
  workspacePath: string;
}) {
  const id = show.challenge.id;
  const link = (t: { id: string; title: string }) => {
    const phase = show.tree.tasks.find((n) => n.id === t.id)?.phase;
    return (
      <>
        <a href={taskHref(agentName, id, t.id)}>{t.title}</a>
        {phase !== undefined ? (
          <>
            {" "}
            <small className="task-link-phase">
              {label(PHASE_LABELS, phase)}
            </small>
          </>
        ) : null}
      </>
    );
  };
  const titleOfId = (tid: string): string =>
    show.tasks.find((t) => t.id === tid)?.title ?? tid;
  const dependents = show.tasks.filter((t) => t.depends_on.includes(task.id));
  const child = task.source_key
    ? show.source_children.find((c) => c.external_key === task.source_key)
    : undefined;
  const holds = show.holds
    .filter((h) => h.task_id === task.id)
    .sort((a, b) => a.raised_at.localeCompare(b.raised_at));
  const openHolds = holds.filter(
    (h) =>
      h.answer === null &&
      h.closed_at === null &&
      h.kind !== HOLD_KIND_PARENT_DIRECT,
  );
  const runs = show.runs
    .filter((r) => r.task_id === task.id)
    .sort((a, b) => b.started_at.localeCompare(a.started_at));
  const latestDelegation = runs.find((r) => DELEGATION_KINDS.includes(r.kind));

  return (
    <article
      className="detail task-detail"
      data-testid="task-detail"
      aria-labelledby="task-title"
    >
      <p className="task-crumb" data-testid="task-crumb">
        <a
          href={hrefOf({
            kind: "agent",
            name: agentName,
            screen: "challenges",
          })}
        >
          課題
        </a>
        {" › "}
        <a
          href={hrefOf({
            kind: "challenge",
            name: agentName,
            id,
            view: "tree",
          })}
        >
          {show.challenge.title}
        </a>
        {" ›"}
      </p>
      <h2 id="task-title" data-testid="task-title">
        {task.title}
      </h2>
      <dl className="detail-items" data-testid="task-items">
        <div>
          <dt>区分</dt>
          <dd data-testid="task-phase" data-phase={node.phase}>
            {label(PHASE_LABELS, node.phase)}
          </dd>
        </div>
        {task.depends_on.length > 0 ? (
          <div data-testid="task-prerequisites">
            <dt>前提</dt>
            <dd>
              <ul className="task-links">
                {task.depends_on.map((d) => (
                  <li key={d}>{link({ id: d, title: titleOfId(d) })}</li>
                ))}
              </ul>
            </dd>
          </div>
        ) : null}
        {dependents.length > 0 ? (
          <div data-testid="task-dependents">
            <dt>この後</dt>
            <dd>
              <ul className="task-links">
                {dependents.map((d) => (
                  <li key={d.id}>{link(d)}</li>
                ))}
              </ul>
            </dd>
          </div>
        ) : null}
        <div>
          <dt>費用</dt>
          <dd>
            <b data-testid="task-cost">{usd(node.cost_usd)}</b> USD
          </dd>
        </div>
        {task.source_key !== null ? (
          <div data-testid="task-source">
            <dt>取り込み元の子</dt>
            <dd>
              {child ? (
                <>
                  <a
                    href={safeHref(child.url)}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {child.title}
                  </a>{" "}
                  <span className="status-pill" data-testid="task-upstream">
                    {label(UPSTREAM_STATE_LABELS, child.upstream_state)}
                  </span>
                </>
              ) : (
                task.source_key
              )}
            </dd>
          </div>
        ) : null}
      </dl>
      {node.phase === "waiting" ? (
        <section
          className="detail-waiting"
          aria-label="あなたを待っているもの"
          data-testid="task-waiting"
        >
          <h3>あなたを待っているもの</h3>
          {openHolds.map((h) => (
            <div
              key={h.raised_at}
              className="detail-question"
              data-testid="task-hold-question"
            >
              <Markdown text={h.question} />
            </div>
          ))}
          <ApprovalGuide
            challengeId={id}
            actions={["answer_question"]}
            workspacePath={workspacePath}
          />
        </section>
      ) : null}
      <section aria-label="やること" data-testid="task-body">
        <h3>やること</h3>
        <Markdown text={task.body} />
      </section>
      <section aria-label="完了条件" data-testid="task-done-criteria">
        <h3>完了条件</h3>
        <Markdown text={task.done_criteria} />
      </section>
      {task.non_goals.length > 0 ? (
        <section aria-label="やらないこと" data-testid="task-non-goals">
          <h3>やらないこと</h3>
          <ul>
            {task.non_goals.map((n, i) => (
              // 並びの位置で足りる（不変の入力）
              // biome-ignore lint/suspicious/noArrayIndexKey: 同じ文が並びうる
              <li key={i}>{n}</li>
            ))}
          </ul>
        </section>
      ) : null}
      {holds.length > 0 ? <Holds holds={holds} /> : null}
      <RunsView
        agentName={agentName}
        runs={runs}
        latestDelegationId={latestDelegation?.id ?? null}
        latestEndedAt={latestDelegation?.ended_at ?? null}
      />
    </article>
  );
}

// 誰が答えたか。経緯の行と同じ決め方（経路と本人確認の方式）。
const answeredBy = (h: Hold): string | null =>
  h.answered_channel === null
    ? null
    : whoOf(h.answered_channel, h.answered_verification);

function Holds({ holds }: { holds: Hold[] }) {
  return (
    <section aria-label="問いと回答" data-testid="task-holds">
      <h3>問いと回答</h3>
      <ol className="task-holds">
        {holds.map((h) => (
          <li key={h.raised_at} data-testid="task-hold">
            <div className="detail-question">
              <Markdown text={h.question} />
            </div>
            {h.answer !== null ? (
              <div data-testid="task-hold-answer">
                <b>回答:</b> {h.answer}
                {answeredBy(h) !== null ? (
                  <p data-testid="task-hold-by">答えた人: {answeredBy(h)}</p>
                ) : null}
                {h.relay_quote !== null ? (
                  <p data-testid="task-hold-quote">
                    対話での発言の要旨: {h.relay_quote}
                  </p>
                ) : null}
                {h.replan ? (
                  <p data-testid="task-hold-replan">計画の作り直しへ回した</p>
                ) : null}
              </div>
            ) : h.closed_at !== null ? (
              <p>答えないまま閉じました。</p>
            ) : h.kind === HOLD_KIND_PARENT_DIRECT ? (
              <p>エージェントが直接進めています。</p>
            ) : (
              <p>まだ答えていません。</p>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

function RunsView({
  agentName,
  runs,
  latestDelegationId,
  latestEndedAt,
}: {
  agentName: string;
  runs: ShowCardResponse["runs"];
  latestDelegationId: string | null;
  latestEndedAt: string | null;
}) {
  // 報告は、そのタスクのいちばん新しい委譲の実行の ID で 1 回だけ読む（読めなければ出さない）。
  const [report, setReport] = useState<RunReport | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: latestEndedAt は読み直しの合図
  useEffect(() => {
    // 実行が終わったとき（latestEndedAt が変わったとき）にも読み直す。走っている間の報告は空のことがある。
    setReport(null);
    if (latestDelegationId === null) return;
    let cancelled = false;
    api.runReport(agentName, latestDelegationId).then(
      (r) => {
        if (!cancelled) setReport(r.report);
      },
      () => {},
    );
    return () => {
      cancelled = true;
    };
  }, [agentName, latestDelegationId, latestEndedAt]);

  return (
    <section aria-label="実行の記録" data-testid="task-runs">
      <h3>実行の記録</h3>
      {runs.length === 0 ? <p>実行の記録はまだありません。</p> : null}
      <ul className="task-runs">
        {runs.map((r) => (
          <li key={r.id} data-testid="task-run" data-run-id={r.id}>
            <span data-testid="run-kind">{label(RUN_KIND_LABELS, r.kind)}</span>
            {" ・ "}
            <span data-testid="run-result">
              {r.result === null
                ? RUN_RUNNING_LABEL
                : label(RUN_RESULT_LABELS, r.result)}
            </span>
            {" ・ "}
            <span data-testid="run-cost">
              {r.cost_usd === null ? NO_COST_LABEL : `${usd(r.cost_usd)} USD`}
            </span>
            {" ・ 始まり "}
            <time data-testid="run-started" dateTime={r.started_at}>
              {shortTime(r.started_at)}
            </time>
            {r.ended_at !== null ? (
              <>
                {" ・ 終わり "}
                <time data-testid="run-ended" dateTime={r.ended_at}>
                  {shortTime(r.ended_at)}
                </time>
              </>
            ) : null}
          </li>
        ))}
      </ul>
      {report !== null && report.assumptions.length > 0 ? (
        <div data-testid="run-assumptions">
          <h4>仮定</h4>
          <ul>
            {report.assumptions.map((a, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 同じ文が並びうる
              <li key={i}>{a}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {report !== null && report.unverified.length > 0 ? (
        <div data-testid="run-unverified">
          <h4>確かめていないこと</h4>
          <ul>
            {report.unverified.map((a, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 同じ文が並びうる
              <li key={i}>{a}</li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}
