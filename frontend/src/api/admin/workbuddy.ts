import { workBuddyPath } from '@/utils/apiRolePath'
/**
 * Admin WorkBuddy API endpoints
 *
 * WorkBuddy 是国内站与国际站两套独立认证的 AI 编程助手。它的登录方式与
 * Antigravity / Grok 不同：授权在 WorkBuddy 自己的页面完成，服务端只能
 * 通过 state 轮询结果，因此没有 exchange-code，取而代之的是 poll。
 *
 * 流程：
 *   1. generateAuthUrl 拿到 auth_url 与 state
 *   2. 打开 auth_url，用户完成登录
 *   3. 反复调用 poll，直到 pending 为 false
 */

import { apiClient } from '../client'

/** WorkBuddy 平台标识。 */
export type WorkBuddyPlatform = 'workbuddy_cn' | 'workbuddy_intl'

export interface WorkBuddyAuthUrlRequest {
  platform: WorkBuddyPlatform
}

export interface WorkBuddyAuthUrlResponse {
  auth_url: string
  state: string
  platform: WorkBuddyPlatform
  expires_at: number
}

export interface WorkBuddyPollRequest {
  platform: WorkBuddyPlatform
  state: string
}

export interface WorkBuddyPollResponse {
  /** 为 true 表示用户尚未完成授权，应继续轮询。 */
  pending: boolean
  access_token?: string
  refresh_token?: string
  expires_at?: number
  refresh_expires_at?: number
  domain?: string
  token_type?: string
  uid?: string
  enterprise_id?: string
  account_name?: string
  platform: WorkBuddyPlatform
}

export interface WorkBuddyRefreshRequest {
  platform: WorkBuddyPlatform
  refresh_token: string
}

export interface WorkBuddyRefreshResponse {
  access_token: string
  refresh_token?: string
  expires_at?: number
  refresh_expires_at?: number
  domain?: string
  token_type?: string
}

export async function generateAuthUrl(
  payload: WorkBuddyAuthUrlRequest
): Promise<WorkBuddyAuthUrlResponse> {
  const { data } = await apiClient.post<WorkBuddyAuthUrlResponse>(
    workBuddyPath('/oauth/auth-url'),
    payload
  )
  return data
}

export async function pollSignIn(
  payload: WorkBuddyPollRequest
): Promise<WorkBuddyPollResponse> {
  const { data } = await apiClient.post<WorkBuddyPollResponse>(
    workBuddyPath('/oauth/poll'),
    payload
  )
  return data
}

export async function refreshWorkBuddyToken(
  payload: WorkBuddyRefreshRequest
): Promise<WorkBuddyRefreshResponse> {
  const { data } = await apiClient.post<WorkBuddyRefreshResponse>(
    '/admin/workbuddy/oauth/refresh',
    payload
  )
  return data
}

export default { generateAuthUrl, pollSignIn, refreshWorkBuddyToken }

/** 账号积分概况（WorkBuddy 按积分结算，上游已算好余额）。 */
export interface WorkBuddyCreditGroup {
  kind: string
  name: string
  total: number
  remain: number
  used: number
  frozen: number
}

export interface WorkBuddyCredits {
  account_id: number
  account: string
  platform: string
  total: number
  groups: WorkBuddyCreditGroup[]
  package_name?: string
  is_paid_user: boolean
  credits_info_available: boolean
  error?: string
}


// wbPath 为只读积分接口选择路径。
//
// 管理员走 /admin/workbuddy/*，普通用户走 /workbuddy/*（两者映射同一批
// 只读处理器）。前端不知道当前用户角色时用这个统一入口，避免普通用户
// 打开仪表盘或账号管理时拿到 403。
const wbPath = workBuddyPath

/** 读取账号积分余额。 */
export async function getWorkBuddyCredits(accountId: number): Promise<WorkBuddyCredits> {
  const { data } = await apiClient.get<WorkBuddyCredits>(
    wbPath(`/accounts/${accountId}/credits`)
  )
  return data
}

/** 账号硬件指纹。按 uid 稳定派生，上游据此判断设备身份。 */
export interface WorkBuddyFingerprint {
  account_id: number
  name: string
  platform: string
  uid: string
  machine_id: string
  session_id: string
  mp_machine_id: string
  web_machine_id: string
  mac: string
  os: string
  arch: string
  os_version: string
  cpu_cores: number
  memory_size: number
  ua: string
  ide_version: string
  gpu?: string
  disk?: number
  screen?: string
  cpu_model?: string
  hostname?: string
  timezone?: string
  locale?: string
  fonts?: string[]
  webgl_vendor?: string
  webgl_renderer?: string
  persisted: boolean
}

