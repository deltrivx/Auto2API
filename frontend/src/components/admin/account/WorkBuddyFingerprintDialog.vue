<template>
  <BaseDialog :show="show" :title="dialogTitle" width="normal" @close="emit('close')">
    <div v-if="loading" class="py-6 text-center text-sm text-gray-400">{{ t('common.loading') }}</div>
    <div v-else-if="error" class="rounded bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ error }}
    </div>
    <div v-else-if="fp" class="space-y-4">
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t(isTrae ? 'admin.accounts.fingerprint.hint' : 'admin.accounts.fingerprint.gatewayHostHint') }}
      </p>

      <!-- 硬件规格：divide-y 列表，标签定宽 112px（w-28），值与标签同行 -->
      <dl class="divide-y divide-gray-200 text-sm dark:divide-dark-600">
        <div v-for="row in specRows" :key="row.label" class="flex gap-4 py-2">
          <dt class="w-28 flex-shrink-0 text-gray-500 dark:text-gray-400">{{ row.label }}</dt>
          <dd class="min-w-0 break-all font-mono text-xs text-gray-900 dark:text-gray-100">
            {{ row.value || missingValue }}
          </dd>
        </div>
      </dl>

      <!-- 设备标识：单独一节，带小标题 -->
      <div>
        <p class="mb-2 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.fingerprint.deviceIdsTitle') }}
        </p>
        <dl class="divide-y divide-gray-200 text-sm dark:divide-dark-600">
          <div v-for="row in idRows" :key="row.label" class="flex gap-4 py-2">
            <dt class="w-28 flex-shrink-0 text-gray-500 dark:text-gray-400">{{ row.label }}</dt>
            <dd class="min-w-0 break-all font-mono text-xs text-gray-900 dark:text-gray-100">
              {{ row.value || missingValue }}
            </dd>
          </div>
        </dl>
      </div>

      <!-- User-Agent：单独一段，值完整换行 -->
      <div>
        <p class="mb-1 text-xs text-gray-500 dark:text-gray-400">User-Agent</p>
        <p class="break-all font-mono text-xs text-gray-900 dark:text-gray-100">{{ fp?.ua || '—' }}</p>
      </div>

    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getWorkBuddyFingerprint, type WorkBuddyFingerprint } from '@/api/admin/workbuddy'
import { fetchDevice as getTraeDevice, type TraeDevice } from '@/api/admin/trae'

// WorkBuddy 显示网关环境和账号标识，Trae 显示登录时绑定的设备信息。
const props = defineProps<{
  show: boolean
  accountId: number
  accountName?: string
  platform?: string
}>()
const isTrae = computed(() => props.platform === 'trae_cn' || props.platform === 'trae_intl')
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const fp = ref<WorkBuddyFingerprint | null>(null)
const traeDevice = ref<TraeDevice | null>(null)

const loading = ref(false)
const error = ref('')

const dialogTitle = computed(() =>
  props.accountName
    ? `${t('admin.accounts.fingerprint.dialogTitle')} · ${props.accountName}`
    : t('admin.accounts.fingerprint.dialogTitle')
)

const missingValue = computed(() => isTrae.value ? '—' : t('admin.accounts.fingerprint.notCollected'))

const specRows = computed(() => {
  const f = fp.value
  if (!f) return []
  const memory = isTrae.value
    ? `${f.memory_size} GB`
    : f.memory_size > 0 ? `${(f.memory_size / 1024 ** 3).toFixed(2)} GiB` : ''
  const hostnameLabel = isTrae.value ? 'hostname' : 'gatewayHostname'
  return [
    { label: t('admin.accounts.fingerprint.system'), value: `${f.os} ${f.os_version}`.trim() },
    { label: t('admin.accounts.fingerprint.cpu'), value: f.cpu_model ? `${f.cpu_model} / ${f.cpu_cores} 核` : `${f.cpu_cores} 核` },
    { label: t('admin.accounts.fingerprint.memory'), value: memory },
    { label: t('admin.accounts.fingerprint.gpu'), value: f.gpu },
    { label: t('admin.accounts.fingerprint.disk'), value: f.disk ? `${f.disk} GB` : '' },
    { label: t('admin.accounts.fingerprint.screen'), value: f.screen },
    { label: t(`admin.accounts.fingerprint.${hostnameLabel}`), value: f.hostname },
    { label: t('admin.accounts.fingerprint.timezone'), value: f.timezone },
    { label: t('admin.accounts.fingerprint.locale'), value: f.locale },
    { label: t('admin.accounts.fingerprint.webgl'), value: f.webgl_renderer }
  ]
})

const idRows = computed(() => {
  const f = fp.value
  if (!f) return []
  return [
    { label: 'MachineID', value: f.machine_id },
    { label: 'SessionID', value: f.session_id },
    { label: 'MAC', value: f.mac }
  ]
})

async function load() {
  if (!props.show || !props.accountId) return
  loading.value = true
  error.value = ''
  try {
    if (isTrae.value) {
      const d = await getTraeDevice(props.accountId)
      traeDevice.value = d
      // 与 WorkBuddy 共用同一套展示结构：Trae 的设备字段映射到同名属性。
      fp.value = {
        uid: d.device_id,
        machine_id: d.machine_id,
        session_id: (d as any).session_id || d.machine_id,
        mac: (d as any).mac || '',
        os: (d as any).os || 'Windows',
        arch: (d as any).arch || 'x64',
        os_version: d.os_version,
        cpu_cores: (d as any).cpu_cores || 0,
        memory_size: (d as any).memory_size || 0,
        ua: (d as any).ua || '',
        ide_version: '',
        gpu: (d as any).gpu || '',
        disk: (d as any).disk || 0,
        screen: (d as any).screen || '',
        cpu_model: (d as any).cpu_model || '',
        hostname: (d as any).hostname || '',
        timezone: (d as any).timezone || '',
        locale: (d as any).locale || '',
        fonts: (d as any).fonts || [],
        webgl_vendor: (d as any).webgl_vendor || '',
        webgl_renderer: (d as any).webgl_renderer || '',
        persisted: true
      } as unknown as WorkBuddyFingerprint
      return
    }
    fp.value = await getWorkBuddyFingerprint(props.accountId)
  } catch (e: any) {
    error.value = e?.message || 'failed'
    fp.value = null
  } finally {
    loading.value = false
  }
}

watch(() => [props.show, props.accountId], load, { immediate: true })
</script>
