import type { ReactNode } from "react";
import { api } from "../api/client.ts";
import type { ChallengeCard, Followup } from "../api/types.gen.ts";
import { guidesForContext } from "../guidance.ts";
import type { LiveState } from "../live/useLive.ts";
import { type Route, hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";

export const FOLLOWUP_STATUSES = ["open", "done", "dropped"] as const;
export type FollowupStatus = (typeof FOLLOWUP_STATUSES)[number];

const STATUS_LABELS: Record<FollowupStatus, string> = {
  open: "未",
  done: "済み",
  dropped: "捨てた",
};
export const NO_OPEN_FOLLOWUPS_TEXT = "未のフォローアップはありません。";
export const UNATTACHED_TEXT = "課題に属さないもの";
export const GUIDANCE_CONTEXT = "followups";
const LOADING = "読み込んでいます";

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
// 書き込みのボタンは置かない（済みにする・捨てる・課題へ上げるは、端末のコマンドの案内だけを出す）。
export function FollowupsScreen({
  live,
  agentName,
  query,
  onNavigate,
}: {
  live: LiveState;
  agentName: string;
  query: Record<string, string> | undefined;
  onNavigate: (r: Route, opts?: { replace?: boolean }) => void;
}) {
  const { result } = useScreenData(live, agentName, () =>
    api.followups(agentName, "all"),
  );
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
      : guidesForContext(GUIDANCE_CONTEXT, agentPath);

  return (
    <section aria-labelledby="screen-title" className="followups">
      <h2 id="screen-title">フォローアップ</h2>
      {result.state === "loading" ? <p>{LOADING}</p> : null}
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
                {STATUS_LABELS[s]}（{count(s)}）
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
                : `${STATUS_LABELS[status]}のフォローアップはありません。`}
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
                        promotedLink={
                          f.promoted_challenge_id
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
          {status === "open" && guides.length > 0 ? (
            <div className="followup-guide" data-testid="followup-guide">
              <p>
                この画面からは、済みにする・捨てる・課題へ上げるは行えません。端末で、次のコマンドを実行します。
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
}: {
  f: Followup;
  promotedLink: ReactNode;
}) {
  return (
    <li className="followup-row" data-testid="followup-row">
      <div className="followup-title">{f.title}</div>
      <div className="followup-meta">
        <span data-testid="followup-source">出どころ: {f.source}</span>{" "}
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
      {promotedLink ? (
        <p data-testid="followup-promoted">上げた先の課題: {promotedLink}</p>
      ) : null}
    </li>
  );
}
