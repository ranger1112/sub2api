<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page: single-viewport layout, no scroll -->
  <div v-else class="relative flex h-screen flex-col overflow-hidden bg-white dark:bg-dark-950">
    <!-- Page-wide faint grid -->
    <div
      class="pointer-events-none absolute inset-0 -z-10 bg-[linear-gradient(rgb(var(--color-primary-500)/0.05)_1px,transparent_1px),linear-gradient(90deg,rgb(var(--color-primary-500)/0.05)_1px,transparent_1px)] bg-[size:64px_64px] [mask-image:radial-gradient(ellipse_65%_60%_at_50%_0%,black_35%,transparent_100%)]"
    ></div>

    <!-- Header -->
    <header class="relative z-20 shrink-0 border-b border-gray-200/70 px-6 py-3 dark:border-white/[0.06]">
      <nav class="mx-auto flex max-w-6xl items-center justify-between">
        <!-- Logo -->
        <div class="flex items-center">
          <div class="h-8 w-8 overflow-hidden rounded-lg shadow-sm">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain" />
          </div>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-3">
          <!-- Language Switcher -->
          <LocaleSwitcher />

          <!-- Doc Link -->
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-white/[0.06] dark:hover:text-white"
            :title="t('home.viewDocs')"
          >
            <Icon name="book" size="md" />
          </a>

          <!-- Theme Toggle -->
          <button
            @click="theme.toggleDark()"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-white/[0.06] dark:hover:text-white"
            :title="theme.isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="theme.isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <!-- Login / Dashboard Button -->
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center gap-1.5 rounded-full bg-gray-900 py-1 pl-1 pr-2.5 transition-colors hover:bg-gray-800 dark:bg-white dark:hover:bg-gray-200"
          >
            <span
              class="flex h-5 w-5 items-center justify-center rounded-full bg-gradient-to-br from-primary-400 to-primary-600 text-[10px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-xs font-medium text-white dark:text-dark-950">{{ t('home.dashboard') }}</span>
            <svg
              class="h-3 w-3 text-gray-400 dark:text-dark-500"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M4.5 19.5l15-15m0 0H8.25m11.25 0v11.25"
              />
            </svg>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex items-center rounded-full bg-gray-900 px-3.5 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-800 dark:bg-white dark:text-dark-950 dark:hover:bg-gray-200"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Hero: fills the remaining viewport, centered column stack -->
    <main
      class="relative z-10 flex min-h-0 flex-1 flex-col items-center justify-center overflow-hidden px-6 text-center"
    >
      <!-- Ambient glow -->
      <div
        class="pointer-events-none absolute left-1/2 top-0 h-[420px] w-[760px] -translate-x-1/2 -translate-y-1/3 rounded-full bg-primary-500/[0.07] blur-[110px]"
      ></div>
      <div
        class="pointer-events-none absolute bottom-0 left-1/2 h-56 w-56 -translate-x-1/2 rounded-full bg-primary-400/[0.05] blur-[90px]"
      ></div>

      <div class="relative flex w-full max-w-3xl flex-col items-center">
        <!-- Eyebrow -->
        <div
          class="inline-flex items-center gap-2 rounded-full border border-gray-200 bg-white/70 px-3.5 py-1.5 text-xs font-medium text-gray-600 shadow-sm backdrop-blur dark:border-white/[0.08] dark:bg-white/[0.03] dark:text-dark-300"
        >
          <span class="h-1.5 w-1.5 rounded-full bg-primary-500"></span>
          {{ t('home.heroEyebrow') }}
        </div>

        <!-- Headline -->
        <h1
          class="mt-6 max-w-2xl text-4xl font-semibold leading-[1.05] tracking-[-0.03em] text-gray-900 dark:text-white sm:text-5xl lg:text-6xl"
        >
          {{ t('home.heroSubtitle') }}
        </h1>

        <!-- Subtitle -->
        <p class="mt-5 max-w-lg text-base leading-relaxed text-gray-600 dark:text-dark-400 sm:text-lg">
          {{ t('home.heroDescription') }}
        </p>

        <!-- CTAs -->
        <div class="mt-8 flex flex-col items-center gap-3 sm:flex-row">
          <router-link
            :to="isAuthenticated ? dashboardPath : '/login'"
            class="btn btn-primary w-full px-7 py-3 text-base shadow-lg shadow-primary-500/25 sm:w-auto"
          >
            {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
            <Icon name="arrowRight" size="md" :stroke-width="2" />
          </router-link>
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="btn btn-secondary w-full px-7 py-3 text-base sm:w-auto"
          >
            {{ t('home.docs') }}
          </a>
          <router-link
            v-else-if="!isAuthenticated"
            to="/login"
            class="btn btn-secondary w-full px-7 py-3 text-base sm:w-auto"
          >
            {{ t('home.login') }}
          </router-link>
        </div>

        <!-- Terminal window preview: wide + flat, product's visual centerpiece -->
        <div class="mt-10 flex w-full justify-center">
          <div class="terminal-glow">
            <div class="terminal-window">
              <!-- Window header -->
              <div class="terminal-header">
                <div class="terminal-buttons">
                  <span class="btn-close"></span>
                  <span class="btn-minimize"></span>
                  <span class="btn-maximize"></span>
                </div>
                <span class="terminal-title">sub2api — zsh</span>
              </div>
              <!-- Terminal content -->
              <div class="terminal-body">
                <div class="code-line line-1">
                  <span class="code-prompt">$</span>
                  <span class="type-cmd">curl -s POST /v1/messages -d model=claude</span>
                  <span class="code-result">
                    <span class="code-arrow">&#8618;</span>
                    <span class="code-success">200 OK</span>
                    <span class="code-response">{"reply":"Hi, I'm Claude"}</span>
                  </span>
                </div>
                <div class="code-line line-2">
                  <span class="code-prompt">$</span>
                  <span class="type-cmd">curl -s POST /v1/messages -d model=gpt-4o</span>
                  <span class="code-result">
                    <span class="code-arrow">&#8618;</span>
                    <span class="code-success">200 OK</span>
                    <span class="code-response">{"reply":"Hi, I'm GPT-4o"}</span>
                  </span>
                </div>
                <div class="code-line line-3">
                  <span class="code-prompt">$</span>
                  <span class="cursor"></span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </main>

    <!-- Compact provider strip -->
    <div class="relative z-10 shrink-0 border-t border-gray-200/70 px-6 py-2.5 dark:border-white/[0.06]">
      <div class="mx-auto flex max-w-6xl flex-wrap items-center justify-center gap-2">
        <span class="mr-1 text-[10px] font-medium uppercase tracking-[0.15em] text-gray-400 dark:text-dark-500">
          {{ t('home.providers.description') }}
        </span>
        <div
          class="flex items-center gap-1.5 rounded-lg border border-gray-200/70 bg-white px-2.5 py-1 dark:border-white/[0.06] dark:bg-white/[0.02]"
        >
          <span class="flex h-4 w-4 items-center justify-center rounded bg-[#D97757] text-[9px] font-bold text-white">C</span>
          <span class="text-xs font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.claude') }}</span>
        </div>
        <div
          class="flex items-center gap-1.5 rounded-lg border border-gray-200/70 bg-white px-2.5 py-1 dark:border-white/[0.06] dark:bg-white/[0.02]"
        >
          <span class="flex h-4 w-4 items-center justify-center rounded bg-[#10A37F] text-[9px] font-bold text-white">G</span>
          <span class="text-xs font-medium text-gray-700 dark:text-dark-200">GPT</span>
        </div>
        <div
          class="flex items-center gap-1.5 rounded-lg border border-gray-200/70 bg-white px-2.5 py-1 dark:border-white/[0.06] dark:bg-white/[0.02]"
        >
          <span class="flex h-4 w-4 items-center justify-center rounded bg-[#4285F4] text-[9px] font-bold text-white">G</span>
          <span class="text-xs font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.gemini') }}</span>
        </div>
        <div
          class="flex items-center gap-1.5 rounded-lg border border-gray-200/70 bg-white px-2.5 py-1 dark:border-white/[0.06] dark:bg-white/[0.02]"
        >
          <span class="flex h-4 w-4 items-center justify-center rounded bg-[#F43F5E] text-[9px] font-bold text-white">A</span>
          <span class="text-xs font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.antigravity') }}</span>
        </div>
        <div
          class="flex items-center gap-1.5 rounded-lg border border-dashed border-gray-200/70 bg-white/60 px-2.5 py-1 opacity-60 dark:border-white/[0.08] dark:bg-white/[0.01]"
        >
          <span class="flex h-4 w-4 items-center justify-center rounded bg-gray-400 text-[9px] font-bold text-white dark:bg-dark-600">+</span>
          <span class="text-xs font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.more') }}</span>
        </div>
      </div>
    </div>

    <!-- Footer -->
    <footer class="relative z-10 shrink-0 border-t border-gray-200/70 px-6 py-2 dark:border-white/[0.06]">
      <div class="mx-auto flex max-w-6xl items-center justify-between gap-4 text-center sm:text-left">
        <p class="text-xs text-gray-500 dark:text-dark-400">
          &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
        </p>
        <div class="flex items-center gap-4">
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            {{ t('home.docs') }}
          </a>
          <a
            :href="githubUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            GitHub
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import { useThemeStore } from '@/stores/theme'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '')
const docUrl = computed(() => appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const theme = useThemeStore()

// GitHub URL
const githubUrl = 'https://github.com/Wei-Shaw/sub2api'

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => isAdmin.value ? '/admin/dashboard' : '/dashboard')
const userInitial = computed(() => {
  const user = authStore.user
  if (!user || !user.email) return ''
  return user.email.charAt(0).toUpperCase()
})

