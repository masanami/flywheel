import type { Hold, ShowCardResponse } from "../api/types.gen.ts";
import { HOLD_KIND_PARENT_DIRECT, PARENT_DIRECT_HOLD_NOTE } from "../labels.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { ApprovalGuide } from "../parts/ApprovalGuide.tsx";
import { ClampedText } from "../parts/ClampedText.tsx";
import {
  HoldAnswerNotices,
  HoldOptions,
  SupervisorNote,
  hasOptions,
  useHoldAnswer,
} from "../parts/HoldOptions.tsx";
import { SpecDocument } from "../parts/SpecDocument.tsx";
import { hrefOf } from "../shell/route.ts";
import {
  NO_WAITING_TEXT,
  WAITING_LABELS,
  waitingKindsOfActions,
} from "../shell/waiting.ts";
import { SliceSplitHold } from "./SliceSplitHold.tsx";
import { SpecDraftHold } from "./SpecDraftHold.tsx";
import { canOpenSpec, specDocumentKey } from "./specLocation.ts";

// 種類の語は課題の画面と同じ（shell/waiting.ts）。知らないコードは生のコードを出す。
const waitingLabel = (a: string): string => {
  const k = waitingKindsOfActions([a])[0];
  return k ? WAITING_LABELS[k] : a;
};

// 答えを待っている問いの保留（種類が親が直接行うの保留は、問いに数えない）。
const openQuestionHolds = (show: ShowCardResponse): Hold[] =>
  show.holds.filter(
    (h) =>
      h.answer === null &&
      h.closed_at === null &&
      h.kind !== HOLD_KIND_PARENT_DIRECT,
  );

// この区画が下書きづくりの保留の所を出すか。出すなら、仕様の文書を開く口はその所に置く。
export function showsSpecDraftHold(show: ShowCardResponse): boolean {
  const holdId = show.challenge.spec_draft?.hold_id ?? null;
  return (
    holdId !== null &&
    show.card.next_human_actions.includes("answer_question") &&
    openQuestionHolds(show).some((h) => h.hold_id === holdId)
  );
}

// あなたを待っているもの。next_human_actions ごとに出す。
// 計画の承認・完了の承認は、それぞれの承認の画面へのリンク（ボタンは承認の画面にある）。回答・予算の増額などはターミナルのコマンドの案内を出す（答える欄は置かない）。
export function Waiting({
  show,
  agentName,
  workspacePath,
  reload,
}: {
  show: ShowCardResponse;
  agentName: string;
  workspacePath: string;
  reload: () => void;
}) {
  const id = show.challenge.id;
  const specDraft = show.challenge.spec_draft;
  const answerState = useHoldAnswer(agentName, id, reload);
  const actions = show.card.next_human_actions;
  const openHolds = show.holds.filter(
    (h) => h.answer === null && h.closed_at === null,
  );
  const questions = openQuestionHolds(show);
  const parentDirect = openHolds.some(
    (h) => h.kind === HOLD_KIND_PARENT_DIRECT,
  );
  // 開いている問いがすべてボタンで答えられるなら、ターミナルの回答の案内は出さない（タスクの詳細と同じ）。
  const guideActions =
    questions.length > 0 && questions.every((h) => hasOptions(h))
      ? actions.filter((a) => a !== "answer_question")
      : actions;
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
                  <div key={h.hold_id} className="detail-question">
                    <p data-testid="hold-id">保留: {h.hold_id}</p>
                    <SupervisorNote
                      findings={h.supervisor_findings}
                      recommendation={h.supervisor_recommendation}
                    />
                    <div data-testid="hold-question">
                      <ClampedText resetKey={h.question}>
                        <Markdown text={h.question} />
                      </ClampedText>
                    </div>
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
                    ) : (
                      <p data-testid="hold-scope">課題全体を止めています</p>
                    )}
                    {specDraft !== null && specDraft.hold_id === h.hold_id ? (
                      <SpecDraftHold
                        draft={specDraft}
                        challengeId={id}
                        workspacePath={workspacePath}
                        specDocument={
                          canOpenSpec(show.challenge) ? (
                            <SpecDocument
                              key={specDocumentKey(agentName, show.challenge)}
                              agentName={agentName}
                              challengeId={id}
                            />
                          ) : null
                        }
                      />
                    ) : null}
                    {show.slice_split?.hold_id === h.hold_id ? (
                      <SliceSplitHold split={show.slice_split} />
                    ) : null}
                    {hasOptions(h) ? (
                      <HoldOptions
                        key={`${h.hold_id}:${h.check ?? ""}`}
                        hold={h}
                        state={answerState}
                      />
                    ) : null}
                  </div>
                ))
              : null}
          </li>
        ))}
      </ul>
      <HoldAnswerNotices state={answerState} />
      <ApprovalGuide
        challengeId={id}
        actions={guideActions}
        workspacePath={workspacePath}
        openHoldIds={questions
          .filter((h) => !hasOptions(h))
          .map((h) => h.hold_id)}
        needsHold={openHolds.length >= 2}
      />
    </section>
  );
}