/** 读取账号硬件指纹。 */
export async function getWorkBuddyFingerprint(accountId: number): Promise<WorkBuddyFingerprint> {
  const { data } = await apiClient.get<WorkBuddyFingerprint>(
    `/admin/workbuddy/accounts/${accountId}/fingerprint`
  )
  return data
}

/** 单日积分汇总（趋势图用）。 */
export interface WorkBuddyCreditDailySummary {
  date: string
  credits: number
  earned?: number
  calls: number
}

/** 读取近 N 天的积分消耗趋势。 */
export async function getWorkBuddyCreditTrend(
  accountId: number,
  days = 14
): Promise<WorkBuddyCreditDailySummary[]> {
  const { data } = await apiClient.get<WorkBuddyCreditDailySummary[]>(
    wbPath(`/accounts/${accountId}/credits/trend`),
    { params: { days } }
  )
  return data
}

/** 签到状态。 */
export interface WorkBuddyCheckinStatus {
  active: boolean
  today_checked_in: boolean
  streak_days: number
  daily_credit: number
  today_credit: number
  checkin_dates?: string[]
  next_streak_day?: number
  streak_bonus_days?: number
}

/** 签到执行结果。 */
export interface WorkBuddyCheckinResult {
  already_checked: boolean
  credit: number
  streak_days: number
  message?: string
}

/** 成长任务。 */
export interface WorkBuddyTask {
  code: string
  title: string
  description: string
  status: string
  reward_credit: number
  reward_energy: number
  task_type: string
  jump_url: string
}

/** 领奖结果。 */
export interface WorkBuddyClaimItem {
  code: string
  title: string
  credit: number
  error?: string
}

/** 读取签到状态。 */
export async function getWorkBuddyCheckin(accountId: number): Promise<WorkBuddyCheckinStatus> {
  const { data } = await apiClient.get<WorkBuddyCheckinStatus>(
    wbPath(`/accounts/${accountId}/checkin`)
  )
  return data
}

/** 执行签到。 */
export async function postWorkBuddyCheckin(accountId: number): Promise<WorkBuddyCheckinResult> {
  const { data } = await apiClient.post<WorkBuddyCheckinResult>(
    wbPath(`/accounts/${accountId}/checkin`)
  )
  return data
}

/** 读取成长任务。 */
export async function getWorkBuddyTasks(accountId: number): Promise<WorkBuddyTask[]> {
  const { data } = await apiClient.get<WorkBuddyTask[]>(wbPath(`/accounts/${accountId}/tasks`))
  return data
}

/** 领取可领任务奖励。 */
export async function postWorkBuddyClaim(
  accountId: number,
  includeHighRisk = false
): Promise<{ results: WorkBuddyClaimItem[]; total_credit: number }> {
  const { data } = await apiClient.post<{ results: WorkBuddyClaimItem[]; total_credit: number }>(
    `/admin/workbuddy/accounts/${accountId}/tasks/claim`,
    { include_high_risk: includeHighRisk, wait_seconds: 10 }
  )
  return data
}

/** 任务清单项。 */
export interface WorkBuddyTaskCatalogItem {
  code: string
  name: string
  risk: 'normal' | 'high'
  auto: boolean
  note?: string
}

/** 任务清单分组。 */
export interface WorkBuddyTaskCatalogGroup {
  group: string
  items: WorkBuddyTaskCatalogItem[]
}

/** 执行记录。 */
export interface WorkBuddyTaskRun {
  id: number
  account_id: number
  account_name: string
  kind: string
  task_code: string
  task_name: string
  result: string
  credit: number
  streak_days: number
  detail: string
  occurred_at: string
}

/** 任务台账聚合项。 */
export interface WorkBuddyTaskSummaryItem {
  task_code: string
  task_name: string
  total: number
  success: number
  last_result: string
  last_at: string
  last_detail: string
}

/** 积分聚合。 */
export interface WorkBuddyCreditSummary {
  today_checkin_credit: number
  today_task_credit: number
  today_total_credit: number
  total_credit: number
}

/** 读取任务清单。 */
export async function getWorkBuddyTaskCatalog(): Promise<{
  groups: WorkBuddyTaskCatalogGroup[]
  total: number
  auto: number
  manual: number
}> {
  const { data } = await apiClient.get('/admin/workbuddy/tasks/catalog')
  return data
}

/** 读取执行历史。 */
export async function getWorkBuddyTaskRuns(limit = 50): Promise<WorkBuddyTaskRun[]> {
  const { data } = await apiClient.get<WorkBuddyTaskRun[]>('/admin/workbuddy/tasks/runs', {
    params: { limit }
  })
  return data
}

