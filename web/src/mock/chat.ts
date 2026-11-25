// 対話の欄の偽の口（エージェントごと。ブラウザ内だけ）。断り方（404・409・400 の順）は仕様の口の形に合わせる。
// 送った文に MOCK_PERMISSION_WORD か MOCK_QUESTION_WORD があると、返事は確認の答えを待つ。無ければすぐ終わる。

import type { ChatApi } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type {
  ChallengeChatSession,
  ChatMessage,
  ChatPrompt,
  ChatPromptAnswerRequest,
  ChatSessionChallenge,
  ChatTranscriptEntry,
} from "../api/types.gen.ts";
import type { MockContext } from "./context.ts";

export const MOCK_PERMISSION_WORD = "許可の確認";
export const MOCK_QUESTION_WORD = "選択肢の問い";
export const MOCK_PERMISSION_COMMAND = `gh issue create --title "通知の再送を直す" --body "${"再送の間隔と回数の上限を決める。".repeat(20)}末尾の印"`;
export const MOCK_QUESTION_TEXT = "仕様はどこまで含めますか？";

const REPLY_COST = 0.12;

// 紐づいた課題と、その課題の仕様づくりに数えた額。
type MockLink = ChatSessionChallenge & { cost_usd: number };

interface MockSession {
  id: string;
  started_at: string;
  closed_at: string | null;
  messages: ChatMessage[];
  links: MockLink[];
  transcript_available: boolean;
}

const message = (
  id: number,
  at: string,
  o: Partial<ChatMessage>,
): ChatMessage => ({
  activity: [],
  body: "",
  cost_usd: 0,
  created_at: at,
  id,
  prompts: [],
  role: "orchestrator",
  state: "done",
  viewing: null,
  ...o,
});

function closedSession(
  id: string,
  at: string,
  firstId: number,
  first: string,
  o: Pick<MockSession, "links" | "transcript_available">,
): MockSession {
  return {
    id,
    started_at: at,
    closed_at: at,
    messages: [
      message(firstId, at, { role: "owner", body: first }),
      message(firstId + 1, at, {
        body: "下書きを作りました。",
        cost_usd: 0.42,
      }),
    ],
    ...o,
  };
}

// alpha には、閉じた会話を 2 つ持たせる（全文が残っているものと、無くなったもの）。古い順に持つ。
const seedSessions = (name: string): MockSession[] =>
  name === "alpha"
    ? [
        closedSession(
          "mock-alpha-gone",
          "2026-10-01T09:00:00.000Z",
          1,
          "古い相談の続き",
          { links: [], transcript_available: false },
        ),
        closedSession(
          "mock-alpha-spec",
          "2026-11-24T09:00:00.000Z",
          3,
          "通知メールの再送の仕様を作りたい",
          {
            links: [
              {
                challenge_id: "C-3",
                title: "通知メールの再送制御",
                cost_usd: 0.42,
              },
            ],
            transcript_available: true,
          },
        ),
      ]
    : [];

const costOf = (s: MockSession) =>
  s.messages.reduce((sum, m) => sum + m.cost_usd, 0);
const isReplying = (s: MockSession | undefined) =>
  s?.messages.some((m) => m.state === "replying") ?? false;

const permissionPrompt = (id: number, at: string): ChatPrompt => ({
  answer: null,
  answered_at: null,
  created_at: at,
  id,
  kind: "permission",
  questions: [],
  state: "pending",
  target: MOCK_PERMISSION_COMMAND,
  tool: "Bash",
});

const questionPrompt = (id: number, at: string): ChatPrompt => ({
  ...permissionPrompt(id, at),
  kind: "question",
  questions: [
    {
      multi_select: false,
      options: [
        { label: "再送だけ", description: "いまの不具合を直す" },
        { label: "通知の全体", description: "作り直す" },
      ],
      question: MOCK_QUESTION_TEXT,
    },
  ],
  target: null,
  tool: "AskUserQuestion",
});

// 答えが確認の種類に合うか（問いは、すべての問いに空でない答えがあり、ほかのキーが無い）。
function fits(p: ChatPrompt, a: ChatPromptAnswerRequest): boolean {
  if (p.kind === "permission") {
    if (a.answers !== undefined) return false;
    return a.decision === "deny" || (a.decision === "allow" && !a.message);
  }
  if (a.decision !== undefined || a.message !== undefined) return false;
  const answers = a.answers ?? {};
  return (
    Object.keys(answers).length === p.questions.length &&
    p.questions.every((q) => (answers[q.question] ?? "").trim() !== "")
  );
}

function transcriptOf(s: MockSession): ChatTranscriptEntry[] {
  const entry = (
    kind: string,
    text: string,
    at: string,
    tool: string | null = null,
  ): ChatTranscriptEntry => ({ at, kind, text, tool, truncated: false });
  return s.messages.flatMap((m) =>
    m.role === "owner"
      ? [entry("owner", m.body, m.created_at)]
      : [
          ...m.activity.flatMap((a) => [
            entry("tool_use", a.target, m.created_at, a.tool),
            entry("tool_result", "（偽の結果）", m.created_at, a.tool),
          ]),
          entry("assistant", m.body, m.created_at),
        ],
  );
}

