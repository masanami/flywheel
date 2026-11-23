import { vi } from "vitest";

export class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  url: string;
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
  open() {
    this.onopen?.();
  }
  send(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
  fail() {
    this.onerror?.();
  }
  static get latest(): FakeEventSource {
    const e = FakeEventSource.instances.at(-1);
    if (!e) throw new Error("EventSource がまだ作られていない");
    return e;
  }
}

export const emptyStatus = {
  actionable: { challenges: [] },
  approved: { operations: [] },
  binary_commit: "unknown",
  cycle_work: {
    stages: {
      classify: 0,
      cleanup: 0,
      improve: 0,
      learn: 0,
      plan: 0,
      release: 0,
      run: 0,
      supervise: 0,
      verify: 0,
    },
    total: 0,
  },
  followups: { open: 0 },
  ingestable: null,
  needs_human: {
    budget_exhausted: [],
    challenges: [],
    discrepancies: [],
    operations: [],
    slots: [],
    triage: [],
  },
  supervisor_checking: { challenges: [], count: 0 },
  waiting_external: { challenges: [] },
};

export const emptyNotices = { notices: [] };

export function fixtures(): Record<string, unknown> {
  const ws = (name: string) => ({
    error: null,
    name,
    path: `/w/${name}`,
    state: "ok",
  });
  return {
    "/api/v1/workspaces": { workspaces: [ws("alpha"), ws("beta")] },
    "/api/v1/workspaces/alpha/status": emptyStatus,
    "/api/v1/workspaces/alpha/challenges": { challenges: [] },
    "/api/v1/workspaces/alpha/cycle": {
      costs: [],
      last_ended_at: "2026-10-17T01:00:00Z",
      last_result: "completed",
      last_started_at: "2026-10-17T00:50:00Z",
      state: "running",
      supervisor_actions: [],
    },
    "/api/v1/workspaces/beta/cycle": {
      costs: [],
      last_ended_at: null,
      last_result: null,
      last_started_at: null,
      state: "idle",
      supervisor_actions: [],
    },
    // 起きたことの知らせ（区切り S4d-2）。既定は 0 件。
    "/api/v1/workspaces/alpha/notices": emptyNotices,
    "/api/v1/workspaces/beta/notices": emptyNotices,
    "/api/v1/workspaces/beta/status": emptyStatus,
    "/api/v1/workspaces/beta/challenges": { challenges: [] },
    "/api/v1/needs-human": { unreadable_workspaces: [], workspaces: [] },
  };
}

export interface Recorded {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: string | null;
}

// bodies のキーが "POST <url>" なら POST の応答。値が関数なら呼び出しごとに Response を作る
// （失敗や版の違いを順に返すため）。それ以外の値は 200 の JSON。
export type FakeResponder = (rec: Recorded) => Response;

// fetch と EventSource を偽物に差し替え、発行した要求を記録する。
export function installFakes(bodies: Record<string, unknown> = fixtures()) {
  const requests: Recorded[] = [];
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | Request, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const method = (
        init?.method ?? (input instanceof Request ? input.method : "GET")
      ).toUpperCase();
      const rec: Recorded = {
        url,
        method,
        headers: Object.fromEntries(
          Object.entries((init?.headers ?? {}) as Record<string, string>),
        ),
        body: typeof init?.body === "string" ? init.body : null,
      };
      requests.push(rec);
      const body = bodies[method === "GET" ? url : `${method} ${url}`];
      if (typeof body === "function") return (body as FakeResponder)(rec);
      if (body === undefined) return new Response("{}", { status: 404 });
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
  return { requests };
}