// Current year for footer
const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  // Check auth state
  authStore.checkAuth()

  // Ensure public settings are loaded (will use cache if already loaded from injected config)
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})
</script>

<style scoped>
/* Terminal glow wrapper - color follows the active accent skin */
.terminal-glow {
  position: relative;
  display: inline-block;
  width: 100%;
  max-width: 680px;
}

.terminal-glow::before {
  content: '';
  position: absolute;
  inset: -20px;
  background: radial-gradient(closest-side, rgb(var(--color-primary-500) / 0.22), transparent 72%);
  filter: blur(26px);
  z-index: -1;
  border-radius: 26px;
}

/* Terminal Window */
.terminal-window {
  width: 100%;
  background: linear-gradient(145deg, #17181b 0%, #0e0f11 100%);
  border-radius: 14px;
  box-shadow:
    0 25px 50px -12px rgba(0, 0, 0, 0.4),
    0 0 0 1px rgba(255, 255, 255, 0.1),
    inset 0 1px 0 rgba(255, 255, 255, 0.1);
  overflow: hidden;
  transform: perspective(1000px) rotateX(2deg) rotateY(-2deg);
  transition: transform 0.3s ease;
}

.terminal-window:hover {
  transform: perspective(1000px) rotateX(0deg) rotateY(0deg) translateY(-4px);
}

/* Terminal Header */
.terminal-header {
  display: flex;
  align-items: center;
  padding: 10px 14px;
  background: rgba(30, 41, 59, 0.8);
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}

.terminal-buttons {
  display: flex;
  gap: 8px;
}

.terminal-buttons span {
  width: 11px;
  height: 11px;
  border-radius: 50%;
}

.btn-close {
  background: #ef4444;
}
.btn-minimize {
  background: #eab308;
}
.btn-maximize {
  background: #22c55e;
}

.terminal-title {
  flex: 1;
  text-align: center;
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: #64748b;
  margin-right: 48px;
}

/* Terminal Body — wide + flat: each request/response pair sits on one row */
.terminal-body {
  padding: 18px 22px;
  font-family: ui-monospace, 'Fira Code', monospace;
  font-size: 13px;
  line-height: 2;
}

.code-line {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  opacity: 0;
  animation: line-appear 0.4s ease forwards;
}

.line-1 {
  animation-delay: 0.15s;
}
.line-2 {
  animation-delay: 1.9s;
}
.line-3 {
  animation-delay: 3.6s;
}

@keyframes line-appear {
  from {
    opacity: 0;
    transform: translateY(5px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.code-prompt {
  color: #e2e8f0;
  font-weight: bold;
}

/* Response cluster (arrow + status + payload) fades in once typing finishes */
.code-result {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  opacity: 0;
  animation: fade-in-result 0.4s ease forwards;
}

.line-1 .code-result {
  animation-delay: 1.3s;
}
.line-2 .code-result {
  animation-delay: 3s;
}

@keyframes fade-in-result {
  from {
    opacity: 0;
    transform: translateY(3px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.code-arrow {
  color: #52525b;
  margin-right: -2px;
}
.code-success {
  color: #34d399;
  background: rgba(52, 211, 153, 0.15);
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 600;
}
.code-response {
  color: #cbd5e1;
}

/* Typewriter reveal for the curl command lines */
.type-cmd {
  display: inline-block;
  overflow: hidden;
  white-space: nowrap;
  vertical-align: bottom;
  width: 0;
  color: #cbd5e1;
  animation: reveal-type 0.9s steps(28, end) forwards;
}

@keyframes reveal-type {
  from {
    width: 0;
  }
  to {
    width: 43ch;
  }
}

.line-1 .type-cmd {
  animation-delay: 0.4s;
}
.line-2 .type-cmd {
  animation-delay: 2.1s;
}

/* Blinking Cursor */
.cursor {
  display: inline-block;
  width: 8px;
  height: 14px;
  background: rgb(var(--color-primary-400));
  animation: blink 1s step-end infinite;
}

@keyframes blink {
  0%,
  50% {
    opacity: 1;
  }
  51%,
  100% {
    opacity: 0;
  }
}
</style>
