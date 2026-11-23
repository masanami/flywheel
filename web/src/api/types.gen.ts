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
  supervisor_checking: StatusSupervisorChecking;
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
  last_execution_detail: string | null;
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
  supervise: number;
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
  holds?: StatusHold[];
  operations: Operation[];
  slots: Slot[];
  triage: Triage[];
}

export interface BudgetExhausted {
  challenge_id: string;
  impl_remaining_usd: number;
  plan_version: number;
  review_remaining_usd: number;
  task_id: string | null;
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

export interface StatusHold {
  challenge_id: string;
  check: string;
  hold_id: string;
  options: HoldOption[];
  task_title: string;
}

export interface HoldOption {
  button?: boolean | null;
  description: string;
  id: string;
  label: string;
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

export interface StatusSupervisorChecking {
  challenges: Challenge[];
  count: number;
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
  additions: TaskAddition[];
  approvals: Approval[];
  budget: Budget;
  challenge: ShowChallenge;
  conflict_fixes: PassedConflictFix[];
  direct_completions: DirectCompletion[];
  followups: Followup[];
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  release_head_changes?: ReleaseHeadChange[];
  runs: ShowRun[];
  source_binding: SourceBinding | null;
  source_children: SourceChild[];
  stages: Stages;
  supervisor_actions: SupervisorAction[];
  supervisor_hands_taken: Record<string, number>;
  tasks: Task[];
}

export interface TaskAddition {
  created_at: string;
  decided_at: string | null;
  followup_id: string | null;
  id: string;
  limit_usd: number | null;
  origin: string;
  origin_task_id: string | null;
  state: string;
  task_ids: string[];
  task_titles: string[];
  total_after_usd: number | null;
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

export interface Budget {
  estimate_usd: number | null;
  limit_reason: string | null;
  limit_usd: number | null;
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
  integration_ref: string | null;
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

export interface PassedConflictFix {
  at: string;
  files: string[];
  from_ref: string;
  operation_id: string | null;
  run_id: string;
  target: string;
  task_id: string | null;
  to_ref: string;
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
  promoted_task_id: string | null;
  reason: string | null;
  source: string;
  status: string;
  title: string;
  updated_at: string;
}

export interface Hold {
  answer: string | null;
  answer_option: string | null;
  answered_at: string | null;
  answered_by: string | null;
  answered_channel: string | null;
  answered_verification: string | null;
  check?: string | null;
  closed_at: string | null;
  from_status: string;
  from_status_label: string;
  hold_id: string;
  kind: string;
  options: HoldOption[];
  question: string;
  raised_at: string;
  relay_quote: string | null;
  relay_session: string | null;
  replan: boolean;
  run_id: string | null;
  scope: string;
  stop_kind: string | null;
  supervisor_findings: string | null;
  supervisor_recommendation: string | null;
  supervisor_state: string | null;
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

export interface ShowRun {
  challenge_id: string | null;
  cost_source: string | null;
  cost_usd: number | null;
  cycle_budget_usd: number | null;
  cycle_id: string | null;
  ended_at: string | null;
  error: string | null;
  fix_pending_ref: string | null;
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

export interface SupervisorAction {
  basis: SupervisorBasis[];
  challenge_id: string;
  conditions: SupervisorCondition[];
  created_at: string;
  cycle_id: string;
  findings: string;
  hand: string | null;
  hold_id: string | null;
  id: number;
  note: string;
  questions: SupervisorQuestion[];
  reject_reason: string | null;
  result: string;
  run_id: string | null;
  stop_kind: string;
  task_id: string | null;
}

export interface SupervisorBasis {
  locator: string;
  path: string;
}

export interface SupervisorCondition {
  number: number;
  text: string;
}

export interface SupervisorQuestion {
  answer: string;
  basis: SupervisorBasis[];
  question: string;
}

export interface Task {
  added: TaskAdded | null;
  artifacts: TaskArtifact[];
  body: string;
  budget_impl_usd: number;
  budget_review_usd: number;
  delegate: TaskDelegate | null;
  depends_on: string[];
  done_criteria: string;
  facts: Record<string, string>;
  id: string;
  landing: string | null;
  non_goals: string[];
  operation: string;
  plan_version: number;
  repo: string;
  source_key: string | null;
  spent_usd: number;
  status: string;
  title: string;
  verification: TaskVerification | null;
}

export interface TaskAdded {
  added_at: string;
  followup_id: string | null;
  origin: string;
  origin_task_id: string | null;
}

export interface TaskArtifact {
  kind: string;
  ref: string;
  role: string | null;
  state: string;
}

export interface TaskDelegate {
  requested_kind: string | null;
  run_id: string;
  state: string;
  summary: string;
}

export interface TaskVerification {
  at: string;
  reason: string;
  verdict: string;
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
  additions: TaskAddition[];
  approvals: Approval[];
  budget: Budget;
  card: Card;
  challenge: ShowChallenge;
  conflict_fixes: PassedConflictFix[];
  direct_completions: DirectCompletion[];
  followups: Followup[];
  holds: Hold[];
  operations: Operation[];
  pairs: StagePair[];
  plans: PlanDetail[];
  runs: ShowRun[];
  source_binding: SourceBinding | null;
  source_children: SourceChild[];
  stages: Stages;
  supervisor_actions: SupervisorAction[];
  supervisor_hands_taken: Record<string, number>;
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
  progress: string | null;
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
  progress: string | null;
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
  check: string;
  from_status: string;
  hold_id: string;
  options: HoldOption[];
  question: string;
  raised_at: string;
  run_id: string | null;
  scope: string;
  supervisor_findings: string | null;
  supervisor_recommendation: string | null;
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
  added_tasks: AddedTask[];
  challenge_id: string;
  conditions_after_release: string[];
  conditions_after_release_heading: string;
  conflict_fixes: PassedConflictFix[];
  direct_completion: DirectCompletion | null;
  done_criteria: string | null;
  followups: Followup[];
  integration_ref: string | null;
  kind: string;
  mergeability_note: string;
  other_pending_operations: ApprovalOtherOperation[];
  pairs: StagePair[];
  plan: ApprovalPlan | null;
  release_held: boolean;
  releases: ApprovalRelease[];
  stages: Stages;
  supervisor_hands_taken: Record<string, number>;
  supervisor_questions: SupervisorQuestion[];
  tasks: ApprovalCompletionTask[];
  title: string;
  uses_integration: boolean | null;
  version: number;
}

export interface AddedTask {
  challenge_id: string;
  origin: string;
  task_id: string;
  title: string;
}

export interface ApprovalOtherOperation {
  id: string;
  kind: string;
  summary: string;
}

export interface ApprovalPlan {
  body: string;
  budget_limit_reason: string | null;
  budget_limit_usd: number | null;
  budget_usd: number | null;
  summary: string | null;
  tasks: ApprovalPlanTask[];
  topics: PlanTopic[];
  uses_integration: boolean | null;
  version: number;
}

export interface ApprovalPlanTask {
  budget_impl_usd: number | null;
  budget_review_usd: number | null;
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

export interface ApprovalCompletionTask {
  budget_usd: number;
  id: string;
  in_tree: boolean;
  phase: string;
  spent_usd: number;
  title: string;
}

export interface CycleStatus {
  last_end_reason: string | null;
  last_ended_at: string | null;
  last_result: string | null;
  last_started_at: string | null;
  running: CycleRunning | null;
  state: string;
}

export interface CycleRunning {
  active_runs: number;
  budget_usd: number;
  max_parallel_runs: number;
  rounds: number;
  spent_usd: number;
  started_at: string;
  stop_requested: boolean;
  trigger: string;
  watching: CycleWatching[];
}

export interface CycleWatching {
  challenge_id: string;
  run_started_at: string;
  spent_usd: number;
  state: string;
  task_id: string | null;
  title: string;
}

export interface CycleStarted {
  started: boolean;
}

export interface CycleStart {
  reason: string;
  started: boolean;
}

export interface TreeResponse {
  challenges: TreeChallenge[];
  recent_done: TreeChallenge[];
}

export interface FollowupListResponse {
  followups: Followup[];
}

export interface ChatResponse {
  cost_usd: number;
  messages: ChatMessage[];
  session_id: string | null;
  started_at: string | null;
  state: string;
  tools: string;
}

export interface ChatMessage {
  activity: ChatActivity[];
  body: string;
  cost_usd: number;
  created_at: string;
  id: number;
  role: string;
  state: string;
  viewing: ChatViewing | null;
}

export interface ChatActivity {
  command: string;
  ok: boolean;
}

export interface ChatViewing {
  challenge_id: string | null;
  task_id: string | null;
  title: string | null;
}

export interface ChatPostRequest {
  text: string;
  viewing?: ChatPostViewing | null;
}

export interface ChatPostViewing {
  challenge_id?: string;
  task_id?: string;
}

export interface ChatPostResponse {
  id: number;
}

export interface SupervisePhase {
  hands: SuperviseHands;
  not_started: PhaseNotStarted[];
  phase: string;
  processed: number;
  skipped: boolean;
}

export interface SuperviseHands {
  answer: number;
  defer: number;
  escalate: number;
  replan: number;
  retry: number;
  reverify: number;
  send_back: number;
  split_followup: number;
}

export interface PhaseNotStarted {
  challenge_id?: string;
  detail?: string;
  reason: string;
}

export interface HoldAnswerRequest {
  check: string;
  option: string;
}

export interface HoldAnswerResponse {
  cycle: CycleStart;
  hold: Hold;
}

export interface IngestRequest {
  items: string[];
}

export interface IngestAcceptedResponse {
  cycle: CycleStart;
  ingested: IngestedItem[];
}

export interface IngestedItem {
  challenge_id: string;
  children: number;
  external_key: string;
}

export interface CycleStopResponse {
  cycle_id: string | null;
  requested: boolean;
  running: boolean;
}

export interface FollowupDoneRequest {
  ids: string[];
  note: string;
}

export interface FollowupDropRequest {
  ids: string[];
  reason: string;
}

export interface CycleScreen {
  costs: CycleCost[];
  last_end_reason: string | null;
  last_ended_at: string | null;
  last_result: string | null;
  last_started_at: string | null;
  running: CycleRunning | null;
  state: string;
  supervisor_actions: CycleSupervisorAction[];
}

export interface CycleCost {
  cost_usd: number;
  kind: string;
  role: string | null;
}

export interface CycleSupervisorAction {
  at: string;
  challenge_id: string;
  challenge_title: string;
  findings: string;
  hand: string | null;
  id: number;
  reject_reason: string | null;
  result: string;
  task_id: string | null;
  task_title: string | null;
}

export interface NoticesResponse {
  notices: Notice[];
}

export interface Notice {
  added_count: number | null;
  at: string;
  challenge_id: string | null;
  challenge_title: string | null;
  cycle_id: string | null;
  end_reason: string | null;
  kind: string;
  origin: string | null;
  task_id: string | null;
  task_title: string | null;
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

export const TASK_PROGRESS_CODES = [
  "working",
  "verifying",
  "verified",
  "landing",
] as const;

export const CHALLENGE_PROGRESS_CODES = [
  "reflecting",
  "checking",
] as const;

export const TASK_LANDING_CODES = [
  "verified",
  "sent",
  "landed",
] as const;

export const VERDICT_CODES = [
  "met",
  "not_met",
  "uncertain",
] as const;

export const DELEGATE_STATE_CODES = [
  "running",
  "waiting",
  "succeeded",
  "failed",
  "cancelled",
] as const;

export const OPERATION_KIND_CODES = [
  "release",
  "delete",
  "external_send",
  "other",
] as const;

export const OPERATION_STATE_CODES = [
  "pending",
  "approved",
  "rejected",
  "executed",
  "closed",
] as const;

export const REQUESTED_KIND_CODES = [
  "answer",
  "approve",
  "observe",
  "review",
] as const;

export const ARTIFACT_KIND_CODES = [
  "pr",
  "branch",
  "commit",
] as const;

export const CONFLICT_FIX_TARGET_CODES = [
  "integration",
  "release",
] as const;

export const EXPECTED_KIND_CODES = [
  "ui",
  "cli",
  "behavior",
] as const;

export const CYCLE_END_REASON_CODES = [
  "settled",
  "budget",
  "stopped",
  "rate_limited",
  "stalled",
  "error",
  "interrupted",
] as const;

export const NOTICE_KIND_CODES = [
  "task_added",
  "released",
  "cycle_ended",
  "supervisor_escalated",
] as const;

export const WATCH_STATE_CODES = [
  "working",
  "verifying",
  "waiting_owner",
] as const;

export const CYCLE_TRIGGER_CODES = [
  "manual",
  "ui",
  "approve",
  "reject",
  "answer",
  "budget",
  "ingest",
  "followup_promote",
  "continue",
] as const;

export const CYCLE_START_REASON_CODES = [
  "started",
  "already_running",
  "no_work",
  "disabled",
  "unavailable",
] as const;

export const EXECUTION_RESULT_CODES = [
  "merged",
  "already_merged",
  "waiting_checks",
  "upstream_failed",
  "checks_failed",
  "not_mergeable",
  "merge_failed",
  "merge_unconfirmed",
  "head_changed",
  "closed",
  "base_changed",
  "no_approved_head",
  "unsupported",
  "merged_unapproved",
  "limit_reached",
  "interrupted",
  "landed",
  "land_sent",
  "land_unconfirmed",
  "land_refused",
  "content_mismatch",
  "reflect_started",
  "reflect_failed",
  "waiting_delegate",
  "waiting_outside",
  "delegate_ended",
  "waiting_fix_check",
] as const;

export const EXECUTION_RESULT_WORDS = [
  "反映先へ入れた",
  "すでに反映先へ入っていた",
  "チェックの完了を待っている",
  "上流を読めなかった",
  "チェックが失敗した",
  "反映先へ入れられない",
  "反映先へ入れるのに失敗した",
  "反映先へ入ったか分からない",
  "承認した後に中身が変わった",
  "成果物が、入らずに閉じられた",
  "成果物の宛先が、宣言の反映先でない",
  "承認した中身の記録が無い",
  "flywheel が扱えない形だった",
  "承認なしで反映された",
  "失敗が連続の回数の上限に達した",
  "試行が途中で止まった",
  "委譲先が反映先へ入れた",
  "委譲先へ「入れてよい」を返した",
  "「入れてよい」が届いたか分からない",
  "委譲先が入れられなかった",
  "委譲先の中身と、上流の中身が食い違っている",
  "反映の依頼を始めた",
  "反映の依頼を始められなかった",
  "委譲先が進めている",
  "flywheel の外での反映を待っている",
  "委譲先の実行が、入れずに終わった",
  "コンフリクトの解消の確かめを待っている",
] as const;

export const CONDITIONS_AFTER_RELEASE_HEADINGS = [
  "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:",
  "承認で本番反映されると満たされる条件:",
  "承認済みの本番反映が走れば満たされる条件:",
] as const;

export const CHAT_MESSAGE_STATE_CODES = [
  "replying",
  "done",
  "stopped",
  "limit",
  "rate_limited",
  "session_lost",
  "failed",
] as const;

export const CHAT_TOOLS_CODES = [
  "restricted",
  "full",
] as const;

export const CHAT_ROLE_CODES = [
  "owner",
  "orchestrator",
] as const;

export const CHAT_PANEL_STATE_CODES = [
  "idle",
  "replying",
] as const;

export const STOP_KIND_CODES = [
  "verify_uncertain",
  "artifact_missing",
  "delegate_stopped",
  "facts_unmet",
  "rework_limit",
  "checks_failed",
  "judgment_failed",
  "intake_unverified",
  "upstream_unreadable",
  "child_question",
] as const;

export const SUPERVISOR_HAND_CODES = [
  "reverify",
  "send_back",
  "retry",
  "split_followup",
  "replan",
  "defer",
  "escalate",
  "answer",
] as const;

export const SUPERVISOR_RESULT_CODES = [
  "executed",
  "failed",
  "rejected",
] as const;

export const SUPERVISOR_REJECT_REASON_CODES = [
  "state_changed",
  "owner_answered",
] as const;

export const SUPERVISOR_STATE_CODES = [
  "pending",
  "handled",
  "escalated",
] as const;

export const FOLLOWUP_SOURCE_CODES = [
  "learn",
  "supervisor",
] as const;
