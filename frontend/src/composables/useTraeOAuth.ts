import { computed, ref } from 'vue'
import {
  completeAuth as apiCompleteAuth,
  generateAuthURL as apiGenerateAuth,
  type TraeAuthResult,
  type TraePlatform
} from '@/api/admin/trae'
import { extractApiErrorMessage } from '@/utils/apiError'

/**
 * Trae 登录状态机。
 *
 * 三步：生成链接 → 用户在浏览器授权 → 粘贴回调地址完成。
 * 设备身份由服务端按 state 取出，前端不接触私钥。
 */
export function useTraeOAuth() {
  const authUrl = ref('')
  const state = ref('')
  const loading = ref(false)
  const error = ref('')
  const result = ref<TraeAuthResult | null>(null)

  /** 已经拿到登录态，可以向账号接口提交凭据。 */
  const ready = computed(() => !!result.value)

  const reset = () => {
    authUrl.value = ''
    state.value = ''
    loading.value = false
    error.value = ''
    result.value = null
  }

  /** 生成授权链接。 */
  const start = async (platform: TraePlatform) => {
    loading.value = true
    error.value = ''
    result.value = null
    try {
      const response = await apiGenerateAuth(platform)
      authUrl.value = response.auth_url
      state.value = response.state
      return response
    } catch (e) {
      error.value = extractApiErrorMessage(e)
      return null
    } finally {
      loading.value = false
    }
  }

  /** 提交粘贴回来的回调地址。 */
  const submitCallback = async (callbackUrl: string) => {
    if (!state.value) {
      error.value = '请先生成授权链接'
      return null
    }
    loading.value = true
    error.value = ''
    try {
      result.value = await apiCompleteAuth(state.value, callbackUrl.trim())
      return result.value
    } catch (e) {
      error.value = extractApiErrorMessage(e)
      return null
    } finally {
      loading.value = false
    }
  }

  return { authUrl, state, loading, error, result, ready, start, submitCallback, reset }
}
