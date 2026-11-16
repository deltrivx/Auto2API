# Auto2API 移动端构建说明

本目录的 iOS / Android 原生工程由 Capacitor 自动生成，无需手工维护。

## 为什么不在仓库里存原生工程

`ios/` 和 `android/` 由 `npx cap add` 生成，包含大量随 Capacitor 版本变动的
模板文件（Xcode 工程、Gradle 脚本、gradle wrapper 等）。

把这些生成物提交进仓库会带来两个问题：

1. 升级 Capacitor 时要手工合并几十个模板文件的差异；
2. 不同机器的 Xcode / Android SDK 版本会让工程文件反复变动。

因此仓库只保留 `capacitor.config.ts` 和依赖声明，原生工程在 CI 中按需生成，
本地开发时执行一次 `pnpm run cap:add` 即可。

## 本地开发

```bash
cd frontend

# 首次：安装依赖并生成原生工程
pnpm install
pnpm run build          # 生成 web 产物到 ../backend/internal/web/dist
pnpm exec cap add ios
pnpm exec cap add android

# 日常：改完前端后同步到原生工程
pnpm run build:mobile   # build + cap sync

# 打开 IDE
pnpm run cap:ios
pnpm run cap:android
```

## CI 构建

由 `.github/workflows/mobile.yml` 负责：

| 产物 | Runner | 签名 |
|---|---|---|
| `app-debug.apk` | ubuntu-26.04 | 调试密钥（自动） |
| `app-release-unsigned.apk` | ubuntu-26.04 | 未签名，需自行签名 |
| `Auto2API.app` (模拟器) | macos-15 | 未签名 |

### Android 自签名

如需可直接安装的 release APK，在仓库 Secrets 中配置：

| Secret | 说明 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | keystore 文件的 base64 |
| `ANDROID_KEYSTORE_PASSWORD` | keystore 密码 |
| `ANDROID_KEY_ALIAS` | 密钥别名 |
| `ANDROID_KEY_PASSWORD` | 密钥密码 |

生成 keystore：

```bash
keytool -genkeypair -v \
  -keystore auto2api-release.keystore \
  -alias auto2api \
  -keyalg RSA -keysize 2048 -validity 10000 \
  -storepass <密码> -keypass <密码> \
  -dname "CN=Auto2API, OU=Mobile, O=Auto2API, L=CN, S=CN, C=CN"

base64 -i auto2api-release.keystore | pbcopy   # 粘贴到 ANDROID_KEYSTORE_BASE64
```

### iOS 自签名

iOS 侧载需要 Apple 开发者证书。当前工作流产出**未签名**的 `.app`，
可用于模拟器调试，不能直接装到真机。

真机安装需要：
1. Apple 开发者账号（免费账号也可，但证书 7 天过期）
2. 在 Secrets 中配置证书（`.p12`）和描述文件

## 应用标识

| 项 | 值 |
|---|---|
| App ID | `local.auto2api.app` |
| 显示名 | Auto2API |
| Web 目录 | `../backend/internal/web/dist` |
