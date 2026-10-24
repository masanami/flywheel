// このファイルは生成物。編集しない。`go generate ./internal/view` で書き出す。
// 正本は internal/view の構造体（json タグ）と、view.ClosedSets（core の閉集合の値）。

export interface StatusResponse {
  actionable: StatusActionable;
  approved: StatusApproved;
  needs_human: StatusNeedsHuman;
  waiting_external: StatusWaitingExternal;
}

export interface StatusActionable {
  challenges: ActionableChallenge[];
}

export interface ActionableChallenge {
  context: string | null;
  created_at: string;
  description: string;
  done_criteria: string;
  headline: string | null;
  id: string;
  priority: string | null;
  reporter: string;
  status: string;
  status_label: string;
  title: string;
  unmet_dependencies: UnmetDependency[];
  updated_at: string;
  urgency: string | null;
  version: number;
}

export interface UnmetDependency {
  challenge_id: string;
  policy_state: string | null;
  status: string;
  upstream_state: string | null;
}

export interface StatusApproved {
  operations: Operation[];
}

export interface Operation {
  approved_head: string | null;
  challenge_id: string;
  consecutive_failures: number;
  created_at: string;
  executed_at: string | null;
  id: string;
  kind: string;
  last_execution_result: string | null;
  merge_commit: string | null;
  ref: string | null;
  state: string;
  summary: string;
  version: number;
  waiting_since: string | null;
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

export interface Challenge {
  context: string | null;
  created_at: string;
  description: string;
  done_criteria: string;
  headline: string | null;
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

export interface Discrepancy {
  challenge_id: string;
  challenge_ids?: string[];
  cycle_only?: boolean;
  kinds: string[];
  operation_id?: string;
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
  challenge: ShowChallenge;
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  runs: Run[];
  source_binding: SourceBinding | null;
  stages: Stages;
}

export interface Approval {
  actor: string;
  channel: string;
  decided_at: string;
  decision: string;
  kind: string;
  operation_id: string | null;
  reason: string | null;
  rejected_fields: string[] | null;
  relay_quote: string | null;
  relay_session: string | null;
  target_version: number;
  verification: string;
}

export interface ShowChallenge {
  context: string | null;
  created_at: string;
  dependencies: Dependency[];
  dependents: string[];
  description: string;
  done_criteria: string;
  headline: string | null;
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

export interface Dependency {
  challenge_id: string;
  policy_state: string | null;
  satisfied: boolean;
  status: string;
  upstream_state: string | null;
}

export interface Hold {
  answer: string | null;
  answered_at: string | null;
  answered_by: string | null;
  answered_channel: string | null;
  answered_verification: string | null;
  from_status: string;
  from_status_label: string;
  question: string;
  raised_at: string;
  relay_quote: string | null;
  relay_session: string | null;
  run_id: string | null;
}

export interface StagePair {
  actual: StageActual | null;
  expected: StageExpected | null;
}

export interface StageActual {
  after: string;
  scene: string;
}

export interface StageExpected {
  after: string;
  before: string;
  kind: string;
  scene: string;
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

export interface Stages {
  actual: StageActual[] | null;
  expected: StageExpected[] | null;
  figure: string | null;
  issues: StageIssue[] | null;
  issues_answer: string | null;
  non_goals: string[] | null;
}

export interface StageIssue {
  options: StageOption[];
  question: string;
  recommended: number;
}

export interface StageOption {
  example: string | null;
  label: string;
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

export interface WorkspaceList {
  workspaces: Workspace[];
}

export interface Workspace {
  error: string | null;
  name: string;
  path: string;
  state: string;
}

export interface WorkspaceChanged {
  type: string;
  workspace: string;
}

export interface ListCardResponse {
  challenges: ChallengeCard[];
}

export interface ChallengeCard {
  card: Card;
  context: string | null;
  created_at: string;
  description: string;
  done_criteria: string;
  headline: string | null;
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

export interface Card {
  context: string | null;
  headline: string | null;
  modifiers: string[];
  next_human_actions: string[];
  slot_ids: string[];
  state: string;
}

export interface ShowCardResponse {
  approvals: Approval[];
  card: Card;
  challenge: ShowChallenge;
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  runs: Run[];
  source_binding: SourceBinding | null;
  stages: Stages;
}

export interface NeedsHumanResponse {
  unreadable_workspaces: string[];
  workspaces: WorkspaceNeedsHuman[];
}

export interface WorkspaceNeedsHuman {
  name: string;
  needs_human: StatusNeedsHuman;
}

export interface QuestionsResponse {
  questions: Question[];
  unreadable_workspaces: string[];
}

export interface Question {
  challenge_id: string;
  from_status: string;
  question: string;
  raised_at: string;
  run_id: string | null;
  title: string;
  workspace: string;
}

export interface RunReportResponse {
  report: RunReport;
}

export interface RunReport {
  assumptions: string[];
  run_id: string;
  unverified: string[];
}

export interface ApprovalSummaryResponse {
  approval: ApprovalSummary;
}

export interface ApprovalSummary {
  challenge_id: string;
  done_criteria: string | null;
  kind: string;
  other_pending_operations: ApprovalOtherOperation[];
  pairs: StagePair[];
  plan: ApprovalPlan | null;
  release_held: boolean;
  releases: ApprovalRelease[];
  stages: Stages;
  title: string;
  version: number;
}

export interface ApprovalOtherOperation {
  id: string;
  kind: string;
  summary: string;
}

export interface ApprovalPlan {
  body: string;
  version: number;
}

export interface ApprovalRelease {
  head_sha: string | null;
  id: string;
  ref: string | null;
  summary: string;
}

export interface CycleStatus {
  last_ended_at: string | null;
  last_result: string | null;
  last_started_at: string | null;
  state: string;
}

export interface CycleStarted {
  started: boolean;
}

export const CARD_ACTION_CODES = [
  "approve_plan",
  "approve_completion",
  "answer_question",
  "triage",
  "increase_budget",
  "approve_operation",
  "review_discrepancy",
  "clear_slot",
] as const;

export const CARD_MODIFIER_CODES = [
  "running",
  "unresponsive",
  "waiting_ci",
  "budget_exhausted",
  "discrepancy",
] as const;

export const STATUS_CODES = [
  "unclassified",
  "classified",
  "awaiting_plan_approval",
  "in_progress",
  "verifying",
  "awaiting_completion_approval",
  "done",
  "awaiting_human",
] as const;

export const DISCREPANCY_KIND_CODES = [
  "upstream_closed",
  "upstream_missing",
  "out_of_policy",
  "upstream_commented",
  "upstream_updated",
  "merged_unapproved",
  "dependency_cycle",
] as const;
