import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type {
  ApprovalSummary,
  PlanQuestion,
  PlanTopic,
} from "../api/types.gen.ts";
import { guidesFor, guidesForContext } from "../guidance.ts";
import type { LiveState } from "../live/useLive.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { ClampedText } from "../parts/ClampedText.tsx";
import { FigureView } from "../parts/FigureView.tsx";
import { IssuesAnswerView, IssuesView } from "../parts/StagesDetail.tsx";
import { CycleStartButton } from "../shell/CycleStartButton.tsx";
import { hrefOf } from "../shell/route.ts";
import { type ScreenData, useScreenData } from "../shell/useScreenData.ts";
import { TaskTree } from "../tree/TaskTree.tsx";

export const NOT_AWAITING_PLAN_TEXT =
  "この課題は、計画の承認待ちではありません。";
export const APPROVED_TEXT = "承認しました。";
export const NEXT_CYCLE_TEXT = "続きは、次のサイクルで進みます。";
export const CONFLICT_TEXT =
  "計画が変わりました。最新の計画を読み直しました。内容を確かめて、もう一度承認してください。";
export const RELOAD_TEXT =
  "操作を受け付けられませんでした。ページを再読み込みしてからやり直してください（自動では再送しません）。";
export const RECOMMEND_OK_TEXT = "推奨のままでよければ、そのまま承認できます。";
export const NO_QUESTIONS_TEXT = "問いはありません。";
export const BUDGET_UNSET_TEXT = "未設定";

const usd = (n: number) => String(Number(n.toFixed(2)));

// 計画の承認の画面。承認の要約の API の応答から、計画の中身（方針・完了したら見えるもの・やらないこと・
// ツリー・問いと推奨・予算の上限・統合先）を出す。区分（phase）は応答の値をそのまま出し、画面では導かない。
// 画面のボタンで受けるのは「この計画を承認する」と、承認の後の「サイクルを回す」だけ。
export function PlanApprovalScreen({
  live,
  agentName,
  id,
  workspacePath,
}: {
  live: LiveState;
  agentName: string;
  id: string;
  workspacePath: string;
}) {
  const { result, reload } = useScreenData(
    live,
    agentName,
    () => api.approvalSummary(agentName, id),
    [id],
  );
  // 承認が通った後は、課題の状態が進んで要約が読めなくなるので、読み直した結果ではなく、承認した時点の題名で出す。
  const [approved, setApproved] = useState<{ title: string } | null>(null);
  const challengeHref = hrefOf({
    kind: "challenge",
    name: agentName,
    id,
    view: "tree",
  });

  if (approved) {
    const data = live.data[agentName];
    return (
      <section aria-labelledby="screen-title" data-testid="plan-approved">
        <h2 id="screen-title">計画の承認: {approved.title}</h2>
        <output className="plan-approved-note">{APPROVED_TEXT}</output>
        <p>{NEXT_CYCLE_TEXT}</p>
        <p>
          <a href={challengeHref}>課題の詳細を開く</a>
        </p>
        <CycleStartButton
          workspace={agentName}
          total={data?.status?.cycle_work.total ?? 0}
          cycleState={data?.cycle?.state ?? ""}
        />
      </section>
    );
  }
  return (
    <Body
      result={result}
      reload={reload}
      agentName={agentName}
      id={id}
      workspacePath={workspacePath}
      challengeHref={challengeHref}
      onApproved={(title) => setApproved({ title })}
    />
  );
}

function NotAwaiting({ href }: { href: string }) {
  return (
    <section aria-labelledby="screen-title" data-testid="plan-not-awaiting">
      <h2 id="screen-title">計画の承認</h2>
      <p>{NOT_AWAITING_PLAN_TEXT}</p>
      <p>
        <a href={href}>課題の詳細を開く</a>
      </p>
    </section>
  );
}

