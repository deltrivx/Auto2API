import { apiClient } from '../client'

/**
 * Trae（字节跳动 AI IDE）的登录与账号接口。
 *
 * 与 WorkBuddy/Marvis 的差别：Trae 的授权页只接受
 * http://127.0.0.1:<port>/authorize 作为回调，部署在服务器上的网关收不到。
 * 因此流程是「生成链接 → 浏览器授权 → 把地址栏里的回调地址粘回来」。
 */
export type TraePlatform = 'trae_cn' | 'trae_intl'

const base = (suffix: string) => `/admin/trae${suffix}`

export interface TraeAuthURLResponse {
  auth_url: string
  state: string
  platform: TraePlatform
  callback_hint: string
}

export interface TraeAuthResult {
  platform: TraePlatform
  credentials: Record<string, unknown>
  uid?: string
  name?: string
  region?: string
  auth_host?: string
}

export interface TraeModel {
  id: string
  name: string
  context?: number
  max_output?: number
  efforts?: string[]
  dev_model?: string
}

export interface TraeCredits {
  platform: TraePlatform
  native_unit: 'credits' | 'usd'
  limit: number
  used: number
  unlimited: boolean
  plan?: string
  updated_at?: string
}

export interface TraeDevice {
  device_id: string
  machine_id: string
  device_brand: string
  device_type: string
  os_version: string
  // 与 WorkBuddy 指纹同名的展示字段（后端按机器码确定性派生）。
  os?: string
  arch?: string
  cpu_model?: string
  cpu_cores?: number
  memory_size?: number
  gpu?: string
  disk?: number
  screen?: string
  session_id?: string
  mac?: string
  ua?: string
  persisted?: boolean
}

/** 生成授权链接。 */
export async function generateAuthURL(platform: TraePlatform): Promise<TraeAuthURLResponse> {
  const { data } = await apiClient.post<TraeAuthURLResponse>(base('/oauth/auth-url'), { platform })
  return data
}

/** 用粘贴回来的回调地址完成授权。 */
export async function completeAuth(state: string, callbackUrl: string): Promise<TraeAuthResult> {
  const { data } = await apiClient.post<TraeAuthResult>(base('/oauth/callback'), {
    state,
    callback_url: callbackUrl
  })
  return data
}

/** 拉取账号模型目录。 */
export async function fetchModels(accountId: number): Promise<TraeModel[]> {
  const { data } = await apiClient.get<TraeModel[]>(base(`/accounts/${accountId}/models`))
  return data
}

/** 读取账号额度（原生单位，展示层折算成积分）。 */
export async function fetchCredits(accountId: number): Promise<TraeCredits> {
  const { data } = await apiClient.get<TraeCredits>(base(`/accounts/${accountId}/credits`))
  return data
}

/** 读取设备指纹。 */
export async function fetchDevice(accountId: number): Promise<TraeDevice> {
  const { data } = await apiClient.get<TraeDevice>(base(`/accounts/${accountId}/device`))
  return data
}

/** 重新分配设备指纹。 */
export async function rotateDevice(accountId: number): Promise<TraeDevice> {
  const { data } = await apiClient.post<TraeDevice>(base(`/accounts/${accountId}/device`), {})
  return data
}

/** 续期登录态。 */
export async function refreshAccount(accountId: number): Promise<void> {
  await apiClient.post(base(`/accounts/${accountId}/refresh`), {})
}
