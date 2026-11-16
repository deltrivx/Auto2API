<template>
  <BaseDialog :show="show" :title="detail?.title || t('admin.creditsTasks.taskDetail')" width="normal" @close="emit('close')">
    <div v-if="loading" class="py-10 text-center text-sm text-gray-400">{{ t('common.loading') }}</div>

    <div v-else-if="detail" class="space-y-4">
      <!-- 顶部任务卡片：与「测试账号连接」弹窗同构 -->
      <div class="flex items-center justify-between rounded-xl border border-gray-200 bg-gradient-to-r from-gray-50 to-gray-100 p-3 dark:border-dark-500 dark:from-dark-700 dark:to-dark-600">
        <div class="flex items-center gap-3">
          <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-gradient-to-br from-primary-500 to-primary-600">
            <Icon name="lightbulb" size="sm" class="text-white" />
          </div>
          <div>
            <div class="font-semibold text-gray-900 dark:text-gray-100">{{ detail.title }}</div>
            <div class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
              <span class="rounded bg-gray-200 px-1.5 py-0.5 text-[10px] font-medium uppercase dark:bg-dark-500">
                {{ detail.period === 'once' ? t('admin.creditsTasks.typeOnce') : t('admin.creditsTasks.typeDaily') }}
              </span>
              <span>{{ effectiveAuto ? t('admin.creditsTasks.modeAuto') : t('admin.creditsTasks.modeManual') }}</span>
            </div>
          </div>
        </div>
        <span class="rounded-full px-2.5 py-1 text-xs font-semibold" :class="riskBadgeClass(detail.risk)">
          {{ riskLabel(detail.risk) }}
        </span>
      </div>

      <!-- 收益与说明 -->
      <div class="space-y-1.5">
        <label class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.creditsTasks.reward') }}</label>
        <div class="flex items-center justify-between rounded-xl border border-gray-200 px-3 py-2 dark:border-dark-500">
          <span class="text-sm text-gray-900 dark:text-gray-100">
            <template v-if="detail.reward > 0">{{ detail.reward }} {{ t('admin.creditsTasks.creditUnit') }}</template>
            <template v-else>{{ t('admin.creditsTasks.randomReward') }}</template>
          </span>
          <span class="text-xs text-gray-400">{{ counts.done }} / {{ detail.accounts.length }} {{ t('admin.creditsTasks.completedAccounts') }}</span>
        </div>
        <p v-if="detail.description" class="px-1 text-xs text-gray-500 dark:text-gray-400">{{ detail.description }}</p>
      </div>

      <!-- 账号列表：行式卡片 + 勾选框 -->
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.creditsTasks.account') }}</label>
          <label class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
            <input
              type="checkbox"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-40"
              :checked="allSelected"
              :indeterminate.prop="partiallySelected"
              :disabled="selectableRows.length === 0"
              @change="toggleAll"
            />
            {{ t('admin.creditsTasks.selectAll') }} ({{ selected.size }})
          </label>
        </div>
        <div class="max-h-[220px] overflow-y-auto">
          <div
            v-for="row in detail.accounts"
            :key="row.account_id"
            class="flex items-center justify-between border-b border-gray-100 py-2.5 last:border-0 dark:border-dark-600"
          >
            <div class="flex min-w-0 items-center gap-2.5">
              <input
                type="checkbox"
                class="rounded border-gray-300 text-primary-600 focus:ring-primary-500 disabled:cursor-not-allowed"
                :checked="selected.has(row.account_id)"
                :disabled="isRowDisabled(row)"
                @change="toggle(row.account_id)"
              />
              <div class="min-w-0">
                <div class="truncate text-sm text-gray-900 dark:text-gray-100">{{ row.account_name }}</div>
                <div class="text-xs text-gray-400">{{ row.detail || statusLabel(row.status) }}</div>
              </div>
            </div>
            <span class="flex-shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium" :class="statusBadgeClass(row.status)">
              {{ statusLabel(row.status) }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex items-center justify-between">
        <button type="button" class="btn btn-secondary btn-sm" @click="emit('close')">{{ t('common.close') }}</button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="running || selected.size === 0 || !effectiveAuto"
          @click="runSelected"
        >
          {{ running ? t('admin.creditsTasks.running') : t('admin.creditsTasks.runSelected') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { WorkBuddyTaskDetail, WorkBuddyTaskAccountResult } from '@/api/admin/workbuddy'

const props = defineProps<{
  show: boolean
  detail: WorkBuddyTaskDetail | null
  loading: boolean
  running: boolean
  // autoMode 是任务卡上「执行方式」开关的当前值。
  // 不能直接用 detail.auto——那是后端「该任务能否自动执行」的能力标记，
  // 与用户在卡片上切到手动/自动是两件事，混用会出现「卡片手动、弹窗自动」。
  autoMode?: boolean
}>()
const emit = defineEmits<{
  close: []
  run: [code: string, accountIds: number[], auto: boolean]
}>()
const { t } = useI18n()

const selected = ref<Set<number>>(new Set())

// effectiveAuto：卡片开关优先，未传时回退到任务自身的能力标记。
const effectiveAuto = computed(() =>
  props.autoMode !== undefined ? props.autoMode : !!props.detail?.auto
)

watch(
  () => props.detail,
  (d) => {
    const next = new Set<number>()
    if (d) {
      for (const row of d.accounts) {
        if (row.claimable && !isRowDisabled(row)) next.add(row.account_id)
      }
    }
    selected.value = next
  }
)

const counts = computed(() => {
  const d = props.detail
  let done = 0
  let running = 0
  let pending = 0
  for (const row of d?.accounts ?? []) {
    if (row.status === 'claimed' || row.status === 'completed') done++
    else if (row.status === 'accepted' || row.status === 'in_progress') running++
    else pending++
  }
  return { done, running, pending }
})

// 单次任务已完成的行不可再执行。
function isRowDisabled(row: WorkBuddyTaskAccountResult): boolean {
  const done = row.status === 'completed' || row.status === 'claimed'
  if (!done) return false
  return props.detail?.period === 'once' || row.status === 'claimed'
}

const selectableRows = computed(() =>
  (props.detail?.accounts ?? []).filter((row) => !isRowDisabled(row))
)

const allSelected = computed(
  () => selectableRows.value.length > 0 && selected.value.size === selectableRows.value.length
)
const partiallySelected = computed(() => selected.value.size > 0 && !allSelected.value)

function toggle(id: number) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleAll() {
  selected.value = allSelected.value
    ? new Set()
    : new Set(selectableRows.value.map((a) => a.account_id))
}

function runSelected() {
  if (!props.detail) return
  emit('run', props.detail.code, [...selected.value], true)
}

function riskLabel(risk: string): string {
  const map: Record<string, string> = {
    low: t('admin.creditsTasks.riskLow'),
    medium: t('admin.creditsTasks.riskMedium'),
    elevated: t('admin.creditsTasks.riskElevated'),
    high: t('admin.creditsTasks.riskHigh')
  }
  return map[risk] ?? risk
}

function riskBadgeClass(risk: string): string {
  switch (risk) {
    case 'high':
      return 'bg-red-100 text-red-700 dark:bg-red-500/20 dark:text-red-400'
    case 'elevated':
      return 'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-400'
    case 'medium':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400'
    default:
      return 'bg-green-100 text-green-700 dark:bg-green-500/20 dark:text-green-400'
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'completed':
      return t('admin.creditsTasks.stCompleted')
    case 'claimed':
      return t('admin.creditsTasks.stClaimed')
    case 'accepted':
      return t('admin.creditsTasks.stAccepted')
    case 'in_progress':
      return t('admin.creditsTasks.stInProgress')
    case 'not_accepted':
      return t('admin.creditsTasks.stNotAccepted')
    default:
      return status || '—'
  }
}

function statusBadgeClass(status: string): string {
  if (status === 'completed') return 'bg-green-100 text-green-700 dark:bg-green-500/20 dark:text-green-400'
  if (status === 'claimed') return 'bg-gray-200 text-gray-600 dark:bg-dark-500 dark:text-gray-300'
  if (status === 'accepted' || status === 'in_progress') return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
}
</script>
