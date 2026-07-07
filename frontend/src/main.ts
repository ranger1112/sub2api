import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import i18n, { initI18n } from './i18n'
import { useAppStore } from '@/stores/app'
import {
  applyThemeToDom,
  readPersistedAccent,
  readPersistedMode,
  useThemeStore
} from '@/stores/theme'
import '@fontsource-variable/inter/wght.css'
import './style.css'

function initThemeClass() {
  // FOUC-safe：挂载前先套用持久化的模式(dark 类)与皮肤(data-accent)。
  applyThemeToDom(readPersistedMode(), readPersistedAccent())
}

async function bootstrap() {
  // Apply theme class globally before app mount to keep all routes consistent.
  initThemeClass()

  const app = createApp(App)
  const pinia = createPinia()
  app.use(pinia)

  // 主题 store：重新应用并挂载"跟随系统"监听。
  useThemeStore().init()

  // Initialize settings from injected config BEFORE mounting (prevents flash)
  // This must happen after pinia is installed but before router and i18n
  const appStore = useAppStore()
  appStore.initFromInjectedConfig()

  // Set document title immediately after config is loaded
  if (appStore.siteName && appStore.siteName !== 'Sub2API') {
    document.title = `${appStore.siteName} - AI API Gateway`
  }

  await initI18n()

  app.use(router)
  app.use(i18n)

  // 等待路由器完成初始导航后再挂载，避免竞态条件导致的空白渲染
  await router.isReady()
  app.mount('#app')
}

bootstrap()
