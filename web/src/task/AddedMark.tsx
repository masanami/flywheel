import type { ShowCardResponse, Task } from "../api/types.gen.ts";
import { TASK_ADDED_MARK } from "../labels.ts";
import { hrefOf } from "../shell/route.ts";
import { shortTime } from "../when.ts";
import { addedOriginWord } from "./DecisionRecords.tsx";

// 後から足したタスクの印。足されていない（added が null）タスクには出さない。
// 出どころがタスクなら、そのタスクの詳細へ。フォローアップなら、フォローアップの一覧へ移れる。
export function AddedMark({
  show,
  task,
  agentName,
}: {
  show: ShowCardResponse;
  task: Task;
  agentName: string;
}) {
  const added = task.added;
  if (added === null) return null;
  const id = show.challenge.id;
  const origin = added.origin_task_id;
  const originTitle =
    origin === null
      ? null
      : (show.tasks.find((t) => t.id === origin)?.title ?? origin);
  return (
    <p className="task-added" data-testid="task-added">
      <span className="status-pill" data-testid="task-added-mark">
        {TASK_ADDED_MARK}
      </span>{" "}
      <time dateTime={added.added_at} data-testid="task-added-at">
        {shortTime(added.added_at)}
      </time>
      {" ・ "}
      <span data-testid="task-added-origin">
        {addedOriginWord(show, added)}
      </span>
      {origin !== null ? (
        <>
          {" ・ "}
          <a
            data-testid="task-added-origin-link"
            href={hrefOf({
              kind: "task",
              name: agentName,
              id,
              taskId: origin,
            })}
          >
            {originTitle}
          </a>
        </>
      ) : null}
      {added.origin === "followup" ? (
        <>
          {" ・ "}
          <a
            data-testid="task-added-followups-link"
            href={hrefOf({
              kind: "agent",
              name: agentName,
              screen: "followups",
            })}
          >
            フォローアップの一覧
          </a>
        </>
      ) : null}
    </p>
  );
}
