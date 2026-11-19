// このファイルは生成物。編集しない。`go generate ./internal/view` で書き出す。
// 正本は internal/view の構造体（json タグ）と、view.ClosedSets（core の閉集合の値と、view の承認の見出し）。

export interface StatusResponse {
  actionable: StatusActionable;
  approved: StatusApproved;
  binary_commit: string;
  cycle_work: CycleWork;
  followups: StatusFollowups;
  ingestable: Ingestable | null;
  needs_human: StatusNeedsHuman;
  parent_agent: StatusParentAgent;
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
  closed_at: string | null;
  closed_reason: string | null;
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
  superseded_by: string | null;
  supersedes: string | null;
  version: number;
  waiting_since: string | null;
}

export interface CycleWork {
  stages: CycleWorkStages;
  total: number;
}

export interface CycleWorkStages {
  classify: number;
  cleanup: number;
  improve: number;
  learn: number;
  plan: number;
  release: number;
  run: number;
  verify: number;
}

export interface StatusFollowups {
  open: number;
}

export interface Ingestable {
  error: string | null;
  items: IngestableItem[];
}

export interface IngestableItem {
  children: IngestableChild[];
  external_key: string;
  number: number;
  parent_challenge_id: string | null;
  parent_external_key: string | null;
  repo: string;
  title: string;
  url: string;
}

