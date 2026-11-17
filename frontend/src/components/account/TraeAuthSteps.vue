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
            {{ t('admin.accounts.traeAuth.stepChannel') }}
          </p>
          <div class="mt-2 grid grid-cols-2 gap-2">
            <button
              v-for="opt in channelOptions"
              :key="opt.value"
              type="button"
              :disabled="auth.loading.value"
              :class="[
                'flex items-center gap-2 rounded-lg border-2 p-3 text-left transition-all',
                platform === opt.value ? opt.activeClass : 'border-gray-200 dark:border-dark-600'
              ]"
              @click="handleChannelChange(opt.value)"
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

      <!-- 步骤 2：生成授权链接 -->
      <li class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >2</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.traeAuth.step1') }}
          </p>
          <button
            v-if="!auth.authUrl.value"
            type="button"
            class="btn btn-primary btn-sm mt-2"
            :disabled="auth.loading.value"
            @click="handleGenerate"
          >
            {{ auth.loading.value ? t('common.loading') : t('admin.accounts.traeAuth.generate') }}
          </button>
          <a
            v-else
            :href="auth.authUrl.value"
            target="_blank"
            rel="noopener noreferrer"
            class="mt-2 block break-all rounded-lg border border-primary-200 bg-primary-50 p-3 text-xs text-primary-700 underline dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300"
            >{{ auth.authUrl.value }}</a
          >
          <p v-if="auth.authUrl.value" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.traeAuth.callbackHint') }}
          </p>
        </div>
      </li>

      <!-- 步骤 3：粘贴回调地址 -->
      <li v-if="auth.authUrl.value" class="flex gap-3">
        <span
          class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >3</span
        >
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.traeAuth.pasteLabel') }}
          </p>
          <textarea
            v-model="callbackInput"
            rows="3"
            class="input mt-2 w-full break-all text-xs"
            :placeholder="t('admin.accounts.traeAuth.pastePlaceholder')"
            :disabled="auth.loading.value"
          ></textarea>
          <div class="mt-2 flex items-center gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="!callbackInput.trim() || auth.loading.value"
              @click="handleSubmit"
            >
              {{
                auth.loading.value
                  ? t('admin.accounts.traeAuth.verifying')
                  : t('admin.accounts.traeAuth.confirm')
              }}
            </button>
            <span
              v-if="auth.result.value"
              class="text-xs text-emerald-600 dark:text-emerald-400"
            >
              {{ t('admin.accounts.traeAuth.successHint', { name: signedName }) }}
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

// Trae 授权：选站点 → 生成链接 → 浏览器登录 → 粘贴回调地址。
//
// 为什么必须粘贴：Trae 授权页只接受 http://127.0.0.1:<port>/authorize，
// 部署在服务器上的网关收不到回调，用户把地址栏内容贴回来即可（与 Marvis 同路）。
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
const signedName = computed(() => props.auth.result.value?.name || props.auth.result.value?.uid || '')

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