function Body({
  result,
  reload,
  agentName,
  id,
  workspacePath,
  challengeHref,
  onApproved,
}: {
  result: ScreenData<{ approval: ApprovalSummary }>;
  reload: () => void;
  agentName: string;
  id: string;
  workspacePath: string;
  challengeHref: string;
  onApproved: (title: string) => void;
}) {
  if (result.state === "loading") return <p>読み込んでいます</p>;
  if (result.state === "error") {
    const e = result.error;
    if (e instanceof ApiError && e.code === "invalid_transition") {
      return <NotAwaiting href={challengeHref} />;
    }
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const summary = result.data.approval;
  if (summary.kind !== "plan") return <NotAwaiting href={challengeHref} />;
  return (
    <PlanView
      summary={summary}
      reload={reload}
      agentName={agentName}
      id={id}
      workspacePath={workspacePath}
      onApproved={onApproved}
    />
  );
}

function PlanView({
  summary,
  reload,
  agentName,
  id,
  workspacePath,
  onApproved,
}: {
  summary: ApprovalSummary;
  reload: () => void;
  agentName: string;
  id: string;
  workspacePath: string;
  onApproved: (title: string) => void;
}) {
  const plan = summary.plan;
  const { expected, non_goals, figure } = summary.stages;
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // 見せた中身と同じ応答の版を送る。自動の再送はしない。
  const approve = async () => {
    setBusy(true);
    setNotice(null);
    try {
      await api.approve(agentName, id, summary.version);
      // 承認が通った後は、要約の読み直しが先に着いてこの部品が外れていても、承認した旨を出す（親が持つ）。
      onApproved(summary.title);
    } catch (e) {
      if (!alive.current) return;
      if (e instanceof ApiError && e.code === "conflict") {
        setNotice(CONFLICT_TEXT);
        reload();
      } else if (
        e instanceof ApiError &&
        (e.status === 403 || e.code === "forbidden_origin")
      ) {
        setNotice(RELOAD_TEXT);
      } else {
        setNotice(e instanceof Error ? e.message : String(e));
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const legacy =
    plan !== null && plan.tasks.length === 0 && plan.summary === null;
  const questionCount =
    plan?.topics.reduce((n, t) => n + t.questions.length, 0) ?? 0;
  const reject = guidesFor(id, workspacePath, ["approve_plan"]).find(
    (g) => g.id === "reject",
  );
  const answer = guidesForContext("plan_approval", workspacePath).find(
    (g) => g.id === "answer-choice",
  );

  return (
    <section
      aria-labelledby="screen-title"
      className="plan-approval"
      data-testid="plan-approval"
    >
      <h2 id="screen-title">計画の承認: {summary.title}</h2>
      {plan && (
        <dl className="plan-meta">
          <dt>予算の上限</dt>
          <dd data-testid="plan-budget">
            {plan.budget_usd !== null
              ? `${usd(plan.budget_usd)} USD`
              : BUDGET_UNSET_TEXT}
          </dd>
          {plan.uses_integration !== null && (
            <>
              <dt>統合先</dt>
              <dd data-testid="plan-integration">
                {plan.uses_integration ? "使う" : "使わない"}
              </dd>
            </>
          )}
        </dl>
      )}
      <p>
        <a
          href={hrefOf({
            kind: "challenge",
            name: agentName,
            id,
            view: "plan",
          })}
        >
          計画の本文を読む
        </a>
      </p>

      {legacy && plan ? (
        <section aria-label="計画の本文" data-testid="plan-legacy-body">
          <h3>計画の本文</h3>
          <Markdown text={plan.body} />
        </section>
      ) : (
        plan?.summary != null && (
          <section aria-label="方針" data-testid="plan-summary">
            <h3>方針</h3>
            <Markdown text={plan.summary} />
          </section>
        )
      )}
      <>
        {expected !== null && expected.length > 0 && (
          <section aria-label="完了したら見えるもの" data-stage="expected">
            <h3>完了したら見えるもの</h3>
            <ol>
              {expected.map((e, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
                <li key={i} data-field="expected">
                  <div data-field="scene">
                    場面: <ClampedText text={e.scene} />
                  </div>
                  <div data-field="before">
                    いま: <ClampedText text={e.before} />
                  </div>
                  <div data-field="after">
                    終わった後: <ClampedText text={e.after} />
                  </div>
                </li>
              ))}
            </ol>
          </section>
        )}
        {non_goals !== null && non_goals.length > 0 && (
          <section aria-label="やらないこと" data-stage="non-goals">
            <h3>やらないこと</h3>
            <ul>
              {non_goals.map((g, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
                <li key={i}>
                  <ClampedText text={g} />
                </li>
              ))}
            </ul>
          </section>
        )}
        {plan && plan.tasks.length > 0 && (
          <section aria-label="タスクのツリー">
            <h3>
              タスクのツリー（
              <span data-testid="plan-task-count">{plan.tasks.length}</span>）
            </h3>
            {/* 承認の前はタスクの ID が無いので、箱は押せない（リンクにしない） */}
            <TaskTree tasks={plan.tasks} />
          </section>
        )}
        <FigureView figure={figure} />
        {legacy && summary.stages.issues !== null && (
          <IssuesView issues={summary.stages.issues} />
        )}
        {legacy && summary.stages.issues_answer !== null && (
          <IssuesAnswerView answer={summary.stages.issues_answer} />
        )}
        {!legacy && (
          <section aria-label="問いと推奨" data-testid="plan-topics">
            <h3>問いと推奨</h3>
            {plan?.topics.map((t, i) => (
              <TopicView key={`${i}:${t.title}`} topic={t} />
            ))}
            {questionCount === 0 ? (
              <p>{NO_QUESTIONS_TEXT}</p>
            ) : (
              <>
                <p>{RECOMMEND_OK_TEXT}</p>
                <p>
                  推奨と違う答えにしたいときは、端末で次のコマンドを実行します。
                </p>
                {answer && (
                  <pre data-guide={answer.id}>
                    <code>{answer.command.replaceAll("<C-ID>", id)}</code>
                  </pre>
                )}
              </>
            )}
          </section>
        )}
      </>

      <div className="approval-control">
        <button
          type="button"
          className="approval-control-confirm"
          disabled={busy}
          onClick={approve}
        >
          この計画を承認する
        </button>
        {notice && (
          <p className="approval-control-error" role="alert">
            {notice}
          </p>
        )}
        <p className="approval-control-question">
          差し戻すときは、端末で次のコマンドを実行します。
        </p>
        {reject && (
          <pre data-guide={reject.id}>
            <code>{reject.command}</code>
          </pre>
        )}
      </div>
    </section>
  );
}

function TopicView({ topic }: { topic: PlanTopic }) {
  return (
    <article className="plan-topic" data-testid="plan-topic">
      <h4>{topic.title}</h4>
      <Markdown text={topic.decision} />
      {topic.questions.map((q) => (
        <QuestionView key={q.number} q={q} />
      ))}
    </article>
  );
}

// 問いの選択肢は見せるだけ（選べない）。推奨の印と、記録されている答えを出す。
function QuestionView({ q }: { q: PlanQuestion }) {
  const answer = q.answer;
  const answerLabel = answer ? (answer.label ?? answer.text ?? "") : null;
  return (
    <div className="plan-question" data-testid="plan-question">
      <p data-field="question">
        問い {q.number}: <ClampedText text={q.question} />
      </p>
      <ol>
        {q.options.map((o, j) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={j} data-recommended={j + 1 === q.recommended}>
            <ClampedText text={o.label} />
            {j + 1 === q.recommended && (
              <span className="plan-recommended-mark"> 推奨</span>
            )}
          </li>
        ))}
      </ol>
      {answerLabel !== null && (
        <p data-field="answer">
          答え: <ClampedText text={answerLabel} />
        </p>
      )}
    </div>
  );
}
