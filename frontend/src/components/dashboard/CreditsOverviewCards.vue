<template>
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <div v-for="card in cards" :key="card.key" class="card p-4">
      <!-- 与原生指标卡同构：图标在左（p-2 圆角块），文本在右 -->
      <div class="flex items-center gap-3">
        <div class="rounded-lg p-2" :class="card.bg">
          <!-- 四张卡都绑定一个平台，统一用官方 mark，不再混用线条图标 -->
          <PlatformIcon :platform="card.platform" size="md" class="text-gray-700 dark:text-gray-200" />
        </div>
        <div class="min-w-0">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ card.label }}</p>
          <!-- 未就绪时用骨架条而不是「—」：占位更直观，也不会闪一下破折号 -->
          <div v-if="!ready" class="my-1 h-5 w-20 animate-pulse rounded bg-gray-200 dark:bg-dark-600"></div>
          <!-- 不用 truncate：数值被截成省略号比换行更难读。 -->
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
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import {
  getWorkBuddyCreditSnapshot,
  getMarvisCreditSnapshot,
  getTraeCreditSnapshot,
  type WorkBuddyCreditSnapshot,
  type MarvisCreditSnapshot,
  type TraeCreditSnapshot
} from '@/api/admin/workbuddy'

// 积分体系四卡：
//
//   1 WorkBuddy 今日积分  今日签到 + 今日任务
//   2 WorkBuddy 总积分    副标题给「今日数据：总量 - 今日消费」
//   3 Marvis 总积分       副标题按授权方式（QQ / 微信）拆分余额
//   4 Trae 总积分         副标题给今日消费
//
// 数据全部来自上游快照，不读本地台账——账号可能在客户端或别处签到/做任务，
// 只有上游数据是准确的。三个平台都按「每次刷新现拉上游」取数，上游每日凌晨
// 自行重置额度，因此不需要任何本地重置逻辑。
//
// 管理端与用户端共用这一个组件，保证两处口径一致。

const { t } = useI18n()
const snap = ref<WorkBuddyCreditSnapshot | null>(null)
const marvis = ref<MarvisCreditSnapshot | null>(null)
const trae = ref<TraeCreditSnapshot | null>(null)

/** 千分位 + 两位小数。 */
function fmt(v: number): string {
  return (Number.isFinite(v) ? v : 0).toLocaleString('zh-CN', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2
  })
}

/** 积分单位文案（全平台统一「积分」）。 */
const unit = computed(() => t('admin.accounts.creditsUnit'))

// 三个快照都到齐才算就绪：只到一部分会让合计与今日数据短暂偏小。
// 某平台没有账号时后端返回空列表（仍是有效快照），不会被误判成未就绪。
const ready = computed(() => snap.value !== null && marvis.value !== null && trae.value !== null)

type Card = {
  key: string
  label: string
  value: string
  hint: string
  // 每张卡都对应一个平台，图标统一取该平台的官方 mark。
  platform: 'workbuddy_cn' | 'marvis' | 'trae_cn'
  // 数值与副标题的颜色，按平台区分，便于扫读。
  color: string
  bg: string
}

