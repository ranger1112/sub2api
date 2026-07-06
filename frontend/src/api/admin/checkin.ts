/**
 * Admin Daily Check-in API endpoints
 * Manages the check-in feature config, analytics, and reward tiers
 */

import { apiClient } from '../client'

export interface CheckinConfig {
  enabled: boolean
  min_reward: number
  max_reward: number
  base_cap: number
  weight_recharge: number
  weight_usage: number
  weight_activity: number
  recharge_cap: number
  usage_cap: number
  streak_cap: number
  beta_min: number
  beta_max: number
  daily_budget: number
  user_monthly_cap: number
  min_account_age_days: number
  require_recharge: boolean
}

export interface CheckinAnalyticsTrendPoint {
  date: string
  gifted: number
  count: number
}

export interface CheckinAnalytics {
  total_gifted: number
  today_gifted: number
  month_gifted: number
  total_checkins: number
  today_checkins: number
  distinct_users_today: number
  distinct_users_month: number
  trend: CheckinAnalyticsTrendPoint[]
}

export interface CheckinTier {
  id: number
  name: string
  enabled: boolean
  match_type: 'recharge' | 'score'
  match_threshold: number
  min_reward: number
  max_reward: number
  base_cap: number
  beta_min: number
  beta_max: number
  sort_order: number
  created_at: string
  updated_at: string
}

export type CheckinTierRequest = Omit<CheckinTier, 'id' | 'created_at' | 'updated_at'>

export async function getConfig(): Promise<CheckinConfig> {
  const { data } = await apiClient.get<CheckinConfig>('/admin/checkin/config')
  return data
}

export async function updateConfig(request: CheckinConfig): Promise<CheckinConfig> {
  const { data } = await apiClient.put<CheckinConfig>('/admin/checkin/config', request)
  return data
}

export async function getAnalytics(): Promise<CheckinAnalytics> {
  const { data } = await apiClient.get<CheckinAnalytics>('/admin/checkin/analytics')
  return data
}

export async function listTiers(): Promise<CheckinTier[]> {
  const { data } = await apiClient.get<CheckinTier[]>('/admin/checkin/tiers')
  return data
}

export async function createTier(request: CheckinTierRequest): Promise<CheckinTier> {
  const { data } = await apiClient.post<CheckinTier>('/admin/checkin/tiers', request)
  return data
}

export async function updateTier(id: number, request: CheckinTierRequest): Promise<CheckinTier> {
  const { data } = await apiClient.put<CheckinTier>(`/admin/checkin/tiers/${id}`, request)
  return data
}

export async function deleteTier(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/checkin/tiers/${id}`)
  return data
}

const adminCheckinAPI = {
  getConfig,
  updateConfig,
  getAnalytics,
  listTiers,
  createTier,
  updateTier,
  deleteTier
}

export default adminCheckinAPI
