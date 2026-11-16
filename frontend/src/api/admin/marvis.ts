import { marvisPath } from '@/utils/apiRolePath'
import { apiClient } from '../client'

// 腾讯 Marvis 的登录与模型目录。
//
// 与 WorkBuddy 的差别：WorkBuddy 服务端能轮询到结果（auth-url → poll），
// Marvis 的授权在 QQ / 微信开放平台完成，回调只会回到浏览器，服务端拿不到。
// 所以流程是「生成链接 → 用户登录 → 把回调地址粘贴回来 → 换登录态」。

/** 登录方式。 */
export type MarvisLoginType = 'qq' | 'wechat'

/** 生成授权链接的结果。 */
export interface MarvisAuthUrlResponse {
  auth_url: string
  /** 登录票据标识，轮询时使用。 */
  state: string
  /** 设备标识。 */
  guid: string
  login_type: string
  /** 上游授权完成后跳回的本服务地址。 */
  callback_url?: string
  expires_at: string
}

/** 轮询结果：未完成时只有 pending。 */
export type MarvisPollResponse = { pending: true } | MarvisSignInResult

/** 换取登录态的入参。 */
export interface MarvisExchangeRequest {
  /** 用户粘贴的内容：完整回调地址、纯 code 或整段 JSON。 */
  input: string
  guid: string
  login_type?: MarvisLoginType
  state?: string
}

/** 登录态。 */
export interface MarvisCredentials {
  openid: string
  access_token: string
  refresh_token?: string
  guid: string
  login_type?: string
  expires_at?: number
  nickname?: string
  avatar?: string
}

/** 登录结果。 */
export interface MarvisSignInResult {
  credentials: MarvisCredentials
  /** 建议的账号显示名。 */
  account_name: string
}

/** 生成 QQ / 微信授权链接。 */
export async function generateMarvisAuthUrl(
  loginType: MarvisLoginType = 'qq'
): Promise<MarvisAuthUrlResponse> {
  const { data } = await apiClient.post<MarvisAuthUrlResponse>(
    marvisPath('/oauth/auth-url'),
    { login_type: loginType }
  )
  return data
}

/** 轮询登录结果。未完成时返回 { pending: true }。 */
export async function pollMarvisSignIn(state: string): Promise<MarvisPollResponse> {
  const { data } = await apiClient.post<MarvisPollResponse>(
    marvisPath('/oauth/poll'),
    { state }
  )
  return data
}

/** 用回调地址里的 code 换登录态（备用路径，部分部署形态下仍可用）。 */
export async function exchangeMarvisCode(
  payload: MarvisExchangeRequest
): Promise<MarvisSignInResult> {
  const { data } = await apiClient.post<MarvisSignInResult>(
    marvisPath('/oauth/exchange'),
    payload
  )
  return data
}

/** 用 refresh token 续期。 */
export async function refreshMarvisToken(payload: {
  openid: string
  access_token?: string
  refresh_token: string
  guid?: string
  login_type?: string
}): Promise<MarvisCredentials> {
  const { data } = await apiClient.post<MarvisCredentials>(
    '/admin/marvis/oauth/refresh',
    payload
  )
  return data
}

/** 读取 Marvis 可用模型。 */
export async function getMarvisModels(): Promise<Array<{ id: string }>> {
  const { data } = await apiClient.get<{ models: Array<{ id: string }> }>(
    '/admin/marvis/models'
  )
  return data?.models ?? []
}

export default {
  generateMarvisAuthUrl,
  pollMarvisSignIn,
  exchangeMarvisCode,
  refreshMarvisToken,
  getMarvisModels
}

/** Marvis 账号的硬件指纹（字段与 WorkBuddy 对齐，管理端共用同一个弹窗）。 */
export interface MarvisFingerprint {
  account_id: number
  name: string
  platform: string
  uid: string
  machine_id: string
  session_id: string
  mac: string
  os: string
  arch: string
  os_version: string
  cpu_cores: number
  memory_size: number
  gpu: string
  disk: number
  screen: string
  cpu_model: string
  ua: string
  ide_version: string
  guid: string
  openid: string
  login_type: string
  persisted: boolean
}

/** 读取 Marvis 账号的设备指纹。 */
export async function getMarvisFingerprint(accountId: number): Promise<MarvisFingerprint> {
  const { data } = await apiClient.get<MarvisFingerprint>(
    marvisPath(`/accounts/${accountId}/fingerprint`)
  )
  return data
}


/** Marvis 积分（与 WorkBuddy 同形状，前端共用积分卡片）。 */
export interface MarvisCredits {
  account_id: number
  account: string
  platform: string
  total: number
  used: number
  available: number
  frozen: number
  used_percent: number
  package_name?: string
  is_paid_user: boolean
  credits_info_available: boolean
  groups: Array<{ name: string; total: number; used: number; remaining: number }>
  error?: string
}

/** 读取 Marvis 账号的令牌钱包。 */
export async function getMarvisCredits(accountId: number): Promise<MarvisCredits> {
  const { data } = await apiClient.get<MarvisCredits>(
    marvisPath(`/accounts/${accountId}/wallet`)
  )
  return data
}
