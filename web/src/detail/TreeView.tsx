import type { ShowCardResponse, TreeRelative } from "../api/types.gen.ts";
import {
  DEPENDENCY_SATISFIED_LABEL,
  DEPENDENCY_UNMET_LABEL,
  FOLLOWUP_SOURCE_LABELS,
  FOLLOWUP_STATUS_LABELS,
  UPSTREAM_STATE_LABELS,
  label,
} from "../labels.ts";
import { ClampedText } from "../parts/ClampedText.tsx";
import { safeHref } from "../safeHref.ts";
import { hrefOf } from "../shell/route.ts";
import { TaskTree } from "../tree/TaskTree.tsx";

const challengeHref = (agentName: string, id: string) =>
  hrefOf({ kind: "challenge", name: agentName, id, view: "tree" });

// 「ツリー」の中身。タスクのツリー・フォローアップ・取り込み元の子・関係する課題・不可逆操作。
// 該当が無い区画は出さない（タスクのツリーだけは、無いことを文で示す）。
export function TreeView({
  show,
  agentName,
  titleOf,
}: {
  show: ShowCardResponse;
  agentName: string;
  // 課題の ID から題名を引く（この課題を前提にしている課題の題名。引けなければ ID を出す）。
  titleOf: (id: string) => string | undefined;
}) {
  const id = show.challenge.id;
  return (
    <div className="detail-tree" data-testid="detail-tree-view">
      <section aria-label="タスクのツリー">
        <h3>タスクのツリー</h3>
        <TaskTree
          tasks={show.tree.tasks}
          taskHref={(taskId) =>
            hrefOf({ kind: "task", name: agentName, id, taskId })
          }
        />
      </section>
      <Followups show={show} agentName={agentName} />
      <SourceChildren show={show} />
      <Related show={show} agentName={agentName} titleOf={titleOf} />
      <Operations show={show} />
    </div>
  );
}

function Followups({
  show,
  agentName,
}: {
  show: ShowCardResponse;
  agentName: string;
}) {
  if (show.followups.length === 0) return null;
  const count = (s: string) =>
    show.followups.filter((f) => f.status === s).length;
  const open = show.followups.filter((f) => f.status === "open");
  return (
    <section aria-label="フォローアップ" data-testid="detail-followups">
      <h3>フォローアップ</h3>
      <p>
        {label(FOLLOWUP_STATUS_LABELS, "open")}{" "}
        <b data-testid="followups-open">{show.tree.followups_open}</b>
        {" ・ "}
        {label(FOLLOWUP_STATUS_LABELS, "done")}{" "}
        <b data-testid="followups-done">{count("done")}</b>
        {" ・ "}
        {label(FOLLOWUP_STATUS_LABELS, "dropped")}{" "}
        <b data-testid="followups-dropped">{count("dropped")}</b>
      </p>
      {open.length > 0 ? (
        <ul>
          {open.map((f) => (
            <li key={f.id}>
              <ClampedText text={f.title} />{" "}
              <small>
                （出どころ: {label(FOLLOWUP_SOURCE_LABELS, f.source)}）
              </small>
            </li>
          ))}
        </ul>
      ) : null}
      <a
        href={hrefOf({
          kind: "agent",
          name: agentName,
          screen: "followups",
          query: { challenge: show.challenge.id },
        })}
      >
        この課題のフォローアップの一覧へ
      </a>
    </section>
  );
}

function SourceChildren({ show }: { show: ShowCardResponse }) {
  if (show.source_children.length === 0) return null;
  return (
    <section aria-label="取り込み元の子" data-testid="detail-source-children">
      <h3>取り込み元の子</h3>
      <ul>
        {show.source_children.map((c) => {
          const task = show.tasks.find((t) => t.source_key === c.external_key);
          return (
            <li key={c.external_key}>
              <a
                href={safeHref(c.url) ?? "#"}
                target="_blank"
                rel="noopener noreferrer"
              >
                {c.title}
              </a>{" "}
              <span className="status-pill">
                {label(UPSTREAM_STATE_LABELS, c.upstream_state)}
              </span>
              {task ? (
                <span data-testid="source-child-task">
                  {" "}
                  → タスク: {task.title}
                </span>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function Related({
  show,
  agentName,
  titleOf,
}: {
  show: ShowCardResponse;
  agentName: string;
  titleOf: (id: string) => string | undefined;
}) {
  const { dependencies, parent, children } = show.tree;
  const dependents = show.challenge.dependents;
  if (
    dependencies.length === 0 &&
    parent === null &&
    children.length === 0 &&
    dependents.length === 0
  ) {
    return null;
  }
  const rel = (r: TreeRelative) => (
    <li key={r.challenge_id}>
      <a href={challengeHref(agentName, r.challenge_id)}>{r.title}</a>{" "}
      <span className="status-pill">{r.status_label}</span>
    </li>
  );
  return (
    <section aria-label="関係する課題" data-testid="detail-related">
      <h3>関係する課題</h3>
      {dependencies.length > 0 ? (
        <div data-related="dependencies">
          <h4>前提</h4>
          <ul>
            {dependencies.map((d) => (
              <li key={d.challenge_id}>
                <a href={challengeHref(agentName, d.challenge_id)}>{d.title}</a>{" "}
                <span className="status-pill">{d.status_label}</span>{" "}
                <span className={d.satisfied ? undefined : "dep-unmet"}>
                  {d.satisfied
                    ? DEPENDENCY_SATISFIED_LABEL
                    : DEPENDENCY_UNMET_LABEL}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {dependents.length > 0 ? (
        <div data-related="dependents">
          <h4>この課題を前提にしている課題</h4>
          <ul>
            {dependents.map((d) => (
              <li key={d}>
                <a href={challengeHref(agentName, d)}>{titleOf(d) ?? d}</a>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {parent !== null ? (
        <div data-related="parent">
          <h4>親</h4>
          <ul>{rel(parent)}</ul>
        </div>
      ) : null}
      {children.length > 0 ? (
        <div data-related="children">
          <h4>子</h4>
          <ul>{children.map(rel)}</ul>
        </div>
      ) : null}
    </section>
  );
}

function Operations({ show }: { show: ShowCardResponse }) {
  if (show.operations.length === 0) return null;
  return (
    <section aria-label="不可逆操作" data-testid="detail-operations">
      <h3>不可逆操作</h3>
      <ul>
        {show.operations.map((o) => (
          <li key={o.id}>
            {o.kind} / {o.state}: <ClampedText text={o.summary} />
            {o.ref ? (
              <span
                className="detail-operation-ref"
                data-testid="operation-ref"
              >
                {" "}
                （参照: {o.ref}）
              </span>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}
