import { useState } from "react";
import { api } from "../api/client.ts";
import type { Activity, Run, ShowCardResponse } from "../api/types.gen.ts";
import {
  HISTORY_ACTION_LABELS,
  JUDGMENT_LABELS,
  LOADING_TEXT,
  NO_COST_LABEL,
  OPERATION_KIND_LABELS,
  RUN_KIND_LABELS,
  RUN_RESULT_LABELS,
  RUN_RUNNING_LABEL,
  WHO_AGENT_RUN,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { usd } from "../usd.ts";
import { shortTime } from "../when.ts";
import { whoOf as whoLabel } from "../who.ts";

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
  op_approve: "plan",
  op_reject: "plan",
  hold: "question",
  answer: "question",
  answer_plan: "question",
  submit: "verify",
  verify_met: "verify",
  verify_not_met: "verify",
  verify_uncertain: "verify",
};

function kindOf(a: Activity): Kind {
  return (
    (a.entity === "challenge" && Object.hasOwn(CHALLENGE_KINDS, a.action)
      ? CHALLENGE_KINDS[a.action]
      : null) ?? null
  );
}

// 課題の側の承認・差し戻しの記録（op_approve・op_reject）の action と、対になる不可逆操作の側の action。
const OPERATION_DECISION_PAIRS: Record<string, string> = {
  op_approve: "approve",
  op_reject: "reject",
};

function operationIdOf(a: Activity): string | null {
  if (typeof a.after !== "object" || a.after === null) return null;
  const oid = (a.after as Record<string, unknown>).operation_id;
  return typeof oid === "string" ? oid : null;
}

// 1 つの出来事が 2 行にならないよう、課題の側の記録と対になる不可逆操作の側の承認・差し戻しの行だけを隠す鍵（action と operation_id）の集合。
// 対になる課題の側の行が無い経路（完了承認と同時の反映の承認・取り下げによる差し戻し）では、不可逆操作の側の行を出す。
function pairedOperationSideKeys(activities: Activity[]): Set<string> {
  const keys = new Set<string>();
  for (const a of activities) {
    if (a.entity !== "challenge") continue;
    const side = OPERATION_DECISION_PAIRS[a.action];
    const oid = operationIdOf(a);
    if (side !== undefined && oid !== null) keys.add(`${side}:${oid}`);
  }
  return keys;
}

function isPairedOperationSideDecision(
  a: Activity,
  paired: Set<string>,
): boolean {
  return a.entity === "operation" && paired.has(`${a.action}:${a.entity_id}`);
}

// 操作の ID を、課題の詳細の operations[] と突き合わせて、操作の種類の語を返す。見つからなければ null。
// 課題の側の行（after.operation_id）と、不可逆操作の側の承認・差し戻しの行（entity_id）に付ける。
function operationKindWord(a: Activity, show: ShowCardResponse): string | null {
  let oid: string | null = null;
  if (a.entity === "challenge" && a.action in OPERATION_DECISION_PAIRS) {
    oid = operationIdOf(a);
  } else if (
    a.entity === "operation" &&
    (a.action === "approve" || a.action === "reject")
  ) {
    oid = a.entity_id;
  }
  if (oid === null) return null;
  const op = show.operations.find((o) => o.id === oid);
  return op === undefined ? null : label(OPERATION_KIND_LABELS, op.kind);
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
  return {
    who: whoLabel(a.channel, a.verification),
    quote: a.verification === "relayed" ? relayQuote(a.after) : null,
  };
}

function activityRow(
  a: Activity,
  i: number,
  taskTitle: (id: string) => string,
  show: ShowCardResponse,
): Row {
  const { who, quote } = whoOf(a);
  const kindWord = operationKindWord(a, show);
  return {
    key: `a${i}`,
    at: a.at,
    kind: kindOf(a),
    task: a.entity === "task" ? taskTitle(a.entity_id) : null,
    who,
    quote,
    what:
      label(
        HISTORY_ACTION_LABELS,
        Object.hasOwn(HISTORY_ACTION_LABELS, `${a.entity}:${a.action}`)
          ? `${a.entity}:${a.action}`
          : a.action,
      ) + (kindWord === null ? "" : `（${kindWord}）`),
    detail: null,
  };
}

function runRow(r: Run, taskTitle: (id: string) => string): Row {
  const of =
    r.kind === "judgment" && r.judgment !== null
      ? label(JUDGMENT_LABELS, r.judgment)
      : label(RUN_KIND_LABELS, r.kind);
  const result =
    r.result === null ? RUN_RUNNING_LABEL : label(RUN_RESULT_LABELS, r.result);
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
  const paired = pairedOperationSideKeys(result.data.activities);
  const rows: Row[] = [
    ...result.data.activities
      .filter((a) => !isPairedOperationSideDecision(a, paired))
      .map((a, i) => activityRow(a, i, taskTitle, show)),
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
              <time dateTime={r.at}>{shortTime(r.at)}</time>
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
