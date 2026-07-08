/**
 * Theme store — 集中管理"外观模式"与"主题色皮肤"。
 *
 * - mode:   'light' | 'dark' | 'system'（跟随系统）
 * - accent: 主题色皮肤 slug，对应 src/style.css 中的 [data-accent] 块
 *
 * 应用方式：
 *   - dark 通过在 <html> 上加/去 `.dark` 类实现（沿用 Tailwind darkMode:'class'）
 *   - accent 通过 <html data-accent="..."> 实现，driving primary-* CSS 变量
 *
 * 持久化：localStorage['theme-mode'] / ['theme-accent']；
 * 并回写旧键 ['theme']（'dark'|'light'）以兼容尚未迁移的读取方。
 */
import { defineStore } from 'pinia'

export type ThemeMode = 'light' | 'dark' | 'system'

export interface AccentOption {
  value: string
  /** 展示用中文名 */
  label: string
  /** 色板圆点预览色（对应该皮肤 500 阶） */
  color: string
}

/** 可选主题色皮肤（新增皮肤时，记得在 src/style.css 补一个 [data-accent] 块） */
export const ACCENTS: AccentOption[] = [
  { value: 'graphite', label: '石墨', color: '#52525b' },
  { value: 'teal', label: '青', color: '#14b8a6' },
  { value: 'indigo', label: 'Linear 靛', color: '#5e6ad2' },
  { value: 'violet', label: '紫', color: '#8b5cf6' },
  { value: 'pink', label: '粉', color: '#ec4899' },
  { value: 'amber', label: '琥珀', color: '#f59e0b' }
]

const ACCENT_VALUES = ACCENTS.map((a) => a.value)

const MODE_KEY = 'theme-mode'
const ACCENT_KEY = 'theme-accent'
const LEGACY_KEY = 'theme'
// 全站默认：石墨（纯黑白灰单色）+ 暗色优先（Linear 雅黑观感）
const DEFAULT_ACCENT = 'graphite'
const DEFAULT_MODE: ThemeMode = 'dark'

/** 读取持久化的外观模式（含旧键迁移）。可在 pinia 初始化前安全调用。 */
export function readPersistedMode(): ThemeMode {
  const m = localStorage.getItem(MODE_KEY)
  if (m === 'light' || m === 'dark' || m === 'system') return m
  const legacy = localStorage.getItem(LEGACY_KEY)
  if (legacy === 'dark') return 'dark'
  if (legacy === 'light') return 'light'
  return DEFAULT_MODE
}

/** 读取持久化的主题色皮肤。可在 pinia 初始化前安全调用。 */
export function readPersistedAccent(): string {
  const a = localStorage.getItem(ACCENT_KEY)
  return a && ACCENT_VALUES.includes(a) ? a : DEFAULT_ACCENT
}

function systemPrefersDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

/** 给定模式是否应呈现深色（system 时看系统偏好）。 */
export function resolveDark(mode: ThemeMode): boolean {
  return mode === 'dark' || (mode === 'system' && systemPrefersDark())
}

/** 把主题应用到 <html>（dark 类 + data-accent）。可在挂载前调用以避免 FOUC。 */
export function applyThemeToDom(mode: ThemeMode, accent: string): void {
  const el = document.documentElement
  el.classList.toggle('dark', resolveDark(mode))
  el.setAttribute('data-accent', accent)
}

export const useThemeStore = defineStore('theme', {
  state: () => ({
    mode: readPersistedMode() as ThemeMode,
    accent: readPersistedAccent(),
    // 响应式系统偏好；init() 里随 matchMedia 更新，供 isDark getter 响应式跟随
    systemDark: systemPrefersDark()
  }),
  getters: {
    /** 当前是否深色（供图标/图表等响应式据此换色）。 */
    isDark(state): boolean {
      // 用响应式 state.systemDark（而非 resolveDark 里的非响应式 matchMedia），
      // 使 isDark 在 mode 或系统偏好变化时都会响应式重算。
      return state.mode === 'dark' || (state.mode === 'system' && state.systemDark)
    },
    accentOptions: () => ACCENTS
  },
  actions: {
    apply() {
      applyThemeToDom(this.mode, this.accent)
    },
    setMode(mode: ThemeMode) {
      this.mode = mode
      localStorage.setItem(MODE_KEY, mode)
      // 兼容旧读取方：把解析后的明/暗写回旧键
      localStorage.setItem(LEGACY_KEY, resolveDark(mode) ? 'dark' : 'light')
      this.apply()
    },
    setAccent(accent: string) {
      if (!ACCENT_VALUES.includes(accent)) return
      this.accent = accent
      localStorage.setItem(ACCENT_KEY, accent)
      this.apply()
    },
    /** 明/暗直接切换（保留给旧的单键切换语义）。 */
    toggleDark() {
      this.setMode(this.isDark ? 'light' : 'dark')
    },
    /** 应用一次并监听系统主题变化（mode==='system' 时自动跟随）。 */
    init() {
      this.apply()
      const mql = window.matchMedia('(prefers-color-scheme: dark)')
      mql.addEventListener('change', (e) => {
        this.systemDark = e.matches
        if (this.mode === 'system') this.apply()
      })
    }
  }
})
