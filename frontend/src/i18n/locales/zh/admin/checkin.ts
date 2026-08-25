export default {
  checkin: {
    title: '每日签到管理',
    description: '配置每日签到奖励功能，查看统计数据，管理奖励档位',
    config: {
      title: '签到配置',
      description: '控制每日签到功能的准入条件、奖励区间和预算保护',
      enabled: '启用每日签到',
      enabledHint: '允许用户领取每日签到奖励',
      minReward: '最小奖励 ($)',
      minRewardHint: '每次签到的最小奖励金额',
      maxReward: '最大奖励 ($)',
      maxRewardHint: '每次签到的最大奖励金额',
      baseCap: '基础上限 ($)',
      baseCapHint: '权重计算前应用的上限值',
      weightRecharge: '充值权重',
      weightUsage: '用量权重',
      weightActivity: '活跃度权重',
      rechargeCap: '充值上限 ($)',
      usageCap: '用量上限 ($)',
      streakCap: '连续签到上限',
      betaMin: 'Beta 最小值',
      betaMinHint: '随机奖励分布因子的下界',
      betaMax: 'Beta 最大值',
      betaMaxHint: '随机奖励分布因子的上界',
      dailyBudget: '每日预算 ($)',
      dailyBudgetHint: '所有用户每日可获得的奖励总额上限',
      userMonthlyCap: '用户月度上限 ($)',
      userMonthlyCapHint: '单个用户每月可获得的奖励总额上限',
      minAccountAgeDays: '最小账号年龄（天）',
      minAccountAgeDaysHint: '低于此天数的账号无法签到',
      requireRecharge: '要求已充值',
      requireRechargeHint: '仅允许至少充值过一次的用户签到',
      groups: {
        rewardRange: '奖励区间',
        weightsCaps: '权重与上限',
        guardrails: '风控参数',
        eligibility: '准入条件'
      },
      errors: {
        minRewardPositive: '最小奖励必须大于 0',
        maxRewardTooLow: '最大奖励必须大于或等于最小奖励',
        baseCapRange: '基础上限必须在最小奖励和最大奖励之间',
        weightNegative: '权重不能为负数',
        capNegative: '上限不能为负数',
        streakCapMin: '连续签到上限必须至少为 1',
        betaMinNegative: 'Beta 最小值不能为负数',
        betaMaxTooLow: 'Beta 最大值必须大于或等于最小值',
        minAccountAgeNegative: '最小账号年龄不能为负数'
      }
    },
    analytics: {
      totalGifted: '累计发放',
      todayGifted: '今日发放',
      monthGifted: '本月发放',
      totalCheckins: '累计签到次数',
      todayCheckins: '今日',
      distinctUsersToday: '今日签到人数',
      distinctUsersMonth: '本月签到人数',
      trendTitle: '发放金额趋势（近 30 天）'
    },
    tiers: {
      title: '奖励档位',
      description: '根据充值金额或活跃度分数定义覆盖默认配置的奖励档位',
      createTier: '创建档位',
      editTier: '编辑档位',
      deleteTier: '删除档位',
      deleteTierConfirm: '确定要删除此奖励档位吗？此操作无法撤销。',
      name: '名称',
      enabled: '启用',
      matchType: '匹配类型',
      matchTypeRecharge: '充值金额',
      matchTypeScore: '活跃度分数',
      matchThreshold: '匹配阈值',
      minReward: '最小奖励 ($)',
      maxReward: '最大奖励 ($)',
      baseCap: '基础上限 ($)',
      betaMin: 'Beta 最小值',
      betaMax: 'Beta 最大值',
      sortOrder: '排序',
      columns: {
        name: '名称',
        enabled: '启用',
        matchType: '匹配类型',
        matchThreshold: '阈值',
        minReward: '最小奖励',
        maxReward: '最大奖励',
        baseCap: '基础上限',
        betaRange: 'Beta 区间',
        sortOrder: '排序',
        actions: '操作'
      },
      errors: {
        nameRequired: '名称不能为空',
        matchTypeInvalid: '匹配类型必须为充值金额或活跃度分数',
        matchThresholdNegative: '匹配阈值不能为负数',
        minRewardPositive: '最小奖励必须大于 0',
        maxRewardTooLow: '最大奖励必须大于或等于最小奖励',
        baseCapRange: '基础上限必须在最小奖励和最大奖励之间',
        betaMinNegative: 'Beta 最小值不能为负数',
        betaMaxTooLow: 'Beta 最大值必须大于或等于最小值'
      }
    },
    messages: {
      configSaved: '签到配置保存成功',
      failedToLoadConfig: '加载签到配置失败',
      failedToSaveConfig: '保存签到配置失败',
      failedToLoadAnalytics: '加载签到统计失败',
      failedToLoadTiers: '加载奖励档位失败',
      tierCreated: '奖励档位创建成功',
      tierUpdated: '奖励档位更新成功',
      tierDeleted: '奖励档位删除成功',
      failedToCreateTier: '创建奖励档位失败',
      failedToUpdateTier: '更新奖励档位失败',
      failedToDeleteTier: '删除奖励档位失败'
    },
    records: {
      title: '签到记录',
      description: '查看每一笔每日签到赠送记录',
      failedToLoad: '加载签到记录失败',
      empty: '暂无签到记录',
      filters: {
        userId: '用户 ID',
        userIdPlaceholder: '按用户 ID 筛选',
        startDate: '开始日期',
        endDate: '结束日期'
      },
      columns: {
        user: '用户',
        date: '签到日期',
        amount: '奖励金额',
        streak: '连续天数',
        score: '活跃度分数',
        createdAt: '创建时间'
      }
    }
  }
}
