import { type ReactNode, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { ChallengeCard, Followup } from "../api/types.gen.ts";
import { guidesForContext } from "../guidance.ts";
import {
  FOLLOWUP_SOURCE_LABELS,
  FOLLOWUP_STATUS_LABELS,
  LOADING_TEXT,
  RELOAD_TEXT,
  label,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { type Route, hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";

export const FOLLOWUP_STATUSES = ["open", "done", "dropped"] as const;
export type FollowupStatus = (typeof FOLLOWUP_STATUSES)[number];

export const NO_OPEN_FOLLOWUPS_TEXT = "未のフォローアップはありません。";
export const UNATTACHED_TEXT = "課題に属さないもの";
export const PROMOTE_GUIDE_ID = "followup-promote";
export const GUIDANCE_CONTEXT = "followups";

// ?status= の値。open・done・dropped のどれでもなければ open。
export function statusOf(value: string | undefined): FollowupStatus {
  return FOLLOWUP_STATUSES.find((s) => s === value) ?? "open";
}

// 課題ごとのまとまり。課題の並びは、フォローアップが API で出てきた順。課題に属さないものは最後。
export function groupByChallenge(
  items: Followup[],
): { challengeId: string | null; items: Followup[] }[] {
  const groups = new Map<string, Followup[]>();
  const loose: Followup[] = [];
  for (const f of items) {
    if (f.challenge_id === null) {
      loose.push(f);
    } else {
      const g = groups.get(f.challenge_id);
      if (g) g.push(f);
      else groups.set(f.challenge_id, [f]);
    }
  }
  const out: { challengeId: string | null; items: Followup[] }[] = [
    ...groups,
  ].map(([challengeId, list]) => ({ challengeId, items: list }));
  if (loose.length > 0) out.push({ challengeId: null, items: loose });
  return out;
}

// フォローアップの画面。一覧の API を status=all で 1 回だけ読み、未・済み・捨てたの切り替えは画面の側で行う。
// 未の行にチェックを出し、選んだものを「済みにする」（確かめた結果）・「捨てる」（理由）で口へ送る。課題やタスクへ上げるボタンは置かない。
export function FollowupsScreen(props: FollowupsScreenProps) {
  // 選びと書いた文は、エージェントごとに持つ（切り替えたら持ち越さない）。
  return <FollowupsBody key={props.agentName} {...props} />;
}

interface FollowupsScreenProps {
  live: LiveState;
  agentName: string;
  query: Record<string, string> | undefined;
  onNavigate: (r: Route, opts?: { replace?: boolean }) => void;
}

function FollowupsBody({
  live,
  agentName,
  query,
  onNavigate,
}: FollowupsScreenProps) {
  const { result, reload } = useScreenData(live, agentName, () =>
    api.followups(agentName, "all"),
  );
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const status = statusOf(query?.status);
  const only = query?.challenge ? query.challenge : null;
  const challenges: ChallengeCard[] =
    live.data[agentName]?.challenges?.challenges ?? [];
  const agentPath = live.workspaces.find((w) => w.name === agentName)?.path;

  const route = (q: Record<string, string>): Route => ({
    kind: "agent",
    name: agentName,
    screen: "followups",
    ...(Object.keys(q).length > 0 ? { query: q } : {}),
  });
  const choose = (s: FollowupStatus) =>
    onNavigate(route({ ...(only ? { challenge: only } : {}), status: s }), {
      replace: true,
    });
  const challengeLink = (id: string) => {
    const c = challenges.find((x) => x.id === id);
    return (
      <a
        href={hrefOf({ kind: "challenge", name: agentName, id, view: "tree" })}
      >
        {c ? c.title : id}
      </a>
    );
  };

  const all =
    result.state === "ready"
      ? result.data.followups.filter(
          (f) => only === null || f.challenge_id === only,
        )
      : [];
  const count = (s: FollowupStatus) => all.filter((f) => f.status === s).length;
  const shown = all.filter((f) => f.status === status);
  const guides =
    agentPath === undefined
      ? []
      : guidesForContext(GUIDANCE_CONTEXT, agentPath).filter(
          (g) => g.id === PROMOTE_GUIDE_ID,
        );
  // 選んだものは、いま未で出ているものだけに絞る（読み直しで済み・捨てたに変わったものを送らない）。
  const openIds = new Set(
    all.filter((f) => f.status === "open").map((f) => f.id),
  );
  const chosen = [...selected].filter((id) => openIds.has(id));
  const toggle = (id: string) =>
    setSelected((cur) => {
      const n = new Set(cur);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <section aria-labelledby="screen-title" className="followups">
      <h2 id="screen-title">フォローアップ</h2>
      {result.state === "loading" ? <p>{LOADING_TEXT}</p> : null}
      {result.state === "error" ? (
        <p role="alert">{String(result.error)}</p>
      ) : null}
      {result.state === "ready" ? (
        <>
          <fieldset
            className="followup-switch"
            aria-label="フォローアップの状態"
          >
            {FOLLOWUP_STATUSES.map((s) => (
              <button
                key={s}
                type="button"
                className="followup-tab"
                aria-pressed={s === status}
                onClick={() => choose(s)}
              >
                {label(FOLLOWUP_STATUS_LABELS, s)}（{count(s)}）
              </button>
            ))}
          </fieldset>
          {only !== null ? (
            <p className="followup-filter" data-testid="followup-filter">
              課題 {challengeLink(only)} のフォローアップだけを出しています。{" "}
              <a
                href={hrefOf(route(status === "open" ? {} : { status }))}
                data-testid="followup-unfilter"
              >
                絞りを外す
              </a>
            </p>
          ) : null}
          {shown.length === 0 ? (
            <p data-testid="followup-empty">
              {status === "open"
                ? NO_OPEN_FOLLOWUPS_TEXT
                : `${label(FOLLOWUP_STATUS_LABELS, status)}のフォローアップはありません。`}
            </p>
          ) : (
            groupByChallenge(shown).map((g) => {
              const c = challenges.find((x) => x.id === g.challengeId);
              return (
                <section
                  key={g.challengeId ?? "none"}
                  className="followup-group"
                  data-testid="followup-group"
                >
                  <h3>
                    {g.challengeId === null ? (
                      UNATTACHED_TEXT
                    ) : (
                      <>
                        {challengeLink(g.challengeId)}{" "}
                        {c ? (
                          <span className="status-pill">{c.status_label}</span>
                        ) : null}
                      </>
                    )}
                  </h3>
                  <ul className="followup-list">
                    {g.items.map((f) => (
                      <FollowupRow
                        key={f.id}
                        f={f}
                        checked={selected.has(f.id)}
                        onToggle={() => toggle(f.id)}
                        promotedLink={
                          f.promoted_task_id === null && f.promoted_challenge_id
                            ? challengeLink(f.promoted_challenge_id)
                            : null
                        }
                      />
                    ))}
                  </ul>
                </section>
              );
            })
          )}
          {status === "open" ? (
            <FollowupActions
              workspace={agentName}
              ids={chosen}
              onDone={() => {
                setSelected(new Set());
                reload();
              }}
            />
          ) : null}
          {status === "open" && guides.length > 0 ? (
            <div className="followup-guide" data-testid="followup-guide">
              <p>
                課題やタスクに上げるときは、対話の欄で伝えます。ターミナルなら、次のコマンドを実行します。
              </p>
              <ul>
                {guides.map((g) => (
                  <li key={g.id}>
                    <code>{g.command}</code>
                    {g.note ? (
                      <span className="followup-note"> {g.note}</span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function FollowupRow({
  f,
  promotedLink,
  checked,
  onToggle,
}: {
  f: Followup;
  promotedLink: ReactNode;
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <li className="followup-row" data-testid="followup-row">
      {f.status === "open" ? (
        <input
          type="checkbox"
          data-testid="followup-check"
          aria-label={`選ぶ: ${f.title}`}
          checked={checked}
          onChange={onToggle}
        />
      ) : null}
      <div className="followup-title">{f.title}</div>
      <div className="followup-meta">
        <span data-testid="followup-source">
          出どころ: {label(FOLLOWUP_SOURCE_LABELS, f.source)}
        </span>{" "}
        <span data-testid="followup-created">
          できた日時: {new Date(f.created_at).toLocaleString()}
        </span>
      </div>
      {f.detail !== "" ? (
        <p className="followup-detail" data-testid="followup-detail">
          {f.detail}
        </p>
      ) : null}
      {f.status === "done" && f.note ? (
        <p data-testid="followup-note">確かめた結果: {f.note}</p>
      ) : null}
      {f.status === "dropped" && f.reason ? (
        <p data-testid="followup-reason">理由: {f.reason}</p>
      ) : null}
      {f.promoted_task_id ? (
        <p data-testid="followup-promoted-task">
          上げた先のタスク: {f.promoted_task_id}
        </p>
      ) : null}
      {promotedLink ? (
        <p data-testid="followup-promoted">上げた先の課題: {promotedLink}</p>
      ) : null}
    </li>
  );
}

type ActionKind = "done" | "drop";

// 「済みにする」（確かめた結果）・「捨てる」（理由）。文が空のままでは送れない。誤りのときは、選びと文を残して理由を出す。
function FollowupActions({
  workspace,
  ids,
  onDone,
}: {
  workspace: string;
  ids: string[];
  onDone: () => void;
}) {
  const [kind, setKind] = useState<ActionKind | null>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const none = ids.length === 0;
  const open = (k: ActionKind) => {
    if (k !== kind) setText("");
    setKind(k);
    setError(null);
  };
  const submit = async () => {
    if (kind === null) return;
    setBusy(true);
    setError(null);
    try {
      if (kind === "done")
        await api.followupsDone(workspace, { ids, note: text });
      else await api.followupsDrop(workspace, { ids, reason: text });
      setKind(null);
      setText("");
      onDone();
    } catch (e) {
      if (
        e instanceof ApiError &&
        (e.status === 403 || e.code === "forbidden_origin")
      )
        setError(RELOAD_TEXT);
      else setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const fieldLabel = kind === "done" ? "確かめた結果" : "捨てる理由";
  return (
    <div className="followup-actions" data-testid="followup-actions">
      <button
        type="button"
        data-testid="followup-done-button"
        disabled={none || busy}
        onClick={() => open("done")}
      >
        済みにする
      </button>{" "}
      <button
        type="button"
        data-testid="followup-drop-button"
        disabled={none || busy}
        onClick={() => open("drop")}
      >
        捨てる
      </button>
      {kind !== null && !none ? (
        <form
          className="followup-form"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <label>
            {fieldLabel}
            <textarea
              data-testid="followup-text"
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </label>
          <button
            type="submit"
            data-testid="followup-submit"
            disabled={busy || text.trim() === ""}
          >
            {kind === "done" ? "済みにする（送る）" : "捨てる（送る）"}
          </button>
        </form>
      ) : null}
      {error ? (
        <p role="alert" data-testid="followup-error">
          {error}
        </p>
      ) : null}
    </div>
  );
}
