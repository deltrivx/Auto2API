import { ref, computed } from 'vue'
import { useAppStore } from '@/stores/app'
import {
  generateAuthUrl as apiGenerateAuthUrl,
  pollSignIn as apiPollSignIn,
  type WorkBuddyPlatform,
  type WorkBuddyPollResponse
} from '@/api/admin/workbuddy'
import { extractApiErrorMessage } from '@/utils/apiError'

/** 轮询间隔：与 WorkBuddy 桌面端一致，1 秒一次。 */
const POLL_INTERVAL_MS = 1000
/** 最多轮询次数，5 分钟后放弃（与服务端登录会话有效期一致）。 */
const POLL_MAX_ATTEMPTS = 300

/**
 * WorkBuddy 登录。
 *
 * 与 Antigravity / Grok 的差别：WorkBuddy 的授权在它自己的页面完成，
 * 服务端无法被动收到回调，只能拿 state 反复查询结果。因此这里的流程是
 * 「生成链接 → 用户在浏览器登录 → 轮询」，没有 exchange-code 那一步。
 */
export function useWorkBuddyOAuth() {
  const appStore = useAppStore()

  const authUrl = ref('')
  const state = ref('')
  const loading = ref(false)
  const polling = ref(false)
  const error = ref('')
  const result = ref<WorkBuddyPollResponse | null>(null)

  /** 是否正在等待浏览器授权完成。 */
  const waiting = computed(() => polling.value)

  const resetState = () => {
    authUrl.value = ''
    state.value = ''
    loading.value = false
    polling.value = false
    error.value = ''
    result.value = null
  }

  /** 生成授权链接。成功后由调用方在浏览器打开 authUrl。 */
  const start = async (platform: WorkBuddyPlatform): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    state.value = ''
    error.value = ''
    result.value = null

    try {
      const response = await apiGenerateAuthUrl({ platform })
      authUrl.value = response.auth_url
      state.value = response.state
      return true
    } catch (err: any) {
      error.value = extractApiErrorMessage(err, '生成授权链接失败')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  /**
   * 轮询直到授权完成或超时。
   *
   * 服务端在用户尚未完成授权时返回 pending=true，这不是错误，继续等待即可。
   */
  const waitForSignIn = async (
    platform: WorkBuddyPlatform
  ): Promise<WorkBuddyPollResponse | null> => {
    if (!state.value) {
      error.value = '请先生成授权链接'
      return null
    }

    polling.value = true
    error.value = ''

    try {
      for (let attempt = 0; attempt < POLL_MAX_ATTEMPTS; attempt++) {
        const response = await apiPollSignIn({ platform, state: state.value })

        if (!response.pending) {
          result.value = response
          polling.value = false
          return response
        }

        // 未完成授权：等待后继续。
        await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
      }

      error.value = '等待授权超时，请重新生成链接'
      appStore.showError(error.value)
      return null
    } catch (err: any) {
      error.value = extractApiErrorMessage(err, '登录失败')
      appStore.showError(error.value)
      return null
    } finally {
      polling.value = false
    }
  }

  /** 中止轮询。 */
  const cancel = () => {
    polling.value = false
  }

  /** 把轮询结果转成账号凭据。 */
  const buildCredentials = (): Record<string, unknown> | null => {
    const r = result.value
    if (!r || !r.access_token) return null

    const creds: Record<string, unknown> = {
      access_token: r.access_token
    }
    if (r.refresh_token) creds.refresh_token = r.refresh_token
    if (r.expires_at) creds.expires_at = String(r.expires_at)
    if (r.refresh_expires_at) creds.refresh_expires_at = String(r.refresh_expires_at)
    if (r.domain) creds.domain = r.domain
    if (r.token_type) creds.token_type = r.token_type
    if (r.uid) creds.uid = r.uid
    if (r.enterprise_id) creds.enterprise_id = r.enterprise_id
    return creds
  }

  return {
    authUrl,
    state,
    loading,
    polling,
    waiting,
    error,
    result,
    resetState,
    start,
    waitForSignIn,
    cancel,
    buildCredentials
  }
}
