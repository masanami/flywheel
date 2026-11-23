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
import { useChat } from "../chat/context.ts";
import {
  CONFLICT_FIX_PENDING_TEXT,
  DELEGATE_STATE_LABELS,
  HOLD_KIND_PARENT_DIRECT,
  LANDING_LABELS,
  LOADING_TEXT,
  NO_COST_LABEL,
  PHASE_LABELS,
  REQUESTED_KIND_LABELS,
  RUN_KIND_LABELS,
  RUN_RESULT_LABELS,
  RUN_RUNNING_LABEL,
  TASK_SPENT_LABEL,
  UPSTREAM_STATE_LABELS,
  VERDICT_LABELS,
  WHO_ME_UI_BUTTON,
  artifactKindLabel,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { ApprovalGuide } from "../parts/ApprovalGuide.tsx";
import {
  HoldAnswerNotices,
  HoldOptions,
  SupervisorNote,
  hasOptions,
  useHoldAnswer,
} from "../parts/HoldOptions.tsx";
import { safeHref } from "../safeHref.ts";
import { NotFound } from "../shell/NotFound.tsx";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { usd } from "../usd.ts";
import { shortTime } from "../when.ts";
import { whoOf } from "../who.ts";
import { AddedMark } from "./AddedMark.tsx";
import { DecisionRecords } from "./DecisionRecords.tsx";

const REPORT_LOAD_FAILED_TEXT = "報告を読み込めませんでした。";
// 委譲の実行（報告の API を呼ぶ対象）の種類。
const DELEGATION_KINDS = ["delegate", "contract"];

// タスクの詳細の画面。課題の詳細の API の応答を、タスクの ID で結んで作る（tasks = 本文、tree.tasks = 区分・費用）。
// 区分と費用は core が返した値をそのまま出す。画面のボタンは置かず、回答はターミナルのコマンドの案内で示す。
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
  const chat = useChat();
  const { result, reload } = useScreenData(
    live,
    agentName,
    () => api.challenge(agentName, id),
    [id],
  );
  const taskTitle =
    result.state === "ready"
      ? (result.data.tasks.find((t) => t.id === taskId)?.title ?? null)
      : null;
  // 欄の「いま見ているもの」へ、開いているタスクの題名を知らせる。離れたら消す。
  const { reportTask } = chat;
  useEffect(() => {
    reportTask(taskId, taskTitle);
    return () => reportTask(null, null);
  }, [reportTask, taskId, taskTitle]);
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
      reload={reload}
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
  reload,
}: {
  show: ShowCardResponse;
  task: Task;
  node: TreeTask;
  agentName: string;
  workspacePath: string;
  reload: () => void;
}) {
  const id = show.challenge.id;
  const answerState = useHoldAnswer(agentName, id, reload);
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
      <AddedMark show={show} task={task} agentName={agentName} />
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
        <div data-testid="task-budget">
          <dt>枠</dt>
          <dd>
            {TASK_SPENT_LABEL}{" "}
            <b data-testid="task-spent">{usd(task.spent_usd)}</b> USD ／ 実装{" "}
            <b data-testid="task-budget-impl">{usd(task.budget_impl_usd)}</b>{" "}
            USD ・ レビュー対応{" "}
            <b data-testid="task-budget-review">
              {usd(task.budget_review_usd)}
            </b>{" "}
            USD
          </dd>
        </div>
        {task.source_key !== null ? (
          <div data-testid="task-source">
            <dt>取り込み元の子</dt>
            <dd>
              {child ? (
                <>
                  <a
                    href={safeHref(child.url) ?? "#"}
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
      {node.phase === "waiting" || openHolds.length > 0 ? (
        <section
          className="detail-waiting"
          aria-label="あなたを待っているもの"
          data-testid="task-waiting"
        >
          <h3>あなたを待っているもの</h3>
          {openHolds.map((h) => (
            <div
              key={h.hold_id}
              className="detail-question"
              data-testid="task-hold-question"
            >
              <p data-testid="task-hold-id">保留: {h.hold_id}</p>
              <SupervisorNote
                findings={h.supervisor_findings}
                recommendation={h.supervisor_recommendation}
              />
              <Markdown text={h.question} />
              {hasOptions(h) ? (
                <HoldOptions
                  key={`${h.hold_id}:${h.check ?? ""}`}
                  hold={h}
                  state={answerState}
                />
              ) : null}
            </div>
          ))}
          {openHolds.length === 0 || openHolds.some((h) => !hasOptions(h)) ? (
            <ApprovalGuide
              challengeId={id}
              actions={["answer_question"]}
              workspacePath={workspacePath}
              openHoldIds={openHolds
                .filter((h) => !hasOptions(h))
                .map((h) => h.hold_id)}
              needsHold={
                openHolds.length > 0 &&
                show.holds.filter(
                  (h) => h.answer === null && h.closed_at === null,
                ).length >= 2
              }
            />
          ) : null}
        </section>
      ) : null}
      {/* 区画の外に置く: 最後の開いている保留が読み直しで消えても、知らせは残る。 */}
      <HoldAnswerNotices state={answerState} />
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
      {task.delegate !== null ? <DelegateSection d={task.delegate} /> : null}
      {task.artifacts.length > 0 ? (
        <ArtifactsSection artifacts={task.artifacts} />
      ) : null}
      {Object.keys(task.facts).length > 0 ? (
        <FactsSection facts={task.facts} />
      ) : null}
      {task.verification !== null || task.landing !== null ? (
        <VerificationSection
          verification={task.verification}
          landing={task.landing}
        />
      ) : null}
      {runs.some((r) => r.fix_pending_ref !== null) ? (
        <p data-testid="task-fix-pending">{CONFLICT_FIX_PENDING_TEXT}</p>
      ) : null}
      {holds.length > 0 ? <Holds holds={holds} /> : null}
      <DecisionRecords show={show} task={task} />
      <RunsView
        agentName={agentName}
        runs={runs}
        latestDelegationId={latestDelegation?.id ?? null}
        latestEndedAt={latestDelegation?.ended_at ?? null}
      />
    </article>
  );
}

// 委譲先が返した値（要約・値・成果物の識別）は flywheel の外で作られた文字。Markdown として整えず、そのまま文字で出す
// （white-space: pre-wrap で改行だけ保つ）。リンクにするのは、成果物の識別のうち safeHref が通る http か https のものだけ。
function DelegateSection({ d }: { d: NonNullable<Task["delegate"]> }) {
  return (
    <section aria-label="委譲先の実行" data-testid="task-delegate">
      <h3>委譲先の実行</h3>
      <p>
        状態:{" "}
        <b data-testid="delegate-state">
          {label(DELEGATE_STATE_LABELS, d.state)}
        </b>
      </p>
      <p data-testid="delegate-summary" style={{ whiteSpace: "pre-wrap" }}>
        {d.summary}
      </p>
      {d.requested_kind !== null ? (
        <p>
          求めていること:{" "}
          <span data-testid="delegate-requested">
            {label(REQUESTED_KIND_LABELS, d.requested_kind)}
          </span>
        </p>
      ) : null}
    </section>
  );
}

function ArtifactsSection({ artifacts }: { artifacts: Task["artifacts"] }) {
  return (
    <section aria-label="成果物" data-testid="task-artifacts">
      <h3>成果物</h3>
      <ul>
        {artifacts.map((a, i) => {
          const href = safeHref(a.ref);
          return (
            // biome-ignore lint/suspicious/noArrayIndexKey: 同じ識別が並びうる
            <li key={i} data-testid="task-artifact">
              <span data-testid="artifact-kind">
                {artifactKindLabel(a.kind)}
              </span>
              {" ・ "}
              {href !== null ? (
                <a
                  data-testid="artifact-ref"
                  href={href}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {a.ref}
                </a>
              ) : (
                <span data-testid="artifact-ref">{a.ref}</span>
              )}
              {a.state === "" ? null : (
                <>
                  {" ・ "}
                  <span data-testid="artifact-state">{a.state}</span>
                </>
              )}
              {a.role === "integration" ? (
                <>
                  {" ・ "}
                  <span className="status-pill" data-testid="artifact-role">
                    統合先
                  </span>
                </>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function FactsSection({ facts }: { facts: Task["facts"] }) {
  return (
    <section aria-label="委譲先が値で返したこと" data-testid="task-facts">
      <h3>委譲先が値で返したこと</h3>
      <dl className="detail-items">
        {Object.entries(facts).map(([k, v]) => (
          <div key={k} data-testid="task-fact">
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function VerificationSection({
  verification,
  landing,
}: {
  verification: Task["verification"];
  landing: Task["landing"];
}) {
  return (
    <section aria-label="検証" data-testid="task-verification">
      <h3>検証</h3>
      {verification !== null ? (
        <>
          <p>
            判定:{" "}
            <b data-testid="verification-verdict">
              {label(VERDICT_LABELS, verification.verdict)}
            </b>
          </p>
          <p data-testid="verification-reason">{verification.reason}</p>
          <p>
            判定した時刻:{" "}
            <time data-testid="verification-at" dateTime={verification.at}>
              {shortTime(verification.at)}
            </time>
          </p>
        </>
      ) : null}
      {landing !== null ? (
        <p>
          統合先へ入れる進み:{" "}
          <span data-testid="task-landing">
            {label(LANDING_LABELS, landing)}
          </span>
        </p>
      ) : null}
    </section>
  );
}

// 誰が答えたか。経路と本人確認の方式で決める（経路 ui だけは、保留への回答なのでボタンと出す）。
// 画面のボタンで答えた回答（経路 ui）は「あなた（画面のボタン）」と出す。
const answeredBy = (h: Hold): string | null =>
  h.answered_channel === null
    ? null
    : h.answered_channel === "ui"
      ? WHO_ME_UI_BUTTON
      : whoOf(h.answered_channel, h.answered_verification);

function Holds({ holds }: { holds: Hold[] }) {
  return (
    <section aria-label="問いと回答" data-testid="task-holds">
      <h3>問いと回答</h3>
      <ol className="task-holds">
        {holds.map((h) => (
          <li key={h.hold_id} data-testid="task-hold">
            <p data-testid="task-hold-listed-id">保留: {h.hold_id}</p>
            <div className="detail-question">
              <Markdown text={h.question} />
            </div>
            {h.answer !== null ? (
              <div data-testid="task-hold-answer">
                <b>回答:</b>
                <Markdown text={h.answer} />
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
  // 報告は、そのタスクのいちばん新しい委譲の実行の ID で 1 回だけ読む。
  // 読めなかったとき（通信の失敗・サーバのエラー）だけ、その旨を出す。実行が無い（not_found）のは「報告が無い」ので出さない。
  const [report, setReport] = useState<RunReport | null>(null);
  const [reportFailed, setReportFailed] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: latestEndedAt は読み直しの合図
  useEffect(() => {
    // 実行が終わったとき（latestEndedAt が変わったとき）にも読み直す。走っている間の報告は空のことがある。
    setReport(null);
    setReportFailed(false);
    if (latestDelegationId === null) return;
    let cancelled = false;
    api.runReport(agentName, latestDelegationId).then(
      (r) => {
        if (!cancelled) setReport(r.report);
      },
      (e) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.code === "not_found") return;
        setReportFailed(true);
      },
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
      {reportFailed ? (
        <p data-testid="run-report-error">{REPORT_LOAD_FAILED_TEXT}</p>
      ) : null}
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
