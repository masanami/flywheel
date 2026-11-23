import type { ShowCardResponse } from "../api/types.gen.ts";
import { HOLD_KIND_PARENT_DIRECT, PARENT_DIRECT_HOLD_NOTE } from "../labels.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { ApprovalGuide } from "../parts/ApprovalGuide.tsx";
import { ClampedText } from "../parts/ClampedText.tsx";
import { hrefOf } from "../shell/route.ts";
import {
  NO_WAITING_TEXT,
  WAITING_LABELS,
  waitingKindsOfActions,
} from "../shell/waiting.ts";

// 種類の語は課題の画面と同じ（shell/waiting.ts）。知らないコードは生のコードを出す。
const waitingLabel = (a: string): string => {
  const k = waitingKindsOfActions([a])[0];
  return k ? WAITING_LABELS[k] : a;
};

// あなたを待っているもの。next_human_actions ごとに出す。
// 計画の承認・完了の承認は、それぞれの承認の画面へのリンク（ボタンは承認の画面にある）。回答・予算の増額などはターミナルのコマンドの案内を出す（答える欄は置かない）。
export function Waiting({
  show,
  agentName,
  workspacePath,
}: {
  show: ShowCardResponse;
  agentName: string;
  workspacePath: string;
}) {
  const id = show.challenge.id;
  const actions = show.card.next_human_actions;
  const openHolds = show.holds.filter(
    (h) => h.answer === null && h.closed_at === null,
  );
  const questions = openHolds.filter((h) => h.kind !== HOLD_KIND_PARENT_DIRECT);
  const parentDirect = openHolds.some(
    (h) => h.kind === HOLD_KIND_PARENT_DIRECT,
  );
  const taskTitle = (taskId: string): string =>
    show.tasks.find((t) => t.id === taskId)?.title ??
    show.tree.tasks.find((t) => t.id === taskId)?.title ??
    taskId;

  return (
    <section
      className="detail-waiting"
      aria-label="あなたを待っているもの"
      data-testid="detail-waiting"
    >
      <h3>あなたを待っているもの</h3>
      {actions.length === 0 ? <p>{NO_WAITING_TEXT}</p> : null}
      {parentDirect ? (
        <p data-testid="parent-direct">{PARENT_DIRECT_HOLD_NOTE}</p>
      ) : null}
      <ul>
        {actions.map((a) => (
          <li key={a} data-action={a}>
            <b>{waitingLabel(a)}</b>
            {a === "approve_plan" ? (
              <>
                {" "}
                <a
                  href={hrefOf({ kind: "approve_plan", name: agentName, id })}
                  data-testid="plan-approval-link"
                >
                  計画の承認の画面を開く
                </a>
              </>
            ) : null}
            {a === "approve_completion" ? (
              <>
                {" "}
                <a
                  href={hrefOf({
                    kind: "approve_completion",
                    name: agentName,
                    id,
                  })}
                  data-testid="completion-approval-link"
                >
                  完了の承認の画面を開く
                </a>
              </>
            ) : null}
            {a === "answer_question"
              ? questions.map((h) => (
                  <div
                    key={h.hold_id}
                    className="detail-question"
                    data-testid="hold-question"
                  >
                    <p data-testid="hold-id">保留: {h.hold_id}</p>
                    <ClampedText resetKey={h.question}>
                      <Markdown text={h.question} />
                    </ClampedText>
                    {h.task_id !== null ? (
                      <p>
                        タスク:{" "}
                        <a
                          href={hrefOf({
                            kind: "task",
                            name: agentName,
                            id,
                            taskId: h.task_id,
                          })}
                        >
                          {taskTitle(h.task_id)}
                        </a>
                      </p>
                    ) : null}
                  </div>
                ))
              : null}
          </li>
        ))}
      </ul>
      <ApprovalGuide
        challengeId={id}
        actions={actions}
        workspacePath={workspacePath}
        openHoldIds={questions.map((h) => h.hold_id)}
        needsHold={openHolds.length >= 2}
      />
    </section>
  );
}
