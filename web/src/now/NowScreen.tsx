import { type ReactNode, useCallback, useEffect, useState } from "react";
import { NoticeItem } from "../NoticeItem.tsx";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { IngestableItem, Question } from "../api/types.gen.ts";
import { type Guide, guidesForContext } from "../guidance.ts";
import { LOADING_TEXT, RELOAD_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { NOTICE_SECTION_TITLE, noticeLine } from "../notices.ts";
import {
  HoldAnswerNotices,
  HoldOptions,
  SupervisorNote,
  hasOptions,
  useHoldAnswer,
} from "../parts/HoldOptions.tsx";
import { safeHref } from "../safeHref.ts";
import { CycleWorkPart } from "../shell/CycleWorkPart.tsx";
import { hrefOf } from "../shell/route.ts";
import {
  NO_WAITING_TEXT,
  WAITING_LABELS,
  waitingKindGroup,
} from "../shell/waiting.ts";
import { type WaitingRow, buildWaitingRows } from "../shell/waitingRows.ts";
import { useNotices } from "../useNotices.ts";
import { useIngestable } from "./useIngestable.ts";

export const INGEST_LOADING_TEXT = "読んでいます";
export const INGEST_BUTTON_TEXT = (n: number) =>
  `選んだ ${n} 件を取り込んで、計画まで進める`;
export const INGEST_BUSY_TEXT = "取り込んでいます";
export const INGEST_DONE_TEXT = "取り込みました。作った課題:";
export const INGEST_CYCLE_STARTED_TEXT = "計画まで進めるサイクルを始めました。";
export const INGEST_CYCLE_RUNNING_TEXT = "サイクルはすでに走っています。";
export const ingestCountText = (challenges: number, children: number) =>
  `課題 ${challenges} 件・取り込み元の子として記録 ${children} 件`;
export const NOTHING_TO_DO_TEXT = "いま進められるものはありません。";

const guideOf = (context: string, path: string): Guide | undefined =>
  guidesForContext(context, path)[0];

// 「いま進められること」: あなたを待っているもの・取り込めるもの・サイクルを回すと進むもの・未のフォローアップ。
// 件数と区分は core の値（状況の API）で、画面は並べるだけ。画面のボタンで受ける書き込みは
// 「サイクルを回す」・取り込み・選択肢で答える回答。
// 題名が引けず titleOf が ID をそのまま返したときは、ID を単独で出さず「課題（ID）」にする。
function withIdIfUntitled(label: string, id: string): string {
  return label === id ? `課題（${id}）` : label;
}

export function NowScreen({
  live,
  agentName,
}: {
  live: LiveState;
  agentName: string;
}) {
  const agent = live.workspaces.find((w) => w.name === agentName);
  const data = live.data[agentName];
  const status = data?.status;
  const { result: ing, reload } = useIngestable(agentName);
  const notices = useNotices(live, agentName);
  // 問いは全体の値を使う。答えた後（問いが変わっていたときを含む）だけ、1 回読み直して、全体の値が次に変わるまで使う。
  const [fresh, setFresh] = useState<readonly Question[] | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: 全体の値が変わったら、読み直した値は捨てる
  useEffect(() => setFresh(null), [live.questions]);
  const reloadQuestions = useCallback(() => {
    api.questions().then(
      (r) => setFresh(r.questions),
      () => {},
    );
  }, []);

  if (!status || !agent) {
    return (
      <section aria-labelledby="screen-title">
        <h2 id="screen-title">いま進められること</h2>
        <p>{LOADING_TEXT}</p>
      </section>
    );
  }

  const titles = new Map<string, string>();
  for (const c of data?.challenges?.challenges ?? []) titles.set(c.id, c.title);
  for (const c of status.needs_human.challenges) titles.set(c.id, c.title);
  const titleOf = (id: string) => titles.get(id) ?? id;

  const questions = (fresh ?? live.questions?.questions ?? []).filter(
    (q) => q.workspace === agentName,
  );
  const rows = buildWaitingRows(agent, status.needs_human, questions, titleOf);

  const work = status.cycle_work;
  const cycleState = data?.cycle?.state ?? "";
  const ingestItems = ing.state === "ready" ? ing.ingestable.items : [];
  const ingestError = ing.state === "ready" ? ing.ingestable.error : null;
  const ingestCount = ing.state === "ready" ? ingestItems.length : null;
  const followupsHref = hrefOf({
    kind: "agent",
    name: agentName,
    screen: "followups",
  });
  const slotGuide = guideOf("slot", agent.path);
  const approvalCount = rows.filter(
    (r) => waitingKindGroup(r.kind) === "approval",
  ).length;
  const answerCount = rows.filter((r) => r.kind === "answer").length;
  const sortedNotices = [...notices]
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
    .map((n) => noticeLine(n, agentName));
  const nothing =
    rows.length === 0 &&
    ing.state === "ready" &&
    ing.ingestable.error === null &&
    ingestItems.length === 0 &&
    work.total === 0;

  return (
    <section aria-labelledby="screen-title" className="now-screen">
      <h2 id="screen-title">いま進められること</h2>
      <div className="now-meta">
        <span data-testid="count-waiting">
          あなたを待っているもの <b>{rows.length} 件</b>
        </span>
        <span data-testid="count-waiting-split">
          承認 <b>{approvalCount} 件</b>・回答 <b>{answerCount} 件</b>
        </span>
        <span data-testid="count-ingestable">
          取り込めるもの{" "}
          <b>{ingestCount === null ? "—" : `${ingestCount} 件`}</b>
        </span>
        <span data-testid="count-cycle-work">
          サイクルを回すと進むもの <b>{work.total} 件</b>
        </span>
        <a data-testid="count-followups" href={followupsHref}>
          未のフォローアップ <b>{status.followups.open} 件</b>
        </a>
      </div>
      {nothing ? <p data-testid="nothing">{NOTHING_TO_DO_TEXT}</p> : null}

      {sortedNotices.length > 0 ? (
        <section
          aria-labelledby="now-notices-title"
          className="now-block"
          data-testid="notices"
        >
          <h3 id="now-notices-title">{NOTICE_SECTION_TITLE}</h3>
          <ul className="now-list">
            {sortedNotices.map((l) => (
              <NoticeItem key={l.key} line={l} />
            ))}
          </ul>
        </section>
      ) : null}

      <section aria-labelledby="now-waiting-title" className="now-block">
        <h3 id="now-waiting-title">あなたを待っている（{rows.length} 件）</h3>
        {rows.length === 0 ? (
          <p>{NO_WAITING_TEXT}</p>
        ) : (
          <ul className="now-list" data-testid="waiting-list">
            {rows.map((r, i) => (
              <WaitingItem
                key={`${r.key}#${i}`}
                row={r}
                slotGuide={slotGuide}
                agentName={agentName}
                questions={questions}
                onAnswered={reloadQuestions}
              />
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="now-ingest-title" className="now-block">
        <div className="now-block-head">
          <h3 id="now-ingest-title">
            取り込めるもの{ingestCount === null ? "" : `（${ingestCount} 件）`}
          </h3>
          <button
            type="button"
            className="now-reload"
            disabled={ing.state === "loading"}
            onClick={reload}
          >
            読み直す
          </button>
        </div>
        {ing.state === "loading" ? (
          <output>{INGEST_LOADING_TEXT}</output>
        ) : null}
        {ing.state === "failed" ? (
          <p role="alert" data-testid="ingestable-error">
            {ing.message}
          </p>
        ) : null}
        {ingestError ? (
          <p role="alert" data-testid="ingestable-error">
            {ingestError}
          </p>
        ) : null}
        <IngestList
          agentName={agentName}
          items={ingestItems}
          titleOf={titleOf}
          onIngested={reload}
        />
      </section>

      <CycleWorkPart
        workspace={agentName}
        workspacePath={agent.path}
        work={work}
        cycleState={cycleState}
      />
    </section>
  );
}

// 取り込み元の行き先。http(s) だけをリンクにし、それ以外は文字として出す（判定は safeHref の 1 つ）。
function SourceLink({ url, children }: { url: string; children: ReactNode }) {
  const href = safeHref(url);
  return href ? (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ) : (
    <span>{children}</span>
  );
}

function WaitingItem({
  row,
  slotGuide,
  agentName,
  questions,
  onAnswered,
}: {
  row: WaitingRow;
  slotGuide: Guide | undefined;
  agentName: string;
  questions: readonly Question[];
  onAnswered: () => void;
}) {
  const group = waitingKindGroup(row.kind);
  return (
    <li className="now-row" data-kind={row.kind}>
      <span
        className="now-kind"
        {...(group ? { "data-kind-group": group } : {})}
      >
        {WAITING_LABELS[row.kind]}
      </span>{" "}
      {row.href ? (
        <a href={row.href}>
          <b>{row.title}</b>
        </a>
      ) : (
        <b>{row.title}</b>
      )}
      {row.hint ? <div className="now-hint">{row.hint}</div> : null}
      {row.kind === "answer" && row.challengeId !== undefined ? (
        <AnswerButtons
          agentName={agentName}
          challengeId={row.challengeId}
          questions={questions.filter(
            (q) => q.challenge_id === row.challengeId,
          )}
          onAnswered={onAnswered}
        />
      ) : null}
      {row.kind === "slot" && slotGuide && row.slotId ? (
        <code className="now-command">
          {slotGuide.command.replaceAll("<SLOT-ID>", () => row.slotId ?? "")}
        </code>
      ) : null}
    </li>
  );
}

// 回答の行の、選択肢のボタン。ボタンで答えられる問い（選択肢と照合の値がある）だけ出す。
// 部品（HoldOptions）は課題の詳細・タスクの詳細と同じもの。親は key に保留の ID と照合の値を含める。
function AnswerButtons({
  agentName,
  challengeId,
  questions,
  onAnswered,
}: {
  agentName: string;
  challengeId: string;
  questions: readonly Question[];
  onAnswered: () => void;
}) {
  const state = useHoldAnswer(agentName, challengeId, onAnswered);
  const answerable = questions.filter((q) => hasOptions(q));
  return (
    <div data-testid="now-answer">
      {answerable.map((q) => (
        <div key={`${q.hold_id}:${q.check}`} data-testid="now-answer-hold">
          {questions.length > 1 ? <p>{q.question}</p> : null}
          <SupervisorNote
            findings={q.supervisor_findings}
            recommendation={q.supervisor_recommendation}
          />
          <HoldOptions hold={q} state={state} />
        </div>
      ))}
      <HoldAnswerNotices state={state} />
    </div>
  );
}

// 取り込めるものの一覧。行のチェック・取り込みのボタン・脇の件数・成立したときの課題へのリンク・誤りの理由。
// 件数は選んだ行の数と、API が返した子の数の合計（親子の判定はしない）。費用の見込みとタスクの案の数は出さない。
function IngestList({
  agentName,
  items,
  titleOf,
  onIngested,
}: {
  agentName: string;
  items: readonly IngestableItem[];
  titleOf: (id: string) => string;
  onIngested: () => void;
}) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<{
    created: { id: string; title: string }[];
    cycle: string;
  } | null>(null);

  // 一覧が読み直されて無くなった行の選択は外す。
  const chosen = items.filter((it) => selected.has(it.external_key));
  const childCount = chosen.reduce((a, it) => a + it.children.length, 0);

  const toggle = (key: string) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const submit = async () => {
    const sent = chosen;
    if (sent.length === 0) return;
    setBusy(true);
    setError(null);
    setDone(null);
    try {
      const res = await api.ingest(agentName, {
        items: sent.map((it) => it.external_key),
      });
      const titleByKey = new Map(sent.map((it) => [it.external_key, it.title]));
      setDone({
        created: res.ingested.map((g) => ({
          id: g.challenge_id,
          title: titleByKey.get(g.external_key) ?? "",
        })),
        cycle: res.cycle.started ? "started" : res.cycle.reason,
      });
      setSelected(new Set());
      onIngested();
    } catch (e) {
      // 失敗したときはチェックを残す（そのままもう一度押せる）。
      if (
        e instanceof ApiError &&
        (e.status === 403 || e.code === "forbidden_origin")
      ) {
        setError(RELOAD_TEXT);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      {items.length > 0 ? (
        <ul className="now-list" data-testid="ingestable-list">
          {items.map((it) => (
            <li key={it.external_key} className="now-row">
              <input
                type="checkbox"
                aria-label={`取り込む: ${it.title}`}
                checked={selected.has(it.external_key)}
                disabled={busy}
                onChange={() => toggle(it.external_key)}
              />{" "}
              <span className="now-hint">#{it.number}</span>{" "}
              <SourceLink url={it.url}>
                <b>{it.title}</b>
              </SourceLink>
              {it.children.length > 0 ? (
                <span className="now-hint">子 {it.children.length} 件</span>
              ) : null}
              {it.parent_challenge_id ? (
                <div className="now-hint">
                  課題:{" "}
                  <a
                    href={hrefOf({
                      kind: "challenge",
                      name: agentName,
                      id: it.parent_challenge_id,
                      view: "tree",
                    })}
                  >
                    {withIdIfUntitled(
                      titleOf(it.parent_challenge_id),
                      it.parent_challenge_id,
                    )}
                  </a>
                </div>
              ) : null}
              {it.children.length > 0 ? (
                <ul className="now-children">
                  {it.children.map((ch) => (
                    <li key={ch.external_key}>
                      <SourceLink url={ch.url}>{ch.title}</SourceLink>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {items.length > 0 ? (
        <div className="now-ingest-actions">
          <button
            type="button"
            data-testid="ingest-button"
            disabled={busy || chosen.length === 0}
            onClick={() => void submit()}
          >
            {INGEST_BUTTON_TEXT(chosen.length)}
          </button>
          {chosen.length > 0 ? (
            <span className="now-hint" data-testid="ingest-counts">
              {ingestCountText(chosen.length, childCount)}
            </span>
          ) : null}
          {busy ? <output>{INGEST_BUSY_TEXT}</output> : null}
        </div>
      ) : null}
      {error !== null ? (
        <p role="alert" data-testid="ingest-error">
          {error}
        </p>
      ) : null}
      {done !== null ? (
        <div data-testid="ingest-done">
          <p>{INGEST_DONE_TEXT}</p>
          <ul>
            {done.created.map((c) => (
              <li key={c.id}>
                <a
                  href={hrefOf({
                    kind: "challenge",
                    name: agentName,
                    id: c.id,
                    view: "tree",
                  })}
                >
                  {c.title !== ""
                    ? c.title
                    : withIdIfUntitled(titleOf(c.id), c.id)}
                </a>
              </li>
            ))}
          </ul>
          {done.cycle === "started" ? (
            <p className="now-hint">{INGEST_CYCLE_STARTED_TEXT}</p>
          ) : done.cycle === "already_running" ? (
            <p className="now-hint">{INGEST_CYCLE_RUNNING_TEXT}</p>
          ) : null}
        </div>
      ) : null}
    </>
  );
}
