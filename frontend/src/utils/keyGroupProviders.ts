import type { GroupPlatform } from '@/types'

export type KeyGroupProvider = 'anthropic' | 'openai' | 'domestic' | 'other'

export const KEY_GROUP_PROVIDERS = ['anthropic', 'openai', 'domestic', 'other'] as const

// Classify by the configured upstream platform, never by a group's display name.
const PROVIDER_BY_PLATFORM: Record<GroupPlatform, KeyGroupProvider> = {
  anthropic: 'anthropic',
  openai: 'openai',
  kimi: 'domestic',
  zhipu: 'domestic',
  deepseek: 'domestic',
  minimax: 'domestic',
  gemini: 'other',
  grok: 'other',
  antigravity: 'other',
  composite: 'other',
  opencode_go: 'other',
  typesafe: 'other',
  workbuddy_cn: 'other',
  workbuddy_intl: 'other',
  trae_cn: 'other',
  trae_intl: 'other',
  command_code: 'other',
  cline: 'other'
}

export function getKeyGroupProvider(platform: GroupPlatform): KeyGroupProvider {
  return PROVIDER_BY_PLATFORM[platform] ?? 'other'
}

// Collections use representative provider marks rather than an invented brand logo.
export const KEY_GROUP_PROVIDER_ICONS: Record<KeyGroupProvider, GroupPlatform[]> = {
  anthropic: ['anthropic'],
  openai: ['openai'],
  domestic: ['deepseek', 'kimi'],
  other: ['gemini', 'grok']
}

/** 创建密钥弹窗里的一个「平台」磁贴。 */
export interface KeyGroupTile {
  /** 平台 id，同时用于过滤分组与回显选中态。 */
  value: string
  /** 平台显示名（来自后端平台目录）。 */
  label: string
  /** 磁贴上的图标，固定为平台自身。 */
  icons: string[]
  /** 该平台下可用分组数量。 */
  count: number
  /** 磁贴下方的说明文字。 */
  hint: string
}

/**
 * buildKeyGroupTiles 按「当前实际存在的分组」生成平台磁贴，一个平台一块。
 *
 * 之前这里是写死的四个厂商类别（Anthropic / OpenAI / 国产模型 / 其他），
 * 与本机实际注册的平台并不对应：没有 Anthropic/OpenAI 分组也会显示灰磁贴，
 * 而 Trae 两个平台落到「其他」，无法单独挑选。改为按 groups 里出现的平台
 * 去重生成，保证「创建密钥页和平台一一对应」，也不会残留空磁贴。
 */
export function buildKeyGroupTiles(
  groups: Array<{ platform: string }>,
  platformLabel: (platform: string) => string
): KeyGroupTile[] {
  const counts = new Map<string, number>()
  for (const group of groups) {
    const platform = String(group.platform || '')
    if (!platform) continue
    counts.set(platform, (counts.get(platform) ?? 0) + 1)
  }

  const tiles: KeyGroupTile[] = []
  for (const [platform, count] of counts) {
    const label = platformLabel(platform) || platform
    tiles.push({
      value: platform,
      label,
      icons: [platform],
      count,
      hint: label
    })
  }
  // 分组多的平台排在前面，避免磁贴顺序随接口返回顺序抖动。
  tiles.sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  return tiles
}
