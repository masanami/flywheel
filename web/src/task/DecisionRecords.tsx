import type { ShowCardResponse, Task } from "../api/types.gen.ts";
import {
  HOLD_KIND_PARENT_DIRECT,
  JUDGMENT_LABELS,
  TASK_ADDED_ORIGIN_LABELS,
  TASK_ADDED_ORIGIN_SUPERVISOR_LABEL,
  VERDICT_LABELS,
  WHO_ME_UI_BUTTON,
  WHO_SUPERVISOR,
  label,
} from "../labels.ts";
import {
  SupervisorHandBody,
  handWord,
  resultWord,
} from "../parts/SupervisorHand.tsx";
import { shortTime } from "../when.ts";
import { whoOf } from "../who.ts";

// 意思決定の記録の 1 行。いつ・誰が・何を決めたか・理由（あれば）。
export interface DecisionRow {
  key: string;
  at: string;
  who: string;
  what: string;
  reason: string | null;
  // スーパーバイザーの手の行だけが持つ（調べたこと・添え書き・条件・答えた問いを出す）
  hand: ShowCardResponse["supervisor_actions"][number] | null;
  quote: string | null;
}

const time = (s: string): number => {
  const t = Date.parse(s);
  return Number.isNaN(t) ? 0 : t;
};

// 保留に答えた人（経路 ui だけは、画面のボタンと出す）。
const answeredBy = (h: ShowCardResponse["holds"][number]): string =>
  h.answered_channel === null
    ? "答えた人は記録なし"
    : h.answered_channel === "ui"
      ? WHO_ME_UI_BUTTON
      : whoOf(h.answered_channel, h.answered_verification);

// 足されたタスクの出どころの語。フォローアップの出どころがスーパーバイザーなら、その旨の語。
export function addedOriginWord(
  show: ShowCardResponse,
  added: NonNullable<Task["added"]>,
): string {
  const followup =
    added.followup_id === null
      ? undefined
      : (show.followups ?? []).find((f) => f.id === added.followup_id);
  if (added.origin === "followup" && followup?.source === "supervisor") {
    return TASK_ADDED_ORIGIN_SUPERVISOR_LABEL;
  }
  return label(TASK_ADDED_ORIGIN_LABELS, added.origin);
}

// そのタスクに関わる出来事を、古い順に並べる。
// 計画の承認は課題につくものなので、どのタスクの記録にも出す（承認し直した課題では、承認のたびに 1 行）。回答とスーパーバイザーの手は、そのタスクのものだけ。
export function decisionRows(
  show: ShowCardResponse,
  task: Task,
): DecisionRow[] {
  const rows: DecisionRow[] = [];
  for (const a of show.approvals ?? []) {
    if (a.kind !== "plan" || a.decision !== "approved") continue;
    if (a.superseded_at !== null) continue;
    rows.push({
      key: `approval-${a.id}`,
      at: a.decided_at,
      who: whoOf(a.channel, a.verification),
      what: "計画を承認した",
      reason: a.reason,
      hand: null,
      quote: a.verification === "relayed" ? a.relay_quote : null,
    });
  }
  for (const h of show.holds) {
    if (h.task_id !== task.id || h.answer === null) continue;
    if (h.kind === HOLD_KIND_PARENT_DIRECT) continue;
    const opt =
      h.answer_option === null
        ? undefined
        : h.options.find((o) => o.id === h.answer_option);
    rows.push({
      key: `hold-${h.hold_id}`,
      at: h.answered_at ?? h.raised_at,
      who: answeredBy(h),
      what: `問いに答えた: ${opt !== undefined ? opt.label : h.answer}`,
      reason: null,
      hand: null,
      quote: h.relay_quote,
    });
  }
  for (const s of show.supervisor_actions ?? []) {
    if (s.task_id !== task.id) continue;
    rows.push({
      key: `sv-${s.id}`,
      at: s.created_at,
      who: WHO_SUPERVISOR,
      what: `${handWord(s)}（${resultWord(s)}）`,
      reason: null,
      hand: s,
      quote: null,
    });
  }
  if (task.verification !== null) {
    const v = task.verification;
    rows.push({
      key: "verification",
      at: v.at,
      who: label(JUDGMENT_LABELS, "J5"),
      what: `完了条件を確かめた: ${label(VERDICT_LABELS, v.verdict)}`,
      reason: v.reason === "" ? null : v.reason,
      hand: null,
      quote: null,
    });
  }
  if (task.added !== null) {
    rows.push({
      key: "added",
      at: task.added.added_at,
      who: addedOriginWord(show, task.added),
      what: "後から足されたタスク",
      reason: null,
      hand: null,
      quote: null,
    });
  }
  return rows.sort((x, y) => time(x.at) - time(y.at));
}

// 意思決定の記録。出すものが無ければ節ごと出さない。
export function DecisionRecords({
  show,
  task,
}: {
  show: ShowCardResponse;
  task: Task;
}) {
  const rows = decisionRows(show, task);
  if (rows.length === 0) return null;
  return (
    <section aria-label="意思決定の記録" data-testid="task-decisions">
      <h3>意思決定の記録</h3>
      <ol className="decision-list">
        {rows.map((r) => (
          <li key={r.key} data-testid="decision-row">
            <time dateTime={r.at}>{shortTime(r.at)}</time>
            <span className="decision-who" data-testid="decision-who">
              {r.who}
            </span>
            <span className="decision-what" data-testid="decision-what">
              {r.what}
            </span>
            {r.reason === null ? null : (
              <span className="decision-reason" data-testid="decision-reason">
                理由: {r.reason}
              </span>
            )}
            {r.quote === null || r.quote === "" ? null : (
              <q className="decision-quote" data-testid="decision-quote">
                {r.quote}
              </q>
            )}
            {r.hand === null ? null : <SupervisorHandBody action={r.hand} />}
          </li>
        ))}
      </ol>
    </section>
  );
}
