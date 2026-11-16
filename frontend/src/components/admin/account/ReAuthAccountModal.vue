<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.reAuthorizeAccount')"
    width="normal"
    @close="handleClose"
  >
    <div v-if="account" class="space-y-4">
      <!-- Account Info -->
      <div
        class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-700"
      >
        <div class="flex items-center gap-3">
          <div
            :class="[
              'flex h-10 w-10 items-center justify-center rounded-lg bg-gradient-to-br',
              platformAccentClass
            ]"
          >
            <Icon name="sparkles" size="md" class="text-white" />
          </div>
          <div>
            <span class="block font-semibold text-gray-900 dark:text-white">{{
              account.name
            }}</span>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{ platformLabel }}
            </span>
          </div>
        </div>
      </div>


      <!-- Gemini OAuth Type Display (read-only) -->
      <div v-if="isGemini" class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-700">
        <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.accounts.oauth.gemini.oauthTypeLabel') }}
        </div>
        <div class="flex items-center gap-3">
          <div
            :class="[
              'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
              geminiOAuthType === 'google_one'
                ? 'bg-purple-500 text-white'
                : geminiOAuthType === 'code_assist'
                  ? 'bg-blue-500 text-white'
                  : 'bg-amber-500 text-white'
            ]"
          >
            <Icon v-if="geminiOAuthType === 'google_one'" name="user" size="sm" />
            <Icon v-else-if="geminiOAuthType === 'code_assist'" name="cloud" size="sm" />
            <Icon v-else name="sparkles" size="sm" />
          </div>
          <div>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">
              {{
                geminiOAuthType === 'google_one'
                  ? 'Google One'
                  : geminiOAuthType === 'code_assist'
                    ? t('admin.accounts.gemini.oauthType.builtInTitle')
                    : t('admin.accounts.gemini.oauthType.customTitle')
              }}
            </span>
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{
                geminiOAuthType === 'google_one'
                  ? t('admin.accounts.gemini.oauthType.googleOneDesc')
                  : geminiOAuthType === 'code_assist'
                    ? t('admin.accounts.gemini.oauthType.builtInDesc')
                    : t('admin.accounts.gemini.oauthType.customDesc')
              }}
            </span>
          </div>
        </div>
      </div>

      <!-- WorkBuddy 重新授权：与添加账号共用同一套两步流程 -->
      <WorkBuddyAuthSteps
        v-if="authFlow === 'workbuddy'"
        :platform="account!.platform as 'workbuddy_cn' | 'workbuddy_intl'"
        :auth="workbuddyOAuth"
        @authorized="handleWorkBuddyConfirm"
      />

      <!-- Marvis 重新授权：与添加账号共用同一套三步流程 -->
      <MarvisAuthSteps
        v-if="authFlow === 'marvis'"
        :auth="marvisOAuth"
        @authorized="handleMarvisConfirm"
      />

      <!-- 未登记平台的兜底：明确说明不支持，而不是套用别的平台的授权界面 -->
      <div
        v-if="!authFlow"
        class="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
      >
        {{ t('admin.accounts.reauthUnsupported') }}
      </div>

      <OAuthAuthorizationFlow
        v-if="authFlow === 'code-exchange'"
        ref="oauthFlowRef"
        :add-method="addMethod"
        :auth-url="currentAuthUrl"
        :session-id="currentSessionId"
        :loading="currentLoading"
        :error="currentError"
        :show-refresh-token-option="isOpenAI || isAntigravity || isGrok"
        :show-sso-option="isGrok"
        :show-email-password-option="false"
        :allow-multiple="false"
        :method-label="t('admin.accounts.inputMethod')"
        :platform="account?.platform"
        @generate-url="handleGenerateUrl"
        @validate-refresh-token="handleValidateRefreshToken"
      />

    </div>

    <template #footer>
      <div v-if="account" class="flex justify-between gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          v-if="isManualInputMethod"
          type="button"
          :disabled="!canExchangeCode"
          class="btn btn-primary"
          @click="handleExchangeCode"
        >
          <svg
            v-if="currentLoading"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            ></circle>
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            ></path>
          </svg>
          {{
            currentLoading
              ? t('admin.accounts.oauth.verifying')
              : t('admin.accounts.oauth.completeAuth')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { authPlatformSpec } from '@/constants/authPlatforms'
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AddMethod, AuthInputMethod } from '@/composables/useAccountOAuth'
import { useWorkBuddyOAuth } from '@/composables/useWorkBuddyOAuth'
import { useMarvisOAuth } from '@/composables/useMarvisOAuth'
import type { Account } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import OAuthAuthorizationFlow from '@/components/account/OAuthAuthorizationFlow.vue'
import WorkBuddyAuthSteps from '@/components/account/WorkBuddyAuthSteps.vue'
import MarvisAuthSteps from '@/components/account/MarvisAuthSteps.vue'

// Type for exposed OAuthAuthorizationFlow component
// Note: defineExpose automatically unwraps refs, so we use the unwrapped types
interface OAuthFlowExposed {
  authCode: string
  oauthState: string
  projectId: string
  sessionKey: string
  inputMethod: AuthInputMethod
  reset: () => void
}

interface Props {
  show: boolean
  account: Account | null
}

const props = defineProps<Props>()
const emit = defineEmits<{
  close: []
  reauthorized: [account: Account]
}>()

const appStore = useAppStore()
const { t } = useI18n()

// OAuth composables
const workbuddyOAuth = useWorkBuddyOAuth()
const marvisOAuth = useMarvisOAuth()

// Refs
const oauthFlowRef = ref<OAuthFlowExposed | null>(null)

// State
const addMethod = ref<AddMethod>('oauth')
const geminiOAuthType = ref<'code_assist' | 'google_one' | 'ai_studio'>('code_assist')

// Computed - check platform
const isOpenAI = computed(() => props.account?.platform === 'openai')
const isOpenAILike = computed(() => isOpenAI.value)
const isGemini = computed(() => props.account?.platform === 'gemini')
const isAntigravity = computed(() => props.account?.platform === 'antigravity')
const isGrok = computed(() => props.account?.platform === 'grok')
// 平台展示信息统一查注册表。未登记的平台上，标签显示「未知平台」，
// 而不是退回某个具体平台的名称——那正是之前 Marvis 显示成 Claude 的原因。
const platformSpec = computed(() => authPlatformSpec(props.account?.platform))
const platformLabel = computed(() =>
  platformSpec.value ? t(platformSpec.value.labelKey) : t('admin.accounts.unknownPlatform')
)
const platformAccentClass = computed(() => {
  switch (platformSpec.value?.flow) {
    case 'workbuddy':
      return 'from-blue-500 to-blue-600'
    case 'marvis':
      return 'from-emerald-500 to-emerald-600'
    default:
      return 'from-gray-400 to-gray-500'
  }
})

// 授权流程由注册表决定：workbuddy/marvis 用各自的步骤组件，
// 其余（未登记）不渲染任何授权面板，只提示该平台不支持重新授权。
const authFlow = computed(() => platformSpec.value?.flow)

// handleWorkBuddyConfirm 轮询拿到新 token 后保存凭据。
async function handleWorkBuddyConfirm() {
  if (!props.account) return
  try {
    await workbuddyOAuth.waitForSignIn(props.account.platform as "workbuddy_cn" | "workbuddy_intl")
  } catch (e: any) {
    appStore.showError(e?.message || 'Authorization failed')
    return
  }
  const creds = workbuddyOAuth.buildCredentials()
  if (!creds) {
    appStore.showError('Authorization incomplete')
    return
  }
  try {
    const updated = await adminAPI.accounts.update(props.account.id, { credentials: creds })
    appStore.showSuccess(t('admin.accounts.oauth.authorized'))
    emit('reauthorized', updated as unknown as Account)
    handleClose()
  } catch (e: any) {
    appStore.showError(e?.message || 'Save failed')
  }
}


// handleMarvisConfirm 用回调地址换取新登录态后保存凭据。
async function handleMarvisConfirm() {
  const signed = marvisOAuth.result.value
  if (!signed?.credentials?.access_token) {
    appStore.showError('Authorization incomplete')
    return
  }
  if (!props.account) return
  try {
    const updated = await adminAPI.accounts.update(props.account.id, {
      credentials: signed.credentials
    })
    appStore.showSuccess(t('admin.accounts.oauth.authorized'))
    emit('reauthorized', updated as unknown as Account)
    handleClose()
  } catch (e: any) {
    appStore.showError(e?.message || 'Save failed')
  }
}

// Computed - current OAuth state based on platform
const currentAuthUrl = computed(() => workbuddyOAuth.authUrl.value)
const currentSessionId = computed(() => workbuddyOAuth.state.value)
const currentLoading = computed(() => workbuddyOAuth.loading.value)
const currentError = computed(() => workbuddyOAuth.error.value)

// Computed — footer "complete auth" only for code-exchange flows, not SSO/password/RT.
const isManualInputMethod = computed(() => {
  const method = oauthFlowRef.value?.inputMethod
  if (method === 'sso_cookie' || method === 'email_password' || method === 'refresh_token') {
    return false
  }
  // OpenAI/Gemini/Antigravity/Grok use manual code paste by default (no cookie auth)
  return (
    isOpenAILike.value ||
    isGemini.value ||
    isAntigravity.value ||
    isGrok.value ||
    method === 'manual'
  )
})

const canExchangeCode = computed(() => {
  const authCode = oauthFlowRef.value?.authCode || ''
  const sessionId = currentSessionId.value
  const loading = currentLoading.value
  return authCode.trim() && sessionId && !loading
})

// Watchers
watch(
  () => props.show,
  (newVal) => {
    if (newVal && props.account) {
      if (isGemini.value) {
        const creds = (props.account.credentials || {}) as Record<string, unknown>
        geminiOAuthType.value =
          creds.oauth_type === 'google_one'
            ? 'google_one'
            : creds.oauth_type === 'ai_studio'
              ? 'ai_studio'
              : 'code_assist'
      }
    } else {
      resetState()
    }
  }
)

// Methods
const resetState = () => {
  addMethod.value = 'oauth'
  geminiOAuthType.value = 'code_assist'
  workbuddyOAuth.resetState()
  // Marvis 也要清：否则关掉弹窗再打开，第二步的授权链接和回调输入还在，
  // 用户看到的是一个已经过期的旧链接。
  marvisOAuth.reset()
  oauthFlowRef.value?.reset()
}

const handleClose = () => {
  emit('close')
}

const handleGenerateUrl = async () => {
  if (!props.account) return
  // 本分支只保留 WorkBuddy：浏览器授权 + 轮询。
  await workbuddyOAuth.start(props.account.platform as 'workbuddy_cn' | 'workbuddy_intl')
}

const handleExchangeCode = async () => {
  // 本分支只保留 WorkBuddy：授权在浏览器完成，服务端轮询结果，
  // 没有 authorization code 的交换步骤。
  appStore.showWarning('WorkBuddy 使用浏览器授权，无需手动输入授权码')
}



/** Re-auth the existing account with one refresh token. */
const handleValidateRefreshToken = async (_refreshTokenInput: string) => {
  // 本分支只保留 WorkBuddy：refresh token 由服务端轮询获取，
  // 手工校验入口已下线。
  appStore.showWarning('WorkBuddy 不支持手工校验 refresh token')
}
/** Re-auth with a single SSO cookie → Build OAuth (not batch create). */
/** Re-auth with a single refresh token. */
</script>
