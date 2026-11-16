<template>
  <div class="space-y-4">
    <ol class="space-y-3">
      <!-- 步骤 1：选择渠道 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >1</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.marvisAuth.step1') }}
          </p>
          <div class="mt-2 grid grid-cols-2 gap-2">
            <button
              v-for="opt in loginOptions"
              :key="opt.value"
              type="button"
              class="flex items-center gap-2 rounded-lg border-2 p-3 text-left transition-all"
              :class="[
                auth.loginType.value === opt.value
                  ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                  : 'border-gray-200 hover:border-primary-300 dark:border-dark-600',
                channelLocked ? 'cursor-not-allowed opacity-60' : ''
              ]"
              :aria-disabled="channelLocked"
              @click="handleSelectChannel(opt.value)"
            >
              <div
                class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md"
                :class="
                  auth.loginType.value === opt.value
                    ? 'bg-primary-500 text-white'
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
                "
              >
                <PlatformIcon :platform="opt.icon" size="md" />
              </div>
              <span
                class="text-sm font-medium"
                :class="
                  auth.loginType.value === opt.value
                    ? 'text-primary-700 dark:text-primary-300'
                    : 'text-gray-700 dark:text-gray-300'
                "
                >{{ opt.label }}</span
              >
            </button>
          </div>
        </div>
      </li>

      <!-- 步骤 2：生成授权链接 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >2</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.marvisAuth.step2') }}
          </p>

          <!-- 链接在按钮**上方**：先生成、链接出现，按钮留在下方作为操作入口。
               链接只在等授权/超时期间展示；完成后收起，让用户聚焦第 3 步。 -->
          <a
            v-if="auth.authUrl.value && auth.stage.value !== 'completed'"
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
              :disabled="!auth.canGenerate.value"
              @click="handleGenerate"
            >
              {{ generateLabel }}
            </button>
            <!-- 超时提示只放在按钮后方，不单独占一行 -->
            <span v-if="auth.stage.value === 'timedOut'" class="text-xs text-red-500">
              {{ t('admin.accounts.marvisAuth.timeoutHint') }}
            </span>
          </div>
        </div>
      </li>

      <!-- 步骤 3：粘贴回调地址 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >3</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.marvisAuth.step3') }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.marvisAuth.step3Hint') }}
          </p>
          <input
            v-model="callbackInput"
            type="text"
            class="input mt-2 text-xs"
            :placeholder="t('admin.accounts.marvisAuth.step3Placeholder')"
            :disabled="!auth.authUrl.value || auth.stage.value === 'completed' || auth.loading.value"
            @input="handleInputChange"
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
            <!-- 按钮后方的结果提示 -->
            <span v-if="statusHint" class="text-xs" :class="statusHintClass">{{ statusHint }}</span>
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
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { useMarvisOAuth } from '@/composables/useMarvisOAuth'
import type { MarvisLoginType } from '@/api/admin/marvis'

// Marvis 授权：三步交互（选渠道 → 生成链接 → 粘贴地址并完成登录）。
//
// 两个按钮各有明确的状态机，见 useMarvisOAuth 的 stage：
//   生成按钮  idle→「生成授权链接」 / waiting→「等待授权中」不可点
//             / timedOut→「重新授权」 / completed→隐藏
//   登录按钮  可点 →「完成登录」 / 失败→「登录失败」可点
//             / 成功→「登录成功」不可点
//
// 粘贴不会自动建号：成功后由父组件接收 authorized 事件，用户再点「创建」。
const props = defineProps<{
  auth: ReturnType<typeof useMarvisOAuth>
}>()
const emit = defineEmits<{ authorized: [] }>()
const { t } = useI18n()

const callbackInput = ref('')

const loginOptions = computed<Array<{ value: MarvisLoginType; label: string; icon: 'qq' | 'wechat' }>>(() => [
  { value: 'qq', label: t('admin.accounts.marvisAuth.loginQQ'), icon: 'qq' },
  { value: 'wechat', label: t('admin.accounts.marvisAuth.loginWeChat'), icon: 'wechat' }
])

// 渠道按钮在「等待授权 / 已完成」期间不可切换。
//
// 渠道选择在生成链接后依然可点：第一步选错时用户必须能改回来。
// 切换渠道会调 auth.reset() 清掉链接与回调输入，第二步从头开始，
// 因此不会出现「旧链接配新渠道」的错配。
// 只在请求进行中锁住，避免连点打出多个并发请求。
const channelLocked = computed(() => props.auth.loading.value)

const generateLabel = computed(() => {
  switch (props.auth.stage.value) {
    case 'waiting':
      return t('admin.accounts.marvisAuth.waiting')
    case 'timedOut':
      return t('admin.accounts.marvisAuth.regenerate')
    default:
      return t('admin.accounts.marvisAuth.generate')
  }
})

const submitLabel = computed(() => {
  if (props.auth.loading.value) return t('admin.accounts.marvisAuth.confirming')
  switch (props.auth.stage.value) {
    case 'completed':
      return t('admin.accounts.marvisAuth.loggedIn')
    case 'failed':
      return t('admin.accounts.marvisAuth.loginFailed')
    default:
      return t('admin.accounts.marvisAuth.confirm')
  }
})

const submitButtonClass = computed(() => {
  if (props.auth.stage.value === 'completed') return 'btn-primary opacity-60'
  if (props.auth.stage.value === 'failed') return 'btn-danger'
  return 'btn-primary'
})

// 完成登录按钮：有链接、未成功、粘贴了内容、不在请求中。
const canSubmitNow = computed(
  () =>
    props.auth.canSubmit.value &&
    !!props.auth.authUrl.value &&
    callbackInput.value.trim().length > 0
)

// 按钮后方的一行提示：成功给昵称，失败给原因。
const statusHint = computed(() => {
  const stage = props.auth.stage.value
  if (stage === 'completed' && props.auth.result.value) {
    return t('admin.accounts.marvisAuth.successHint', { name: props.auth.result.value.account_name })
  }
  if (stage === 'failed' && props.auth.failure.value) {
    return props.auth.failure.value
  }
  return ''
})

const statusHintClass = computed(() =>
  props.auth.stage.value === 'completed'
    ? 'text-emerald-600 dark:text-emerald-400'
    : 'text-red-500 dark:text-red-400'
)


async function handleSelectChannel(type: MarvisLoginType) {
  if (channelLocked.value) return
  if (props.auth.loginType.value === type) return
  props.auth.reset()
  callbackInput.value = ''
  props.auth.loginType.value = type
}

async function handleGenerate() {
  callbackInput.value = ''
  await props.auth.start(props.auth.loginType.value)
}

// 重新粘贴时清掉上一次的失败态，让按钮回到「完成登录」。
function handleInputChange() {
  props.auth.clearFailure()
}

async function handleSubmit() {
  if (!canSubmitNow.value) return
  const result = await props.auth.complete(callbackInput.value)
  if (result) emit('authorized')
}

defineExpose({
  resetInput: () => {
    callbackInput.value = ''
  }
})
</script>
