<template>
  <div class="space-y-4">
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-green-100 p-2 dark:bg-green-900/30 text-green-600 dark:text-green-400">
          <Icon name="dollar" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.totalGifted') }}
          </p>
          <p class="text-xl font-bold text-green-600 dark:text-green-400">
            {{ formatCurrency(analytics?.total_gifted ?? 0) }}
          </p>
        </div>
      </div>

      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-blue-100 p-2 dark:bg-blue-900/30 text-blue-600 dark:text-blue-400">
          <Icon name="gift" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.todayGifted') }}
          </p>
          <p class="text-xl font-bold text-gray-900 dark:text-white">
            {{ formatCurrency(analytics?.today_gifted ?? 0) }}
          </p>
        </div>
      </div>

      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-purple-100 p-2 dark:bg-purple-900/30 text-purple-600 dark:text-purple-400">
          <Icon name="calendar" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.monthGifted') }}
          </p>
          <p class="text-xl font-bold text-gray-900 dark:text-white">
            {{ formatCurrency(analytics?.month_gifted ?? 0) }}
          </p>
        </div>
      </div>

      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-amber-100 p-2 dark:bg-amber-900/30 text-amber-600 dark:text-amber-400">
          <Icon name="chart" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.totalCheckins') }}
          </p>
          <p class="text-xl font-bold text-gray-900 dark:text-white">
            {{ formatNumber(analytics?.total_checkins ?? 0) }}
          </p>
          <p class="text-xs text-gray-400">
            {{ t('admin.checkin.analytics.todayCheckins') }}: {{ formatNumber(analytics?.today_checkins ?? 0) }}
          </p>
        </div>
      </div>
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-indigo-100 p-2 dark:bg-indigo-900/30 text-indigo-600 dark:text-indigo-400">
          <Icon name="users" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.distinctUsersToday') }}
          </p>
          <p class="text-xl font-bold text-gray-900 dark:text-white">
            {{ formatNumber(analytics?.distinct_users_today ?? 0) }}
          </p>
        </div>
      </div>

      <div class="card p-4 flex items-center gap-3">
        <div class="rounded-lg bg-teal-100 p-2 dark:bg-teal-900/30 text-teal-600 dark:text-teal-400">
          <Icon name="users" size="md" />
        </div>
        <div>
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.checkin.analytics.distinctUsersMonth') }}
          </p>
          <p class="text-xl font-bold text-gray-900 dark:text-white">
            {{ formatNumber(analytics?.distinct_users_month ?? 0) }}
          </p>
        </div>
      </div>
    </div>

    <!-- Lightweight inline trend bar list (last 30 days) -->
    <div v-if="trendPoints.length" class="card p-4">
      <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.checkin.analytics.trendTitle') }}
      </h3>
      <div class="flex items-end gap-1" style="height: 96px">
        <div
          v-for="point in trendPoints"
          :key="point.date"
          class="group relative flex-1 rounded-t bg-primary-300 transition-colors hover:bg-primary-500 dark:bg-primary-900/50 dark:hover:bg-primary-700"
          :style="{ height: barHeight(point.gifted) + '%' }"
          :title="`${point.date}: ${formatCurrency(point.gifted)} (${point.count})`"
        ></div>
      </div>
      <div class="mt-2 flex justify-between text-xs text-gray-400 dark:text-dark-400">
        <span>{{ trendPoints[0]?.date }}</span>
        <span>{{ trendPoints[trendPoints.length - 1]?.date }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CheckinAnalytics } from '@/api/admin/checkin'
import { formatCurrency, formatNumber } from '@/utils/format'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  analytics: CheckinAnalytics | null
}>()

const { t } = useI18n()

const trendPoints = computed(() => props.analytics?.trend ?? [])

const maxGifted = computed(() => {
  const max = Math.max(0, ...trendPoints.value.map((p) => p.gifted))
  return max > 0 ? max : 1
})

const barHeight = (gifted: number): number => {
  const pct = (gifted / maxGifted.value) * 100
  return Math.max(pct, 2)
}
</script>
