<template>
  <div class="card">
    <div class="flex items-center justify-between border-b border-gray-100 px-6 py-4 dark:border-white/[0.07]">
      <h2 class="text-[15px] font-semibold tracking-tight text-gray-900 dark:text-white">{{ t('dashboard.recentUsage') }}</h2>
      <span class="badge badge-gray">{{ t('dashboard.last7Days') }}</span>
    </div>
    <div class="p-6">
      <div v-if="loading" class="flex items-center justify-center py-12">
        <LoadingSpinner size="lg" />
      </div>
      <div v-else-if="data.length === 0" class="py-8">
        <EmptyState :title="t('dashboard.noUsageRecords')" :description="t('dashboard.startUsingApi')" />
      </div>
      <div v-else class="space-y-2">
        <div v-for="log in data" :key="log.id" class="flex items-center justify-between rounded-lg bg-gray-50 p-4 transition-colors hover:bg-gray-100 dark:bg-white/[0.03] dark:hover:bg-white/[0.05]">
          <div class="flex items-center gap-4">
            <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-primary-100 dark:bg-primary-900/30">
              <Icon name="beaker" size="md" class="text-primary-600 dark:text-primary-400" />
            </div>
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-white">{{ log.model }}</p>
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(log.created_at) }}</p>
            </div>
          </div>
          <div class="text-right">
            <p class="font-mono text-sm font-semibold tabular-nums">
              <span class="text-gray-900 dark:text-white" :title="t('dashboard.actual')">${{ formatCost(log.actual_cost) }}</span>
              <span class="font-normal text-gray-400 dark:text-dark-500" :title="t('dashboard.standard')"> / ${{ formatCost(log.total_cost) }}</span>
            </p>
            <p class="font-mono text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ (log.input_tokens + log.output_tokens).toLocaleString() }} tokens</p>
          </div>
        </div>

        <router-link to="/usage" class="flex items-center justify-center gap-2 py-3 text-sm font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300">
          {{ t('dashboard.viewAllUsage') }}
          <Icon name="arrowRight" size="sm" />
        </router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { UsageLog } from '@/types'

defineProps<{
  data: UsageLog[]
  loading: boolean
}>()
const { t } = useI18n()
const formatCost = (c: number) => c.toFixed(4)
</script>