const cards = computed<Card[]>(() => {
  const wb = snap.value
  const mv = marvis.value
  const tr = trae.value

  const todayCheckin = (wb?.accounts ?? []).reduce(
    (n, a) => n + (a.today_checked_in ? a.daily_credit : 0),
    0
  )
  const todayTask = wb?.today_task_credits ?? 0
  const wbTotal = wb?.total_credits ?? 0
  const wbTodayCost = todayCheckin + todayTask

  // Marvis 卡片显示「所有账号的余额之和」，不是总额度之和。
  // available_credits 是各账号钱包余额（avail_tokens）的累加，
  // total_credits 是额度上限的累加，口径不同。
  const mvTotal = mv?.available_credits ?? 0

  // Trae：额度来自上游（国内站积分 / 国际站美元），后端已折算成积分
  // （1 美元 = 100 积分），这里直接用。
  // Trae 卡片显示「两个站点所有账号的余额之和」，不是额度上限之和。
  const trTotal = tr?.available_credits ?? 0

  return [
    {
      key: 'wb-today',
      label: t('admin.accounts.creditsTodayWorkBuddy'),
      value: fmt(wbTodayCost),
      // 两个分项是相加得出主数值的关系，用 + 而不是 · 更直观。
      hint: `${t('admin.accounts.creditsCheckinShort')}${fmt(todayCheckin)} + ${t('admin.accounts.creditsTaskShort')}${fmt(todayTask)}`,
      platform: 'workbuddy_cn',
      color: 'text-primary-600 dark:text-primary-400',
      bg: 'bg-primary-50 dark:bg-primary-900/20'
    },
    {
      key: 'wb-total',
      label: t('admin.accounts.creditsTotalWorkBuddy'),
      value: fmt(wbTotal),
      // 副标题第二行 = 国内版所有账号总余额 + 国际版所有账号总余额。
      // 两个数都直接来自快照的站点拆分字段，不在前端做减法。
      hint: `${t('admin.accounts.creditsCNTotal')} ${fmt(Number(wb?.total_credits_cn ?? 0))} ${unit.value} + ${t('admin.accounts.creditsIntlTotal')} ${fmt(Number(wb?.total_credits_intl ?? 0))} ${unit.value}`,
      platform: 'workbuddy_cn',
      color: 'text-emerald-600 dark:text-emerald-400',
      bg: 'bg-emerald-50 dark:bg-emerald-900/20'
    },
    {
      key: 'marvis',
      label: t('admin.accounts.creditsMarvisTotal'),
      // 换算成积分后量级在万级，K 缩写反而不直观，直接两位小数完整显示。
      value: fmt(mvTotal),
      // 副标题按授权方式拆分余额：QQ 登录的账号之和、微信登录的账号之和。
      // 两个数都由后端按 login_type 分别累加，前端只做展示。
      hint: `QQ：${fmt(Number(mv?.available_qq ?? 0))} ${unit.value} + 微信：${fmt(Number(mv?.available_wx ?? 0))} ${unit.value}`,
      platform: 'marvis',
      color: 'text-cyan-600 dark:text-cyan-400',
      bg: 'bg-cyan-50 dark:bg-cyan-900/20'
    },
    {
      key: 'trae',
      label: t('admin.accounts.creditsTraeTotal'),
      // 全部平台统一两位小数完整显示，不再用 K 缩写。
      value: fmt(trTotal),
      // 参考 WorkBuddy 卡片：副标题按站点拆分余额，两个数都由后端分别累加。
      hint: `${t('admin.accounts.creditsCNTotal')} ${fmt(Number(tr?.available_cn ?? 0))} ${unit.value} + ${t('admin.accounts.creditsIntlTotal')} ${fmt(Number(tr?.available_intl ?? 0))} ${unit.value}`,
      platform: 'trae_cn',
      color: 'text-amber-600 dark:text-amber-400',
      bg: 'bg-amber-50 dark:bg-amber-900/20'
    }
  ]
})

/** 归一化快照：后端异常返回时兜底成空快照，避免卡片永远停在骨架。 */
function normalizeWb(s: WorkBuddyCreditSnapshot | null): WorkBuddyCreditSnapshot | null {
  if (!s) return null
  return { ...s, accounts: s.accounts ?? [] }
}

// load 读取三个平台的积分快照。
//
// 首次无数据时带 sync=1 让后端同步跑一轮再返回，否则仪表盘会先显示骨架条。
// 三个请求并发，不串行等。失败时保留上一次的值，避免刷新时闪回骨架。
async function load(forceSync = false) {
  const [wb, mv, tr] = await Promise.all([
    (async () => {
      try {
        let s = await getWorkBuddyCreditSnapshot(forceSync)
        if ((s.accounts?.length ?? 0) === 0 && !forceSync) s = await getWorkBuddyCreditSnapshot(true)
        return normalizeWb(s)
      } catch {
        return snap.value
      }
    })(),
    (async () => {
      try {
        let s = await getMarvisCreditSnapshot(forceSync)
        if ((s.accounts?.length ?? 0) === 0 && !forceSync) s = await getMarvisCreditSnapshot(true)
        return s
      } catch {
        return marvis.value
      }
    })(),
    (async () => {
      try {
        let s = await getTraeCreditSnapshot(forceSync)
        if ((s.accounts?.length ?? 0) === 0 && !forceSync) s = await getTraeCreditSnapshot(true)
        return s
      } catch {
        return trae.value
      }
    })()
  ])
  if (wb) snap.value = wb
  if (mv) marvis.value = mv
  if (tr) trae.value = tr
}

// 定时刷新：与频道状态页、账号列表用同一套 useAutoRefresh，
// 间隔可配置并记住用户选择。页面不可见时暂停，避免后台空转。
const autoRefresh = useAutoRefresh({
  storageKey: 'credits-overview-auto-refresh',
  intervals: [30, 60, 300] as const,
  defaultInterval: 60,
  defaultEnabled: true,
  // 定时刷新走 sync=1：积分来自上游，缓存值会让人误以为没在刷新。
  onRefresh: () => load(true),
  shouldPause: () => document.hidden
})

onMounted(() => {
  void load()
  // useAutoRefresh 只负责管理状态，定时器要显式启动。
  if (autoRefresh.enabled.value) autoRefresh.start()
})

// 供「刷新」按钮与其它组件调用。
defineExpose({ refresh: () => load(true) })
</script>
