<template>
  <div class="flex flex-col gap-0.5 text-xs" data-testid="account-credits-cell">
    <span v-if="loading" class="text-gray-400">{{ t('common.loading') }}</span>
    <span v-else-if="error" class="text-red-500" :title="error">{{ t('admin.accounts.creditsUnavailable') }}</span>
    <template v-else-if="credits">
      <div class="flex items-center gap-1">
        <span class="font-medium text-gray-900 dark:text-white">
          {{ formatValue(credits.total) }}
        </span>
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.creditsUnit') }}</span>
        <span
          v-if="credits.package_name"
          class="rounded bg-gray-100 px-1 py-0.5 text-[10px] text-gray-600 dark:bg-dark-600 dark:text-gray-300"
        >{{ credits.package_name }}</span>
      </div>
<!-- 趋势图：有数据时画柱状，无数据时画等高灰色条，
           保证每个账号视觉高度一致、不出现空行。 -->
      <div
        class="flex h-[22px] items-end gap-0.5"
        :title="trendAvailable ? trendTitle : t('admin.accounts.creditsNoTrend')"
      >
        <div
          v-for="(h, i) in barHeights"
          :key="i"
          class="w-1 rounded-sm transition-colors"
          :class="trendAvailable ? 'bg-emerald-400/80 dark:bg-emerald-500/70' : 'bg-gray-200 dark:bg-dark-600'"
          :style="{ height: h + 'px' }"
        />
      </div>

      <div class="flex items-center gap-2">
        <button
          type="button"
          class="text-[11px] text-primary-600 hover:underline dark:text-primary-400"
          @click="emit('show-detail', { id: props.accountId })"
        >{{ t('admin.accounts.creditsDetail') }}</button>
        <button
          type="button"
          class="text-[11px] text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400"
          :disabled="loading"
          @click="load"
        >{{ t('admin.accounts.creditsRefresh') }}</button>
      </div>
    </template>
    <span v-else class="text-gray-400">—</span>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getWorkBuddyCredits,
  getWorkBuddyCreditTrend,
  type WorkBuddyCredits,
  type WorkBuddyCreditDailySummary
} from '@/api/admin/workbuddy'
import { fetchCredits as fetchTraeCredits } from '@/api/admin/trae'
import { getMarvisCredits, type MarvisCredits } from '@/api/admin/marvis'

const props = defineProps<{ accountId: number; platform: string; accountName?: string }>()
const emit = defineEmits<{ 'show-detail': [{ id: number }] }>()
const { t } = useI18n()

const credits = ref<WorkBuddyCredits | null>(null)
const loading = ref(false)
const error = ref('')
// trend 是近 14 天积分消耗，用于缩略图。
const trend = ref<WorkBuddyCreditDailySummary[]>([])

// trendAvailable 为 false 时画统一高度的灰色占位条，
// 保证账号之间视觉一致，不会因为没数据而高度参差。
const trendAvailable = computed(() => trend.value.some((d) => d.credits > 0))

// barHeights 返回 14 根柱子的像素高度。
// 有数据按最大值归一化；无数据时全部 4px，形成一条平齐的灰色带。
const barHeights = computed(() => {
  const pts = trend.value
  if (!trendAvailable.value || pts.length === 0) {
    return Array.from({ length: 14 }, () => 4)
  }
  const vals = pts.map((p) => p.credits)
  const max = Math.max(...vals, 1)
  return vals.map((v) => Math.max(2, Math.round((v / max) * 18)))
})

const trendTitle = computed(() =>
  trend.value
    .map((d) => `${d.date} ${Math.round(d.credits)}`)
    .join('\n')
)



const traeUnlimited = ref(false)

function formatValue(v: number | undefined): string {
  if (v == null) return '—'
  return v.toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: 2 })
}

async function load() {
  loading.value = true
  error.value = ''
  // Trae：额度来自上游（国内站积分 / 国际站美元）。
  // 折算成积分这一步必须由后端完成（TraeCreditsAsPoints，1 美元 = 100 积分），
  // 前端拿到的 limit/used/available 已经是积分。
  // 早先这里直接用原生 limit - used 当积分显示，国际站账号就变成了
  // 「1 美元 = 1 积分」，与明细弹窗口径对不上。
  if (props.platform === 'trae_cn' || props.platform === 'trae_intl') {
    try {
      const c = await fetchTraeCredits(props.accountId)
      traeUnlimited.value = c.unlimited === true
      credits.value = {
        account_id: props.accountId,
        platform: props.platform,
        total: c.unlimited ? 0 : Math.max(0, c.available ?? (c.limit ?? 0) - (c.used ?? 0)),
        groups: [],
        package_name: c.plan || '',
        credits_info_available: true
      } as unknown as WorkBuddyCredits
      trend.value = []
      return
    } catch (e: any) {
      error.value = e?.message || 'failed'
      credits.value = null
      return
    } finally {
      loading.value = false
    }
  }

  // Marvis 走自己的钱包接口：它按 token 计量，没有 WorkBuddy 那套分组积分。
  if (props.platform === 'marvis') {
    try {
      const m: MarvisCredits = await getMarvisCredits(props.accountId)
      credits.value = {
        account_id: m.account_id,
        account: m.account,
        platform: m.platform,
        // 账号列表这一格要显示「还剩多少」，不是「总额」。
        // WorkBuddy 分支算的也是可用量（total - used），这里保持一致。
        total: m.available ?? Math.max(0, (m.total ?? 0) - (m.used ?? 0)),
        groups: m.groups ?? [],
        package_name: m.package_name,
        is_paid_user: m.is_paid_user,
        credits_info_available: m.credits_info_available
      } as unknown as WorkBuddyCredits
      trend.value = []
      return
    } catch (e: any) {
      error.value = e?.message || 'failed'
      credits.value = null
      return
    } finally {
      loading.value = false
    }
  }
  try {
    const [c, tr] = await Promise.allSettled([
      getWorkBuddyCredits(props.accountId),
      getWorkBuddyCreditTrend(props.accountId, 14)
    ])
    if (c.status === 'fulfilled') {
      credits.value = c.value
      if (c.value?.error) error.value = c.value.error
    } else {
      throw c.reason
    }
    trend.value = tr.status === 'fulfilled' ? tr.value : []
  } catch (e: any) {
    error.value = e?.message || 'failed'
    credits.value = null
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.accountId, load)
</script>
