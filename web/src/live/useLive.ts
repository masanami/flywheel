import { useCallback, useEffect, useRef, useState } from "react";
import { api, eventsPath, parseChanged } from "../api/client.ts";
import type {
  CycleStatus,
  ListCardResponse,
  NeedsHumanResponse,
  StatusResponse,
  Workspace,
} from "../api/types.gen.ts";

export interface WorkspaceData {
  status?: StatusResponse;
  challenges?: ListCardResponse;
  cycle?: CycleStatus;
  error?: string;
}

export interface LiveState {
  connected: boolean;
  // 最後に読み込みに成功した時刻。まだ無ければ null。
  lastUpdated: Date | null;
  workspaces: Workspace[];
  data: Record<string, WorkspaceData>;
  needsHuman: NeedsHumanResponse | null;
  loadError: string | null;
  fleetError: string | null;
}

export const RECONNECT_MS = 3000;
// 周のプロセスが落ちたことを再読み込みなしで出すための定期の取り直しの間隔（M4H37）。
export const POLL_MS = 60_000;

const initial: LiveState = {
  connected: false,
  lastUpdated: null,
  workspaces: [],
  data: {},
  needsHuman: null,
  loadError: null,
  fleetError: null,
};

async function loadWorkspace(name: string): Promise<WorkspaceData> {
  try {
    // 列の見出しは補助の表示。cycle だけが失敗しても課題の表示は残し、見出しを出さない。
    const [status, challenges, cycle] = await Promise.all([
      api.status(name),
      api.challenges(name),
      api.cycle(name).catch(() => undefined),
    ]);
    return { status, challenges, cycle };
  } catch (e) {
    return { error: String(e) };
  }
}

// SSE を購読し、通知のたびに必要な分だけ GET し直す。切れたら再接続し、戻ったら全体を読み直す。
export function useLive(): LiveState {
  const [state, setState] = useState<LiveState>(initial);
  // 要求の世代。遅れて返った古い応答が新しい表示を上書きしないよう、最新の世代の応答だけを反映する。
  const gen = useRef({
    list: 0,
    fleet: 0,
    ws: {} as Record<string, number>,
    poll: {} as Record<string, number>,
  });
  const known = useRef<Workspace[]>([]);

  const refreshFleet = useCallback(async () => {
    const token = ++gen.current.fleet;
    // 問いの一覧（GET /api/v1/questions）は要求しない。問いは課題の詳細に出す（M4H22・M4H23）。
    const [nh] = await Promise.allSettled([api.needsHuman()]);
    if (token !== gen.current.fleet) return;
    setState((s) => ({
      ...s,
      needsHuman: nh.status === "fulfilled" ? nh.value : s.needsHuman,
      lastUpdated: nh.status === "fulfilled" ? new Date() : s.lastUpdated,
      fleetError: nh.status === "rejected" ? String(nh.reason) : null,
    }));
  }, []);

  const refreshWorkspace = useCallback(async (name: string) => {
    const token = (gen.current.ws[name] ?? 0) + 1;
    gen.current.ws[name] = token;
    const d = await loadWorkspace(name);
    if (gen.current.ws[name] !== token) return;
    setState((s) => ({
      ...s,
      data: { ...s.data, [name]: d },
      lastUpdated: d.error ? s.lastUpdated : new Date(),
    }));
  }, []);

  // 一覧を取り直す。state が ok のワークスペースの名前を返す（取得に失敗したら null）。
  const refreshList = useCallback(async (): Promise<Workspace[] | null> => {
    const token = ++gen.current.list;
    try {
      const list = await api.workspaces();
      if (token !== gen.current.list) return null;
      known.current = list.workspaces;
      setState((s) => ({
        ...s,
        workspaces: list.workspaces,
        lastUpdated: new Date(),
        loadError: null,
      }));
      return list.workspaces;
    } catch (e) {
      if (token === gen.current.list) {
        setState((s) => ({ ...s, loadError: String(e) }));
      }
      return null;
    }
  }, []);

  // 定期の取り直し。cycle と challenges だけを GET する（status・questions・周の起動はしない）。
  const pollWorkspace = useCallback(async (name: string) => {
    // 全体の読み直し（refreshWorkspace）が必ず優先されるよう、その世代は進めず、途中で進んだら捨てる。
    const wsToken = gen.current.ws[name] ?? 0;
    const token = (gen.current.poll[name] ?? 0) + 1;
    gen.current.poll[name] = token;
    const [challenges, cycle] = await Promise.allSettled([
      api.challenges(name),
      api.cycle(name),
    ]);
    if (
      (gen.current.ws[name] ?? 0) !== wsToken ||
      gen.current.poll[name] !== token
    )
      return;
    setState((s) => {
      const cur = s.data[name];
      if (!cur || cur.error) return s;
      return {
        ...s,
        data: {
          ...s.data,
          [name]: {
            ...cur,
            challenges:
              challenges.status === "fulfilled"
                ? challenges.value
                : cur.challenges,
            // 取得に失敗したら古い状態を残さず、読み込み時と同じく見出しを出さない。
            cycle: cycle.status === "fulfilled" ? cycle.value : undefined,
          },
        },
      };
    });
  }, []);

  const loadAll = useCallback(async () => {
    const list = await refreshList();
    const ok = (list ?? known.current).filter((w) => w.state === "ok");
    await Promise.all([
      refreshFleet(),
      ...ok.map((w) => refreshWorkspace(w.name)),
    ]);
  }, [refreshList, refreshFleet, refreshWorkspace]);

  const loadChanged = useCallback(
    async (name: string) => {
      // 一覧（state）も取り直す。他のワークスペースの status・challenges は要求しない（AC-87）。
      const list = await refreshList();
      const target = (list ?? known.current).find((w) => w.name === name);
      await Promise.all([
        refreshFleet(),
        target && target.state === "ok" ? refreshWorkspace(name) : undefined,
      ]);
    },
    [refreshList, refreshFleet, refreshWorkspace],
  );

  useEffect(() => {
    const poll = setInterval(() => {
      if (document.visibilityState === "hidden") return;
      for (const w of known.current) {
        if (w.state === "ok") void pollWorkspace(w.name);
      }
    }, POLL_MS);
    return () => clearInterval(poll);
  }, [pollWorkspace]);

  useEffect(() => {
    let closed = false;
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const connect = () => {
      if (closed) return;
      const es = new EventSource(eventsPath);
      source = es;
      es.onopen = () => {
        setState((s) => ({ ...s, connected: true }));
        void loadAll(); // 初回も再接続後も全体を読む（AC-91）
      };
      es.onmessage = (ev: MessageEvent<string>) => {
        const changed = parseChanged(ev.data);
        if (changed) void loadChanged(changed.workspace);
      };
      es.onerror = () => {
        es.close();
        if (source === es) source = null;
        setState((s) => ({ ...s, connected: false }));
        if (!closed) timer = setTimeout(connect, RECONNECT_MS);
      };
    };
    connect();

    return () => {
      closed = true;
      if (timer) clearTimeout(timer);
      source?.close();
    };
  }, [loadAll, loadChanged]);

  return state;
}
