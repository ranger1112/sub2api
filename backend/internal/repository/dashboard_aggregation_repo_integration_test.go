//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// DashboardAggregationRepoSuite 覆盖 Stage 2 引入的 group_daily 预聚合写入
// 与 GetGroupUsageSummary 混合查询，重点验证：混合查询结果与原全表 SUM 查询结果一致。
type DashboardAggregationRepoSuite struct {
	suite.Suite
	ctx       context.Context
	tx        *dbent.Tx
	client    *dbent.Client
	usageRepo *usageLogRepository
	aggRepo   *dashboardAggregationRepository
}

func (s *DashboardAggregationRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.tx = tx
	s.client = tx.Client()
	s.usageRepo = newUsageLogRepositoryWithSQL(s.client, tx)
	s.aggRepo = newDashboardAggregationRepositoryWithSQL(tx)
}

func TestDashboardAggregationRepoSuite(t *testing.T) {
	suite.Run(t, new(DashboardAggregationRepoSuite))
}

func (s *DashboardAggregationRepoSuite) createUsageLogWithGroup(
	user *service.User,
	apiKey *service.APIKey,
	account *service.Account,
	groupID int64,
	cost float64,
	createdAt time.Time,
) *service.UsageLog {
	g := groupID
	log := &service.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		AccountID:    account.ID,
		GroupID:      &g,
		RequestID:    uuid.New().String(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    cost,
		ActualCost:   cost,
		CreatedAt:    createdAt,
	}
	_, err := s.usageRepo.Create(s.ctx, log)
	s.Require().NoError(err, "create usage_log with group_id=%d", groupID)
	return log
}

// TestGroupUsageSummary_HybridMatchesLegacy 验证 Stage 2 引入的混合查询
// 与原 GetAllGroupUsageSummary 全表 SUM 在相同数据下结果一致。
//
// 准备：单个 group 写入历史 + 今日 usage_logs，调度 AggregateRange 填充 group_daily 表。
// 期望：aggRepo.GetGroupUsageSummary 与 usageRepo.GetAllGroupUsageSummary 返回值一致；
// 且 total_cost = 历史 + 今日，today_cost 仅含今日。
func (s *DashboardAggregationRepoSuite) TestGroupUsageSummary_HybridMatchesLegacy() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "stage2-grp"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "stage2@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-stage2",
		Name:   "k",
	})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-stage2"})

	now := time.Now().UTC().Truncate(time.Hour)
	todayStart := truncateToDayUTC(now)
	yesterdayMidday := todayStart.Add(-12 * time.Hour)
	twoDaysAgoMidday := todayStart.Add(-36 * time.Hour)
	todayMidday := todayStart.Add(2 * time.Hour)

	s.createUsageLogWithGroup(user, apiKey, account, group.ID, 10.0, twoDaysAgoMidday)
	s.createUsageLogWithGroup(user, apiKey, account, group.ID, 5.0, yesterdayMidday)
	s.createUsageLogWithGroup(user, apiKey, account, group.ID, 3.0, todayMidday)

	// 聚合窗口需覆盖所有历史天，end 给 today 的尾部
	s.Require().NoError(
		s.aggRepo.AggregateRange(s.ctx, twoDaysAgoMidday.Add(-time.Hour), now.Add(time.Hour)),
		"aggregate range should succeed",
	)

	hybrid, err := s.aggRepo.GetGroupUsageSummary(s.ctx, todayStart)
	s.Require().NoError(err, "hybrid query")

	legacy, err := s.usageRepo.GetAllGroupUsageSummary(s.ctx, todayStart)
	s.Require().NoError(err, "legacy query")

	require.ElementsMatch(s.T(), legacy, hybrid,
		"混合查询结果应与全表 SUM 在数据集相同时完全一致（顺序无关）")

	var found bool
	for _, row := range hybrid {
		if row.GroupID == group.ID {
			found = true
			require.InDelta(s.T(), 18.0, row.TotalCost, 1e-9,
				"total_cost 应为历史 10+5 加今日 3 = 18")
			require.InDelta(s.T(), 3.0, row.TodayCost, 1e-9,
				"today_cost 应仅含今日的 3.0")
		}
	}
	s.Require().True(found, "返回结果应包含被测 group")
}

// TestGroupUsageSummary_HybridIncludesEmptyGroup 验证 LEFT JOIN groups 行为：
// 即使某个 group 完全没有 usage_log，也应该出现在结果里（total_cost=0, today_cost=0）。
func (s *DashboardAggregationRepoSuite) TestGroupUsageSummary_HybridIncludesEmptyGroup() {
	emptyGroup := mustCreateGroup(s.T(), s.client, &service.Group{Name: "stage2-empty"})

	todayStart := truncateToDayUTC(time.Now().UTC())
	hybrid, err := s.aggRepo.GetGroupUsageSummary(s.ctx, todayStart)
	s.Require().NoError(err)

	var found bool
	for _, row := range hybrid {
		if row.GroupID == emptyGroup.ID {
			found = true
			require.InDelta(s.T(), 0.0, row.TotalCost, 1e-9,
				"无用量的 group total_cost 应为 0")
			require.InDelta(s.T(), 0.0, row.TodayCost, 1e-9,
				"无用量的 group today_cost 应为 0")
		}
	}
	s.Require().True(found, "无用量 group 也必须出现在返回结果中")
}

// TestGroupUsageSummary_HybridIgnoresNullGroup 验证 group_id IS NULL 的 usage_log
// 不污染聚合表，也不出现在混合查询结果中。
func (s *DashboardAggregationRepoSuite) TestGroupUsageSummary_HybridIgnoresNullGroup() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "stage2-with-group"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "null-grp@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-null-grp",
		Name:   "k",
	})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-null-grp"})

	now := time.Now().UTC().Truncate(time.Hour)
	todayStart := truncateToDayUTC(now)
	yesterdayMidday := todayStart.Add(-12 * time.Hour)

	// 一条 null group_id（设为 0 通过 nil 间接表达，这里手动 Create）
	nullLog := &service.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		AccountID:    account.ID,
		GroupID:      nil,
		RequestID:    uuid.New().String(),
		Model:        "claude-3",
		InputTokens:  1,
		OutputTokens: 1,
		TotalCost:    99.0,
		ActualCost:   99.0,
		CreatedAt:    yesterdayMidday,
	}
	_, err := s.usageRepo.Create(s.ctx, nullLog)
	s.Require().NoError(err, "create usage_log with null group_id")

	// 一条带 group_id 的对比
	s.createUsageLogWithGroup(user, apiKey, account, group.ID, 7.0, yesterdayMidday)

	s.Require().NoError(
		s.aggRepo.AggregateRange(s.ctx, yesterdayMidday.Add(-time.Hour), now.Add(time.Hour)),
	)

	hybrid, err := s.aggRepo.GetGroupUsageSummary(s.ctx, todayStart)
	s.Require().NoError(err)

	for _, row := range hybrid {
		if row.GroupID == group.ID {
			require.InDelta(s.T(), 7.0, row.TotalCost, 1e-9,
				"该 group total_cost 不能被 null group 的 99 污染")
		}
	}
}
