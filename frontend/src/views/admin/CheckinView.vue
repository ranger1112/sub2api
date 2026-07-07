<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- (B) Analytics -->
      <CheckinStatsCards :analytics="analytics" />

      <!-- (A) Config -->
      <div class="card">
        <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('admin.checkin.config.title') }}
          </h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.config.description') }}
          </p>
        </div>

        <form @submit.prevent="handleSaveConfig" class="space-y-8 p-6">
          <!-- Enabled toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t('admin.checkin.config.enabled') }}
              </label>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.checkin.config.enabledHint') }}
              </p>
            </div>
            <Toggle v-model="configForm.enabled" />
          </div>

          <!-- Reward range -->
          <div>
            <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.config.groups.rewardRange') }}
            </h3>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div>
                <label class="input-label">{{ t('admin.checkin.config.minReward') }}</label>
                <input
                  v-model.number="configForm.min_reward"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.min_reward" class="input-error-text">{{ configErrors.min_reward }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.minRewardHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.maxReward') }}</label>
                <input
                  v-model.number="configForm.max_reward"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.max_reward" class="input-error-text">{{ configErrors.max_reward }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.maxRewardHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.baseCap') }}</label>
                <input
                  v-model.number="configForm.base_cap"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.base_cap" class="input-error-text">{{ configErrors.base_cap }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.baseCapHint') }}</p>
              </div>
            </div>
          </div>

          <!-- Weights & caps -->
          <div>
            <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.config.groups.weightsCaps') }}
            </h3>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div>
                <label class="input-label">{{ t('admin.checkin.config.weightRecharge') }}</label>
                <input
                  v-model.number="configForm.weight_recharge"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.weight_recharge" class="input-error-text">{{ configErrors.weight_recharge }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.weightUsage') }}</label>
                <input
                  v-model.number="configForm.weight_usage"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.weight_usage" class="input-error-text">{{ configErrors.weight_usage }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.weightActivity') }}</label>
                <input
                  v-model.number="configForm.weight_activity"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.weight_activity" class="input-error-text">{{ configErrors.weight_activity }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.rechargeCap') }}</label>
                <input
                  v-model.number="configForm.recharge_cap"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.recharge_cap" class="input-error-text">{{ configErrors.recharge_cap }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.usageCap') }}</label>
                <input
                  v-model.number="configForm.usage_cap"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.usage_cap" class="input-error-text">{{ configErrors.usage_cap }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.streakCap') }}</label>
                <input
                  v-model.number="configForm.streak_cap"
                  type="number"
                  step="1"
                  min="1"
                  class="input"
                />
                <p v-if="configErrors.streak_cap" class="input-error-text">{{ configErrors.streak_cap }}</p>
              </div>
            </div>
          </div>

          <!-- Guardrails -->
          <div>
            <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.config.groups.guardrails') }}
            </h3>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <label class="input-label">{{ t('admin.checkin.config.betaMin') }}</label>
                <input
                  v-model.number="configForm.beta_min"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.beta_min" class="input-error-text">{{ configErrors.beta_min }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.betaMinHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.betaMax') }}</label>
                <input
                  v-model.number="configForm.beta_max"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.beta_max" class="input-error-text">{{ configErrors.beta_max }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.betaMaxHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.dailyBudget') }}</label>
                <input
                  v-model.number="configForm.daily_budget"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.daily_budget" class="input-error-text">{{ configErrors.daily_budget }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.dailyBudgetHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('admin.checkin.config.userMonthlyCap') }}</label>
                <input
                  v-model.number="configForm.user_monthly_cap"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.user_monthly_cap" class="input-error-text">{{ configErrors.user_monthly_cap }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.userMonthlyCapHint') }}</p>
              </div>
            </div>
          </div>

          <!-- Eligibility -->
          <div>
            <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.config.groups.eligibility') }}
            </h3>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <label class="input-label">{{ t('admin.checkin.config.minAccountAgeDays') }}</label>
                <input
                  v-model.number="configForm.min_account_age_days"
                  type="number"
                  step="1"
                  min="0"
                  class="input"
                />
                <p v-if="configErrors.min_account_age_days" class="input-error-text">{{ configErrors.min_account_age_days }}</p>
                <p v-else class="input-hint">{{ t('admin.checkin.config.minAccountAgeDaysHint') }}</p>
              </div>
              <div class="flex items-center justify-between">
                <div>
                  <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
                    {{ t('admin.checkin.config.requireRecharge') }}
                  </label>
                  <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                    {{ t('admin.checkin.config.requireRechargeHint') }}
                  </p>
                </div>
                <Toggle v-model="configForm.require_recharge" />
              </div>
            </div>
          </div>

          <div class="flex justify-end border-t border-gray-100 pt-6 dark:border-dark-700">
            <button type="submit" :disabled="savingConfig" class="btn btn-primary">
              {{ savingConfig ? t('common.saving') : t('common.save') }}
            </button>
          </div>
        </form>
      </div>

      <!-- (C) Reward Tiers -->
      <div class="card">
        <div class="flex items-center justify-between border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.tiers.title') }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.checkin.tiers.description') }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <button
              @click="loadTiers"
              :disabled="tiersLoading"
              class="btn btn-secondary"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="md" :class="tiersLoading ? 'animate-spin' : ''" />
            </button>
            <button @click="openCreateTierDialog" class="btn btn-primary">
              <Icon name="plus" size="md" class="mr-1" />
              {{ t('admin.checkin.tiers.createTier') }}
            </button>
          </div>
        </div>

        <div class="p-2">
          <DataTable :columns="tierColumns" :data="tiers" :loading="tiersLoading">
            <template #cell-enabled="{ value }">
              <span class="badge" :class="value ? 'badge-success' : 'badge-gray'">
                {{ value ? t('common.enabled') : t('common.disabled') }}
              </span>
            </template>
            <template #cell-match_type="{ value }">
              <span class="text-sm text-gray-700 dark:text-gray-300">
                {{ value === 'recharge' ? t('admin.checkin.tiers.matchTypeRecharge') : t('admin.checkin.tiers.matchTypeScore') }}
              </span>
            </template>
            <template #cell-match_threshold="{ value }">
              <span class="text-sm text-gray-700 dark:text-gray-300">{{ value }}</span>
            </template>
            <template #cell-min_reward="{ value }">
              <span class="text-sm text-gray-900 dark:text-white">{{ formatCurrency(value) }}</span>
            </template>
            <template #cell-max_reward="{ value }">
              <span class="text-sm text-gray-900 dark:text-white">{{ formatCurrency(value) }}</span>
            </template>
            <template #cell-base_cap="{ value }">
              <span class="text-sm text-gray-900 dark:text-white">{{ formatCurrency(value) }}</span>
            </template>
            <template #cell-beta_range="{ row }">
              <span class="text-sm text-gray-500 dark:text-dark-400">{{ row.beta_min }} - {{ row.beta_max }}</span>
            </template>
            <template #cell-actions="{ row }">
              <div class="flex items-center space-x-1">
                <button
                  @click="openEditTierDialog(row)"
                  class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-600 dark:hover:text-gray-300"
                  :title="t('common.edit')"
                >
                  <Icon name="edit" size="sm" />
                </button>
                <button
                  @click="handleDeleteTier(row)"
                  class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                  :title="t('common.delete')"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </template>
          </DataTable>
        </div>
      </div>

      <!-- (D) Check-in Records -->
      <div class="card">
        <div class="flex items-center justify-between border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.checkin.records.title') }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.checkin.records.description') }}
            </p>
          </div>
          <button
            @click="loadRecords"
            :disabled="recordsLoading"
            class="btn btn-secondary"
            :title="t('common.refresh')"
          >
            <Icon name="refresh" size="md" :class="recordsLoading ? 'animate-spin' : ''" />
          </button>
        </div>

        <div class="flex flex-wrap items-end gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <div>
            <label class="input-label">{{ t('admin.checkin.records.filters.userId') }}</label>
            <input
              v-model.number="recordFilters.user_id"
              type="number"
              min="1"
              class="input w-32"
              :placeholder="t('admin.checkin.records.filters.userIdPlaceholder')"
            />
          </div>
          <div>
            <label class="input-label">{{ t('admin.checkin.records.filters.startDate') }}</label>
            <input v-model="recordFilters.start_date" type="date" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.checkin.records.filters.endDate') }}</label>
            <input v-model="recordFilters.end_date" type="date" class="input" />
          </div>
          <div class="flex gap-2">
            <button @click="handleRecordsFilterApply" class="btn btn-primary">{{ t('common.search') }}</button>
            <button @click="handleRecordsFilterReset" class="btn btn-secondary">{{ t('common.reset') }}</button>
          </div>
        </div>

        <div class="p-2">
          <DataTable :columns="recordColumns" :data="records" :loading="recordsLoading">
            <template #cell-user="{ row }">
              <div class="text-sm">
                <p class="font-medium text-gray-900 dark:text-white">{{ row.user_email }}</p>
                <p class="text-xs text-gray-500 dark:text-dark-400">{{ row.user_username }}</p>
              </div>
            </template>
            <template #cell-reward_amount="{ value }">
              <span class="text-sm text-gray-900 dark:text-white">{{ formatCurrency(value) }}</span>
            </template>
            <template #cell-created_at="{ value }">
              <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) }}</span>
            </template>
            <template #empty>
              <div class="flex flex-col items-center">
                <Icon name="inbox" size="xl" class="mb-4 h-12 w-12 text-gray-400 dark:text-dark-500" />
                <p class="text-lg font-medium text-gray-900 dark:text-gray-100">
                  {{ t('admin.checkin.records.empty') }}
                </p>
              </div>
            </template>
          </DataTable>
        </div>

        <Pagination
          v-if="recordsPagination.total > 0"
          :page="recordsPagination.page"
          :total="recordsPagination.total"
          :page-size="recordsPagination.page_size"
          @update:page="handleRecordsPageChange"
          @update:pageSize="handleRecordsPageSizeChange"
        />
      </div>
    </div>

    <!-- Create / Edit Tier Dialog -->
    <BaseDialog
      :show="showTierDialog"
      :title="editingTier ? t('admin.checkin.tiers.editTier') : t('admin.checkin.tiers.createTier')"
      width="normal"
      @close="closeTierDialog"
    >
      <form id="checkin-tier-form" @submit.prevent="handleSaveTier" class="space-y-4">
        <div>
          <label class="input-label">{{ t('admin.checkin.tiers.name') }}</label>
          <input v-model="tierForm.name" type="text" required class="input" />
          <p v-if="tierErrors.name" class="input-error-text">{{ tierErrors.name }}</p>
        </div>

        <div class="flex items-center justify-between">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.checkin.tiers.enabled') }}
          </label>
          <Toggle v-model="tierForm.enabled" />
        </div>

        <div>
          <label class="input-label">{{ t('admin.checkin.tiers.matchType') }}</label>
          <Select v-model="tierForm.match_type" :options="matchTypeOptions" />
          <p v-if="tierErrors.match_type" class="input-error-text">{{ tierErrors.match_type }}</p>
        </div>

        <div>
          <label class="input-label">{{ t('admin.checkin.tiers.matchThreshold') }}</label>
          <input v-model.number="tierForm.match_threshold" type="number" step="0.01" min="0" class="input" />
          <p v-if="tierErrors.match_threshold" class="input-error-text">{{ tierErrors.match_threshold }}</p>
        </div>

        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="input-label">{{ t('admin.checkin.tiers.minReward') }}</label>
            <input v-model.number="tierForm.min_reward" type="number" step="0.01" min="0" class="input" />
            <p v-if="tierErrors.min_reward" class="input-error-text">{{ tierErrors.min_reward }}</p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.checkin.tiers.maxReward') }}</label>
            <input v-model.number="tierForm.max_reward" type="number" step="0.01" min="0" class="input" />
            <p v-if="tierErrors.max_reward" class="input-error-text">{{ tierErrors.max_reward }}</p>
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.checkin.tiers.baseCap') }}</label>
          <input v-model.number="tierForm.base_cap" type="number" step="0.01" min="0" class="input" />
          <p v-if="tierErrors.base_cap" class="input-error-text">{{ tierErrors.base_cap }}</p>
        </div>

        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="input-label">{{ t('admin.checkin.tiers.betaMin') }}</label>
            <input v-model.number="tierForm.beta_min" type="number" step="0.01" min="0" class="input" />
            <p v-if="tierErrors.beta_min" class="input-error-text">{{ tierErrors.beta_min }}</p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.checkin.tiers.betaMax') }}</label>
            <input v-model.number="tierForm.beta_max" type="number" step="0.01" min="0" class="input" />
            <p v-if="tierErrors.beta_max" class="input-error-text">{{ tierErrors.beta_max }}</p>
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.checkin.tiers.sortOrder') }}</label>
          <input v-model.number="tierForm.sort_order" type="number" step="1" class="input" />
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="closeTierDialog" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" form="checkin-tier-form" :disabled="savingTier" class="btn btn-primary">
            {{ savingTier ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Delete Tier Confirmation -->
    <ConfirmDialog
      :show="showDeleteTierDialog"
      :title="t('admin.checkin.tiers.deleteTier')"
      :message="t('admin.checkin.tiers.deleteTierConfirm')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDeleteTier"
      @cancel="showDeleteTierDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { CheckinConfig, CheckinAnalytics, CheckinTier, CheckinTierRequest, CheckinRecord } from '@/api/admin/checkin'
import { formatCurrency, formatDateTime } from '@/utils/format'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import CheckinStatsCards from '@/components/admin/checkin/CheckinStatsCards.vue'

const { t } = useI18n()
const appStore = useAppStore()

// ==================== Config ====================

const defaultConfig = (): CheckinConfig => ({
  enabled: false,
  min_reward: 0,
  max_reward: 0,
  base_cap: 0,
  weight_recharge: 0,
  weight_usage: 0,
  weight_activity: 0,
  recharge_cap: 0,
  usage_cap: 0,
  streak_cap: 0,
  beta_min: 0,
  beta_max: 0,
  daily_budget: 0,
  user_monthly_cap: 0,
  min_account_age_days: 0,
  require_recharge: false
})

const configForm = reactive<CheckinConfig>(defaultConfig())
const configErrors = reactive<Partial<Record<keyof CheckinConfig, string>>>({})
const savingConfig = ref(false)

const loadConfig = async () => {
  try {
    const data = await adminAPI.checkin.getConfig()
    Object.assign(configForm, data)
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.checkin.messages.failedToLoadConfig'))
    console.error('Error loading check-in config:', error)
  }
}

// Shared reward-band invariants used by both the global check-in config and
// per-tier overrides: min_reward>0, max_reward>=min_reward, base_cap within
// [min_reward,max_reward], beta_min>=0, beta_max>=beta_min. Callers pass their
// own i18n key prefix so each keeps its own error message strings.
interface RewardBandValues {
  min_reward: number
  max_reward: number
  base_cap: number
  beta_min: number
  beta_max: number
}

const validateRewardBand = (
  vals: RewardBandValues,
  errors: Partial<Record<keyof RewardBandValues, string>>,
  keyPrefix: string
): boolean => {
  let valid = true
  if (!(vals.min_reward > 0)) {
    errors.min_reward = t(`${keyPrefix}.minRewardPositive`)
    valid = false
  }
  if (vals.max_reward < vals.min_reward) {
    errors.max_reward = t(`${keyPrefix}.maxRewardTooLow`)
    valid = false
  }
  if (vals.base_cap < vals.min_reward || vals.base_cap > vals.max_reward) {
    errors.base_cap = t(`${keyPrefix}.baseCapRange`)
    valid = false
  }
  if (vals.beta_min < 0) {
    errors.beta_min = t(`${keyPrefix}.betaMinNegative`)
    valid = false
  }
  if (vals.beta_max < vals.beta_min) {
    errors.beta_max = t(`${keyPrefix}.betaMaxTooLow`)
    valid = false
  }
  return valid
}

const validateConfig = (): boolean => {
  Object.keys(configErrors).forEach((key) => delete configErrors[key as keyof CheckinConfig])

  let valid = validateRewardBand(
    {
      min_reward: configForm.min_reward,
      max_reward: configForm.max_reward,
      base_cap: configForm.base_cap,
      beta_min: configForm.beta_min,
      beta_max: configForm.beta_max
    },
    configErrors,
    'admin.checkin.config.errors'
  )
  if (configForm.weight_recharge < 0) {
    configErrors.weight_recharge = t('admin.checkin.config.errors.weightNegative')
    valid = false
  }
  if (configForm.weight_usage < 0) {
    configErrors.weight_usage = t('admin.checkin.config.errors.weightNegative')
    valid = false
  }
  if (configForm.weight_activity < 0) {
    configErrors.weight_activity = t('admin.checkin.config.errors.weightNegative')
    valid = false
  }
  if (configForm.recharge_cap < 0) {
    configErrors.recharge_cap = t('admin.checkin.config.errors.capNegative')
    valid = false
  }
  if (configForm.usage_cap < 0) {
    configErrors.usage_cap = t('admin.checkin.config.errors.capNegative')
    valid = false
  }
  if (configForm.streak_cap < 1) {
    configErrors.streak_cap = t('admin.checkin.config.errors.streakCapMin')
    valid = false
  }
  if (configForm.daily_budget < 0) {
    configErrors.daily_budget = t('admin.checkin.config.errors.capNegative')
    valid = false
  }
  if (configForm.user_monthly_cap < 0) {
    configErrors.user_monthly_cap = t('admin.checkin.config.errors.capNegative')
    valid = false
  }
  if (configForm.min_account_age_days < 0) {
    configErrors.min_account_age_days = t('admin.checkin.config.errors.minAccountAgeNegative')
    valid = false
  }
  return valid
}

// Coerces a v-model.number field back to a finite number, falling back to the
// given default when the input was cleared (empty string) or otherwise invalid.
// Guards against an emptied numeric input reaching the wire as a raw string.
const num = (v: unknown, fallback: number): number => {
  if (v === '' || v === null || v === undefined || (typeof v === 'number' && Number.isNaN(v))) return fallback
  return Number(v)
}

const handleSaveConfig = async () => {
  if (!validateConfig()) return

  const d = defaultConfig()
  const payload: CheckinConfig = {
    enabled: configForm.enabled,
    min_reward: num(configForm.min_reward, d.min_reward),
    max_reward: num(configForm.max_reward, d.max_reward),
    base_cap: num(configForm.base_cap, d.base_cap),
    weight_recharge: num(configForm.weight_recharge, d.weight_recharge),
    weight_usage: num(configForm.weight_usage, d.weight_usage),
    weight_activity: num(configForm.weight_activity, d.weight_activity),
    recharge_cap: num(configForm.recharge_cap, d.recharge_cap),
    usage_cap: num(configForm.usage_cap, d.usage_cap),
    streak_cap: Math.trunc(num(configForm.streak_cap, d.streak_cap)),
    beta_min: num(configForm.beta_min, d.beta_min),
    beta_max: num(configForm.beta_max, d.beta_max),
    daily_budget: num(configForm.daily_budget, d.daily_budget),
    user_monthly_cap: num(configForm.user_monthly_cap, d.user_monthly_cap),
    min_account_age_days: Math.trunc(num(configForm.min_account_age_days, d.min_account_age_days)),
    require_recharge: configForm.require_recharge
  }

  savingConfig.value = true
  try {
    const updated = await adminAPI.checkin.updateConfig(payload)
    Object.assign(configForm, updated)
    appStore.showSuccess(t('admin.checkin.messages.configSaved'))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.checkin.messages.failedToSaveConfig'))
  } finally {
    savingConfig.value = false
  }
}

// ==================== Analytics ====================

const analytics = ref<CheckinAnalytics | null>(null)

const loadAnalytics = async () => {
  try {
    analytics.value = await adminAPI.checkin.getAnalytics()
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.checkin.messages.failedToLoadAnalytics'))
    console.error('Error loading check-in analytics:', error)
  }
}

// ==================== Tiers ====================

const tiers = ref<CheckinTier[]>([])
const tiersLoading = ref(false)
const savingTier = ref(false)

const showTierDialog = ref(false)
const showDeleteTierDialog = ref(false)
const editingTier = ref<CheckinTier | null>(null)
const deletingTier = ref<CheckinTier | null>(null)

let tierAbortController: AbortController | null = null

const tierColumns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.checkin.tiers.columns.name') },
  { key: 'enabled', label: t('admin.checkin.tiers.columns.enabled') },
  { key: 'match_type', label: t('admin.checkin.tiers.columns.matchType') },
  { key: 'match_threshold', label: t('admin.checkin.tiers.columns.matchThreshold') },
  { key: 'min_reward', label: t('admin.checkin.tiers.columns.minReward') },
  { key: 'max_reward', label: t('admin.checkin.tiers.columns.maxReward') },
  { key: 'base_cap', label: t('admin.checkin.tiers.columns.baseCap') },
  { key: 'beta_range', label: t('admin.checkin.tiers.columns.betaRange') },
  { key: 'sort_order', label: t('admin.checkin.tiers.columns.sortOrder'), sortable: true },
  { key: 'actions', label: t('admin.checkin.tiers.columns.actions') }
])