/** 读取任务台账与积分聚合。 */
export async function getWorkBuddyTaskSummary(): Promise<{
  tasks: WorkBuddyTaskSummaryItem[]
  credits: WorkBuddyCreditSummary
}> {
  const { data } = await apiClient.get('/admin/workbuddy/tasks/summary')
  return data
}

/** 执行单个任务。 */
export async function postWorkBuddyRunTask(
  accountId: number,
  code: string
): Promise<WorkBuddyClaimItem> {
  const { data } = await apiClient.post<WorkBuddyClaimItem>(
    `/admin/workbuddy/accounts/${accountId}/tasks/${code}/run`
  )
  return data
}

/** 自动任务开关。 */
export interface WorkBuddyAutoTaskConfig {
  auto_checkin: boolean
  auto_claim: boolean
  include_high_risk: boolean
}

/** 读取自动任务开关。 */
export async function getWorkBuddyAutoTask(accountId: number): Promise<WorkBuddyAutoTaskConfig> {
  const { data } = await apiClient.get<WorkBuddyAutoTaskConfig>(
    `/admin/workbuddy/accounts/${accountId}/auto-task`
  )
  return data
}

/** 保存自动任务开关。 */
export async function putWorkBuddyAutoTask(
  accountId: number,
  cfg: WorkBuddyAutoTaskConfig
): Promise<WorkBuddyAutoTaskConfig> {
  const { data } = await apiClient.put<WorkBuddyAutoTaskConfig>(
    `/admin/workbuddy/accounts/${accountId}/auto-task`,
    cfg
  )
  return data
}

/** 看板任务格。 */
export interface WorkBuddyTaskBoardItem {
  code: string
  title: string
  description: string
  task_desc: string
  icon_url: string
  tag: string
  badge_name: string
  jump_url: string
  risk: 'low' | 'medium' | 'elevated' | 'high'
  auto: boolean
  period: 'daily' | 'once'
  reward: number
  account_total: number
  account_done: number
  account_claimed: number
  account_claimable: number
  valid_end?: string
  status: 'none' | 'partial' | 'done'
}

/** 看板数据。 */
export interface WorkBuddyTaskBoard {
  items: WorkBuddyTaskBoardItem[]
  account_count: number
  refreshed_at: string
  errors?: string[]
}

/** 任务在某账号上的结果。 */
export interface WorkBuddyTaskAccountResult {
  account_id: number
  account_name: string
  platform: string
  status: string
  result: string
  credit: number
  detail: string
  claimable: boolean
}

/** 任务详情。 */
export interface WorkBuddyTaskDetail {
  code: string
  title: string
  description: string
  icon_url: string
  risk: string
  auto: boolean
  period: 'daily' | 'once'
  reward: number
  valid_end?: string
  accounts: WorkBuddyTaskAccountResult[]
}

/** 积分包。 */
export interface WorkBuddyCreditPackage {
  package_code: string
  name: string
  remain: number
  total: number
  used: number
  expire_at: string | null
  days_left: number
  source: string
}

/** 趋势点。 */
export interface WorkBuddyTaskTrendPoint {
  date: string
  checkin_credit: number
  task_credit: number
  total_credit: number
}

/** 读取任务看板。 */
export async function getWorkBuddyTaskBoard(): Promise<WorkBuddyTaskBoard> {
  const { data } = await apiClient.get<WorkBuddyTaskBoard>(wbPath('/tasks/board'))
  return data
}

/** 读取任务详情（各账号状态）。 */
export async function getWorkBuddyTaskDetail(code: string): Promise<WorkBuddyTaskDetail> {
  const { data } = await apiClient.get<WorkBuddyTaskDetail>(
    `/admin/workbuddy/tasks/${encodeURIComponent(code)}/detail`
  )
  return data
}

/** 在指定账号上执行任务。 */
export async function postWorkBuddyRunTaskAll(
  code: string,
  accountIds: number[],
  auto = false
): Promise<{ code: string; total: number; success: number; failed: number; credit: number }> {
  const { data } = await apiClient.post(
    `/admin/workbuddy/tasks/${encodeURIComponent(code)}/run`,
    { account_ids: accountIds, auto }
  )
  return data
}

/** 读取积分包明细。 */
export async function getWorkBuddyCreditPackages(
  accountId: number
): Promise<WorkBuddyCreditPackage[]> {
  const { data } = await apiClient.get<WorkBuddyCreditPackage[]>(
    wbPath(`/accounts/${accountId}/credits/packages`)
  )
  return data
}

/** 读取积分趋势。 */
export async function getWorkBuddyTaskTrend(days = 7): Promise<WorkBuddyTaskTrendPoint[]> {
  const { data } = await apiClient.get<WorkBuddyTaskTrendPoint[]>(
    wbPath('/tasks/trend'),
    { params: { days } }
  )
  return data
}

