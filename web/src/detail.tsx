import { useEffect, useState } from "react";
import { api } from "./api/client.ts";
import type {
  Activity,
  RunReport,
  ShowCardResponse,
  Workspace,
} from "./api/types.gen.ts";
import { ClampedText } from "./cards.tsx";
import { OUTSIDE_NOTE, guidesFor } from "./guidance.ts";
import { MODIFIER_LABELS, STATE_LABELS, label } from "./labels.ts";

interface Loaded {
  show: ShowCardResponse;
  activities: Activity[];
  reports: Record<string, RunReport>;
}

const byTimeAsc = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

async function load(workspace: string, id: string): Promise<Loaded> {
  const [show, log] = await Promise.all([
    api.challenge(workspace, id),
    api.log(workspace, id),
  ]);
  const reports: Record<string, RunReport> = {};
  await Promise.all(
    show.runs.map(async (r) => {
      try {
        reports[r.id] = (await api.runReport(workspace, r.id)).report;
      } catch {
        // report の無い run は、assumptions と unverified を出さないだけにする
      }
    }),
  );
  return { show, activities: log.activities, reports };
}

function List({ items }: { items: string[] }) {
  return (
    <ul>
      {items.map((x, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 同じ文言が並びうる固定の一覧
        <li key={i}>
          <ClampedText text={x} />
        </li>
      ))}
    </ul>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h3>
        {title}（{count}）
      </h3>
      {count === 0 ? <p>なし</p> : children}
    </section>
  );
}

// 課題 1 件の詳細。外部由来の文字列はすべてプレーンテキストで出す。
export function DetailScreen({
  workspace,
  id,
  workspaces,
  refreshKey,
  onClose,
}: {
  workspace: string;
  id: string;
  workspaces: Workspace[];
  refreshKey: unknown;
  onClose: () => void;
}) {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    let live = true;
    load(workspace, id).then(
      (l) => {
        if (live) {
          setLoaded(l);
          setError(null);
        }
      },
      (e) => live && setError(String(e)),
    );
    return () => {
      live = false;
    };
  }, [workspace, id, refreshKey]);

  const path = workspaces.find((w) => w.name === workspace)?.path ?? "";
  return (
    <section aria-label="課題の詳細">
      <button type="button" onClick={onClose}>
        一覧へ戻る
      </button>
      {error ? <p role="alert">読み込みに失敗: {error}</p> : null}
      {loaded ? (
        <Body l={loaded} workspace={workspace} path={path} />
      ) : error ? null : (
        <p>読み込み中</p>
      )}
    </section>
  );
}

function Body({
  l,
  workspace,
  path,
}: {
  l: Loaded;
  workspace: string;
  path: string;
}) {
  const { show, activities, reports } = l;
  const c = show.challenge;
  const guides = guidesFor(c.id, path, c.status, show.card.modifiers);
  const plans = [...show.plans].sort((a, b) => a.version - b.version);
  const runs = [...show.runs].sort((a, b) =>
    byTimeAsc(b.started_at, a.started_at),
  );
  const acts = [...activities].sort((a, b) => byTimeAsc(a.at, b.at));
  return (
    <>
      <h2>
        <ClampedText text={c.title} />
      </h2>
      <p>
        {workspace} / <code>{c.id}</code> ・ いまの状態:{" "}
        {label(STATE_LABELS, show.card.state)}
        {show.card.modifiers.map((m) => (
          <span key={m}> [{label(MODIFIER_LABELS, m)}]</span>
        ))}
      </p>
      {guides.length > 0 ? (
        <section aria-label="承認方法の案内">
          <h3>端末で実行するコマンド</h3>
          <p>{OUTSIDE_NOTE}</p>
          {guides.map((g) => (
            <pre key={g.id} data-guide={g.id}>
              <code>{g.command}</code>
            </pre>
          ))}
        </section>
      ) : null}
      <section aria-label="説明">
        <h3>説明</h3>
        <ClampedText text={c.description} />
        <h3>完了条件</h3>
        <ClampedText text={c.done_criteria} />
      </section>
      <Section title="計画" count={plans.length}>
        {plans.map((p) => (
          <article key={p.version} data-plan={p.version}>
            <h4>
              v{p.version}（{p.created_at}）
            </h4>
            <pre style={{ whiteSpace: "pre-wrap" }}>{p.body}</pre>
          </article>
        ))}
      </Section>
      <Section title="承認と差し戻しの記録" count={show.approvals.length}>
        <ul>
          {show.approvals.map((a) => (
            <li
              key={`${a.kind}/${a.target_version}/${a.decided_at}/${a.operation_id ?? ""}`}
            >
              {a.decided_at} {a.kind} v{a.target_version}: {a.decision}（
              {a.actor}）
              {a.reason ? (
                <>
                  {" "}
                  <ClampedText text={a.reason} />
                </>
              ) : null}
            </li>
          ))}
        </ul>
      </Section>
      <Section title="保留の問いと回答" count={show.holds.length}>
        <ul>
          {show.holds.map((h) => (
            <li key={h.raised_at}>
              <div>
                問い（{h.raised_at}）: <ClampedText text={h.question} />
              </div>
              <div>
                回答:{" "}
                {h.answer === null ? "未回答" : <ClampedText text={h.answer} />}
              </div>
            </li>
          ))}
        </ul>
      </Section>
      <Section title="不可逆操作" count={show.operations.length}>
        <ul>
          {show.operations.map((o) => (
            <li key={o.id}>
              {o.kind} / {o.state}: <ClampedText text={o.summary} />
            </li>
          ))}
        </ul>
      </Section>
      <Section title="run" count={runs.length}>
        <ul>
          {runs.map((r) => {
            const rep = reports[r.id];
            return (
              <li key={r.id} data-run={r.id}>
                <code>{r.id}</code> {r.kind} ・ {r.started_at}
                {r.result ? ` ・ ${r.result}` : ""}
                {rep && rep.assumptions.length > 0 ? (
                  <div>
                    仮定:
                    <List items={rep.assumptions} />
                  </div>
                ) : null}
                {rep && rep.unverified.length > 0 ? (
                  <div>
                    未検証:
                    <List items={rep.unverified} />
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      </Section>
      <Section title="作業ログ" count={acts.length}>
        <ol>
          {acts.map((a) => (
            <li key={`${a.at}/${a.entity}/${a.entity_id}/${a.action}`}>
              {a.at} {a.actor} {a.action} {a.entity} {a.entity_id}
            </li>
          ))}
        </ol>
      </Section>
    </>
  );
}
