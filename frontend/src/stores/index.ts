/**
 * Pinia Stores Export
 * Central export point for all application stores
 */

export { useAuthStore } from './auth'
export { useAppStore } from './app'
export { useAdminSettingsStore } from './adminSettings'
export { useSubscriptionStore } from './subscriptions'
export { useCheckinStore } from './checkin'
export { useOnboardingStore } from './onboarding'
export { useAnnouncementStore } from './announcements'
export { usePaymentStore } from './payment'
export { useAdminComplianceStore } from './adminCompliance'
export { useThemeStore, ACCENTS } from './theme'
export type { ThemeMode, AccentOption } from './theme'

// Re-export types for convenience
export type { User, LoginRequest, RegisterRequest, AuthResponse } from '@/types'
export type { Toast, ToastType, AppState } from '@/types'
