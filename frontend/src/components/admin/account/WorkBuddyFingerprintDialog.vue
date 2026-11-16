<template>
  <BaseDialog :show="show" :title="dialogTitle" width="normal" @close="emit('close')">
    <div v-if="loading" class="py-6 text-center text-sm text-gray-400">{{ t('common.loading') }}</div>
    <div v-else-if="error" class="rounded bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ error }}
    </div>
    <div v-else-if="fp" class="space-y-4">
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.fingerprint.hint') }}
      </p>

      <!-- 硬件规格：divide-y 列表，标签定宽 112px（w-28），值与标签同行 -->
      <dl class="divide-y divide-gray-200 text-sm dark:divide-dark-600">
        <div v-for="row in specRows" :key="row.label" class="flex gap-4 py-2">
          <dt class="w-28 flex-shrink-0 text-gray-500 dark:text-gray-400">{{ row.label }}</dt>
          <dd class="min-w-0 break-all font-mono text-xs text-gray-900 dark:text-gray-100">
            {{ row.value || '—' }}
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
              {{ row.value || '—' }}
            </dd>
          </div>
        </dl>
      </div>

      <!-- User-Agent：单独一段，值完整换行 -->
      <div>
        <p class="mb-1 text-xs text-gray-500 dark:text-gray-400">User-Agent</p>
        <p class="break-all font-mono text-xs text-gray-900 dark:text-gray-100">{{ fp.ua || '—' }}</p>
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
import { getMarvisFingerprint, type MarvisFingerprint } from '@/api/admin/marvis'

// platform 决定走哪套指纹接口：WorkBuddy 与 Marvis 的设备标识含义不同，
// 前者是完整硬件画像，后者只有 guid + 每次现签的 uskey。
const props = defineProps<{
  show: boolean
  accountId: number
  accountName?: string
  platform?: string
}>()
const isMarvis = computed(() => props.platform === 'marvis')
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const fp = ref<WorkBuddyFingerprint | null>(null)
const marvisFp = ref<MarvisFingerprint | null>(null)

const loading = ref(false)
const error = ref('')

const dialogTitle = computed(() =>
  props.accountName
    ? `${t('admin.accounts.fingerprint.dialogTitle')} · ${props.accountName}`
    : t('admin.accounts.fingerprint.dialogTitle')
)

const specRows = computed(() => {
  const f = fp.value
  if (!f) return []
  return [
    { label: t('admin.accounts.fingerprint.system'), value: `${f.os} ${f.os_version}` },
    { label: t('admin.accounts.fingerprint.cpu'), value: f.cpu_model ? `${f.cpu_model} / ${f.cpu_cores} 核` : `${f.cpu_cores} 核` },
    { label: t('admin.accounts.fingerprint.memory'), value: `${f.memory_size} GB` },
    { label: t('admin.accounts.fingerprint.gpu'), value: f.gpu || '—' },
    { label: t('admin.accounts.fingerprint.disk'), value: f.disk ? `${f.disk} GB` : '—' },
    { label: t('admin.accounts.fingerprint.screen'), value: f.screen || '—' }
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
    if (isMarvis.value) {
      const m = await getMarvisFingerprint(props.accountId)
      marvisFp.value = m
      // Marvis 与 WorkBuddy 共用同一套展示结构，字段一一对应。
      fp.value = {
        uid: m.uid,
        machine_id: m.machine_id,
        session_id: m.session_id,
        mac: m.mac,
        os: m.os,
        arch: m.arch,
        os_version: m.os_version,
        cpu_cores: m.cpu_cores,
        memory_size: m.memory_size,
        ua: m.ua,
        ide_version: m.ide_version,
        gpu: m.gpu,
        disk: m.disk,
        screen: m.screen,
        cpu_model: m.cpu_model,
        persisted: m.persisted
      } as unknown as WorkBuddyFingerprint
    } else {
      fp.value = await getWorkBuddyFingerprint(props.accountId)
      marvisFp.value = null
    }
  } catch (e: any) {
    error.value = e?.message || 'failed'
    fp.value = null
  } finally {
    loading.value = false
  }
}

watch(() => [props.show, props.accountId], load, { immediate: true })
</script>
