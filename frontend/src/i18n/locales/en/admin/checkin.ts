export default {
  checkin: {
    title: 'Daily Check-in Management',
    description: 'Configure the daily check-in reward feature, review analytics, and manage reward tiers',
    config: {
      title: 'Check-in Configuration',
      description: 'Control eligibility, reward ranges, and budget guardrails for the daily check-in feature',
      enabled: 'Enable Daily Check-in',
      enabledHint: 'Allow users to claim a daily check-in reward',
      minReward: 'Min Reward ($)',
      minRewardHint: 'Minimum reward amount per check-in',
      maxReward: 'Max Reward ($)',
      maxRewardHint: 'Maximum reward amount per check-in',
      baseCap: 'Base Cap ($)',
      baseCapHint: 'Upper bound applied before weighting is considered',
      weightRecharge: 'Recharge Weight',
      weightUsage: 'Usage Weight',
      weightActivity: 'Activity Weight',
      rechargeCap: 'Recharge Cap ($)',
      usageCap: 'Usage Cap ($)',
      streakCap: 'Streak Cap',
      betaMin: 'Beta Min',
      betaMinHint: 'Lower bound of the random reward distribution factor',
      betaMax: 'Beta Max',
      betaMaxHint: 'Upper bound of the random reward distribution factor',
      dailyBudget: 'Daily Budget ($)',
      dailyBudgetHint: 'Maximum total reward gifted per day across all users',
      userMonthlyCap: 'User Monthly Cap ($)',
      userMonthlyCapHint: 'Maximum total reward a single user can receive per month',
      minAccountAgeDays: 'Min Account Age (days)',
      minAccountAgeDaysHint: 'Accounts younger than this cannot check in',
      requireRecharge: 'Require Recharge',
      requireRechargeHint: 'Only allow users who have recharged at least once to check in',
      groups: {
        rewardRange: 'Reward Range',
        weightsCaps: 'Weights & Caps',
        guardrails: 'Guardrails',
        eligibility: 'Eligibility'
      },
      errors: {
        minRewardPositive: 'Min reward must be greater than 0',
        maxRewardTooLow: 'Max reward must be greater than or equal to min reward',
        baseCapRange: 'Base cap must be within the min/max reward range',
        weightNegative: 'Weight cannot be negative',
        capNegative: 'Cap cannot be negative',
        streakCapMin: 'Streak cap must be at least 1',
        betaMinNegative: 'Beta min cannot be negative',
        betaMaxTooLow: 'Beta max must be greater than or equal to beta min',
        minAccountAgeNegative: 'Min account age cannot be negative'
      }
    },
    analytics: {
      totalGifted: 'Total Gifted',
      todayGifted: 'Today Gifted',
      monthGifted: 'Month Gifted',
      totalCheckins: 'Total Check-ins',
      todayCheckins: 'Today',
      distinctUsersToday: 'Distinct Users (Today)',
      distinctUsersMonth: 'Distinct Users (Month)',
      trendTitle: 'Gifted Amount Trend (Last 30 Days)'
    },
    tiers: {
      title: 'Reward Tiers',
      description: 'Define reward tiers that override the default config based on recharge amount or activity score',
      createTier: 'Create Tier',
      editTier: 'Edit Tier',
      deleteTier: 'Delete Tier',
      deleteTierConfirm: 'Are you sure you want to delete this reward tier? This action cannot be undone.',
      name: 'Name',
      enabled: 'Enabled',
      matchType: 'Match Type',
      matchTypeRecharge: 'Recharge Amount',
      matchTypeScore: 'Activity Score',
      matchThreshold: 'Match Threshold',
      minReward: 'Min Reward ($)',
      maxReward: 'Max Reward ($)',
      baseCap: 'Base Cap ($)',
      betaMin: 'Beta Min',
      betaMax: 'Beta Max',
      sortOrder: 'Sort Order',
      columns: {
        name: 'Name',
        enabled: 'Enabled',
        matchType: 'Match Type',
        matchThreshold: 'Threshold',
        minReward: 'Min Reward',
        maxReward: 'Max Reward',
        baseCap: 'Base Cap',
        betaRange: 'Beta Range',
        sortOrder: 'Sort Order',
        actions: 'Actions'
      },
      errors: {
        nameRequired: 'Name is required',
        matchTypeInvalid: 'Match type must be recharge or score',
        matchThresholdNegative: 'Match threshold cannot be negative',
        minRewardPositive: 'Min reward must be greater than 0',
        maxRewardTooLow: 'Max reward must be greater than or equal to min reward',
        baseCapRange: 'Base cap must be within the min/max reward range',
        betaMinNegative: 'Beta min cannot be negative',
        betaMaxTooLow: 'Beta max must be greater than or equal to beta min'
      }
    },
    messages: {
      configSaved: 'Check-in configuration saved successfully',
      failedToLoadConfig: 'Failed to load check-in configuration',
      failedToSaveConfig: 'Failed to save check-in configuration',
      failedToLoadAnalytics: 'Failed to load check-in analytics',
      failedToLoadTiers: 'Failed to load reward tiers',
      tierCreated: 'Reward tier created successfully',
      tierUpdated: 'Reward tier updated successfully',
      tierDeleted: 'Reward tier deleted successfully',
      failedToCreateTier: 'Failed to create reward tier',
      failedToUpdateTier: 'Failed to update reward tier',
      failedToDeleteTier: 'Failed to delete reward tier'
    },
    records: {
      title: 'Check-in Records',
      description: 'Browse individual daily check-in gift records',
      failedToLoad: 'Failed to load check-in records',
      empty: 'No check-in records found',
      filters: {
        userId: 'User ID',
        userIdPlaceholder: 'Filter by user ID',
        startDate: 'Start Date',
        endDate: 'End Date'
      },
      columns: {
        user: 'User',
        date: 'Check-in Date',
        amount: 'Reward Amount',
        streak: 'Streak',
        score: 'Score',
        createdAt: 'Created At'
      }
    }
  }
}