export function chatMock(ctx: MockContext) {
  const agents: Record<string, MockSession[]> = {};
  let nextId = 100;

  // 古い順の会話の並び。
  const sessionsOf = (name: string): MockSession[] => {
    ctx.wsOf(name);
    agents[name] ??= seedSessions(name);
    return agents[name];
  };
  const openOf = (name: string) =>
    sessionsOf(name).find((s) => s.closed_at === null);
  const sessionOf = (name: string, id: string): MockSession => {
    const s = sessionsOf(name).find((x) => x.id === id);
    if (!s) throw new ApiError("not_found", "その会話はありません", 404);
    return s;
  };
  const conflict = (text: string) => new ApiError("conflict", text, 409);

  const api: ChatApi = {
    async chat(name) {
      await ctx.wait();
      const s = openOf(name);
      return structuredClone({
        cost_usd: s ? costOf(s) : 0,
        messages: s?.messages ?? [],
        session_id: s?.id ?? null,
        started_at: s?.started_at ?? null,
        state: isReplying(s) ? "replying" : "idle",
        tools: "restricted",
      });
    },
    async chatSend(name, req) {
      await ctx.wait();
      let s = openOf(name);
      if (isReplying(s)) throw conflict("返事が進行中です");
      const now = new Date().toISOString();
      if (!s) {
        s = {
          id: `mock-${name}-${++nextId}`,
          started_at: now,
          closed_at: null,
          messages: [],
          links: [],
          transcript_available: true,
        };
        sessionsOf(name).push(s);
      }
      const id = ++nextId;
      const replyId = ++nextId;
      const prompt = req.text.includes(MOCK_PERMISSION_WORD)
        ? permissionPrompt(++nextId, now)
        : req.text.includes(MOCK_QUESTION_WORD)
          ? questionPrompt(++nextId, now)
          : null;
      const tool = {
        id: `mock-tool-${replyId}`,
        tool: prompt?.tool ?? "Bash",
        target: "flywheel status",
        state: prompt ? "running" : "ok",
      };
      if (prompt) {
        tool.target =
          prompt.kind === "permission"
            ? MOCK_PERMISSION_COMMAND.slice(0, 200)
            : "";
      }
      s.messages.push(
        message(id, now, { role: "owner", body: req.text }),
        message(replyId, now, {
          activity: [tool],
          body: prompt
            ? "確認させてください。"
            : "# 確認しました\n\n- 状況を見ました",
          cost_usd: prompt ? 0 : REPLY_COST,
          prompts: prompt ? [prompt] : [],
          state: prompt ? "replying" : "done",
        }),
      );
      return { id };
    },
    async chatStop(name) {
      await ctx.wait();
      for (const m of openOf(name)?.messages ?? []) {
        if (m.state !== "replying") continue;
        m.state = "stopped";
        for (const a of m.activity) {
          if (a.state === "running") a.state = "error";
        }
        for (const p of m.prompts) {
          if (p.state === "pending") p.state = "expired";
        }
      }
      return {};
    },
    async chatNew(name) {
      await ctx.wait();
      const s = openOf(name);
      if (isReplying(s)) throw conflict("返事が進行中です");
      if (s) s.closed_at = new Date().toISOString();
      return {};
    },
    async chatAnswer(name, promptId, req) {
      await ctx.wait();
      const found = sessionsOf(name)
        .flatMap((s) => s.messages)
        .flatMap((m) => m.prompts.map((p) => ({ m, p })))
        .find((x) => x.p.id === promptId);
      if (!found) {
        throw new ApiError("not_found", "その確認はありません", 404);
      }
      const { m, p } = found;
      if (p.state !== "pending") throw conflict("答えを待っていません");
      if (!fits(p, req)) {
        throw new ApiError("validation_failed", "答えの形が違います", 400);
      }
      p.state =
        p.kind === "question"
          ? "answered"
          : req.decision === "allow"
            ? "allowed"
            : "denied";
      p.answer = {
        decision: req.decision ?? null,
        message: req.message ?? null,
        answers: req.answers ?? null,
      };
      p.answered_at = new Date().toISOString();
      for (const a of m.activity) {
        if (a.state === "running") {
          a.state = p.state === "denied" ? "error" : "ok";
        }
      }
      m.state = "done";
      m.cost_usd = REPLY_COST;
      return {};
    },
    async chatSessions(name) {
      await ctx.wait();
      return {
        sessions: [...sessionsOf(name)]
          .sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
          .map((s) => ({
            challenges: s.links.map(({ challenge_id, title }) => ({
              challenge_id,
              title,
            })),
            closed_at: s.closed_at,
            cost_usd: costOf(s),
            first_message: s.messages[0]?.body.slice(0, 80) ?? "",
            id: s.id,
            open: s.closed_at === null,
            started_at: s.started_at,
            transcript_available: s.transcript_available,
          })),
      };
    },
    async chatResume(name, sessionId) {
      await ctx.wait();
      const s = sessionOf(name, sessionId);
      if (s.closed_at === null) return {};
      const current = openOf(name);
      if (isReplying(current)) throw conflict("返事が進行中です");
      if (!s.transcript_available) {
        throw new ApiError("transcript_unavailable", "全文がありません", 409);
      }
      if (current) current.closed_at = new Date().toISOString();
      s.closed_at = null;
      return {};
    },
    async chatTranscript(name, sessionId) {
      await ctx.wait();
      const s = sessionOf(name, sessionId);
      if (!s.transcript_available) {
        throw new ApiError("transcript_unavailable", "全文がありません", 404);
      }
      return { entries: transcriptOf(s) };
    },
  };

  // 課題の詳細に足す値（紐づいた会話と、仕様づくりの費用）。
  const challengeChat = (name: string, challengeId: string) => {
    const linked: ChallengeChatSession[] = sessionsOf(name).flatMap((s) =>
      s.links
        .filter((l) => l.challenge_id === challengeId)
        .map((l) => ({
          cost_usd: l.cost_usd,
          first_message: s.messages[0]?.body.slice(0, 80) ?? "",
          id: s.id,
          started_at: s.started_at,
        })),
    );
    return {
      chat_sessions: linked,
      spec_cost_usd: linked.reduce((sum, s) => sum + s.cost_usd, 0),
    };
  };

  return { api, challengeChat };
}