const matchTypeOptions = computed(() => [
  { value: 'recharge', label: t('admin.checkin.tiers.matchTypeRecharge') },
  { value: 'score', label: t('admin.checkin.tiers.matchTypeScore') }
])

const defaultTierForm = (): CheckinTierRequest => ({
  name: '',
  enabled: true,
  match_type: 'recharge',
  match_threshold: 0,
  min_reward: 0,
  max_reward: 0,
  base_cap: 0,
  beta_min: 0,
  beta_max: 0,
  sort_order: 0
})

const tierForm = reactive<CheckinTierRequest>(defaultTierForm())
const tierErrors = reactive<Partial<Record<keyof CheckinTierRequest, string>>>({})

const loadTiers = async () => {
  if (tierAbortController) {
    tierAbortController.abort()
  }
  const currentController = new AbortController()
  tierAbortController = currentController
  tiersLoading.value = true

  try {
    const data = await adminAPI.checkin.listTiers()
    if (currentController.signal.aborted || tierAbortController !== currentController) return
    tiers.value = data
  } catch (error: any) {
    if (currentController.signal.aborted || tierAbortController !== currentController) return
    appStore.showError(error?.message || t('admin.checkin.messages.failedToLoadTiers'))
    console.error('Error loading check-in tiers:', error)
  } finally {
    if (tierAbortController === currentController) {
      tiersLoading.value = false
      tierAbortController = null
    }
  }
}

