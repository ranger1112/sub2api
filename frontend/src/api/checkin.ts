/**
 * Daily check-in API endpoints
 * Handles daily check-in reward status and claiming for users
 */

import { apiClient } from './client'

export interface CheckinHistoryItem {
  date: string
  reward: number
  streak: number
}

export interface CheckinStatus {
  enabled: boolean
  can_check_in: boolean
  checked_in_today: boolean
  streak: number
  last_reward?: number
  last_check_in_date?: string | null
  total_reward: number
  next_available_at?: string | null
  min_reward: number
  max_reward: number
  history: CheckinHistoryItem[]
}

export interface CheckinClaimResult {
  reward_amount: number
  new_balance: number
  streak: number
  check_in_date: string
}

/**
 * Get the current user's daily check-in status
 * @returns Check-in status including streak, eligibility, and reward range
 */
export async function getCheckinStatus(): Promise<CheckinStatus> {
  const { data } = await apiClient.get<CheckinStatus>('/checkin')
  return data
}

/**
 * Claim today's check-in reward
 * @returns Claim result with the reward amount and updated balance
 */
export async function claimCheckin(): Promise<CheckinClaimResult> {
  const { data } = await apiClient.post<CheckinClaimResult>('/checkin')
  return data
}

export const checkinAPI = {
  getCheckinStatus,
  claimCheckin
}

export default checkinAPI
