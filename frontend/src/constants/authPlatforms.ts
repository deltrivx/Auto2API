// 授权流程的平台注册表。
//
// 重新授权弹窗原先用一串 if/else 判断平台，每加一个平台就要在多处补分支，
// 漏一处就会落到最后的兜底（历史上是 Claude 的授权面板）。这里改成查表：
// 平台没登记就是「没有授权流程」，不会静默套用别的平台的界面。
//
// 两个字段：
//   labelKey  —— 弹窗里显示的平台名 i18n key
//   flow      —— 使用哪套授权组件：与添加账号页共用同一套步骤组件

export type AuthFlowKind = 'workbuddy' | 'marvis' | 'code-exchange'

export interface AuthPlatformSpec {
  labelKey: string
  flow: AuthFlowKind
}

/** 当前由网关自身维护授权流程的平台。 */
export const AUTH_PLATFORMS: Record<string, AuthPlatformSpec> = {
  workbuddy_cn: {
    labelKey: 'admin.accounts.workbuddyAccount',
    flow: 'workbuddy'
  },
  workbuddy_intl: {
    labelKey: 'admin.accounts.workbuddyAccount',
    flow: 'workbuddy'
  },
  marvis: {
    labelKey: 'admin.accounts.marvisAccount',
    flow: 'marvis'
  }
}

/** 查平台配置；未登记返回 undefined，调用方据此显示「不支持」而不是套用默认值。 */
export function authPlatformSpec(platform: string | undefined | null): AuthPlatformSpec | undefined {
  if (!platform) return undefined
  return AUTH_PLATFORMS[platform]
}