const validateTier = (): boolean => {
  Object.keys(tierErrors).forEach((key) => delete tierErrors[key as keyof CheckinTierRequest])

  let valid = true
  if (!tierForm.name.trim()) {
    tierErrors.name = t('admin.checkin.tiers.errors.nameRequired')
    valid = false
  }
  if (tierForm.match_type !== 'recharge' && tierForm.match_type !== 'score') {
    tierErrors.match_type = t('admin.checkin.tiers.errors.matchTypeInvalid')
    valid = false
  }
  if (tierForm.match_threshold < 0) {
    tierErrors.match_threshold = t('admin.checkin.tiers.errors.matchThresholdNegative')
    valid = false
  }
  const bandValid = validateRewardBand(
    {
      min_reward: tierForm.min_reward,
      max_reward: tierForm.max_reward,
      base_cap: tierForm.base_cap,
      beta_min: tierForm.beta_min,
      beta_max: tierForm.beta_max
    },
    tierErrors,
    'admin.checkin.tiers.errors'
  )
  return valid && bandValid
}

const openCreateTierDialog = () => {
  editingTier.value = null
  Object.assign(tierForm, defaultTierForm())
  Object.keys(tierErrors).forEach((key) => delete tierErrors[key as keyof CheckinTierRequest])
  showTierDialog.value = true
}

