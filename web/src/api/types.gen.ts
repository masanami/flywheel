// このファイルは生成物。編集しない。`go generate ./internal/view` で書き出す。
// 正本は internal/view の構造体（json タグ）。

export interface StatusResponse {
  actionable: StatusActionable;
  approved: StatusApproved;
  needs_human: StatusNeedsHuman;
  waiting_external: StatusWaitingExternal;
}

export interface StatusActionable {
  challenges: Challenge[];
}

export interface Challenge {
  created_at: string;
  description: string;
  done_criteria: string;
  id: string;
  priority: string | null;
  reporter: string;
  status: string;
  status_label: string;
  title: string;
  updated_at: string;
  urgency: string | null;
  version: number;
}

export interface StatusApproved {
  operations: Operation[];
}

export interface Operation {
  challenge_id: string;
  created_at: string;
  id: string;
  kind: string;
  ref: string | null;
  state: string;
  summary: string;
  version: number;
}

export interface StatusNeedsHuman {
  budget_exhausted: BudgetExhausted[];
  challenges: Challenge[];
  discrepancies: Discrepancy[];
  operations: Operation[];
  slots: Slot[];
  triage: Triage[];
}

export interface BudgetExhausted {
  challenge_id: string;
  impl_remaining_usd: number;
  plan_version: number;
  review_remaining_usd: number;
}

export interface Discrepancy {
  challenge_id: string;
  kinds: string[];
}

export interface Slot {
  path: string;
  repo: string;
  run_id: string | null;
  slot_id: string;
}

export interface Triage {
  challenge_id: string;
  reason: string;
  run_id: string;
}

export interface StatusWaitingExternal {
  challenges: WaitingExternal[];
}

export interface WaitingExternal {
  challenge_id: string;
  checks: string;
  pr_url: string;
}

export interface ListResponse {
  challenges: Challenge[];
}

export interface ShowResponse {
  approvals: Approval[];
  challenge: Challenge;
  holds: Hold[];
  operations: Operation[];
  plans: PlanDetail[];
  runs: Run[];
  source_binding: SourceBinding | null;
}

export interface Approval {
  actor: string;
  channel: string;
  decided_at: string;
  decision: string;
  kind: string;
  operation_id: string | null;
  reason: string | null;
  target_version: number;
  verification: string;
}

export interface Hold {
  answer: string | null;
  answered_at: string | null;
  answered_by: string | null;
  from_status: string;
  from_status_label: string;
  question: string;
  raised_at: string;
}

export interface PlanDetail {
  body: string;
  created_at: string;
  spec: unknown;
  version: number;
}

export interface Run {
  challenge_id: string | null;
  cost_source: string | null;
  cost_usd: number | null;
  cycle_budget_usd: number | null;
  cycle_id: string | null;
  ended_at: string | null;
  id: string;
  judgment: string | null;
  kind: string;
  max_budget_usd: number;
  rate_limited: boolean;
  result: string | null;
  session_id: string | null;
  started_at: string;
}

export interface SourceBinding {
  comments_count: number;
  created_at: string;
  external_key: string;
  fingerprint: string;
  policy_state: string;
  read_comments_count: number;
  read_upstream_updated_at: string | null;
  source_id: string;
  updated_at: string;
  upstream_state: string;
  upstream_updated_at: string | null;
  url: string;
}

export interface LogResponse {
  activities: Activity[];
}

export interface Activity {
  action: string;
  actor: string;
  after: unknown;
  at: string;
  before: unknown;
  channel: string;
  entity: string;
  entity_id: string;
  verification: string;
}

export interface RunsResponse {
  runs: Run[];
}
