<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- 一、数据总览
           排版与仪表盘顶部各项统计严格一致：grid-cols-2 / lg:grid-cols-4，
           卡片 p-4，图标在左（rounded-lg p-2 浅色底），数值在右。 -->
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <div v-for="card in overviewCards" :key="card.key" class="card p-4">
          <div class="flex items-center gap-3">
            <div class="rounded-lg p-2" :class="card.bg">
              <Icon :name="card.icon" size="md" :class="card.color" :stroke-width="2" />
            </div>
            <div class="min-w-0">
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ card.label }}</p>
              <p class="text-xl font-bold text-gray-900 dark:text-white">{{ card.value }}</p>
              <p v-if="card.hint" class="text-xs text-gray-400">{{ card.hint }}</p>
            </div>
          </div>
        </div>
      </div>

      <!-- 二、积分趋势 -->
      <div class="card p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.creditsTasks.trendTitle') }}
            </h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.creditsTasks.trendHint') }}
            </p>
          </div>
          <div class="flex gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
            <button
              v-for="d in [7, 30]"
              :key="d"
              type="button"
              class="rounded px-3 py-1 text-xs font-medium transition-colors"
              :class="trendDays === d
                ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
                : 'text-gray-500 dark:text-gray-400'"
              @click="switchTrend(d)"
            >{{ d }} {{ t('admin.creditsTasks.days') }}</button>
          </div>
        </div>

        <div v-if="trend.length === 0" class="py-10 text-center text-sm text-gray-400">
          {{ t('admin.creditsTasks.noTrend') }}
        </div>
        <div v-else class="overflow-x-auto">
          <!-- 折线图用 SVG 手绘：不引入图表库，保持包体与主题一致 -->
          <svg :viewBox="`0 0 ${chartW} ${chartH}`" class="h-48 w-full" preserveAspectRatio="none">
            <!-- 网格线 -->
            <line
              v-for="i in 4"
              :key="'g' + i"
              :x1="0"
              :y1="(chartH / 4) * (i - 1)"
              :x2="chartW"
              :y2="(chartH / 4) * (i - 1)"
              stroke="currentColor"
              class="text-gray-200 dark:text-dark-600"
              stroke-width="1"
            />
            <!-- 签到线 -->
            <polyline
              :points="polyline('checkin')"
              fill="none"
              stroke="#06b6d4"
              stroke-width="2"
              stroke-linejoin="round"
            />
            <!-- 任务线 -->
            <polyline
              :points="polyline('task')"
              fill="none"
              stroke="#10b981"
              stroke-width="2"
              stroke-linejoin="round"
            />
            <!-- 合计线 -->
            <polyline
              :points="polyline('total')"
              fill="none"
              stroke="#f59e0b"
              stroke-width="2"
              stroke-dasharray="4 3"
              stroke-linejoin="round"
            />
          </svg>
          <div class="mt-2 flex flex-wrap items-center gap-4 text-xs text-gray-500 dark:text-gray-400">
            <span class="flex items-center gap-1"><i class="h-0.5 w-4 bg-cyan-500"></i>{{ t('admin.creditsTasks.checkinCredit') }}</span>
            <span class="flex items-center gap-1"><i class="h-0.5 w-4 bg-emerald-500"></i>{{ t('admin.creditsTasks.taskCredit') }}</span>
            <span class="flex items-center gap-1"><i class="h-0.5 w-4 bg-amber-500"></i>{{ t('admin.creditsTasks.totalCreditLine') }}</span>
            <span class="ml-auto">{{ trend[0]?.date }} → {{ trend[trend.length - 1]?.date }}</span>
          </div>
        </div>
      </div>

      <!-- 三、任务方格 -->
      <div class="card p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.creditsTasks.tasksTitle') }}
            </h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ board?.account_count ?? 0 }} {{ t('admin.creditsTasks.accountsJoined') }} ·
              {{ board?.items?.length ?? 0 }} {{ t('admin.creditsTasks.taskCount') }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <label class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
              <input
                type="checkbox"
                class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                :checked="tasksAllSelected"
                :indeterminate.prop="tasksPartiallySelected"
                @change="toggleSelectAllTasks"
              />
              {{ t('admin.creditsTasks.selectAll') }} ({{ selectedTasks.size }})
            </label>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="running || selectedTasks.size === 0"
              @click="runSelectedTasks"
            >{{ running ? t('admin.creditsTasks.running') : t('admin.creditsTasks.run') }}</button>
          </div>
        </div>

        <div v-if="!board || board.items.length === 0" class="py-10 text-center text-sm text-gray-400">
          {{ t('admin.creditsTasks.noTasks') }}
        </div>
        <div v-else class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          <div
            v-for="item in board.items"
            :key="item.code"
            class="group relative flex cursor-pointer flex-col rounded-xl border p-3 text-left transition-all hover:shadow-md"
            @click="openDetail(item)"
            :class="item.account_claimable > 0
              ? 'border-emerald-300 bg-emerald-50/40 dark:border-emerald-700 dark:bg-emerald-900/10'
              : 'border-gray-200 hover:border-primary-300 dark:border-dark-600'"
          >
            <input
              type="checkbox"
              class="absolute right-2 top-2 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="selectedTasks.has(item.code)"
              @click.stop
              @change="toggleTask(item.code)"
            />
            <div class="flex cursor-pointer items-start gap-2">
              <!-- 图标来自项目内置的 91 字形图标集（Icon.vue），按任务语义映射。
                   不用上游 icon_url：那批图重复、空白、尺寸不一，混排很乱。 -->
              <div class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg" :class="iconBg(item)">
                <Icon :name="iconName(item)" size="sm" :class="iconColor(item)" />
              </div>
              <div class="min-w-0 flex-1">
                <p class="truncate text-sm font-medium text-gray-900 dark:text-white" :title="item.title">
                  {{ item.title }}
                </p>
                <div class="mt-1 flex flex-wrap items-center gap-1">
                  <span class="rounded px-1 py-0.5 text-[10px] font-medium" :class="riskClass(item.risk)">
                    {{ riskLabel(item.risk) }}
                  </span>
                  <!-- 类型：人工 / 单次 / 每日 -->
                  <span
                    v-if="!item.auto"
                    class="rounded bg-orange-50 px-1 py-0.5 text-[10px] font-medium text-orange-600 dark:bg-orange-900/20 dark:text-orange-400"
                  >{{ t('admin.creditsTasks.typeManual') }}</span>
                  <span
                    v-else
                    class="rounded px-1 py-0.5 text-[10px] font-medium"
                    :class="item.period === 'once'
                      ? 'bg-purple-50 text-purple-600 dark:bg-purple-900/20 dark:text-purple-400'
                      : 'bg-sky-50 text-sky-600 dark:bg-sky-900/20 dark:text-sky-400'"
                  >{{ item.period === 'once' ? t('admin.creditsTasks.typeOnce') : t('admin.creditsTasks.typeDaily') }}</span>
                </div>
              </div>
            </div>

            <div class="mt-2 flex items-end justify-between">
              <div>
                <p class="text-lg font-bold text-emerald-600 dark:text-emerald-400">
                  <template v-if="item.reward > 0">{{ item.reward }}<span class="ml-0.5 text-xs font-normal text-gray-400">{{ t('admin.creditsTasks.creditUnit') }}</span></template>
                  <template v-else-if="item.reward < 0"><span class="text-sm">{{ t('admin.creditsTasks.randomReward') }}</span></template>
                  <template v-else><span class="text-sm text-gray-400">{{ t('admin.creditsTasks.noReward') }}</span></template>
                </p>
                <p class="text-[11px] text-gray-400">
                  {{ item.account_done }} / {{ item.account_total }} {{ t('admin.creditsTasks.completedAccounts') }}
                </p>
              </div>
              <span
                v-if="item.account_claimable > 0"
                class="rounded-full bg-emerald-500 px-2 py-0.5 text-[10px] font-medium text-white"
              >{{ item.account_claimable }} {{ t('admin.creditsTasks.claimable') }}</span>
            </div>

            <!-- 进度条 -->
            <div class="mt-2 h-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-600">
              <div
                class="h-full rounded-full bg-emerald-500 transition-all"
                :style="{ width: donePercent(item) + '%' }"
              />
            </div>

            <!-- 执行模式：自动（勾选后批量执行）/ 手动（仅手动触发） -->
            <div class="mt-2 flex items-center justify-between border-t border-gray-100 pt-2 dark:border-dark-600">
              <span class="text-[10px] text-gray-400">{{ t('admin.creditsTasks.execMode') }}</span>
              <button
                type="button"
                class="flex items-center gap-1 text-[10px] font-medium"
                :disabled="!item.auto"
                :class="item.auto
                  ? 'text-primary-600 dark:text-primary-400'
                  : 'cursor-not-allowed text-gray-300 dark:text-dark-500'"
                @click.stop="toggleAuto(item)"
              >
                <span
                  class="relative inline-flex h-3.5 w-6 items-center rounded-full transition-colors"
                  :class="isAutoMode(item) ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-500'"
                >
                  <span
                    class="inline-block h-2.5 w-2.5 transform rounded-full bg-white transition-transform"
                    :class="isAutoMode(item) ? 'translate-x-3' : 'translate-x-0.5'"
                  />
                </span>
                {{ isAutoMode(item) ? t('admin.creditsTasks.modeAuto') : t('admin.creditsTasks.modeManual') }}
              </button>
            </div>
          </div>
        </div>

        <p v-if="board?.errors?.length" class="mt-3 text-xs text-orange-500">
          {{ board.errors.length }} {{ t('admin.creditsTasks.fetchErrors') }}
        </p>
      </div>

      <!-- 任务详情弹窗 -->
      <TaskDetailDialog
        :show="detailVisible"
        :detail="detail"
        :loading="detailLoading"
        :running="running"
        :auto-mode="detailAutoMode"
        @close="detailVisible = false"
        @run="runSelected"
      />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import TaskDetailDialog from '@/components/admin/credits/TaskDetailDialog.vue'
import { useAppStore } from '@/stores/app'
import {
  getWorkBuddyTaskBoard,
  getWorkBuddyTaskDetail,
  postWorkBuddyRunTaskAll,
  postWorkBuddyExecuteTasks,
  getWorkBuddyTaskSummary,
  getWorkBuddyCreditSnapshot,
  type WorkBuddyTaskBoard,
  type WorkBuddyTaskBoardItem,
  type WorkBuddyTaskDetail,
  type WorkBuddyTaskTrendPoint,
  type WorkBuddyCreditSummary,
  type WorkBuddyCreditSnapshot
} from '@/api/admin/workbuddy'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const running = ref(false)
const board = ref<WorkBuddyTaskBoard | null>(null)
const trend = ref<WorkBuddyTaskTrendPoint[]>([])
const trendDays = ref(7)
const summary = ref<{ credits: WorkBuddyCreditSummary } | null>(null)
const cloud = ref<WorkBuddyCreditSnapshot | null>(null)

const selectedTasks = ref<Set<string>>(new Set())
// autoModes 记录每个任务的执行模式：true=自动（参与批量执行），false=手动。
const autoModes = reactive<Record<string, boolean>>({})

const detailVisible = ref(false)
const detailLoading = ref(false)
// 打开弹窗时，把卡片上该任务的执行方式一并传进去，保证两处显示一致。
const detailAutoMode = computed(() => {
  const code = detail.value?.code
  if (!code) return undefined
  const item = (board.value?.items ?? []).find((x) => x.code === code)
  return item ? isAutoMode(item) : undefined
})
const detail = ref<WorkBuddyTaskDetail | null>(null)

const chartW = 800
const chartH = 160

const overviewCards = computed<Array<{ key: string; label: string; value: string; hint: string; icon: 'clock' | 'lightbulb' | 'chart' | 'trophy'; color: string; bg: string }>>(() => {
  // 概览数字以云端快照为准：积分可能来自任何终端，本地台账只反映本系统的
  // 执行记录，不能作为「今日/累计」的依据。
  const snap = cloud.value
  const todayCheckin = (snap?.accounts ?? []).reduce(
    (n, a) => n + (a.today_checked_in ? a.daily_credit : 0),
    0
  )
  // 任务积分同样取云端：账号可能在别处完成任务，本地台账不可作准。
  const todayTask = snap?.today_task_credits ?? 0
  const c = {
    today_checkin_credit: todayCheckin,
    today_task_credit: todayTask,
    today_total_credit: todayCheckin + todayTask,
    total_credit: snap?.total_credits ?? 0
  }
  // 快照尚未就绪时显示占位符而不是 0：0 是真实值，用它做占位会误导。
  const ready = !!snap && snap.accounts.length > 0
  const fmt = (v: number) => (ready ? v.toFixed(0) : '—')
  return [
    {
      key: 'checkin',
      label: t('admin.creditsTasks.checkinCredit'),
      value: fmt(c?.today_checkin_credit ?? 0),
      hint: t('admin.creditsTasks.todayHint'),
      icon: 'clock',
      color: 'text-cyan-600 dark:text-cyan-400',
      bg: 'bg-cyan-50 dark:bg-cyan-900/20'
    },
    {
      key: 'task',
      label: t('admin.creditsTasks.taskCredit'),
      value: fmt(c?.today_task_credit ?? 0),
      hint: t('admin.creditsTasks.todayHint'),
      icon: 'lightbulb',
      color: 'text-emerald-600 dark:text-emerald-400',
      bg: 'bg-emerald-50 dark:bg-emerald-900/20'
    },
    {
      key: 'today',
      label: t('admin.creditsTasks.todayCredit'),
      value: fmt(c?.today_total_credit ?? 0),
      hint: t('admin.creditsTasks.todaySumHint'),
      icon: 'chart',
      color: 'text-primary-600 dark:text-primary-400',
      bg: 'bg-primary-50 dark:bg-primary-900/20'
    },
    {
      key: 'total',
      label: t('admin.creditsTasks.totalCredit'),
      value: fmt(c?.total_credit ?? 0),
      hint: t('admin.creditsTasks.totalHint'),
      icon: 'trophy',
      color: 'text-amber-600 dark:text-amber-400',
      bg: 'bg-amber-50 dark:bg-amber-900/20'
    }
  ]
})

const tasksAllSelected = computed(() => {
  const items = board.value?.items ?? []
  return items.length > 0 && selectedTasks.value.size === items.length
})
const tasksPartiallySelected = computed(
  () => selectedTasks.value.size > 0 && !tasksAllSelected.value
)

function riskLabel(risk: string): string {
  const map: Record<string, string> = {
    low: t('admin.creditsTasks.riskLow'),
    medium: t('admin.creditsTasks.riskMedium'),
    elevated: t('admin.creditsTasks.riskElevated'),
    high: t('admin.creditsTasks.riskHigh')
  }
  return map[risk] ?? risk
}

function riskClass(risk: string): string {
  switch (risk) {
    case 'high':
      return 'bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-400'
    case 'elevated':
      return 'bg-orange-50 text-orange-600 dark:bg-orange-900/20 dark:text-orange-400'
    case 'medium':
      return 'bg-amber-50 text-amber-600 dark:bg-amber-900/20 dark:text-amber-400'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
  }
}

// iconName 按任务语义挑选内置图标。
//
// 关键词顺序即优先级：先认具体行为（签到/抽奖/聊天/分享），
// 再退回按周期与风险分类，保证每个任务都有明确图标、没有空白。
function iconName(item: WorkBuddyTaskBoardItem): 'calendar' | 'gift' | 'cube' | 'upload' | 'chatBubble' | 'grid' | 'sparkles' | 'bolt' | 'book' | 'beaker' | 'mail' | 'trophy' | 'checkCircle' | 'lightbulb' {
  const c = item.code.toLowerCase()
  if (c.includes('checkin')) return 'calendar'
  if (c.includes('lottery')) return 'gift'
  if (c.includes('blindbox') || c.includes('buddy')) return 'cube'
  if (c.includes('share') || c.includes('invite')) return 'upload'
  if (c.includes('chat')) return 'chatBubble'
  if (c.includes('template')) return 'grid'
  if (c.includes('expert') || c.includes('skill')) return 'sparkles'
  if (c.includes('automation')) return 'bolt'
  if (c.includes('library') || c.includes('read')) return 'book'
  if (c.includes('canvas')) return 'beaker'
  if (c.includes('wechat') || c.includes('subscribe')) return 'mail'
  if (c.includes('badge') || c.includes('trophy')) return 'trophy'
  if (item.period === 'once') return 'checkCircle'
  return 'lightbulb'
}

// iconBg 返回图标底衬的浅色（与顶部概览卡同风格：浅底 + 同色图形）。
function iconBg(item: WorkBuddyTaskBoardItem): string {
  if (item.risk === 'high') return 'bg-red-50 dark:bg-red-900/20'
  if (item.risk === 'elevated') return 'bg-orange-50 dark:bg-orange-900/20'
  if (item.code.includes('checkin')) return 'bg-cyan-50 dark:bg-cyan-900/20'
  if (item.code.includes('lottery') || item.code.includes('blindbox')) return 'bg-amber-50 dark:bg-amber-900/20'
  if (item.period === 'once') return 'bg-purple-50 dark:bg-purple-900/20'
  return 'bg-primary-50 dark:bg-primary-900/20'
}

// iconColor 按任务类别着色，与顶部概览卡保持同一套色板。
function iconColor(item: WorkBuddyTaskBoardItem): string {
  // 与顶部四项数据的图标用同一套色阶（*-600 / dark:*-400）。
  if (item.risk === 'high') return 'text-red-600 dark:text-red-400'
  if (item.risk === 'elevated') return 'text-orange-600 dark:text-orange-400'
  if (item.code.includes('checkin')) return 'text-cyan-600 dark:text-cyan-400'
  if (item.code.includes('lottery') || item.code.includes('blindbox')) return 'text-amber-600 dark:text-amber-400'
  if (item.period === 'once') return 'text-purple-600 dark:text-purple-400'
  return 'text-primary-600 dark:text-primary-400'
}

function donePercent(item: WorkBuddyTaskBoardItem): number {
  if (!item.account_total) return 0
  return Math.round((item.account_done / item.account_total) * 100)
}

// polyline 生成一条折线的 SVG 点串。
function polyline(field: 'checkin' | 'task' | 'total'): string {
  const pts = trend.value
  if (pts.length === 0) return ''
  const values = pts.map((p) => {
    if (field === 'checkin') return p.checkin_credit
    if (field === 'task') return p.task_credit
    return p.total_credit
  })
  const max = Math.max(...values, 1)
  const stepX = pts.length > 1 ? chartW / (pts.length - 1) : chartW
  return values
    .map((v, i) => {
      const x = i * stepX
      const y = chartH - (v / max) * (chartH - 8) - 4
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

function toggleTask(code: string) {
  const next = new Set(selectedTasks.value)
  if (next.has(code)) next.delete(code)
  else next.add(code)
  selectedTasks.value = next
}

function toggleSelectAllTasks() {
  if (tasksAllSelected.value) {
    selectedTasks.value = new Set()
  } else {
    selectedTasks.value = new Set((board.value?.items ?? []).map((x) => x.code))
  }
}

// toggleAuto 切换任务的执行模式：自动参与批量执行，手动只能单独触发。
function toggleAuto(item: WorkBuddyTaskBoardItem) {
  if (!item.auto) return
  // 单次任务已完成：不允许切回自动。
  if (item.period === 'once' && item.account_done >= item.account_total) return
  autoModes[item.code] = autoModes[item.code] === false
}

// isAutoMode 返回任务的执行模式。
//
// 默认值：高风险任务默认手动（不参与批量执行），其余默认自动。
// 高风险多涉及分享/邀请/跨端行为，批量触发容易被上游判异常。
function isAutoMode(item: WorkBuddyTaskBoardItem): boolean {
  // 单次任务全部完成后自动转为手动：没有可执行的账号了，
  // 继续显示「自动」会让用户以为还会批量执行。
  if (item.period === 'once' && item.account_done >= item.account_total) {
    return false
  }
  const explicit = autoModes[item.code]
  if (explicit !== undefined) return explicit
  return item.risk !== 'high'
}

// isRunnable 判断任务在当前模式下是否会被批量执行。
function isRunnable(item: WorkBuddyTaskBoardItem): boolean {
  return item.auto && isAutoMode(item)
}

async function loadBoard() {
  try {
    board.value = await getWorkBuddyTaskBoard()
  } catch (e: any) {
    appStore.showError(e?.message || 'failed')
  }
}

// loadCloud 拉取云端快照。
//
// sync=true 时后台会重新同步上游，但接口立刻返回当前快照（可能还是空的）。
// 首次进入页面快照为空，需要短轮询等到这一轮同步完成——否则概览会先闪
// 一下全 0，让人误以为积分丢了。
// loadCloud 拉取云端快照。
//
// 之前是「空就每 1.5s 轮询、最多 10 次」——首屏要等十几秒才出数字。
// 现在改为：先立即展示已有快照（哪怕为空也先把骨架渲染出来），
// 再触发一次后台同步，并用 800ms 的短轮询等它落定。同步通常在 2-3 秒
// 内完成，用户几乎感觉不到等待。
// loadCloud 拉取云端快照。
//
// 后端在无数据时会同步跑一轮同步再返回（最多 20 秒），所以这里一次请求
// 就能拿到真实数字，不需要前端轮询等待。
async function loadCloud(sync = false) {
  try {
    const snap = await getWorkBuddyCreditSnapshot(sync)
    cloud.value = snap
    if (snap.accounts.length > 0) loadTrend()
  } catch {
    cloud.value = null
  }
}

async function loadSummary() {
  try {
    summary.value = await getWorkBuddyTaskSummary()
  } catch {
    summary.value = null
  }
}

// loadTrend 直接取云端快照里的每日活跃分。
//
// 之前读本地台账，只有本系统执行过的操作才有数据，所以曲线永远是平的。
// heatmap 是上游按天统计的活动记录，能真实反映每天发生了什么。
async function loadTrend(days = trendDays.value) {
  const daily = cloud.value?.daily ?? []
  const slice = daily.slice(-days)
  trend.value = slice.map((d) => ({
    date: d.date,
    checkin_credit: d.score,
    task_credit: 0,
    total_credit: d.score
  }))
}

async function switchTrend(days: number) {
  trendDays.value = days
  loadTrend(days)
}

async function loadAll() {
  loading.value = true
  try {
    await Promise.all([loadBoard(), loadSummary(), loadTrend(), loadCloud(true)])
  } finally {
    loading.value = false
  }
}

async function openDetail(item: WorkBuddyTaskBoardItem) {
  detailVisible.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await getWorkBuddyTaskDetail(item.code)
  } catch (e: any) {
    appStore.showError(e?.message || 'failed')
    detailVisible.value = false
  } finally {
    detailLoading.value = false
  }
}

// runSelected 在弹窗里勾选的账号上执行当前任务。
async function runSelected(code: string, accountIds: number[], auto: boolean) {
  running.value = true
  try {
    const res = await postWorkBuddyRunTaskAll(code, accountIds, auto)
    appStore.showSuccess(
      t('admin.creditsTasks.runResult', {
        success: res.success,
        failed: res.failed,
        credit: res.credit.toFixed(0)
      })
    )
    await Promise.all([loadBoard(), loadSummary(), loadTrend(), loadCloud(true)])
    if (detailVisible.value && detail.value) {
      detail.value = await getWorkBuddyTaskDetail(code)
    }
  } catch (e: any) {
    appStore.showError(e?.message || 'failed')
  } finally {
    running.value = false
  }
}

// runSelectedTasks 执行勾选的任务；手动模式的任务跳过。
async function runSelectedTasks() {
  const items = (board.value?.items ?? []).filter(
    (x) => selectedTasks.value.has(x.code) && isRunnable(x)
  )
  if (items.length === 0) {
    appStore.showError(t('admin.creditsTasks.noRunnableSelected'))
    return
  }
  running.value = true
  let ok = 0
  let fail = 0
  let credit = 0
  try {
    // 走执行接口：先上报事件推进任务，达标后自动领奖。
    // 只勾选任务不选账号时，后端会对全部账号执行。
    for (const item of items) {
      const results = await postWorkBuddyExecuteTasks([item.code], [], true, false)
      for (const list of Object.values(results)) {
        for (const r of list) {
          if (r.outcome === 'claimed') {
            ok++
            credit += r.credit
          } else if (r.outcome === 'failed') {
            fail++
          }
        }
      }
    }
    appStore.showSuccess(
      t('admin.creditsTasks.runResult', { success: ok, failed: fail, credit: credit.toFixed(0) })
    )
    selectedTasks.value = new Set()
    await Promise.all([loadBoard(), loadSummary(), loadTrend(), loadCloud(true)])
  } catch (e: any) {
    appStore.showError(e?.message || 'failed')
  } finally {
    running.value = false
  }
}


// 自动刷新：页面打开期间每 20 秒静默拉一次看板与快照。
//
// 后端已经做了缓存 + 后台刷新，这里的轮询主要是把「后台刷新的结果」
// 及时反映到界面上——用户不需要手动点刷新就能看到新完成的任务。
let pollTimer: ReturnType<typeof setInterval> | null = null

// 每隔几轮轮询就带一次 sync=1，让后端真的去上游取数。
//
// 只读缓存的轮询只能看到「后台刷新完成」的结果，而后台同步周期是 5 分钟、
// Redis 快照 TTL 是 10 分钟：碰上跨零点、或者任务刚在别的终端领完，
// 顶部四个数字可以一直停在旧值上。每 3 轮（约 1 分钟）主动同步一次，
// 既能把延迟压到可接受范围，又不会把上游打爆。
let pollTick = 0
// lastSeenDay 记录页面上次看到的日期，用来识别跨零点。
let lastSeenDay = ''

function dayKey(): string {
  const d = new Date()
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

function startPolling() {
  stopPolling()
  pollTick = 0
  lastSeenDay = dayKey()
  pollTimer = setInterval(() => {
    if (document.hidden) return
    // 跨零点：今日签到 / 今日任务都要重新从上游取，缓存里还是昨天的数字。
    const crossedMidnight = dayKey() !== lastSeenDay
    if (crossedMidnight) {
      lastSeenDay = dayKey()
      pollTick = 0
    }
    pollTick++
    void loadBoard()
    void loadCloud(crossedMidnight || pollTick % 3 === 0)
  }, 20000)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

onMounted(() => {
  void loadAll()
  startPolling()
})

onBeforeUnmount(stopPolling)
</script>