const openEditTierDialog = (tier: CheckinTier) => {
  editingTier.value = tier
  Object.assign(tierForm, {
    name: tier.name,
    enabled: tier.enabled,
    match_type: tier.match_type,
    match_threshold: tier.match_threshold,
    min_reward: tier.min_reward,
    max_reward: tier.max_reward,
    base_cap: tier.base_cap,
    beta_min: tier.beta_min,
    beta_max: tier.beta_max,
    sort_order: tier.sort_order
  })
  Object.keys(tierErrors).forEach((key) => delete tierErrors[key as keyof CheckinTierRequest])
  showTierDialog.value = true
}

const closeTierDialog = () => {
  showTierDialog.value = false
  editingTier.value = null
}

const handleSaveTier = async () => {
  if (!validateTier()) return

  const d = defaultTierForm()
  const payload: CheckinTierRequest = {
    name: tierForm.name,
    enabled: tierForm.enabled,
    match_type: tierForm.match_type,
    match_threshold: num(tierForm.match_threshold, d.match_threshold),
    min_reward: num(tierForm.min_reward, d.min_reward),
    max_reward: num(tierForm.max_reward, d.max_reward),
    base_cap: num(tierForm.base_cap, d.base_cap),
    beta_min: num(tierForm.beta_min, d.beta_min),
    beta_max: num(tierForm.beta_max, d.beta_max),
    sort_order: Math.trunc(num(tierForm.sort_order, d.sort_order))
  }

  savingTier.value = true
  try {
    if (editingTier.value) {
      await adminAPI.checkin.updateTier(editingTier.value.id, payload)
      appStore.showSuccess(t('admin.checkin.messages.tierUpdated'))
    } else {
      await adminAPI.checkin.createTier(payload)
      appStore.showSuccess(t('admin.checkin.messages.tierCreated'))
    }
    closeTierDialog()
    loadTiers()
  } catch (error: any) {
    appStore.showError(
      error?.message ||
        (editingTier.value ? t('admin.checkin.messages.failedToUpdateTier') : t('admin.checkin.messages.failedToCreateTier'))
    )
  } finally {
    savingTier.value = false
  }
}

