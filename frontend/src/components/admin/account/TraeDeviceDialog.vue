<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.traeDevice.title')"
    width="normal"
    @close="handleClose"
  >
    <div class="space-y-1 text-sm">
      <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.traeDevice.hint') }}
      </p>
      <div
        v-for="row in rows"
        :key="row.key"
        class="flex justify-between gap-3 border-b border-gray-100 py-1.5 last:border-0 dark:border-dark-700"
      >
        <span class="shrink-0 text-gray-500 dark:text-gray-400">
          {{ t(`admin.accounts.traeDevice.${row.key}`) }}
        </span>
        <span class="break-all text-right font-mono text-xs text-gray-900 dark:text-white">
          {{ row.value }}
        </span>
      </div>
      <p v-if="error" class="pt-2 text-xs text-red-500">{{ error }}</p>
    </div>

    <template #footer>
      <div class="flex justify-between gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="loading" @click="rotate">
          {{ loading ? t('common.loading') : t('admin.accounts.traeDevice.rotate') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { fetchDevice, rotateDevice, type TraeDevice } from '@/api/admin/trae'
import { extractApiErrorMessage } from '@/utils/apiError'

// Trae 的设备指纹：与 WorkBuddy 的「硬件指纹」同一个角色，但字段不同。
// 设备在登录时生成并与账号绑定，之后每个请求和每次续期都要用它签名，
// 所以这里既能查看，也能重新分配（换一台「机器」）。
const props = defineProps<{
  show: boolean
  accountId: number
  accountName?: string
}>()
const emit = defineEmits<{ close: []; rotated: [] }>()
const { t } = useI18n()

const device = ref<TraeDevice | null>(null)
const loading = ref(false)
const error = ref('')

const rows = ref<Array<{ key: string; value: string }>>([])

const applyDevice = (value: TraeDevice | null) => {
  device.value = value
  rows.value = value
    ? [
        { key: 'deviceId', value: value.device_id || '—' },
        { key: 'machineId', value: value.machine_id || '—' },
        { key: 'brand', value: value.device_brand || '—' },
        { key: 'type', value: value.device_type || '—' },
        { key: 'osVersion', value: value.os_version || '—' }
      ]
    : []
}

const load = async () => {
  if (!props.accountId) return
  loading.value = true
  error.value = ''
  try {
    applyDevice(await fetchDevice(props.accountId))
  } catch (e) {
    error.value = extractApiErrorMessage(e)
    applyDevice(null)
  } finally {
    loading.value = false
  }
}

const rotate = async () => {
  if (!props.accountId) return
  loading.value = true
  error.value = ''
  try {
    applyDevice(await rotateDevice(props.accountId))
    emit('rotated')
  } catch (e) {
    error.value = extractApiErrorMessage(e)
  } finally {
    loading.value = false
  }
}

const handleClose = () => emit('close')

watch(
  () => props.show,
  (visible) => {
    if (visible) void load()
  }
)
</script>
