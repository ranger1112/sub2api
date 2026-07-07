<template>
  <div ref="root" class="relative">
    <button
      type="button"
      class="sidebar-link w-full"
      :class="{ 'sidebar-link-collapsed': collapsed }"
      :title="collapsed ? '外观设置' : undefined"
      @click="open = !open"
    >
      <component :is="currentIcon" class="h-5 w-5 flex-shrink-0" />
      <span
        class="sidebar-label"
        :class="{ 'sidebar-label-collapsed': collapsed }"
        :aria-hidden="collapsed ? 'true' : 'false'"
        >外观</span
      >
    </button>

    <transition name="fade">
      <div
        v-if="open"
        class="absolute bottom-full left-0 z-50 mb-2 w-56 rounded-xl border border-gray-200 bg-white p-3 shadow-lg dark:border-dark-700 dark:bg-dark-900"
      >
        <ThemeControls />
      </div>
    </transition>
  </div>
</template>

<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { onClickOutside } from '@vueuse/core'
import { useThemeStore } from '@/stores/theme'
import ThemeControls from './ThemeControls.vue'

defineProps<{ collapsed?: boolean }>()

const theme = useThemeStore()
const open = ref(false)
const root = ref<HTMLElement | null>(null)
onClickOutside(root, () => (open.value = false))

function svg(path: string) {
  return {
    render: () =>
      h('svg', { fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.5' }, [
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

const currentIcon = computed(() =>
  theme.mode === 'light' ? SunIcon : theme.mode === 'dark' ? MoonIcon : DesktopIcon
)
</script>