/** 单账号云端积分快照。 */
export interface WorkBuddyAccountSnapshot {
  account_id: number
  account_name: string
  checkin_credits: number
  streak_days: number
  today_checked_in: boolean
  daily_credit: number
  total_credits: number
  package_credits: number
  task_claimable: number
  task_claimed: number
  updated_at: string
  err?: string
}

/** 全账号云端积分快照。 */
export interface WorkBuddyCreditSnapshot {
  accounts: WorkBuddyAccountSnapshot[]
  synced_at: string
  checkin_credits: number
  total_credits: number
  task_claimable: number
  today_task_credits: number
  auto_claimed_credits: number
  daily?: Array<{ date: string; score: number; credits: number }>
  syncing: boolean
}

/** 读取云端积分快照；sync=true 时在后台触发一次上游同步。 */
export async function getWorkBuddyCreditSnapshot(sync = false): Promise<WorkBuddyCreditSnapshot> {
  const { data } = await apiClient.get<WorkBuddyCreditSnapshot>(wbPath('/credits/snapshot'), {
    params: sync ? { sync: 1 } : undefined
  })
  return data
}

/** Marvis 积分快照（与 WorkBuddy 同形状，前端共用卡片）。 */
export interface MarvisCreditSnapshot {
  accounts: Array<{
    account_id: number
    account: string
    total: number
    used: number
    available: number
    used_percent: number
    error?: string
  }>
  synced_at: string | null
  total_credits: number
  used_credits: number
  available_credits: number
  /** 按授权方式拆分的可用余额：QQ 登录 / 微信登录。 */
  available_qq?: number
  available_wx?: number
  today_used: number
}

/** Trae 积分快照（国内站积分 / 国际站美元按 1:100 折算成积分）。 */
export interface TraeCreditSnapshot {
  accounts: Array<{
    account_id: number
    account: string
    platform: string
    total: number
    used: number
    available: number
    unlimited?: boolean
    error?: string
  }>
  synced_at: string | null
  total_credits: number
  used_credits: number
  available_credits: number
  /** 按站点拆分的余额（均已折算成积分）。 */
  available_cn?: number
  available_intl?: number
  today_used: number
}

/** 读取 Trae 积分快照；sync=true 时现拉一次上游额度。 */
export async function getTraeCreditSnapshot(sync = false): Promise<TraeCreditSnapshot> {
  const { data } = await apiClient.get<TraeCreditSnapshot>(wbPath('/credits/trae'), {
    params: sync ? { sync: 1 } : undefined
  })
  return data
}

/** 读取 Marvis 积分快照；sync=true 时现拉一次上游钱包。 */
export async function getMarvisCreditSnapshot(sync = false): Promise<MarvisCreditSnapshot> {
  const { data } = await apiClient.get<MarvisCreditSnapshot>(wbPath('/credits/marvis'), {
    params: sync ? { sync: 1 } : undefined
  })
  return data
}


/** 执行任务结果项。 */
export interface WorkBuddyTaskExecItem {
  task_code: string
  task_name: string
  outcome: 'claimed' | 'reported' | 'skipped' | 'unsupported' | 'failed'
  credit: number
  message?: string
}

/** 批量执行任务（含事件上报）。 */
export async function postWorkBuddyExecuteTasks(
  taskCodes: string[],
  accountIds: number[],
  allowReport = true,
  allowHighRisk = false
): Promise<Record<string, WorkBuddyTaskExecItem[]>> {
  const { data } = await apiClient.post<{ results: Record<string, WorkBuddyTaskExecItem[]> }>(
    '/admin/workbuddy/tasks/execute',
    { task_codes: taskCodes, account_ids: accountIds, allow_report: allowReport, allow_high_risk: allowHighRisk }
  )
  return data.results
}


/** 积分包明细。 */
export interface WorkBuddyCreditPackageItem {
  package_code: string
  name: string
  remain: number
  total: number
  used: number
  expire_at: string | null
  expire_date: string
  days_left: number
  source: string
  use_percent: number
}

/** 账号完整积分明细（汇总 + 各积分包）。 */
export interface WorkBuddyCreditsDetail {
  account_id: number
  account: string
  platform: string
  total: number
  groups: Array<{ kind: string; name: string; total: number; remain: number; used: number; frozen: number }>
  package_name?: string
  packages: WorkBuddyCreditPackageItem[]
  package_remain_sum: number
  package_err?: string
  error?: string
}

/** 读取账号完整积分明细。 */
export async function getWorkBuddyCreditsDetail(accountId: number): Promise<WorkBuddyCreditsDetail> {
  const { data } = await apiClient.get<WorkBuddyCreditsDetail>(
    wbPath(`/accounts/${accountId}/credits/detail`)
  )
  return data
}
