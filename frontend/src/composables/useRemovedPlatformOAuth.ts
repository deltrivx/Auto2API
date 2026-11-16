import { ref } from 'vue'

/**
 * 已移除平台的 OAuth composable 桩实现。
 *
 * 本分支只服务 WorkBuddy，OpenAI / Gemini / Antigravity / Grok 的 OAuth
 * 流程已从产品中移除。但 CreateAccountModal 等组件里仍保留了按平台分派的
 * 调用点，为了避免一次性大改引入回归，这里保留同名 composable 与相同形状的
 * 返回值，但所有方法都是空操作：
 *
 *   - 状态 ref 恒为空/未加载
 *   - generateAuthUrl / exchangeAuthCode 等一律返回 null
 *   - buildCredentials 返回空对象
 *
 * 这些平台无法通过 UI 选中，因此这些桩在运行时不会被真正触达。
 */

/** 平台 OAuth 桩的统一形状。 */
export interface RemovedPlatformOAuth {
  authUrl: ReturnType<typeof ref<string>>
  sessionId: ReturnType<typeof ref<string>>
  state: ReturnType<typeof ref<string>>
  oauthState: ReturnType<typeof ref<string>>
  loading: ReturnType<typeof ref<boolean>>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error: ReturnType<typeof ref<any>>
  resetState: () => void
  // 返回类型统一放宽为 any：桩不会真正产出数据，但调用方仍按原类型
  // （如 GeminiTokenInfo）使用返回值，过窄的类型会在编译期报错。
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  generateAuthUrl: (...args: any[]) => Promise<any>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exchangeAuthCode: (...args: any[]) => Promise<any>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  validateRefreshToken: (...args: any[]) => Promise<any>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  validateSSOToken: (...args: any[]) => Promise<any>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  authorizePassword: (...args: any[]) => Promise<any>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  buildCredentials: (...args: any[]) => any
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  buildExtraInfo: (...args: any[]) => any
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  getCapabilities: () => Promise<any>
}

/**
 * 创建平台 OAuth 桩。
 *
 * @param _platform 平台标识，仅用于调试；桩行为与平台无关。
 */
export function useRemovedPlatformOAuth(_platform: string): RemovedPlatformOAuth {
  const authUrl = ref('')
  const sessionId = ref('')
  const state = ref('')
  const oauthState = ref('')
  const loading = ref(false)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const error = ref<any>('')

  return {
    authUrl,
    sessionId,
    state,
    oauthState,
    loading,
    error,
    resetState: () => {
      authUrl.value = ''
      sessionId.value = ''
      state.value = ''
      oauthState.value = ''
      loading.value = false
      error.value = ''
    },
    generateAuthUrl: async () => false,
    exchangeAuthCode: async () => null,
    validateRefreshToken: async () => null,
    validateSSOToken: async () => null,
    authorizePassword: async () => null,
    buildCredentials: () => ({}),
    buildExtraInfo: () => ({}),
    getCapabilities: async () => null,
  }
}
