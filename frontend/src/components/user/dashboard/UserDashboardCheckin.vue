<template>
  <div v-if="status && status.enabled" class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('checkin.title') }}</h2>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('checkin.subtitle') }}</p>
    </div>
    <div class="p-4">
      <div class="flex items-center gap-4 rounded-xl bg-gray-50 p-4 dark:bg-dark-800/50">
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl bg-amber-100 dark:bg-amber-900/30">
          <Icon name="gift" size="lg" class="text-amber-600 dark:text-amber-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('checkin.streakLabel', { streak: status.streak }) }}
          </p>
          <p class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('checkin.rangeHint', { min: formatCurrency(status.min_reward), max: formatCurrency(status.max_reward) }) }}
          </p>
        </div>
      </div>

      <button
        @click="handleClaim"
        :disabled="!status.can_check_in || claiming"
        class="btn btn-primary mt-4 w-full"
      >
        {{ buttonLabel }}
      </button>

      <p v-if="status.checked_in_today" class="mt-2 text-center text-xs text-gray-400 dark:text-dark-500">
        {{ t('checkin.comeBackTomorrow') }}
      </p>
      <p
        v-else-if="status.enabled && !status.can_check_in"
        class="mt-2 text-center text-xs text-gray-400 dark:text-dark-500"
      >
        {{ t('checkin.unavailable') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import Icon from '@/components/icons/Icon.vue'
import { useCheckinStore } from '@/stores/checkin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { formatCurrency } from '@/utils/format'

const { t } = useI18n()
const checkinStore = useCheckinStore()
const appStore = useAppStore()
const { status, claiming } = storeToRefs(checkinStore)

const buttonLabel = computed(() => {
  if (claiming.value) return t('common.loading')
  if (status.value?.checked_in_today) return t('checkin.checkedInToday')
  return t('checkin.checkIn')
})

// Known business error codes returned by the check-in claim endpoint, mapped to
// localized copy. Falls back to the raw server message for anything else.
// Built lazily (not module/setup-time) so translations stay correct across locale switches.
function errorI18nMap(): Record<string, string> {
  return {
    CHECKIN_DISABLED: t('checkin.errDisabled'),
    CHECKIN_ALREADY_CLAIMED: t('checkin.errAlreadyClaimed'),
    CHECKIN_NOT_ELIGIBLE: t('checkin.errNotEligible'),
    CHECKIN_BUDGET_EXHAUSTED: t('checkin.errBudgetExhausted'),
    CHECKIN_MONTHLY_CAP_REACHED: t('checkin.errMonthlyCap')
  }
}

function resolveErrorMessage(err: unknown): string {
  const map = errorI18nMap()
  const code = extractApiErrorCode(err)
  if (code && map[code]) return map[code]
  const message = extractApiErrorMessage(err, t('common.error'))
  if (map[message]) return map[message]
  return message
}

async function handleClaim() {
  if (!status.value?.can_check_in || claiming.value) return
  try {
    const result = await checkinStore.claim()
    appStore.showSuccess(t('checkin.rewardToast', { amount: formatCurrency(result.reward_amount) }))
  } catch (error) {
    appStore.showError(resolveErrorMessage(error))
    // Reconcile with the server in case client/server state drifted (e.g. the
    // server rejected with CHECKIN_ALREADY_CLAIMED). Swallow errors so they
    // can't mask the original toast above.
    try {
      await checkinStore.fetchStatus(true)
    } catch {
      // ignore
    }
  }
}

onMounted(() => {
  checkinStore.fetchStatus()
})
</script>
