import { ref, computed } from 'vue'
import { useAppStore } from '@/stores/app'
import {
  generateMarvisAuthUrl,
  exchangeMarvisCode,
  type MarvisLoginType,
  type MarvisSignInResult
} from '@/api/admin/marvis'
import { extractApiErrorMessage } from '@/utils/apiError'

/** 授权等待时长：与后端票据 TTL 一致，5 分钟。 */
const AUTH_TIMEOUT_MS = 5 * 60 * 1000
/** 超时检查间隔。只用来判断是否超时，界面上不显示倒计时。 */
const TICK_MS = 1000

/**
 * 登录按钮的四种状态。
 *
 * idle       未生成链接——按钮「生成授权链接」可点
 * waiting    链接已生成、用户在授权——按钮「等待授权中」不可点
 * timedOut   超过等待时长——按钮「重新授权」可点
 * completed  已换取到登录态——按钮「登录成功」不可点，由用户点「创建」结束
 *
 * 换登录态本身还有一个瞬时状态 exchanging，用 loading 表达，不占这四个之一。
 */
export type MarvisLoginStage = 'idle' | 'waiting' | 'timedOut' | 'completed' | 'failed'

/**
 * 腾讯 Marvis 登录。
 *
 * 上游的 redirect_uri 有严格白名单（只能是 Marvis 自己的回调），所以 code
 * 只会落在用户浏览器的地址栏里，服务端拿不到。流程因此是：
 *
 *   1. 生成授权链接 → 用户在浏览器里完成授权
 *   2. 把回调地址粘贴回来
 *   3. **用户自己点「完成登录」** 才换取登录态
 *   4. 换取成功后，用户再点「创建」才真正建号
 *
 * 第 3、4 步刻意不自动：粘贴动作只把按钮点亮，不由粘贴直接触发网络请求，
 * 也不在成功后替用户建号 —— 账号名、分组这些字段用户还要确认。
 */
export function useMarvisOAuth() {
  const appStore = useAppStore()

  const authUrl = ref('')
  const state = ref('')
  const guid = ref('')
  const loginType = ref<MarvisLoginType>('qq')
  const loading = ref(false)
  const error = ref('')
  const result = ref<MarvisSignInResult | null>(null)
  /** 换取失败的原因；成功时清空。 */
  const failure = ref('')

  // 授权是否已超时。不对外暴露剩余秒数——界面上不显示时间。
  const expired = ref(false)
  let deadline = 0
  let tickTimer: ReturnType<typeof setInterval> | null = null

  const clearTimer = () => {
    if (tickTimer) {
      clearInterval(tickTimer)
      tickTimer = null
    }
  }

  /** 当前阶段。 */
  const stage = computed<MarvisLoginStage>(() => {
    if (result.value) return 'completed'
    if (failure.value) return 'failed'
    if (!authUrl.value) return 'idle'
    if (expired.value) return 'timedOut'
    return 'waiting'
  })

  /** 生成/重新生成按钮是否可点。 */
  const canGenerate = computed(() => !loading.value && stage.value !== 'waiting' && stage.value !== 'completed')
  /** 完成登录按钮是否可点：需要有链接、已粘贴内容、且不在换取中/已完成。 */
  const canSubmit = computed(
    () => !!authUrl.value && stage.value !== 'completed' && !loading.value
  )

  const reset = () => {
    clearTimer()
    authUrl.value = ''
    state.value = ''
    guid.value = ''
    loading.value = false
    error.value = ''
    failure.value = ''
    result.value = null
    expired.value = false
  }

  /**
   * 生成授权链接。
   *
   * 生成成功后按钮即进入 waiting 并锁死，直到超时或用户完成登录；
   * 超时后重新调用本函数即可拿一条新链接。
   */
  const start = async (type: MarvisLoginType = 'qq'): Promise<boolean> => {
    clearTimer()
    loading.value = true
    authUrl.value = ''
    state.value = ''
    guid.value = ''
    error.value = ''
    failure.value = ''
    result.value = null
    expired.value = false
    loginType.value = type
    try {
      const resp = await generateMarvisAuthUrl(type)
      authUrl.value = resp.auth_url
      state.value = resp.state
      guid.value = resp.guid

      deadline = Date.now() + AUTH_TIMEOUT_MS
      tickTimer = setInterval(() => {
        if (Date.now() >= deadline && !result.value) {
          expired.value = true
          clearTimer()
        }
      }, TICK_MS)
      return true
    } catch (e) {
      error.value = extractApiErrorMessage(e, '生成授权链接失败')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  /**
   * 用回调地址换取登录态。
   *
   * 由用户点按钮触发，不自动执行。失败只记录原因，不弹全局错误——
   * 用户会重新粘贴，界面上的提示已经足够。
   */
  const complete = async (input: string): Promise<MarvisSignInResult | null> => {
    const text = input.trim()
    if (!text) {
      failure.value = '请先粘贴登录完成后的回调地址'
      return null
    }
    if (!state.value) {
      failure.value = '请先生成授权链接'
      return null
    }
    loading.value = true
    failure.value = ''
    error.value = ''
    try {
      const resp = await exchangeMarvisCode({
        input: text,
        guid: guid.value,
        login_type: loginType.value,
        state: state.value
      })
      result.value = resp
      clearTimer()
      return resp
    } catch (e) {
      failure.value = extractApiErrorMessage(e, '登录失败')
      return null
    } finally {
      loading.value = false
    }
  }

  /**
   * 用户重新粘贴时调用：把上一次的失败结果清掉，按钮回到可提交状态。
   *
   * 已经成功（completed）时不动——成功之后应当去点「创建」，
   * 而不是被一次误粘贴打回初始态。
   */
  const clearFailure = () => {
    if (failure.value) failure.value = ''
  }

  return {
    authUrl,
    state,
    guid,
    loginType,
    loading,
    error,
    failure,
    result,
    stage,
    canGenerate,
    canSubmit,
    start,
    complete,
    clearFailure,
    reset
  }
}
