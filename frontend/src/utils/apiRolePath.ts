// 按当前登录角色选择 API 前缀。
//
// 网关对普通用户开放了账号相关的读取、测试与授权接口（挂在 authenticated
// 路由组下），写操作仍只在 /admin。前端凡是「普通用户也会调用」的接口
// 都要走这里，否则会硬编码到 /admin 拿到 403。
//
// 角色读取每次都现查 localStorage：模块加载时可能还没登录，缓存会抽错。

function currentRole(): string {
  for (const key of ['auth_user', 'user', 'auth']) {
    try {
      const raw = localStorage.getItem(key)
      if (!raw) continue
      const u = JSON.parse(raw)
      const role = u?.role ?? u?.user?.role ?? u?.state?.user?.role
      if (role) return String(role)
    } catch {
      // 忽略解析失败，继续试下一个键
    }
  }
  return ''
}

export function isAdminRole(): boolean {
  return currentRole() === 'admin'
}

/** 选前缀：管理员用 adminPrefix，其余用 userPrefix。 */
export function rolePath(adminPrefix: string, userPrefix: string): string {
  return isAdminRole() ? adminPrefix : userPrefix
}

/** 账号接口：/admin/accounts 或 /accounts。 */
export function accountsPath(): string {
  return rolePath('/admin/accounts', '/accounts')
}

/** WorkBuddy 接口：/admin/workbuddy 或 /workbuddy。 */
export function workBuddyPath(suffix: string): string {
  return rolePath(`/admin/workbuddy${suffix}`, `/workbuddy${suffix}`)
}