const handleDeleteTier = (tier: CheckinTier) => {
  deletingTier.value = tier
  showDeleteTierDialog.value = true
}

const confirmDeleteTier = async () => {
  if (!deletingTier.value) return

  try {
    await adminAPI.checkin.deleteTier(deletingTier.value.id)
    appStore.showSuccess(t('admin.checkin.messages.tierDeleted'))
    showDeleteTierDialog.value = false
    deletingTier.value = null
    loadTiers()
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.checkin.messages.failedToDeleteTier'))
  }
}

// ==================== Records ====================

const records = ref<CheckinRecord[]>([])
const recordsLoading = ref(false)

const recordsPagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0
})

const recordFilters = reactive({
  user_id: undefined as number | undefined,
  start_date: '',
  end_date: ''
})

let recordsAbortController: AbortController | null = null

const recordColumns = computed<Column[]>(() => [
  { key: 'user', label: t('admin.checkin.records.columns.user') },
  { key: 'check_in_date', label: t('admin.checkin.records.columns.date') },
  { key: 'reward_amount', label: t('admin.checkin.records.columns.amount') },
  { key: 'streak_count', label: t('admin.checkin.records.columns.streak') },
  { key: 'score', label: t('admin.checkin.records.columns.score') },
  { key: 'created_at', label: t('admin.checkin.records.columns.createdAt') }
])

