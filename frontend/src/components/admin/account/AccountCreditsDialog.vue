<template>
  <BaseDialog :show="show" :title="t('admin.accounts.creditsDetailTitle')" width="normal" @close="emit('close')">
    <div v-if="loading" class="py-10 text-center text-sm text-gray-400">{{ t('common.loading') }}</div>
    <div v-else-if="error" class="rounded-xl bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ error }}
    </div>

    <div v-else-if="detail" class="space-y-4">
      <!-- 顶部账号卡片：与「测试账号连接」弹窗同构 -->
      <div class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 bg-gradient-to-r from-gray-50 to-gray-100 p-3 dark:border-dark-500 dark:from-dark-700 dark:to-dark-600">
        <div class="flex min-w-0 items-center gap-3">
          <!-- 图标跟随渠道：与账号列表/创建弹窗里的平台图标保持一致 -->
          <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-primary-500/10 text-primary-600 dark:text-primary-400">
            <PlatformIcon :platform="detail.platform" size="lg" />
          </div>
          <!-- min-w-0 + truncate：国际版账号名很长（邮箱形式），
               不限制会把右侧的积分徽章挤出去或换行。 -->
          <div class="min-w-0">
            <div
              class="truncate font-semibold text-gray-900 dark:text-gray-100"
              :title="accountName || detail.account"
            >{{ shortName(accountName || detail.account) }}</div>
            <div class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
              <span class="rounded bg-gray-200 px-1.5 py-0.5 text-[10px] font-medium uppercase dark:bg-dark-500">
                {{ platformBadge }}
              </span>
              <span>{{ platformLabel }}</span>
            </div>
          </div>
        </div>
        <span class="flex-shrink-0 whitespace-nowrap rounded-full bg-green-100 px-2.5 py-1 text-xs font-semibold text-green-700 dark:bg-green-500/20 dark:text-green-400">
          {{ formatValue(detail.total) }} {{ t('admin.accounts.creditsUnit') }}
        </span>
      </div>

      <!-- 积分类型汇总 -->
      <div class="space-y-1.5">
        <label class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.accounts.creditsGroupsTitle') }}</label>
        <div v-if="!detail.groups || detail.groups.length === 0" class="py-2 text-sm text-gray-400">
          {{ t('admin.accounts.creditsNoGroups') }}
        </div>
        <!-- 细线分隔：不用方框，行与行之间一条淡横线 -->
        <div v-else class="divide-y divide-gray-100 dark:divide-dark-600">
          <div v-for="g in detail.groups" :key="g.kind + g.name" class="flex items-center justify-between py-2">
            <div class="flex min-w-0 items-center gap-2">
              <span class="h-1.5 w-1.5 flex-shrink-0 rounded-full" :class="groupDot(g)"></span>
              <span class="truncate text-sm text-gray-900 dark:text-gray-100">{{ g.name }}</span>
            </div>
            <div class="flex-shrink-0 text-right">
              <span class="font-mono text-sm font-medium text-gray-900 dark:text-gray-100">{{ formatValue(g.remain) }}</span>
              <span class="text-xs text-gray-400"> / {{ formatValue(g.total) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 积分包明细：与 AutoBuddy「全部积分包」对齐 -->
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.accounts.creditsPackagesTitle') }}
          </label>
          <span class="text-xs text-gray-400">{{ packages.length }} {{ t('admin.accounts.creditsPackagesCount') }}</span>
        </div>
        <div v-if="packageError" class="py-2 text-xs text-orange-500">{{ packageError }}</div>
        <div v-else-if="packages.length === 0" class="py-2 text-sm text-gray-400">
          {{ t('admin.accounts.creditsNoPackages') }}
        </div>
        <div v-else class="max-h-[260px] overflow-y-auto">
          <!-- 细线分隔 -->
          <div class="divide-y divide-gray-100 dark:divide-dark-600">
            <div v-for="p in packages" :key="p.package_code + (p.expire_date || '')" class="py-3">
              <div class="flex min-w-0 items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="truncate text-sm font-medium text-gray-900 dark:text-gray-100">{{ packageNameCN(p.name) }}</div>
                  <div class="mt-1 text-[11px] text-gray-400">
                    <template v-if="p.expire_date">{{ t('admin.accounts.creditsExpireAt') }} {{ p.expire_date }}</template>
                    <template v-else>{{ t('admin.accounts.creditsNoExpire') }}</template>
                  </div>
                </div>
                <div class="flex-shrink-0 text-right text-xs">
                  <div class="font-medium text-gray-900 dark:text-gray-100">
                    {{ formatValue(p.remain) }} / {{ formatValue(p.total) }}
                  </div>
                  <div class="mt-1 text-[11px] text-gray-400">
                    {{ t('admin.accounts.creditsUsed') }} {{ formatValue(p.used) }}
                  </div>
                </div>
              </div>
              <!-- 使用进度条 -->
              <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-600">
                <div
                  class="h-full rounded-full transition-all"
                  :class="progressBarClass(p)"
                  :style="{ width: Math.max(0, Math.min(100, p.use_percent)) + '%' }"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex items-center justify-end gap-2">
        <button type="button" class="btn btn-secondary btn-sm" @click="emit('close')">{{ t('common.close') }}</button>
        <button type="button" class="btn btn-primary btn-sm" :disabled="loading" @click="reload">
          {{ loading ? t('common.loading') : t('admin.accounts.creditsRefresh') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformDisplayName } from '@/constants/platformCatalog'
import { getMarvisCredits } from '@/api/admin/marvis'
import {
  getWorkBuddyCreditsDetail,
  type WorkBuddyCreditsDetail
} from '@/api/admin/workbuddy'

const props = defineProps<{
  show: boolean
  accountId: number
  accountName?: string
  platform?: string
}>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const detail = ref<WorkBuddyCreditsDetail | null>(null)

// 平台名与徽标都从平台清单读取，不按平台硬编码 —— 新接一个平台
// 这个弹窗不需要改动。
//
// 这两个 computed 必须在 detail 声明之后：写在前面会命中 TDZ，
// 组件 setup 直接抛 ReferenceError，弹窗点开即关。
const platformLabel = computed(() => platformDisplayName(detail.value?.platform))
const platformBadge = computed(() => {
  const id = String(detail.value?.platform ?? '')
  if (id === 'workbuddy_cn') return 'CN'
  if (id === 'workbuddy_intl') return 'INTL'
  if (!id) return '—'
  return id.slice(0, 4).toUpperCase()
})
const loading = ref(false)
const error = ref('')

const packages = computed(() => detail.value?.packages ?? [])
const packageError = computed(() => detail.value?.package_err || '')

// packageNameCN 兜底翻译：后端已映射，这里防止旧缓存显示英文。
function packageNameCN(name: string): string {
  if (!name) return t('admin.accounts.creditsPackageUnnamed')
  const lower = name.toLowerCase()
  if (lower.includes('free plan')) return '免费套餐订阅'
  if (lower.includes('bonus pack') || lower.includes('bonus credits')) return '赠送包'
  if (lower.includes('top-up') || lower.includes('topup')) return '充值包'
  if (lower.includes('reward')) return '奖励包'
  return name
}

// shortName 精简过长的账号名。
//
// 邮箱形式（国际版）保留 @ 前的部分并截断；其他形式按字符数截断。
// 完整名称放在 title 里，悬停可见。
function shortName(name: string): string {
  if (!name) return ''
  const max = 18
  if (name.length <= max) return name
  // 邮箱：优先保留 @ 前的标识
  const at = name.indexOf('@')
  if (at > 0 && at <= max) {
    return name.slice(0, at)
  }
  return name.slice(0, max - 1) + '…'
}

function formatValue(v: number): string {
  if (v >= 1e4) return (v / 1e4).toFixed(2) + '万'
  if (v >= 1) return v.toFixed(2)
  return v.toFixed(4)
}

function groupDot(g: { kind: string; name: string }): string {
  const k = (g.kind || '').toLowerCase()
  if (k.includes('base')) return 'bg-primary-500'
  if (k.includes('bonus')) return 'bg-purple-500'
  if (k.includes('addon') || k.includes('top')) return 'bg-amber-500'
  if (k.includes('benefit') || k.includes('activity')) return 'bg-emerald-500'
  return 'bg-gray-400'
}

// 进度条配色：剩余越少越偏橙红，便于快速识别快用完的包。
function progressBarClass(p: { use_percent: number; days_left: number }): string {
  if (p.days_left >= 0 && p.days_left <= 7) return 'bg-red-500/80'
  if (p.use_percent >= 80) return 'bg-orange-500/80'
  return 'bg-emerald-500/80'
}

async function reload() {
  if (!props.accountId) return
  loading.value = true
  error.value = ''
  try {
    // Marvis 走自己的钱包接口：它按 token 计量，没有 WorkBuddy 的分组积分。
    if (props.platform === 'marvis') {
      const m = await getMarvisCredits(props.accountId)
      detail.value = {
        account_id: m.account_id,
        account: m.account,
        platform: m.platform,
        total: m.total,
        // 字段名必须与 WorkBuddyCreditGroup 一致：渲染读的是 remain 不是
        // remaining，kind 用于图标的取色与行 key。
        groups: (m.groups ?? []).map((g) => ({
          kind: 'marvis_tokens',
          name: g.name,
          total: g.total,
          remain: g.remaining,
          used: g.used,
          frozen: m.frozen ?? 0
        })),
        package_name: m.package_name,
        is_paid_user: m.is_paid_user,
        credits_info_available: m.credits_info_available
      } as unknown as WorkBuddyCreditsDetail
      if (m.error) error.value = m.error
      return
    }
    detail.value = await getWorkBuddyCreditsDetail(props.accountId)
    if (detail.value?.error) error.value = detail.value.error
  } catch (e: any) {
    error.value = e?.message || 'failed'
    detail.value = null
  } finally {
    loading.value = false
  }
}

watch(() => [props.show, props.accountId], ([show]) => {
  if (show) void reload()
})
</script>
