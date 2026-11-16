<template>
  <div class="relative">
    <!-- Admin: Full version badge with dropdown -->
    <template v-if="isAdmin">
      <button
        @click="toggleDropdown"
        class="flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs transition-colors"
        :class="[
          hasUpdate
            ? 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400 dark:hover:bg-amber-900/50'
            : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-800 dark:text-dark-400 dark:hover:bg-dark-700'
        ]"
        :title="hasUpdate ? t('version.updateAvailable') : t('version.upToDate')"
      >
        <span v-if="currentVersion" class="font-medium">{{ displayVersion }}</span>
        <span
          v-else
          class="h-3 w-12 animate-pulse rounded bg-gray-200 font-medium dark:bg-dark-600"
        ></span>
        <!-- Update indicator -->
        <span v-if="hasUpdate" class="relative flex h-2 w-2">
          <span
            class="absolute inline-flex h-full w-full animate-ping rounded-full bg-amber-400 opacity-75"
          ></span>
          <span class="relative inline-flex h-2 w-2 rounded-full bg-amber-500"></span>
        </span>
      </button>

      <!-- Dropdown -->
      <transition name="dropdown">
        <div
          v-if="dropdownOpen"
          ref="dropdownRef"
          class="version-modal z-50 flex flex-col overflow-hidden whitespace-normal rounded-xl border border-gray-200 bg-white shadow-2xl dark:border-dark-700 dark:bg-dark-800"
        >
          <!-- Header with refresh button -->
          <div
            class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700"
          >
            <span class="text-sm font-medium text-gray-700 dark:text-dark-300">{{
              t('version.currentVersion')
            }}</span>
            <button
              @click="refreshVersion(true)"
              class="rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-dark-200"
              :disabled="loading"
              :title="t('version.refresh')"
            >
              <Icon
                name="refresh"
                size="sm"
                :stroke-width="2"
                :class="{ 'animate-spin': loading }"
              />
            </button>
          </div>

          <div class="version-modal-body">
            <!-- Loading state -->
            <div v-if="loading" class="flex items-center justify-center py-6">
              <svg class="h-6 w-6 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
            </div>

            <!-- Content -->
            <template v-else>
              <!-- Version display - centered and prominent -->
              <div class="mb-4 text-center">
                <div class="inline-flex items-center gap-2">
                  <span
                    v-if="currentVersion"
                    class="text-2xl font-bold text-gray-900 dark:text-white"
                    >{{ displayVersion }}</span
                  >
                  <span v-else class="text-2xl font-bold text-gray-400 dark:text-dark-500">--</span>
                  <!-- Show check mark when up to date -->
                  <span
                    v-if="!hasUpdate"
                    class="flex h-5 w-5 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30"
                  >
                    <svg
                      class="h-3 w-3 text-green-600 dark:text-green-400"
                      fill="currentColor"
                      viewBox="0 0 20 20"
                    >
                      <path
                        fill-rule="evenodd"
                        d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
                        clip-rule="evenodd"
                      />
                    </svg>
                  </span>
                </div>
                <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                  {{
                    hasUpdate
                      ? t('version.latestVersion') + ': v' + latestVersion
                      : t('version.upToDate')
                  }}
                </p>
              </div>

              <!-- Priority 1: Update error (must check before hasUpdate) -->
              <div v-if="updateError" class="space-y-2">
                <div
                  class="flex items-center gap-3 rounded-lg border border-red-200 bg-red-50 p-3 dark:border-red-800/50 dark:bg-red-900/20"
                >
                  <div
                    class="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/50"
                  >
                    <Icon
                      name="x"
                      size="sm"
                      :stroke-width="2"
                      class="text-red-600 dark:text-red-400"
                    />
                  </div>
                  <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium text-red-700 dark:text-red-300">
                      {{ t('version.updateFailed') }}
                    </p>
                    <p class="truncate text-xs text-red-600/70 dark:text-red-400/70">
                      {{ updateError }}
                    </p>
                  </div>
                </div>

                <!-- Retry button -->
                <button
                  @click="handleUpdate"
                  :disabled="updating"
                  class="flex w-full items-center justify-center gap-2 rounded-lg bg-red-500 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-red-600 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ t('version.retry') }}
                </button>
              </div>

              <!-- Priority 2: Update success - need restart -->
              <div v-else-if="updateSuccess && needRestart" class="space-y-2">
                <div
                  class="flex items-center gap-3 rounded-lg border border-green-200 bg-green-50 p-3 dark:border-green-800/50 dark:bg-green-900/20"
                >
                  <div
                    class="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/50"
                  >
                    <svg
                      class="h-4 w-4 text-green-600 dark:text-green-400"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
                    </svg>
                  </div>
                  <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium text-green-700 dark:text-green-300">
                      {{
                        successKind === 'rollback'
                          ? t('version.rollbackComplete')
                          : t('version.updateComplete')
                      }}
                    </p>
                    <p class="text-xs text-green-600/70 dark:text-green-400/70">
                      {{ t('version.restartRequired') }}
                    </p>
                  </div>
                </div>

                <!-- Restart button with countdown -->
                <button
                  @click="handleRestart"
                  :disabled="restarting"
                  class="flex w-full items-center justify-center gap-2 rounded-lg bg-green-500 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-green-600 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <svg
                    v-if="restarting"
                    class="h-4 w-4 animate-spin"
                    fill="none"
                    viewBox="0 0 24 24"
                  >
                    <circle
                      class="opacity-25"
                      cx="12"
                      cy="12"
                      r="10"
                      stroke="currentColor"
                      stroke-width="4"
                    ></circle>
                    <path
                      class="opacity-75"
                      fill="currentColor"
                      d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                    ></path>
                  </svg>
                  <svg
                    v-else
                    class="h-4 w-4"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    stroke-width="2"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                    />
                  </svg>
                  <template v-if="restarting">
                    <span>{{ t('version.restarting') }}</span>
                    <span v-if="restartCountdown > 0" class="tabular-nums"
                      >({{ restartCountdown }}s)</span
                    >
                  </template>
                  <span v-else>{{ t('version.restartNow') }}</span>
                </button>
              </div>

              <!-- Priority 3: Update available for source build - show git pull hint -->
              <div v-else-if="hasUpdate && !isReleaseBuild" class="space-y-2">
                <a
                  v-if="releaseInfo?.html_url && releaseInfo.html_url !== '#'"
                  :href="releaseInfo.html_url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="group flex items-center gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 transition-colors hover:bg-amber-100 dark:border-amber-800/50 dark:bg-amber-900/20 dark:hover:bg-amber-900/30"
                >
                  <div
                    class="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-900/50"
                  >
                    <Icon
                      name="download"
                      size="sm"
                      :stroke-width="2"
                      class="text-amber-600 dark:text-amber-400"
                    />
                  </div>
                  <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium text-amber-700 dark:text-amber-300">
                      {{ t('version.updateAvailable') }}
                    </p>
                    <p class="text-xs text-amber-600/70 dark:text-amber-400/70">
                      v{{ latestVersion }}
                    </p>
                  </div>
                  <svg
                    class="h-4 w-4 text-amber-500 transition-transform group-hover:translate-x-0.5 dark:text-amber-400"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    stroke-width="2"
                  >
                    <path stroke-linecap="round" stroke-linejoin="round" d="M9 5l7 7-7 7" />
                  </svg>
                </a>
                <!-- Source build hint -->
                <div
                  class="flex items-center gap-2 rounded-lg border border-blue-200 bg-blue-50 p-2 dark:border-blue-800/50 dark:bg-blue-900/20"
                >
                  <svg
                    class="h-3.5 w-3.5 flex-shrink-0 text-blue-500 dark:text-blue-400"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    stroke-width="2"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                    />
                  </svg>
                  <p class="text-xs text-blue-600 dark:text-blue-400">
                    {{ t('version.sourceModeHint') }}
                  </p>
                </div>
              </div>

              <!-- Priority 4: Update available for release build - show update button -->
              <div v-else-if="hasUpdate && isReleaseBuild" class="space-y-3">
                <!-- 顶部：环形进度 + 版本与状态文案 -->
                <div class="flex items-center gap-4">
                  <!-- 环形进度：未开始时是下载图标，下载中按百分比画弧，完成后是勾 -->
                  <div class="relative h-16 w-16 flex-shrink-0">
                    <svg class="h-16 w-16 -rotate-90" viewBox="0 0 64 64">
                      <circle
                        cx="32"
                        cy="32"
                        r="28"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="5"
                        class="text-gray-200 dark:text-dark-600"
                      />
                      <circle
                        v-if="downloadPercent > 0"
                        cx="32"
                        cy="32"
                        r="28"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="5"
                        stroke-linecap="round"
                        class="text-primary-500 transition-all duration-300"
                        :stroke-dasharray="ringCircumference"
                        :stroke-dashoffset="ringOffset"
                      />
                    </svg>
                    <div class="absolute inset-0 flex items-center justify-center">
                      <span v-if="downloadPercent > 0 && downloadPercent < 100" class="text-xs font-semibold text-primary-600 dark:text-primary-400">
                        {{ downloadPercent }}%
                      </span>
                      <Icon v-else-if="downloadPercent >= 100" name="check" size="sm" :stroke-width="2.5" class="text-green-500" />
                      <Icon v-else name="download" size="sm" :stroke-width="2" class="text-gray-400" />
                    </div>
                  </div>

                  <div class="min-w-0 flex-1">
                    <!-- 第一行：新版版本号 -->
                    <p class="text-sm font-semibold text-gray-900 dark:text-white">
                      {{ t('version.newVersion') }} v{{ latestVersion }}
                    </p>
                    <!-- 第二行：副标题显示进度或状态 -->
                    <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                      {{ statusSubtitle }}
                    </p>
                  </div>
                </div>

                <!-- 更新日志：可滚动 -->
                <div
                  v-if="releaseNotes"
                  class="max-h-40 overflow-y-auto rounded-lg border border-gray-200 bg-gray-50 p-3 text-xs leading-5 whitespace-pre-wrap text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
                >{{ releaseNotes }}</div>

                <!-- 错误提示 -->
                <div
                  v-if="updateError"
                  class="rounded-lg border border-red-200 bg-red-50 p-2.5 text-xs text-red-600 dark:border-red-800/50 dark:bg-red-900/20 dark:text-red-400"
                >{{ updateError }}</div>

                <!-- 主按钮：文案随状态变化 -->
                <button
                  @click="handleUpdate"
                  :disabled="updating || downloading"
                  class="flex w-full items-center justify-center gap-2 rounded-lg bg-primary-500 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-primary-600 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ primaryButtonText }}
                </button>

                <!-- 底部两个开关 -->
                <div class="flex items-center justify-between border-t border-gray-100 pt-2.5 dark:border-dark-700">
                  <label class="flex cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-dark-300">
                    <button
                      type="button"
                      role="switch"
                      :aria-checked="autoDownload"
                      class="relative inline-flex h-5 w-9 flex-shrink-0 items-center rounded-full transition-colors"
                      :class="autoDownload ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
                      @click="toggleAutoDownload"
                    >
                      <span
                        class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform duration-200"
                        :class="autoDownload ? 'translate-x-[18px]' : 'translate-x-[2px]'"
                      />
                    </button>
                    {{ t('version.autoDownload') }}
                  </label>

                  <label class="flex cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-dark-300">
                    <button
                      type="button"
                      role="switch"
                      :aria-checked="autoInstall"
                      class="relative inline-flex h-5 w-9 flex-shrink-0 items-center rounded-full transition-colors"
                      :class="autoInstall ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
                      @click="toggleAutoInstall"
                    >
                      <span
                        class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform duration-200"
                        :class="autoInstall ? 'translate-x-[18px]' : 'translate-x-[2px]'"
                      />
                    </button>
                    {{ t('version.autoInstall') }}
                  </label>
                </div>
              </div>

              <!-- Priority 5: Up to date - GitHub link + version rollback -->
              <div v-else class="space-y-2">
                <a
                  v-if="releaseInfo?.html_url && releaseInfo.html_url !== '#'"
                  :href="releaseInfo.html_url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="flex items-center justify-center gap-2 py-2 text-sm text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-dark-200"
                >
                  <svg class="h-4 w-4" fill="currentColor" viewBox="0 0 24 24">
                    <path
                      fill-rule="evenodd"
                      clip-rule="evenodd"
                      d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.17 6.839 9.49.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.604-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.464-1.11-1.464-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.029-2.683-.103-.253-.446-1.27.098-2.647 0 0 .84-.269 2.75 1.025A9.578 9.578 0 0112 6.836c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.377.203 2.394.1 2.647.64.699 1.028 1.592 1.028 2.683 0 3.842-2.339 4.687-4.566 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.167 22 16.418 22 12c0-5.523-4.477-10-10-10z"
                    />
                  </svg>
                  {{ t('version.viewRelease') }}
                </a>

                <!-- Version rollback entry -->
                <div class="border-t border-gray-100 pt-2 dark:border-dark-700">
                  <button
                    @click="toggleRollbackPanel"
                    class="group flex w-full items-center justify-between rounded-lg px-2 py-1.5 text-xs text-gray-400 transition-colors hover:bg-gray-50 hover:text-gray-600 dark:text-dark-500 dark:hover:bg-dark-700/50 dark:hover:text-dark-300"
                  >
                    <span class="flex items-center gap-1.5">
                      <Icon name="clock" size="xs" :stroke-width="2" />
                      {{ t('version.rollback') }}
                    </span>
                    <Icon
                      name="chevronDown"
                      size="xs"
                      :stroke-width="2"
                      class="transition-transform duration-200"
                      :class="{ 'rotate-180': rollbackPanelOpen }"
                    />
                  </button>

                  <transition name="rollback">
                    <div v-if="rollbackPanelOpen" class="mt-2 space-y-2">
                      <!-- Source build: online rollback unavailable, use git instead -->
                      <div
                        v-if="!isReleaseBuild"
                        class="flex items-center gap-2 rounded-lg border border-blue-200 bg-blue-50 p-2 dark:border-blue-800/50 dark:bg-blue-900/20"
                      >
                        <svg
                          class="h-3.5 w-3.5 flex-shrink-0 text-blue-500 dark:text-blue-400"
                          fill="none"
                          viewBox="0 0 24 24"
                          stroke="currentColor"
                          stroke-width="2"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                          />
                        </svg>
                        <p class="min-w-0 flex-1 text-xs leading-4 text-blue-600 dark:text-blue-400">
                          {{ t('version.rollbackSourceHint') }}
                        </p>
                      </div>

                      <!-- Loading versions -->
                      <div
                        v-else-if="rollbackVersionsLoading"
                        class="flex items-center justify-center py-4"
                      >
                        <svg
                          class="h-5 w-5 animate-spin text-primary-500"
                          fill="none"
                          viewBox="0 0 24 24"
                        >
                          <circle
                            class="opacity-25"
                            cx="12"
                            cy="12"
                            r="10"
                            stroke="currentColor"
                            stroke-width="4"
                          ></circle>
                          <path
                            class="opacity-75"
                            fill="currentColor"
                            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                          ></path>
                        </svg>
                      </div>

                      <!-- Load error + retry -->
                      <div v-else-if="rollbackVersionsError" class="space-y-2">
                        <p
                          class="rounded-lg border border-red-200 bg-red-50 p-2.5 text-xs text-red-600 dark:border-red-800/50 dark:bg-red-900/20 dark:text-red-400"
                        >
                          {{ rollbackVersionsError }}
                        </p>
                        <button
                          @click="loadRollbackVersions"
                          class="w-full rounded-lg border border-gray-200 py-1.5 text-xs text-gray-500 transition-colors hover:bg-gray-50 hover:text-gray-700 dark:border-dark-700 dark:text-dark-400 dark:hover:bg-dark-700/50 dark:hover:text-dark-200"
                        >
                          {{ t('version.retry') }}
                        </button>
                      </div>

                      <!-- No versions available -->
                      <p
                        v-else-if="rollbackVersions.length === 0"
                        class="py-3 text-center text-xs text-gray-400 dark:text-dark-500"
                      >
                        {{ t('version.noRollbackVersions') }}
                      </p>

                      <!-- Version list -->
                      <template v-else>
                        <p class="px-0.5 text-[11px] text-gray-400 dark:text-dark-500">
                          {{ t('version.rollbackSelectVersion') }}
                        </p>

                        <button
                          v-for="item in rollbackVersions"
                          :key="item.version"
                          @click="selectRollbackVersion(item.version)"
                          :disabled="rollingBack"
                          class="flex w-full items-center justify-between rounded-lg border px-3 py-2 text-left transition-all disabled:cursor-not-allowed disabled:opacity-60"
                          :class="
                            selectedRollbackVersion === item.version
                              ? 'border-amber-300 bg-amber-50 shadow-sm dark:border-amber-700 dark:bg-amber-900/20'
                              : 'border-gray-200 hover:border-gray-300 hover:bg-gray-50 dark:border-dark-700 dark:hover:border-dark-600 dark:hover:bg-dark-700/40'
                          "
                        >
                          <span class="flex items-center gap-2">
                            <span
                              class="flex h-3.5 w-3.5 items-center justify-center rounded-full border transition-colors"
                              :class="
                                selectedRollbackVersion === item.version
                                  ? 'border-amber-500'
                                  : 'border-gray-300 dark:border-dark-500'
                              "
                            >
                              <span
                                v-if="selectedRollbackVersion === item.version"
                                class="h-1.5 w-1.5 rounded-full bg-amber-500"
                              ></span>
                            </span>
                            <span
                              class="text-sm font-semibold"
                              :class="
                                selectedRollbackVersion === item.version
                                  ? 'text-amber-700 dark:text-amber-300'
                                  : 'text-gray-700 dark:text-dark-200'
                              "
                              >v{{ item.version }}</span
                            >
                          </span>
                          <span class="text-[11px] tabular-nums text-gray-400 dark:text-dark-500">
                            {{ formatPublishedAt(item.published_at) }}
                          </span>
                        </button>

                        <!-- Selected version: manual command (per deploy method) + confirm -->
                        <transition name="rollback">
                          <div v-if="selectedRollbackVersion" class="space-y-2">
                            <p class="px-0.5 text-[11px] text-gray-400 dark:text-dark-500">
                              {{ t('version.manualRollbackCommand') }}
                            </p>

                            <!-- Terminal-style block with deploy-method tabs -->
                            <div
                              class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600"
                            >
                              <div
                                class="flex items-center justify-between border-b border-gray-200 bg-gray-100 px-2 py-1.5 dark:border-dark-600 dark:bg-dark-700"
                              >
                                <div
                                  class="flex items-center gap-0.5 rounded-md bg-gray-200/70 p-0.5 dark:bg-dark-600/70"
                                >
                                  <button
                                    v-for="tab in manualTabs"
                                    :key="tab.key"
                                    @click="manualTab = tab.key"
                                    class="rounded px-2 py-0.5 text-[11px] font-medium transition-colors"
                                    :class="
                                      manualTab === tab.key
                                        ? 'bg-white text-gray-700 shadow-sm dark:bg-dark-800 dark:text-dark-100'
                                        : 'text-gray-400 hover:text-gray-600 dark:text-dark-400 dark:hover:text-dark-200'
                                    "
                                  >
                                    {{ tab.label }}
                                  </button>
                                </div>
                                <button
                                  @click="copyToClipboard(activeManualCommand)"
                                  class="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-gray-400 transition-colors hover:bg-gray-200 hover:text-gray-600 dark:text-dark-400 dark:hover:bg-dark-600 dark:hover:text-dark-200"
                                >
                                  <Icon
                                    :name="copied ? 'check' : 'copy'"
                                    size="xs"
                                    :stroke-width="2"
                                    :class="copied ? 'text-green-500' : ''"
                                  />
                                  {{ copied ? t('version.copied') : t('version.copyCommand') }}
                                </button>
                              </div>
                              <code
                                class="block select-all whitespace-pre-wrap break-all bg-gray-50 p-2.5 font-mono text-[10px] leading-relaxed text-gray-600 dark:bg-dark-900 dark:text-dark-300"
                                >{{ activeManualCommand }}</code
                              >
                            </div>

                            <p
                              class="flex items-start gap-1.5 px-0.5 text-[11px] leading-4 text-amber-600 dark:text-amber-400"
                            >
                              <Icon
                                name="exclamationTriangle"
                                size="xs"
                                :stroke-width="2"
                                class="mt-px flex-shrink-0"
                              />
                              {{ t('version.rollbackWarning') }}
                            </p>

                            <p
                              v-if="rollbackError"
                              class="rounded-lg border border-red-200 bg-red-50 p-2 text-xs text-red-600 dark:border-red-800/50 dark:bg-red-900/20 dark:text-red-400"
                            >
                              {{ rollbackError }}
                            </p>

                            <button
                              @click="handleRollback"
                              :disabled="rollingBack"
                              class="flex w-full items-center justify-center gap-2 rounded-lg bg-amber-500 px-4 py-2 text-sm font-medium text-white shadow-sm transition-colors hover:bg-amber-600 disabled:cursor-not-allowed disabled:opacity-50"
                            >
                              <svg
                                v-if="rollingBack"
                                class="h-4 w-4 animate-spin"
                                fill="none"
                                viewBox="0 0 24 24"
                              >
                                <circle
                                  class="opacity-25"
                                  cx="12"
                                  cy="12"
                                  r="10"
                                  stroke="currentColor"
                                  stroke-width="4"
                                ></circle>
                                <path
                                  class="opacity-75"
                                  fill="currentColor"
                                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                                ></path>
                              </svg>
                              <Icon v-else name="clock" size="sm" :stroke-width="2" />
                              <span>{{
                                rollingBack
                                  ? t('version.rollingBack')
                                  : t('version.rollbackConfirm', {
                                      version: 'v' + selectedRollbackVersion
                                    })
                              }}</span>
                            </button>
                          </div>
                        </transition>
                      </template>
                    </div>
                  </transition>
                </div>
              </div>
            </template>
          </div>
        </div>
      </transition>
    </template>

    <!-- Non-admin: Simple static version text -->
    <span v-else-if="version" class="text-xs text-gray-500 dark:text-dark-400">
      v{{ version }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import {
  performUpdate,
  restartService,
  getRollbackVersions,
  rollback as rollbackAPI,
  type RollbackVersionInfo
} from '@/api/admin/system'
import { useClipboard } from '@/composables/useClipboard'
import Icon from '@/components/icons/Icon.vue'

const GITHUB_REPO = 'deltrivx/Auto2API'
// Docker Hub image published by CI (tags carry no "v" prefix, e.g. weishaw/auto2api:0.1.146)
const DOCKER_IMAGE = 'weishaw/auto2api'

const { t } = useI18n()

const props = defineProps<{
  version?: string
}>()

const authStore = useAuthStore()
const appStore = useAppStore()

const isAdmin = computed(() => authStore.isAdmin)

const dropdownOpen = ref(false)
const dropdownRef = ref<HTMLElement | null>(null)

// Use store's cached version state
const loading = computed(() => appStore.versionLoading)
const currentVersion = computed(() => appStore.currentVersion || props.version || '')

// 后端已经返回带 v 前缀的版本（如 v1.0.0），此处只在缺失时补上，
// 避免渲染成 vv1.0.0。
const displayVersion = computed(() => {
  const raw = currentVersion.value
  if (!raw) return ''
  return /^v/i.test(raw) ? raw : `v${raw}`
})
const latestVersion = computed(() => appStore.latestVersion)
const hasUpdate = computed(() => appStore.hasUpdate)
const releaseInfo = computed(() => appStore.releaseInfo)

// 更新日志正文：Release body，去掉 Markdown 标题行后作为纯文本展示。
const releaseNotes = computed(() => {
  const body = (releaseInfo.value as { body?: string } | null)?.body || ''
  return body
    .split('\n')
    .filter((line) => !/^#+\s/.test(line.trim()))
    .join('\n')
    .trim()
})
const buildType = computed(() => appStore.buildType)

// Update process states (local to this component)
const updating = ref(false)
const restarting = ref(false)
const needRestart = ref(false)
const updateError = ref('')
const updateSuccess = ref(false)
const restartCountdown = ref(0)
// Distinguishes the success + restart panel between update and rollback flows
const successKind = ref<'update' | 'rollback'>('update')

// ── 在线升级面板状态 ────────────────────────────────────────────────
// 下载进度 0-100；100 表示已下载完成、待安装。
const downloadPercent = ref(0)
const downloading = ref(false)
const installed = ref(false)
// 两个开关持久化到 localStorage，用户设一次即可。
const autoDownload = ref(localStorage.getItem('update-auto-download') === '1')
const autoInstall = ref(localStorage.getItem('update-auto-install') === '1')

// 环形进度参数：半径 28 → 周长 2πr。
const ringCircumference = 2 * Math.PI * 28
const ringOffset = computed(
  () => ringCircumference * (1 - Math.min(100, Math.max(0, downloadPercent.value)) / 100)
)

// 第二行副标题：按阶段显示进度与说明。
const statusSubtitle = computed(() => {
  if (updateError.value) return t('version.statusFailed')
  if (installed.value) return t('version.statusInstalled')
  if (downloadPercent.value >= 100) return t('version.statusReadyInstall')
  if (downloading.value) return t('version.statusDownloading', { percent: downloadPercent.value })
  if (downloadPercent.value > 0) return t('version.statusPaused', { percent: downloadPercent.value })
  return t('version.statusIdle')
})

// 主按钮文案：开始更新 → 下载中 → 安装更新 → 已安装。
const primaryButtonText = computed(() => {
  if (installed.value) return t('version.installed')
  if (downloadPercent.value >= 100) return t('version.installUpdate')
  if (downloading.value) return t('version.downloading', { percent: downloadPercent.value })
  return t('version.startUpdate')
})

function toggleAutoDownload() {
  autoDownload.value = !autoDownload.value
  localStorage.setItem('update-auto-download', autoDownload.value ? '1' : '0')
  if (autoDownload.value && hasUpdate.value && downloadPercent.value === 0) {
    void startDownload()
  }
}

function toggleAutoInstall() {
  autoInstall.value = !autoInstall.value
  localStorage.setItem('update-auto-install', autoInstall.value ? '1' : '0')
  if (autoInstall.value && autoDownload.value) {
    autoDownload.value = false
    localStorage.setItem('update-auto-download', '0')
    if (hasUpdate.value && downloadPercent.value === 0) void startDownload()
  }
}

// Rollback states
const rollbackPanelOpen = ref(false)
const rollbackVersions = ref<RollbackVersionInfo[]>([])
const rollbackVersionsLoading = ref(false)
const rollbackVersionsError = ref('')
const selectedRollbackVersion = ref('')
const rollingBack = ref(false)
const rollbackError = ref('')

const { copied, copyToClipboard } = useClipboard()

// Manual rollback methods differ by deployment: script installs use install.sh,
// docker deployments pin the image tag instead
const manualTab = ref<'script' | 'docker'>('script')

const manualTabs = computed(() => [
  { key: 'script' as const, label: t('version.deployScript') },
  { key: 'docker' as const, label: t('version.deployDocker') }
])

const scriptRollbackCommand = computed(() => {
  if (!selectedRollbackVersion.value) return ''
  const tag = `v${selectedRollbackVersion.value}`
  return `curl -sSL https://raw.githubusercontent.com/${GITHUB_REPO}/${tag}/deploy/install.sh | sudo bash -s -- rollback ${tag}`
})

const dockerRollbackCommand = computed(() => {
  if (!selectedRollbackVersion.value) return ''
  return [
    `# ${t('version.dockerEditCompose')}`,
    `image: ${DOCKER_IMAGE}:${selectedRollbackVersion.value}`,
    '',
    `# ${t('version.dockerRecreate')}`,
    'docker compose up -d'
  ].join('\n')
})

const activeManualCommand = computed(() =>
  manualTab.value === 'docker' ? dockerRollbackCommand.value : scriptRollbackCommand.value
)

// Only show update check for release builds (binary/docker deployment)
const isReleaseBuild = computed(() => buildType.value === 'release')

function toggleDropdown() {
  dropdownOpen.value = !dropdownOpen.value
}

function closeDropdown() {
  dropdownOpen.value = false
}

async function refreshVersion(force = true) {
  if (!isAdmin.value) return

  // Reset update states when refreshing
  updateError.value = ''
  updateSuccess.value = false
  needRestart.value = false
  resetRollbackState()

  await appStore.fetchVersion(force)
}

// 后端只暴露一个「下载并替换」的接口，进度无法逐块上报。
// 这里用一个收敛到 90% 的假进度让用户看到动效，接口返回后再补到 100%，
// 失败则停在当前值并把错误显示出来——真实结果始终以接口返回为准。
let progressTimer: ReturnType<typeof setInterval> | null = null

function stopProgress() {
  if (progressTimer) {
    clearInterval(progressTimer)
    progressTimer = null
  }
}

function startProgress() {
  stopProgress()
  downloadPercent.value = 0
  downloading.value = true
  progressTimer = setInterval(() => {
    const cur = downloadPercent.value
    if (cur >= 90) return
    // 越接近 90 增幅越小，形成自然的减速感。
    const step = Math.max(1, Math.round((90 - cur) / 12))
    downloadPercent.value = Math.min(90, cur + step)
  }, 400)
}

// 下载并替换二进制。返回是否成功。
async function startDownload(): Promise<boolean> {
  if (downloading.value || updating.value) return false
  updating.value = true
  updateError.value = ''
  updateSuccess.value = false
  installed.value = false
  startProgress()

  try {
    const result = await performUpdate()
    stopProgress()
    downloadPercent.value = 100
    downloading.value = false
    successKind.value = 'update'
    updateSuccess.value = true
    needRestart.value = result.need_restart
    installed.value = true
    appStore.clearVersionCache()
    return true
  } catch (error: unknown) {
    stopProgress()
    downloading.value = false
    const err = error as { response?: { data?: { message?: string } }; message?: string }
    updateError.value = err.response?.data?.message || err.message || t('version.updateFailed')
    return false
  } finally {
    updating.value = false
  }
}

// 主按钮：未下载则开始下载；已下载则走安装（后端已在下载阶段完成替换）。
async function handleUpdate() {
  if (updating.value || downloading.value) return
  if (downloadPercent.value >= 100) {
    // 已经替换完成，只需重启服务生效。
    installed.value = true
    return
  }
  await startDownload()
}

function resetRollbackState() {
  rollbackPanelOpen.value = false
  rollbackVersions.value = []
  rollbackVersionsError.value = ''
  selectedRollbackVersion.value = ''
  rollbackError.value = ''
  manualTab.value = 'script'
}

async function toggleRollbackPanel() {
  if (!isAdmin.value) return
  rollbackPanelOpen.value = !rollbackPanelOpen.value
  // Source builds only show a hint, no version list to fetch
  if (
    rollbackPanelOpen.value &&
    isReleaseBuild.value &&
    rollbackVersions.value.length === 0 &&
    !rollbackVersionsLoading.value
  ) {
    await loadRollbackVersions()
  }
}

async function loadRollbackVersions() {
  if (!isAdmin.value) return
  rollbackVersionsLoading.value = true
  rollbackVersionsError.value = ''
  try {
    const data = await getRollbackVersions()
    rollbackVersions.value = data.versions || []
  } catch (error: unknown) {
    const err = error as { response?: { data?: { message?: string } }; message?: string }
    rollbackVersionsError.value =
      err.response?.data?.message || err.message || t('version.loadVersionsFailed')
  } finally {
    rollbackVersionsLoading.value = false
  }
}

function selectRollbackVersion(version: string) {
  if (rollingBack.value) return
  rollbackError.value = ''
  selectedRollbackVersion.value = selectedRollbackVersion.value === version ? '' : version
}

function formatPublishedAt(publishedAt: string): string {
  if (!publishedAt) return ''
  const date = new Date(publishedAt)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleDateString()
}

async function handleRollback() {
  if (!isAdmin.value) return
  if (rollingBack.value || !selectedRollbackVersion.value) return

  rollingBack.value = true
  rollbackError.value = ''

  try {
    const result = await rollbackAPI(selectedRollbackVersion.value)
    successKind.value = 'rollback'
    updateSuccess.value = true
    needRestart.value = result.need_restart
    rollbackPanelOpen.value = false
    // Clear version cache so the next check reflects the rolled-back version
    appStore.clearVersionCache()
  } catch (error: unknown) {
    const err = error as { response?: { data?: { message?: string } }; message?: string }
    rollbackError.value = err.response?.data?.message || err.message || t('version.rollbackFailed')
  } finally {
    rollingBack.value = false
  }
}

async function handleRestart() {
  if (restarting.value) return

  restarting.value = true
  restartCountdown.value = 8

  try {
    await restartService()
    // Service will restart, page will reload automatically or show disconnected
  } catch (error) {
    // Expected - connection will be lost during restart
    console.log('Service restarting...')
  }

  // Start countdown
  const countdownInterval = setInterval(() => {
    restartCountdown.value--
    if (restartCountdown.value <= 0) {
      clearInterval(countdownInterval)
      // Try to check if service is back before reload
      checkServiceAndReload()
    }
  }, 1000)
}

async function checkServiceAndReload() {
  const maxRetries = 5
  const retryDelay = 1000

  for (let i = 0; i < maxRetries; i++) {
    try {
      const response = await fetch('/health', {
        method: 'GET',
        cache: 'no-cache'
      })
      if (response.ok) {
        // Service is back, reload page
        window.location.reload()
        return
      }
    } catch {
      // Service not ready yet
    }

    if (i < maxRetries - 1) {
      await new Promise((resolve) => setTimeout(resolve, retryDelay))
    }
  }

  // After retries, reload anyway
  window.location.reload()
}

function handleClickOutside(event: MouseEvent) {
  const target = event.target as Node
  const button = (event.target as Element).closest('button')
  if (dropdownRef.value && !dropdownRef.value.contains(target) && !button?.contains(target)) {
    closeDropdown()
  }
}

// 自动下载：有新版且开关打开时自动开始下载。
// autoInstall 勾选时等价于「自动下载 + 自动安装」，所以也走这里
// （后端下载阶段即完成替换，勾选 autoInstall 即自动装好）。
watch(
  () => hasUpdate.value,
  (yes) => {
    if (!yes || !isAdmin.value) return
    if (!autoDownload.value && !autoInstall.value) return
    if (downloadPercent.value > 0 || downloading.value || updating.value) return
    void startDownload()
  },
  { immediate: true }
)

onMounted(() => {
  if (isAdmin.value) {
    // Use cached version if available, otherwise fetch
    appStore.fetchVersion(false)
  }
  document.addEventListener('click', handleClickOutside)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
  stopProgress()
})
</script>

<style scoped>
/* 弹窗固定在版本号下方（原有位置），宽高统一。
   所有面板（初始/回退/更新）共用同一尺寸，切换时不再发生任何
   视觉上的大小或位置变化；内容过长的面板在自己的区域内滚动。 */
.version-modal {
  position: absolute;
  left: 0;
  top: 100%;
  margin-top: 0.5rem;
  width: 16rem;
  height: 24rem;
  max-height: 80vh;
}

/* 内容区独立滚动：面板切换不改变弹窗尺寸，
   过长内容（回退版本列表、更新日志）在这里滚动。 */
.version-modal-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 1rem;
  scrollbar-width: thin;
}

.version-modal-body::-webkit-scrollbar {
  width: 6px;
}

.version-modal-body::-webkit-scrollbar-thumb {
  border-radius: 3px;
  background: rgb(0 0 0 / 0.18);
}

.version-modal-body::-webkit-scrollbar-track {
  background: transparent;
}

.dropdown-enter-active,
.dropdown-leave-active {
  transition: opacity 0.2s ease;
}

/* 弹窗用内联 transform 做屏幕居中，转场里不能再写 transform，
   否则会覆盖居中偏移把弹窗推到视口外。只做淡入淡出。 */
.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
}

.rollback-enter-active,
.rollback-leave-active {
  transition: all 0.2s ease;
}

.rollback-enter-from,
.rollback-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

.line-clamp-3 {
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
