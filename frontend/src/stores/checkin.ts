/**
 * Daily Check-in Store
 * Global state management for the daily check-in reward feature with caching and deduplication
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { checkinAPI, type CheckinStatus, type CheckinClaimResult } from '@/api'
import { useAuthStore } from '@/stores/auth'

// Cache TTL: 30 seconds
const CACHE_TTL_MS = 30_000

// Request generation counter to invalidate stale in-flight responses
let requestGeneration = 0

export const useCheckinStore = defineStore('checkin', () => {
  // State
  const status = ref<CheckinStatus | null>(null)
  const loading = ref(false)
  const claiming = ref(false)
  const loaded = ref(false)
  const lastFetchedAt = ref<number | null>(null)

  // In-flight request deduplication
  let activePromise: Promise<CheckinStatus> | null = null

  /**
   * Fetch check-in status with caching and deduplication
   * @param force - Force refresh even if cache is valid
   */
  async function fetchStatus(force = false): Promise<CheckinStatus> {
    const now = Date.now()

    // Return cached data if valid
    if (
      !force &&
      loaded.value &&
      lastFetchedAt.value &&
      now - lastFetchedAt.value < CACHE_TTL_MS &&
      status.value
    ) {
      return status.value
    }

    // Return in-flight request if exists (deduplication)
    if (activePromise && !force) {
      return activePromise
    }

    const currentGeneration = ++requestGeneration

    // Start new request
    loading.value = true
    const requestPromise = checkinAPI
      .getCheckinStatus()
      .then((data) => {
        if (currentGeneration === requestGeneration) {
          status.value = data
          loaded.value = true
          lastFetchedAt.value = Date.now()
        }
        return data
      })
      .catch((error) => {
        console.error('Failed to fetch check-in status:', error)
        throw error
      })
      .finally(() => {
        if (activePromise === requestPromise) {
          loading.value = false
          activePromise = null
        }
      })

    activePromise = requestPromise

    return activePromise
  }

  /**
   * Claim today's check-in reward
   * Refreshes the user's balance and local status optimistically on success.
   * @returns The claim result so the caller can display a toast
   * @throws Rethrows the API error for the caller to handle (e.g. toast)
   */
  async function claim(): Promise<CheckinClaimResult> {
    claiming.value = true
    try {
      const result = await checkinAPI.claimCheckin()

      // Optimistically update local status so the UI reflects the claim immediately
      if (status.value) {
        status.value = {
          ...status.value,
          can_check_in: false,
          checked_in_today: true,
          streak: result.streak,
          last_reward: result.reward_amount,
          last_check_in_date: result.check_in_date,
          total_reward: status.value.total_reward + result.reward_amount
        }
      }

      // Invalidate any in-flight fetchStatus() request so a stale GET resolving
      // after this optimistic update can't clobber it (same guard as clear()).
      requestGeneration++
      activePromise = null

      lastFetchedAt.value = Date.now()

      // Refresh balance shown elsewhere in the app
      try {
        await useAuthStore().refreshUser()
      } catch (error) {
        console.error('Failed to refresh user after check-in:', error)
      }

      return result
    } finally {
      claiming.value = false
    }
  }

  /**
   * Invalidate cache (force next fetch to reload)
   */
  function invalidateCache(): void {
    lastFetchedAt.value = null
  }

  /**
   * Clear all check-in data
   */
  function clear(): void {
    requestGeneration++
    activePromise = null
    status.value = null
    loaded.value = false
    lastFetchedAt.value = null
  }

  return {
    // State
    status,
    loading,
    claiming,
    loaded,
    lastFetchedAt,

    // Actions
    fetchStatus,
    claim,
    invalidateCache,
    clear
  }
})