export interface IngestableChild {
  external_key: string;
  number: number;
  parent_external_key: string;
  title: string;
  url: string;
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

export interface StatusParentAgent {
  challenges: Challenge[];
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
  direct_completions: DirectCompletion[];
  followups: Followup[];
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  release_head_changes?: ReleaseHeadChange[];
  runs: Run[];
  source_binding: SourceBinding | null;
  source_children: SourceChild[];
  stages: Stages;
  tasks: Task[];
}

export interface Approval {
  actor: string;
  channel: string;
  decided_at: string;
  decision: string;
  id: string;
  kind: string;
  merge_in_verified_head: string | null;
  operation_id: string | null;
  reason: string | null;
  rejected_fields: string[] | null;
  relay_quote: string | null;
  relay_session: string | null;
  superseded_at: string | null;
  superseded_by: string | null;
  superseded_reason: string | null;
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
  followup_of: string | null;
  headline: string | null;
  id: string;
  improvement: ImprovementKey | null;
  priority: string | null;
  reporter: string;
  status: string;
  status_label: string;
  title: string;
  updated_at: string;
  urgency: string | null;
  uses_integration: boolean | null;
  version: number;
}

export interface Dependency {
  challenge_id: string;
  policy_state: string | null;
  satisfied: boolean;
  status: string;
  upstream_state: string | null;
}

export interface ImprovementKey {
  layer: string;
  target: string;
}

export interface DirectCompletion {
  artifacts: string[];
  created_at: string;
  note: string | null;
}

export interface Followup {
  challenge_id: string | null;
  created_at: string;
  detail: string;
  id: string;
  note: string | null;
  promoted_challenge_id: string | null;
  reason: string | null;
  source: string;
  status: string;
  title: string;
  updated_at: string;
}

export interface Hold {
  answer: string | null;
  answered_at: string | null;
  answered_by: string | null;
  answered_channel: string | null;
  answered_verification: string | null;
  closed_at: string | null;
  from_status: string;
  from_status_label: string;
  kind: string;
  question: string;
  raised_at: string;
  relay_quote: string | null;
  relay_session: string | null;
  replan: boolean;
  run_id: string | null;
  task_id: string | null;
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

export interface ReleaseHeadChange {
  head_change: HeadChange;
  operation_id: string;
}

export interface HeadChange {
  checks_passed: boolean;
  commits: HeadChangeCommit[];
  conflict_fixes: ConflictFix[];
  detail: string;
  head: string;
  kind: string;
  verified_head: string;
}

export interface HeadChangeCommit {
  role: string;
  sha: string;
  subject: string;
}

export interface ConflictFix {
  files: ConflictFixFile[];
  merge_commit: string;
}

export interface ConflictFixFile {
  filename: string;
  patch: string | null;
  patch_unavailable: boolean;
}

export interface Run {
  challenge_id: string | null;
  cost_source: string | null;
  cost_usd: number | null;
  cycle_budget_usd: number | null;
  cycle_id: string | null;
  ended_at: string | null;
  error: string | null;
  flywheel_version: string | null;
  id: string;
  judgment: string | null;
  kind: string;
  max_budget_usd: number;
  rate_limited: boolean;
  result: string | null;
  session_id: string | null;
  started_at: string;
  task_id: string | null;
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

export interface SourceChild {
  external_key: string;
  parent_external_key: string;
  title: string;
  upstream_state: string;
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

export interface Task {
  body: string;
  depends_on: string[];
  done_criteria: string;
  id: string;
  non_goals: string[];
  operation: string;
  plan_version: number;
  repo: string;
  source_key: string | null;
  status: string;
  title: string;
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
  direct_completions: DirectCompletion[];
  followups: Followup[];
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  runs: Run[];
  source_binding: SourceBinding | null;
  source_children: SourceChild[];
  stages: Stages;
  tasks: Task[];
  tree: TreeChallenge;
  waiting: Waiting;
}

export interface TreeChallenge {
  budget_usd: number | null;
  children: TreeRelative[];
  cost_usd: number;
  dependencies: TreeDependency[];
  followups_open: number;
  id: string;
  next_human_actions: string[];
  parent: TreeRelative | null;
  status: string;
  status_label: string;
  tasks: TreeTask[];
  tasks_done: number;
  tasks_total: number;
  title: string;
  updated_at: string;
  uses_integration: boolean | null;
}

export interface TreeRelative {
  challenge_id: string;
  status: string;
  status_label: string;
  title: string;
}

export interface TreeDependency {
  challenge_id: string;
  satisfied: boolean;
  status: string;
  status_label: string;
  title: string;
}

export interface TreeTask {
  cost_usd: number;
  depends_on: string[];
  id: string | null;
  key: string;
  phase: string;
  status: string | null;
  title: string;
}

export interface Waiting {
  prerequisites: WaitingChallenge[];
}

export interface WaitingChallenge {
  challenge_id: string;
  status: string;
  title: string;
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
  task_id: string | null;
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
  conditions_after_release: string[];
  conditions_after_release_heading: string;
  direct_completion: DirectCompletion | null;
  done_criteria: string | null;
  kind: string;
  mergeability_note: string;
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
  budget_usd: number | null;
  summary: string | null;
  tasks: ApprovalPlanTask[];
  topics: PlanTopic[];
  uses_integration: boolean | null;
  version: number;
}

export interface ApprovalPlanTask {
  depends_on: string[];
  key: string;
  phase: string;
  source: string | null;
  title: string;
}

export interface PlanTopic {
  decision: string;
  questions: PlanQuestion[];
  title: string;
}

export interface PlanQuestion {
  answer: PlanAnswer | null;
  number: number;
  options: StageOption[];
  question: string;
  recommended: number;
}

export interface PlanAnswer {
  choice: number | null;
  label: string | null;
  recommended: boolean;
  text: string | null;
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

export interface TreeResponse {
  challenges: TreeChallenge[];
  recent_done: TreeChallenge[];
}

export interface FollowupListResponse {
  followups: Followup[];
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
  "withdrawn",
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

export const HOLD_KIND_CODES = [
  "human",
  "parent_direct",
] as const;

export const TASK_PHASE_CODES = [
  "running",
  "waiting",
  "next",
  "blocked",
  "done",
  "cancelled",
] as const;

export const CONDITIONS_AFTER_RELEASE_HEADINGS = [
  "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:",
  "承認で本番反映されると満たされる条件:",
  "承認済みの本番反映が走れば満たされる条件:",
] as const;
