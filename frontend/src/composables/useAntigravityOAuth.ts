/**
 * Antigravity OAuth composable（已废弃）。
 *
 * Antigravity 平台已从产品中移除。保留本文件只是为了不打断既有 import：
 * 实现转发到统一的桩，所有方法为空操作。新代码请使用 useWorkBuddyOAuth。
 */
import { useRemovedPlatformOAuth } from './useRemovedPlatformOAuth'

export function useAntigravityOAuth() {
  return useRemovedPlatformOAuth('antigravity')
}
