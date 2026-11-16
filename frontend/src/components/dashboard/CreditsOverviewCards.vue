<template>
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <div v-for="card in cards" :key="card.key" class="card p-4">
      <!-- 与原生指标卡同构：图标在左（p-2 圆角块），文本在右 -->
      <div class="flex items-center gap-3">
        <div class="rounded-lg p-2" :class="card.bg">
          <!-- 平台卡用官方 mark，其余沿用线条图标 -->
          <PlatformIcon
            v-if="card.platform"
            :platform="card.platform"
            size="md"
            :class="card.color"
          />
          <Icon v-else :name="card.icon || 'chart'" size="md" :class="card.color" :stroke-width="2" />
        </div>
        <div class="min-w-0">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ card.label }}</p>
          <!-- 未就绪时用骨架条而不是「—」：占位更直观，也不会闪一下破折号 -->
          <div v-if="!ready" class="my-1 h-5 w-20 animate-pulse rounded bg-gray-200 dark:bg-dark-600"></div>
          <!-- 不用 truncate：数值被截成省略号比换行更难读。
               万/亿分级已把最长宽度压到 11 字符左右，配合小屏字号收缩即可完整显示。 -->
          <p v-else class="whitespace-nowrap text-base font-bold leading-tight sm:text-xl" :class="card.color">
            {{ card.value }}
          </p>
          <p v-if="ready" class="text-xs text-gray-400">{{ card.hint }}</p>
          <div v-else class="mt-1 h-3 w-24 animate-pulse rounded bg-gray-200 dark:bg-dark-600"></div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import {
  getWorkBuddyCreditSnapshot,
  getMarvisCreditSnapshot,
  type WorkBuddyCreditSnapshot,
  type MarvisCreditSnapshot
} from '@/api/admin/workbuddy'

// 积分体系四卡：
//
//   1 WorkBuddy 今日积分  今日签到 + 今日任务
//   2 WorkBuddy 总积分    全部 WorkBuddy 账号当前可用积分之和
//   3 Marvis 积分         Marvis 总额度（副标题给今日消耗）
//   4 平台总积分          WorkBuddy 总积分 + Marvis 可用积分
//
// 数据全部来自上游快照，不读本地台账——账号可能在客户端或别处签到/做任务，
// 只有上游数据是准确的。管理端与用户端共用这一个组件，保证两处口径一致。

const { t } = useI18n()
const snap = ref<WorkBuddyCreditSnapshot | null>(null)
const marvis = ref<MarvisCreditSnapshot | null>(null)

// 数字格式化：千分位 + 两位小数。
function fmt(v: number): string {
  return v.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

// 卡片里的数值格式化：统一千分位 + 两位小数。
//
// 四张卡片口径一致，量级差异交给千分位表达（9,244.00 / 7,646.00），
// 不再对数值切换 K / M 单位——同一排卡片里混用单位会让大小难以横向比较。
function fmtCard(v: number): string {
  return fmt(v)
}

// 第 3、4 张卡是 Marvis 与合计，量级可达千万，用 K 压缩显示。
// 小数位保持两位：1,234,567 → 1,234.57K，量级与精度兼顾。
function fmtCardK(v: number): string {
  return `${fmt(v / 1_000)}K`
}

// 两个快照都到齐才算就绪：只到一半会让第 4 张卡短暂显示偏小的合计数。
const ready = computed(() => {
  const wb = snap.value
  const mv = marvis.value
  if (!wb || wb.accounts.length === 0) return false
  // Marvis 没有账号时也算就绪（总量为 0），否则只接 WorkBuddy 的部署会
  // 永远停在骨架态。
  return mv !== null
})

type Card = {
  key: string
  label: string
  value: string
  hint: string
  platform?: 'workbuddy_cn' | 'marvis'
  icon?: 'chart' | 'trophy' | 'clock' | 'grid'
  color: string
  bg: string
}

const cards = computed<Card[]>(() => {
  const wb = snap.value
  const mv = marvis.value

  const todayCheckin = (wb?.accounts ?? []).reduce(
    (n, a) => n + (a.today_checked_in ? a.daily_credit : 0),
    0
  )
  const todayTask = wb?.today_task_credits ?? 0
  const wbTotal = wb?.total_credits ?? 0
  const mvTotal = mv?.total_credits ?? 0

  return [
    {
      key: 'wb-today',
      label: t('admin.accounts.creditsTodayWorkBuddy'),
      value: fmt(todayCheckin + todayTask),
      hint: `${t('admin.accounts.creditsCheckinShort')}${fmt(todayCheckin)} · ${t('admin.accounts.creditsTaskShort')}${fmt(todayTask)}`,
      icon: 'chart',
      color: 'text-primary-600 dark:text-primary-400',
      bg: 'bg-primary-50 dark:bg-primary-900/20'
    },
    {
      key: 'wb-total',
      label: t('admin.accounts.creditsTotalWorkBuddy'),
      value: fmt(wbTotal),
      hint: t('admin.accounts.creditsWorkBuddyAccountsHint'),
      icon: 'trophy',
      color: 'text-emerald-600 dark:text-emerald-400',
      bg: 'bg-emerald-50 dark:bg-emerald-900/20'
    },
    {
      key: 'marvis',
      label: t('admin.accounts.creditsMarvis'),
      value: fmtCardK(mvTotal),
      hint: t('admin.accounts.creditsMarvisAvailableHint'),
      icon: 'clock',
      color: 'text-cyan-600 dark:text-cyan-400',
      bg: 'bg-cyan-50 dark:bg-cyan-900/20'
    },
    {
      key: 'all',
      label: t('admin.accounts.creditsAllPlatforms'),
      value: fmtCardK(wbTotal + mvTotal),
      hint: t('admin.accounts.creditsAllPlatformsHint'),
      icon: 'grid',
      color: 'text-amber-600 dark:text-amber-400',
      bg: 'bg-amber-50 dark:bg-amber-900/20'
    }
  ]
})

// load 读取两个平台的积分快照。
//
// 首次无数据时带 sync=1 让后端同步跑一轮再返回，否则仪表盘会先显示
// 骨架条，用户以为功能坏了。两个请求并发，不串行等。
async function load() {
  const [wb, mv] = await Promise.all([
    (async () => {
      try {
        let s = await getWorkBuddyCreditSnapshot(false)
        if (s.accounts.length === 0) s = await getWorkBuddyCreditSnapshot(true)
        return s
      } catch {
        return null
      }
    })(),
    (async () => {
      try {
        let s = await getMarvisCreditSnapshot(false)
        if (s.accounts.length === 0) s = await getMarvisCreditSnapshot(true)
        return s
      } catch {
        return null
      }
    })()
  ])
  snap.value = wb
  marvis.value = mv
}

onMounted(load)
</script>
