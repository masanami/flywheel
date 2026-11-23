import type { ShowCardResponse } from "../api/types.gen.ts";
import {
  SupervisorHandBody,
  handWord,
  resultWord,
} from "../parts/SupervisorHand.tsx";
import { hrefOf } from "../shell/route.ts";
import { shortTime } from "../when.ts";

// 課題の詳細の「スーパーバイザーの記録」。この課題で打った手の数と、手ごとの行（古い順）。
// 手の数は、成った手だけを数える。行は、断られた手・失敗した手も含めて全部出す。
// 手が 0 件なら節ごと出さない。タスクにつく手は、行からそのタスクの詳細へ移れる。
export function SupervisorRecords({
  show,
  agentName,
}: {
  show: ShowCardResponse;
  agentName: string;
}) {
  const actions = [...(show.supervisor_actions ?? [])].sort((a, b) =>
    a.created_at.localeCompare(b.created_at),
  );
  if (actions.length === 0) return null;
  const id = show.challenge.id;
  const titleOf = (tid: string) =>
    show.tasks.find((t) => t.id === tid)?.title ?? tid;
  return (
    <section aria-label="スーパーバイザーの記録" data-testid="sv-records">
      <h3>スーパーバイザーの記録</h3>
      <p data-testid="sv-records-count">
        手の数: {actions.filter((a) => a.result === "executed").length}
      </p>
      <ol className="sv-records">
        {actions.map((a) => (
          <li key={a.id} data-testid="sv-record">
            <time dateTime={a.created_at}>{shortTime(a.created_at)}</time>{" "}
            <b data-testid="sv-record-hand">{handWord(a)}</b>
            {" ・ "}
            <span data-testid="sv-record-result">{resultWord(a)}</span>
            {a.task_id !== null ? (
              <>
                {" ・ "}
                <a
                  data-testid="sv-record-task"
                  href={hrefOf({
                    kind: "task",
                    name: agentName,
                    id,
                    taskId: a.task_id,
                  })}
                >
                  {titleOf(a.task_id)}
                </a>
              </>
            ) : null}
            <SupervisorHandBody action={a} />
          </li>
        ))}
      </ol>
    </section>
  );
}
