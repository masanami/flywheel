import { useState } from "react";
import { api } from "../api/client.ts";
import type { Activity, Run, ShowCardResponse } from "../api/types.gen.ts";
import {
  HISTORY_ACTION_LABELS,
  JUDGMENT_LABELS,
  LOADING_TEXT,
  NO_COST_LABEL,
  RUN_KIND_LABELS,
  RUN_RESULT_LABELS,
  RUN_RUNNING_LABEL,
  WHO_AGENT_RUN,
  WHO_ME_RELAYED,
  WHO_ME_TTY,
  WHO_ME_UI,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { usd } from "../usd.ts";

// 絞りの種類。実行の記録は作業ログの記録ではないので、種類は "run"。
export const HISTORY_FILTERS = [
  { id: "all", label: "すべて" },
  { id: "plan", label: "計画と承認" },
  { id: "question", label: "問いと回答" },
  { id: "verify", label: "完了の検証" },
  { id: "run", label: "実行" },
] as const;
type FilterId = (typeof HISTORY_FILTERS)[number]["id"];
type Kind = Exclude<FilterId, "all"> | null;

// 作業ログの記録の種類（entity と action の組）。表に無い記録は null（「すべて」のときだけ出る）。
const CHALLENGE_KINDS: Record<string, Kind> = {
  plan: "plan",
  approve: "plan",
  approve_hold_release: "plan",
  reject: "plan",
  hold: "question",
  answer: "question",
  answer_plan: "question",
  submit: "verify",
  verify_met: "verify",
  verify_not_met: "verify",
  verify_uncertain: "verify",
};
const OPERATION_KINDS: Record<string, Kind> = {
  approve: "plan",
  reject: "plan",
};

function kindOf(a: Activity): Kind {
  const table =
    a.entity === "challenge"
      ? CHALLENGE_KINDS
      : a.entity === "operation"
        ? OPERATION_KINDS
        : null;
  return (
    (table && Object.hasOwn(table, a.action) ? table[a.action] : null) ?? null
  );
}

interface Row {
  key: string;
  at: string;
  kind: Kind;
  task: string | null;
  who: string;
  quote: string | null;
  what: string;
  // 実行の記録の補足（結果・費用）
  detail: string | null;
}

function relayQuote(after: unknown): string | null {
  if (typeof after !== "object" || after === null) return null;
  const q = (after as Record<string, unknown>).relay_quote;
  return typeof q === "string" && q !== "" ? q : null;
}

function whoOf(a: Activity): { who: string; quote: string | null } {
  if (a.verification === "relayed")
    return { who: WHO_ME_RELAYED, quote: relayQuote(a.after) };
  if (a.channel === "ui") return { who: WHO_ME_UI, quote: null };
  if (a.channel === "cli" && a.verification === "tty_confirm")
    return { who: WHO_ME_TTY, quote: null };
  return { who: a.channel, quote: null };
}

const known = (table: Record<string, string>, v: string): string =>
  (Object.hasOwn(table, v) ? table[v] : undefined) ?? v;

function activityRow(
  a: Activity,
  i: number,
  taskTitle: (id: string) => string,
): Row {
  const { who, quote } = whoOf(a);
  return {
    key: `a${i}`,
    at: a.at,
    kind: kindOf(a),
    task: a.entity === "task" ? taskTitle(a.entity_id) : null,
    who,
    quote,
    what: known(
      HISTORY_ACTION_LABELS,
      Object.hasOwn(HISTORY_ACTION_LABELS, `${a.entity}:${a.action}`)
        ? `${a.entity}:${a.action}`
        : a.action,
    ),
    detail: null,
  };
}

function runRow(r: Run, taskTitle: (id: string) => string): Row {
  const of =
    r.kind === "judgment" && r.judgment !== null
      ? known(JUDGMENT_LABELS, r.judgment)
      : known(RUN_KIND_LABELS, r.kind);
  const result =
    r.result === null ? RUN_RUNNING_LABEL : known(RUN_RESULT_LABELS, r.result);
  // 費用の書き方は、課題の項目と同じ（数と USD）。
  const cost = r.cost_usd === null ? NO_COST_LABEL : `${usd(r.cost_usd)} USD`;
  return {
    key: `r${r.id}`,
    at: r.started_at,
    kind: "run",
    task: r.task_id === null ? null : taskTitle(r.task_id),
    who: WHO_AGENT_RUN,
    quote: null,
    what: of,
    detail: `${result}・${cost}`,
  };
}

function time(s: string): number {
  const t = Date.parse(s);
  return Number.isNaN(t) ? 0 : t;
}

export function formatAt(s: string): string {
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 「経緯」の中身。作業ログ（その課題の ID つき）と実行の記録を、新しい順に 1 本の時系列にする。
// 作業ログの API は、この部品を開いている間（「経緯」を選んでいる間）だけ読み、変化の通し番号が進んだら読み直す。
export function History({
  live,
  agentName,
  show,
}: {
  live: LiveState;
  agentName: string;
  show: ShowCardResponse;
}) {
  const id = show.challenge.id;
  const [filter, setFilter] = useState<FilterId>("all");
  const { result } = useScreenData(
    live,
    agentName,
    () => api.log(agentName, id),
    [id],
  );
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error") {
    const e = result.error;
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const titles = new Map(show.tasks.map((t) => [t.id, t.title]));
  const taskTitle = (tid: string) => titles.get(tid) ?? tid;
  const rows: Row[] = [
    ...result.data.activities.map((a, i) => activityRow(a, i, taskTitle)),
    ...show.runs.map((r) => runRow(r, taskTitle)),
  ].sort((x, y) => time(y.at) - time(x.at));
  const shown = filter === "all" ? rows : rows.filter((r) => r.kind === filter);
  return (
    <section aria-label="経緯" data-testid="detail-history" className="history">
      <nav className="history-filters" aria-label="種類で絞る">
        {HISTORY_FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            aria-pressed={f.id === filter}
            onClick={() => setFilter(f.id)}
          >
            {f.label}
          </button>
        ))}
      </nav>
      {shown.length === 0 ? (
        <p data-testid="history-empty">この種類の出来事はまだありません。</p>
      ) : (
        <ol className="history-list">
          {shown.map((r) => (
            <li key={r.key} data-testid="history-row">
              <time dateTime={r.at}>{formatAt(r.at)}</time>
              <span className="history-who" data-testid="history-who">
                {r.who}
              </span>
              {r.task === null ? null : (
                <span className="history-task" data-testid="history-task">
                  {r.task}
                </span>
              )}
              <span className="history-what" data-testid="history-what">
                {r.what}
              </span>
              {r.detail === null ? null : (
                <span className="history-detail" data-testid="history-detail">
                  {r.detail}
                </span>
              )}
              {r.quote === null ? null : (
                <q className="history-quote" data-testid="history-quote">
                  {r.quote}
                </q>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