const loadRecords = async () => {
  if (recordsAbortController) {
    recordsAbortController.abort()
  }
  const currentController = new AbortController()
  recordsAbortController = currentController
  recordsLoading.value = true

  try {
    const data = await adminAPI.checkin.listRecords({
      page: recordsPagination.page,
      page_size: recordsPagination.page_size,
      user_id: recordFilters.user_id || undefined,
      start_date: recordFilters.start_date || undefined,
      end_date: recordFilters.end_date || undefined
    })
    if (currentController.signal.aborted || recordsAbortController !== currentController) return
    records.value = data.items
    recordsPagination.total = data.total
  } catch (error: any) {
    if (currentController.signal.aborted || recordsAbortController !== currentController) return
    appStore.showError(error?.message || t('admin.checkin.records.failedToLoad'))
    console.error('Error loading check-in records:', error)
  } finally {
    if (recordsAbortController === currentController) {
      recordsLoading.value = false
      recordsAbortController = null
    }
  }
}

const handleRecordsPageChange = (page: number) => {
  recordsPagination.page = page
  loadRecords()
}

const handleRecordsPageSizeChange = (pageSize: number) => {
  recordsPagination.page_size = pageSize
  recordsPagination.page = 1
  loadRecords()
}

const handleRecordsFilterApply = () => {
  recordsPagination.page = 1
  loadRecords()
}

const handleRecordsFilterReset = () => {
  recordFilters.user_id = undefined
  recordFilters.start_date = ''
  recordFilters.end_date = ''
  recordsPagination.page = 1
  loadRecords()
}

onMounted(() => {
  loadConfig()
  loadAnalytics()
  loadTiers()
  loadRecords()
})

onUnmounted(() => {
  tierAbortController?.abort()
  recordsAbortController?.abort()
})
</script>
