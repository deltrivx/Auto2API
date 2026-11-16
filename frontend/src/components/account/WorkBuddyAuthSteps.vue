<template>
  <div class="space-y-4">
    <ol class="space-y-3">
      <!-- 步骤 1：选择渠道（国内版 / 国际版） -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >1</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.workbuddyReauth.stepChannel') }}
          </p>
          <div class="mt-2 grid grid-cols-2 gap-2">
            <button
              v-for="opt in channelOptions"
              :key="opt.value"
              type="button"
              :aria-disabled="channelLocked"
              :class="[
                'flex items-center gap-2 rounded-lg border-2 p-3 text-left transition-all',
                platform === opt.value
                  ? opt.activeClass
                  : 'border-gray-200 dark:border-dark-600',
                channelLocked ? 'cursor-not-allowed opacity-60' : ''
              ]"
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
                <PlatformIcon :platform="opt.icon" size="md" />
              </div>
              <span
                class="text-sm font-medium"
                :class="
                  platform === opt.value
                    ? opt.textActiveClass
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
            {{ t('admin.accounts.workbuddyReauth.step1') }}
          </p>
          <button
            v-if="!auth.authUrl.value"
            type="button"
            class="btn btn-primary btn-sm mt-2"
            :disabled="auth.loading.value"
            @click="handleGenerate"
          >
            {{ auth.loading.value ? t('common.loading') : t('admin.accounts.workbuddyReauth.generate') }}
          </button>
          <template v-else>
            <!-- 链接在上、按钮在下，与 Marvis 保持一致 -->
            <a
              :href="auth.authUrl.value"
              target="_blank"
              rel="noopener noreferrer"
              class="mt-2 block break-all rounded-lg border border-primary-200 bg-primary-50 p-3 text-xs text-primary-700 underline dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300"
              >{{ auth.authUrl.value }}</a
            >
            <div class="mt-2">
              <button
                type="button"
                class="btn btn-sm"
                :class="confirmDone ? 'btn-primary opacity-60' : 'btn-primary'"
                :disabled="!auth.authUrl.value || auth.polling.value || confirmDone"
                @click="handleConfirm"
              >
                {{
                  auth.polling.value
                    ? t('admin.accounts.workbuddyReauth.polling')
                    : confirmDone
                      ? t('admin.accounts.workbuddyReauth.loggedIn')
                      : t('admin.accounts.workbuddyReauth.confirm')
                }}
              </button>
              <!-- 成功后把昵称告诉用户，由他去点「创建」 -->
              <span v-if="confirmDone && signedName" class="ml-2 text-xs text-emerald-600 dark:text-emerald-400">
                {{ t('admin.accounts.workbuddyReauth.successHint', { name: signedName }) }}
              </span>
            </div>
          </template>
        </div>
      </li>
    </ol>

    <p v-if="auth.error.value" class="rounded bg-red-50 p-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ auth.error.value }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { useWorkBuddyOAuth } from '@/composables/useWorkBuddyOAuth'

// WorkBuddy 浏览器授权：三步流程（选渠道 → 生成链接 → 确认登录）。
//
// 与 Marvis 的组件保持同一套语义：渠道选定后锁定，授权成功只上报昵称，
// 真正建号由用户在弹窗底部点「创建」完成。
const props = defineProps<{
  platform: 'workbuddy_cn' | 'workbuddy_intl'
  auth: ReturnType<typeof useWorkBuddyOAuth>
}>()
const emit = defineEmits<{ authorized: []; 'channel-change': ['workbuddy_cn' | 'workbuddy_intl'] }>()
const { t } = useI18n()

// 两个渠道的图标是同一套 WorkBuddy / CodeBuddy mark，靠颜色区分：
// 国内版沿用蓝色，国际版给一套紫色（CodeBuddy 的品牌调性）。
const channelOptions = computed<
  Array<{
    value: 'workbuddy_cn' | 'workbuddy_intl'
    label: string
    icon: 'workbuddy_cn' | 'workbuddy_intl'
    activeClass: string
    iconActiveClass: string
    textActiveClass: string
  }>
>(() => [
  {
    value: 'workbuddy_cn',
    label: t('admin.accounts.workbuddyReauth.channelCN'),
    icon: 'workbuddy_cn',
    activeClass: 'border-blue-500 bg-blue-50 dark:bg-blue-900/20',
    iconActiveClass: 'bg-blue-500 text-white',
    textActiveClass: 'text-blue-700 dark:text-blue-300'
  },
  {
    value: 'workbuddy_intl',
    label: t('admin.accounts.workbuddyReauth.channelIntl'),
    icon: 'workbuddy_intl',
    activeClass: 'border-violet-500 bg-violet-50 dark:bg-violet-900/20',
    iconActiveClass: 'bg-violet-500 text-white',
    textActiveClass: 'text-violet-700 dark:text-violet-300'
  }
])

// 渠道在生成链接后仍可切换：第一步选错必须能改回来。
// handleChannelChange 会调 resetState() 清掉链接与轮询，第二步重新开始，
// 因此不存在「旧链接配新站点」的错配。轮询进行中锁住，避免中途换渠道
// 让在途请求写回错误的账号。
const channelLocked = computed(() => props.auth.polling.value)

const confirmDone = computed(() => !!props.auth.result?.value)
const signedName = computed(() => props.auth.result?.value?.account_name || '')

function handleChannelChange(value: 'workbuddy_cn' | 'workbuddy_intl') {
  if (value === props.platform) return
  props.auth.resetState()
  emit('channel-change', value)
}

async function handleGenerate() {
  await props.auth.start(props.platform)
}

async function handleConfirm() {
  const result = await props.auth.waitForSignIn(props.platform)
  if (result) emit('authorized')
}
</script>
