<template>
  <div class="space-y-3">
    <!-- 外观模式 -->
    <div>
      <p class="mb-1.5 text-xs font-medium text-gray-500 dark:text-gray-400">外观</p>
      <div class="grid grid-cols-3 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-800">
        <button
          v-for="m in modes"
          :key="m.value"
          type="button"
          class="flex items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium transition-colors"
          :class="
            theme.mode === m.value
              ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
              : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'
          "
          @click="theme.setMode(m.value)"
        >
          <component :is="m.icon" class="h-3.5 w-3.5" />
          <span>{{ m.label }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { h } from 'vue'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const theme = useThemeStore()

// 内联 SVG 图标（无需引入图标库）
function svg(path: string) {
  return {
    render: () =>
      h('svg', { fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.8' }, [
        h('path', { 'stroke-linecap': 'round', 'stroke-linejoin': 'round', d: path })
      ])
  }
}

const SunIcon = svg(
  'M12 3v2.25m6.364.386l-1.591 1.591M21 12h-2.25m-.386 6.364l-1.591-1.591M12 18.75V21m-4.773-4.227l-1.591 1.591M5.25 12H3m4.227-4.773L5.636 5.636M15.75 12a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0z'
)
const MoonIcon = svg(
  'M21.752 15.002A9.718 9.718 0 0118 15.75c-5.385 0-9.75-4.365-9.75-9.75 0-1.33.266-2.597.748-3.752A9.753 9.753 0 003 11.25C3 16.635 7.365 21 12.75 21a9.753 9.753 0 009.002-5.998z'
)
const DesktopIcon = svg(
  'M9 17.25v1.007a3 3 0 01-.879 2.122L7.5 21h9l-.621-.621A3 3 0 0115 18.257V17.25m6-12V15a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 15V5.25m18 0A2.25 2.25 0 0018.75 3H5.25A2.25 2.25 0 003 5.25m18 0V12a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 12V5.25'
)

const modes: { value: ThemeMode; label: string; icon: unknown }[] = [
  { value: 'light', label: '亮', icon: SunIcon },
  { value: 'dark', label: '暗', icon: MoonIcon },
  { value: 'system', label: '系统', icon: DesktopIcon }
]
</script>
