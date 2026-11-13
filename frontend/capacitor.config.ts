import type { CapacitorConfig } from '@capacitor/cli'

/**
 * Auto2API 移动端配置（Capacitor 混合应用）。
 *
 * iOS / Android 外壳加载的是 frontend 构建产物，与 Web 端同源，
 * 因此三端 UI 保持一致，只需各自打包。
 *
 * 构建产物目录必须指向 vite 的 outDir，否则 cap sync 会拷不到资源。
 */
const config: CapacitorConfig = {
  appId: 'local.auto2api.app',
  appName: 'Auto2API',
  webDir: '../backend/internal/web/dist',

  ios: {
    // 允许应用内跳转到任意域名（网关地址由用户配置，无法提前枚举）
    limitsNavigationsToAppBoundDomains: false,
    contentInset: 'always'
  },

  server: {
    androidScheme: 'https',
    iosScheme: 'capacitor'
  },

  plugins: {
    SplashScreen: {
      launchShowDuration: 1200,
      backgroundColor: '#061B45',
      showSpinner: false
    },
    Keyboard: {
      resize: 'body'
    }
  }
}

export default config
