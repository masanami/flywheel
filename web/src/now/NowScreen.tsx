import type { ReactNode } from "react";
import { type Guide, guidesForContext, shellQuote } from "../guidance.ts";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { safeHref } from "../safeHref.ts";
import { CycleWorkPart } from "../shell/CycleWorkPart.tsx";
import { hrefOf } from "../shell/route.ts";
import {
  NO_WAITING_TEXT,
  WAITING_LABELS,
  waitingKindGroup,
} from "../shell/waiting.ts";
import { type WaitingRow, buildWaitingRows } from "../shell/waitingRows.ts";
import { useIngestable } from "./useIngestable.ts";

export const INGEST_LOADING_TEXT = "読んでいます";
export const INGEST_TERMINAL_NOTE = "取り込みは、端末か対話で行います。";
export const NOTHING_TO_DO_TEXT = "いま進められるものはありません。";

const guideOf = (context: string, path: string): Guide | undefined =>
  guidesForContext(context, path)[0];

// 「いま進められること」: あなたを待っているもの・取り込めるもの・サイクルを回すと進むもの・未のフォローアップ。
// 件数と区分は core の値（状況の API）で、画面は並べるだけ。画面のボタンで受ける書き込みは
// 「サイクルを回す」だけ（取り込み・回答は端末のコマンドの案内を出す）。
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

  const questions = (live.questions?.questions ?? []).filter(
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
  const ingestGuide = guideOf("ingest", agent.path);
  const slotGuide = guideOf("slot", agent.path);
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
        {ingestItems.length > 0 ? (
          <ul className="now-list" data-testid="ingestable-list">
            {ingestItems.map((it) => (
              <li key={it.external_key} className="now-row">
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
                      {titleOf(it.parent_challenge_id)}
                    </a>
                  </div>
                ) : null}
                {ingestGuide ? (
                  <code className="now-command">
                    {/* external_key は # を含む。シェルの設定によっては # が特別な意味を持つので、1 語として引用する */}
                    {ingestGuide.command.replaceAll("<external_key>", () =>
                      shellQuote(it.external_key),
                    )}
                  </code>
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
        <p className="now-hint">{INGEST_TERMINAL_NOTE}</p>
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
function SourceLink({
  url,
  children,
}: {
  url: string;
  children: ReactNode;
}) {
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
}: {
  row: WaitingRow;
  slotGuide: Guide | undefined;
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
      {row.kind === "slot" && slotGuide && row.slotId ? (
        <code className="now-command">
          {slotGuide.command.replaceAll("<SLOT-ID>", () => row.slotId ?? "")}
        </code>
      ) : null}
    </li>
  );
}
