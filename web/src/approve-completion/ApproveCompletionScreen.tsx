import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError, isNotAwaitingError } from "../api/error.ts";
import type { ApprovalSummary } from "../api/types.gen.ts";
import {
  APPROVAL_REJECT_NOTE,
  COMPLETION_APPROVED_NOTE,
  COMPLETION_BUTTON_NOTE,
  COMPLETION_RELEASE_NOTE,
} from "../guidance.ts";
import {
  LOADING_TEXT,
  OPERATION_KIND_LABELS,
  PHASE_LABELS,
  RELEASE_HEAD_MISSING_TEXT,
  RELOAD_TEXT,
  TASK_SPENT_LABEL,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { ConflictFixesView } from "../parts/ConflictFixesView.tsx";
import { DirectCompletionView } from "../parts/DirectCompletionView.tsx";
import { PairsView } from "../parts/StagesDetail.tsx";
import { safeHref } from "../safeHref.ts";
import { CycleStartLine } from "../shell/CycleStartLine.tsx";
import { hrefOf } from "../shell/route.ts";
import { type ScreenData, useScreenData } from "../shell/useScreenData.ts";
import { usd } from "../usd.ts";

export const NOT_AWAITING_COMPLETION_TEXT =
  "この課題は、完了の承認待ちではありません。";
export const NO_OPEN_FOLLOWUPS_TEXT = "未のフォローアップはありません";
export const COMPLETION_CONFLICT_TEXT =
  "承認の対象が更新されていました。最新の要約を読み直しました。内容を確かめて、もう一度ボタンを押してください。";
export const COMPLETION_UPDATED_TEXT =
  "承認の対象が更新されました。内容を確かめてから承認してください。";
export const APPROVE_BUTTON_TEXT = "完了を承認する";

// 完了の承認の画面。承認の要約 1 つ（タスク・フォローアップ・通したコンフリクトの解消も入っている）を読み、左の列に確かめの結果・反映するもの・タスク・
// フォローアップを、右の列に承認の操作を出す。画面のボタンで受けるのは、反映を保留にした完了の承認だけ
// （反映まで承認する・差し戻す・問いに答えるは、対話で伝える案内だけを出す）。
export function ApproveCompletionScreen({
  live,
  agentName,
  id,
}: {
  live: LiveState;
  agentName: string;
  id: string;
}) {
  const { result, reload } = useScreenData<ApprovalSummary>(
    live,
    agentName,
    async () => (await api.approvalSummary(agentName, id)).approval,
    [id],
  );
  // 承認が通った後は課題の状態が進んで要約が読めなくなるので、承認した時点の題名で出す。
  const [approved, setApproved] = useState<{
    title: string;
    cycle: string;
  } | null>(null);
  const challengeHref = hrefOf({
    kind: "challenge",
    name: agentName,
    id,
    view: "tree",
  });

  if (approved) {
    return (
      <section aria-labelledby="screen-title" data-testid="completion-approved">
        <h2 id="screen-title">完了の承認: {approved.title}</h2>
        <output className="plan-approved-note">
          {COMPLETION_APPROVED_NOTE}
        </output>
        <CycleStartLine reason={approved.cycle} />
        <p>
          <a href={challengeHref}>課題の詳細を開く</a>
        </p>
      </section>
    );
  }
  return (
    <Body
      result={result}
      reload={reload}
      agentName={agentName}
      id={id}
      challengeHref={challengeHref}
      onApproved={(title, cycle) => setApproved({ title, cycle })}
    />
  );
}

function NotAwaiting({ href }: { href: string }) {
  return (
    <section
      aria-labelledby="screen-title"
      data-testid="completion-not-awaiting"
    >
      <h2 id="screen-title">完了の承認</h2>
      <p>{NOT_AWAITING_COMPLETION_TEXT}</p>
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
  challengeHref,
  onApproved,
}: {
  result: ScreenData<ApprovalSummary>;
  reload: () => void;
  agentName: string;
  id: string;
  challengeHref: string;
  onApproved: (title: string, cycle: string) => void;
}) {
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error") {
    const e = result.error;
    if (isNotAwaitingError(e)) {
      return <NotAwaiting href={challengeHref} />;
    }
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const approval = result.data;
  if (approval.kind !== "completion") {
    return <NotAwaiting href={challengeHref} />;
  }
  return (
    <CompletionView
      approval={approval}
      reload={reload}
      agentName={agentName}
      id={id}
      challengeHref={challengeHref}
      onApproved={onApproved}
    />
  );
}

function CompletionView({
  approval,
  reload,
  agentName,
  id,
  challengeHref,
  onApproved,
}: {
  approval: ApprovalSummary;
  reload: () => void;
  agentName: string;
  id: string;
  challengeHref: string;
  onApproved: (title: string, cycle: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  // 押した結果が「終わりの状態」だったとき、承認待ちでない旨の案内に切り替える。
  const [gone, setGone] = useState(false);
  // 開いたあとの読み直しで版が変わったら知らせる（押すまで出したままにする）。
  const [seen, setSeen] = useState(approval.version);
  const updated = approval.version !== seen;
  // 版の食い違いの後、読み直しが終わるまでの間（ボタンは押せないままにする）。
  const [awaitingReload, setAwaitingReload] = useState(false);
  const alive = useRef(true);
  // 読み直しの結果（新しい応答）が届いたら、その版を「見た版」として扱い、食い違いの通知だけを出す。
  // biome-ignore lint/correctness/useExhaustiveDependencies: 合図は新しい応答の到着だけ
  useEffect(() => {
    if (!awaitingReload) return;
    setAwaitingReload(false);
    setSeen(approval.version);
    setNotice(COMPLETION_CONFLICT_TEXT);
    setBusy(false);
  }, [approval]);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // 見せた中身と同じ応答の版を送る。食い違いのときは読み直すだけで、自動では送り直さない。
  const approve = async () => {
    setBusy(true);
    setNotice(null);
    setSeen(approval.version);
    let reloading = false;
    try {
      const res = await api.approve(agentName, id, approval.version);
      onApproved(approval.title, res.cycle?.reason ?? "unavailable");
    } catch (e) {
      if (!alive.current) return;
      if (isNotAwaitingError(e)) {
        setGone(true);
      } else if (e instanceof ApiError && e.code === "conflict") {
        // 読み直しが終わるまでボタンを押せないままにし、通知は読み直しの後に出す。
        reloading = true;
        setAwaitingReload(true);
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
      if (alive.current && !reloading) setBusy(false);
    }
  };

  if (gone) return <NotAwaiting href={challengeHref} />;
  const showIntegration =
    approval.uses_integration === true || approval.integration_ref !== null;
  const listedTasks = approval.tasks;

  return (
    <section
      aria-labelledby="screen-title"
      className="plan-approval"
      data-testid="approve-completion"
    >
      <h2 id="screen-title">完了の承認: {approval.title}</h2>
      <div className="plan-cols">
        <div className="plan-main" data-testid="completion-main">
          <section aria-label="確かめの結果" data-testid="completion-checks">
            <h3>確かめの結果</h3>
            {approval.done_criteria && (
              <p className="card-detail-multiline">{approval.done_criteria}</p>
            )}
            {approval.direct_completion && (
              <DirectCompletionView
                completion={approval.direct_completion}
                showNote
              />
            )}
            <p data-testid="completion-pairs-lead">
              完了したら見えるものと、確かめた実物の対（{approval.pairs.length}
              件）
            </p>
            {approval.pairs.length > 0 && <PairsView pairs={approval.pairs} />}
            {approval.mergeability_note !== "" && (
              <p data-testid="approval-mergeability">
                取り込みの可否: {approval.mergeability_note}
              </p>
            )}
            {approval.conditions_after_release.length > 0 && (
              <div data-testid="approval-conditions-after-release">
                <h4>{approval.conditions_after_release_heading}</h4>
                <ul>
                  {approval.conditions_after_release.map((c) => (
                    <li key={c}>{c}</li>
                  ))}
                </ul>
              </div>
            )}
          </section>

          <section aria-label="反映するもの" data-testid="completion-reflect">
            <h3>反映するもの</h3>
            {showIntegration && (
              <p data-testid="completion-integration">
                統合先:{" "}
                <code>{approval.integration_ref ?? "まだありません"}</code>
              </p>
            )}
            {approval.releases.length === 0 ? (
              <p>反映の操作はありません</p>
            ) : (
              <ul>
                {approval.releases.map((r) => {
                  const href = safeHref(r.ref);
                  return (
                    <li key={r.id} data-release={r.id}>
                      <span>{r.summary}</span>
                      {r.ref !== null && (
                        <>
                          {" "}
                          {href ? (
                            <a
                              href={href}
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              {r.ref}
                            </a>
                          ) : (
                            <span>{r.ref}</span>
                          )}
                        </>
                      )}{" "}
                      <span data-testid="release-head">
                        中身の識別:{" "}
                        {r.head_sha !== null ? (
                          <code>{r.head_sha}</code>
                        ) : (
                          RELEASE_HEAD_MISSING_TEXT
                        )}
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
            <ConflictFixesView
              fixes={approval.conflict_fixes}
              titleOfTask={(tid) =>
                approval.tasks.find((t) => t.id === tid)?.title
              }
              showAt={false}
            />
            <h4>同時には承認されない不可逆操作</h4>
            {approval.other_pending_operations.length === 0 ? (
              <p>なし</p>
            ) : (
              <ul data-testid="completion-other-operations">
                {approval.other_pending_operations.map((o) => (
                  <li key={o.id} data-operation={o.id}>
                    <b>{label(OPERATION_KIND_LABELS, o.kind)}</b>: {o.summary}
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="タスク" data-testid="completion-tasks">
            <h3>タスク</h3>
            {listedTasks.length === 0 ? (
              <p>タスクはありません</p>
            ) : (
              <ul>
                {listedTasks.map((t) => {
                  return (
                    <li key={t.id} data-task={t.id}>
                      {t.in_tree ? (
                        <a
                          href={hrefOf({
                            kind: "task",
                            name: agentName,
                            id,
                            taskId: t.id,
                          })}
                        >
                          {t.title}
                        </a>
                      ) : (
                        <span>{t.title}</span>
                      )}{" "}
                      <span data-testid="completion-task-state">
                        {label(PHASE_LABELS, t.phase)}
                      </span>{" "}
                      <span data-testid="completion-task-cost">
                        {TASK_SPENT_LABEL} {usd(t.spent_usd)} USD / 枠{" "}
                        {usd(t.budget_usd)} USD
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>

          <section
            aria-label="フォローアップ"
            data-testid="completion-followups"
          >
            <h3>フォローアップ</h3>
            {approval.followups.length === 0 ? (
              <p>{NO_OPEN_FOLLOWUPS_TEXT}</p>
            ) : (
              <>
                <p>未のフォローアップ: {approval.followups.length} 件</p>
                <ul>
                  {approval.followups.map((f) => (
                    <li key={f.id}>{f.title}</li>
                  ))}
                </ul>
              </>
            )}
          </section>
        </div>

        <div className="plan-side" data-testid="completion-side">
          <section aria-label="承認の操作">
            <h3>承認の操作</h3>
            <div className="approval-control">
              <button
                type="button"
                className="approval-control-confirm"
                disabled={busy}
                onClick={approve}
              >
                {APPROVE_BUTTON_TEXT}
              </button>
            </div>
            <p data-testid="completion-button-note">{COMPLETION_BUTTON_NOTE}</p>
            {updated && <p role="alert">{COMPLETION_UPDATED_TEXT}</p>}
            {notice && (
              <p className="approval-control-error" role="alert">
                {notice}
              </p>
            )}
          </section>
          <section aria-label="対話での案内" data-testid="completion-guide">
            <h3>対話での案内</h3>
            <p data-testid="completion-release-note">
              {COMPLETION_RELEASE_NOTE}
            </p>
            <p data-testid="completion-reject-note">{APPROVAL_REJECT_NOTE}</p>
          </section>
        </div>
      </div>
    </section>
  );
}
