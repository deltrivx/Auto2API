<template>
  <div class="space-y-4">
    <ol class="space-y-3">
      <!-- 步骤 1：选择站点 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >1</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.traeAuth.step1') }}
          </p>
          <div class="mt-2 grid grid-cols-2 gap-2">
            <button
              v-for="opt in channelOptions"
              :key="opt.value"
              type="button"
              class="flex items-center gap-2 rounded-lg border-2 p-3 text-left transition-all"
              :class="[
                platform === opt.value
                  ? opt.activeClass
                  : 'border-gray-200 hover:border-gray-300 dark:border-dark-600',
                channelLocked ? 'cursor-not-allowed opacity-60' : ''
              ]"
              :aria-disabled="channelLocked"
              @click="channelLocked ? undefined : handleChannelChange(opt.value)"
            >
              <div
                class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md"
                :class="
                  platform === opt.value
                    ? opt.iconActiveClass
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
                "
              >
                <PlatformIcon :platform="opt.value" size="md" />
              </div>
              <span
                class="text-sm font-medium"
                :class="
                  platform === opt.value ? opt.textActiveClass : 'text-gray-700 dark:text-gray-300'
                "
                >{{ opt.label }}</span
              >
            </button>
          </div>
        </div>
      </li>

      <!-- 步骤 2：生成授权链接（链接在按钮上方，与 Marvis 一致） -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >2</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.traeAuth.step2') }}
          </p>

          <a
            v-if="auth.authUrl.value"
            :href="auth.authUrl.value"
            target="_blank"
            rel="noopener noreferrer"
            class="mt-2 block break-all rounded-lg border border-primary-200 bg-primary-50 p-3 text-xs text-primary-700 underline dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300"
            >{{ auth.authUrl.value }}</a
          >

          <div class="mt-2 flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="auth.loading.value || !!auth.result.value"
              @click="handleGenerate"
            >
              {{ generateLabel }}
            </button>
          </div>
        </div>
      </li>

      <!-- 步骤 3：粘贴回调地址 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >3</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.traeAuth.step3') }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.traeAuth.step3Hint') }}
          </p>
          <input
            v-model="callbackInput"
            type="text"
            class="input mt-2 text-xs"
            :placeholder="t('admin.accounts.traeAuth.step3Placeholder')"
            :disabled="!auth.authUrl.value || !!auth.result.value || auth.loading.value"
          />

          <div class="mt-2 flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn btn-sm"
              :class="submitButtonClass"
              :disabled="!canSubmitNow"
              @click="handleSubmit"
            >
              {{ submitLabel }}
            </button>
            <span v-if="statusHint" class="text-xs text-emerald-600 dark:text-emerald-400">
              {{ statusHint }}
            </span>
          </div>
        </div>
      </li>
    </ol>

    <p
      v-if="auth.error.value"
      class="rounded bg-red-50 p-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400"
    >
      {{ auth.error.value }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { useTraeOAuth } from '@/composables/useTraeOAuth'

// Trae 授权：三步交互（选站点 → 生成链接 → 粘贴回调并完成登录），
// 样式与文案与 Marvis 一致——两者都是「服务器收不到回调，只能把地址粘回来」。
//
// 与 Marvis 的差别只有第一步：Trae 的两个站点账号不通，需要先选站点。
// 粘贴不会自动建号：成功后由父组件接收 authorized 事件，用户再点「创建」。
const props = defineProps<{
  platform: 'trae_cn' | 'trae_intl'
  auth: ReturnType<typeof useTraeOAuth>
}>()
const emit = defineEmits<{
  authorized: []
  'channel-change': ['trae_cn' | 'trae_intl']
}>()
const { t } = useI18n()

const callbackInput = ref('')

const channelOptions = computed(() => [
  {
    value: 'trae_cn' as const,
    label: t('admin.accounts.traeAuth.channelCN'),
    activeClass: 'border-amber-500 bg-amber-50 dark:bg-amber-900/20',
    iconActiveClass: 'bg-amber-500 text-white',
    textActiveClass: 'text-amber-700 dark:text-amber-300'
  },
  {
    value: 'trae_intl' as const,
    label: t('admin.accounts.traeAuth.channelIntl'),
    activeClass: 'border-sky-500 bg-sky-50 dark:bg-sky-900/20',
    iconActiveClass: 'bg-sky-500 text-white',
    textActiveClass: 'text-sky-700 dark:text-sky-300'
  }
])

// 渠道在请求进行中锁住，避免连点打出多个并发请求。
const channelLocked = computed(() => props.auth.loading.value)

const generateLabel = computed(() => {
  if (props.auth.loading.value) return t('admin.accounts.traeAuth.waiting')
  if (props.auth.result.value) return t('admin.accounts.traeAuth.loggedIn')
  return t('admin.accounts.traeAuth.generate')
})

const submitLabel = computed(() => {
  if (props.auth.loading.value) return t('admin.accounts.traeAuth.confirming')
  if (props.auth.result.value) return t('admin.accounts.traeAuth.loggedIn')
  if (props.auth.error.value) return t('admin.accounts.traeAuth.loginFailed')
  return t('admin.accounts.traeAuth.confirm')
})

const submitButtonClass = computed(() => {
  if (props.auth.result.value) return 'btn-primary opacity-60'
  if (props.auth.error.value) return 'btn-danger'
  return 'btn-primary'
})

const canSubmitNow = computed(
  () =>
    !!props.auth.authUrl.value &&
    !props.auth.result.value &&
    !props.auth.loading.value &&
    callbackInput.value.trim().length > 0
)

const statusHint = computed(() => {
  const result = props.auth.result.value
  if (!result) return ''
  const name = result.name || result.uid || ''
  return t('admin.accounts.traeAuth.successHint', { name })
})

function handleChannelChange(value: 'trae_cn' | 'trae_intl') {
  if (value === props.platform) return
  callbackInput.value = ''
  props.auth.reset()
  emit('channel-change', value)
}

async function handleGenerate() {
  await props.auth.start(props.platform)
}

async function handleSubmit() {
  const result = await props.auth.submitCallback(callbackInput.value)
  if (result) emit('authorized')
}
</script>
