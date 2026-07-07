package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CheckInHandler handles admin daily check-in config, analytics and reward-tier management.
type CheckInHandler struct {
	checkInAdminService *service.CheckInAdminService
}

// NewCheckInHandler creates a new admin CheckInHandler.
func NewCheckInHandler(checkInAdminService *service.CheckInAdminService) *CheckInHandler {
	return &CheckInHandler{checkInAdminService: checkInAdminService}
}

// ---------------------------------------------------------------------------
// Config DTOs
// ---------------------------------------------------------------------------

// checkInConfigDTO is the read/write shape for the 16 daily check-in settings.
type checkInConfigDTO struct {
	Enabled           bool    `json:"enabled"`
	MinReward         float64 `json:"min_reward"`
	MaxReward         float64 `json:"max_reward"`
	BaseCap           float64 `json:"base_cap"`
	WeightRecharge    float64 `json:"weight_recharge"`
	WeightUsage       float64 `json:"weight_usage"`
	WeightActivity    float64 `json:"weight_activity"`
	RechargeCap       float64 `json:"recharge_cap"`
	UsageCap          float64 `json:"usage_cap"`
	StreakCap         int     `json:"streak_cap"`
	BetaMin           float64 `json:"beta_min"`
	BetaMax           float64 `json:"beta_max"`
	DailyBudget       float64 `json:"daily_budget"`
	UserMonthlyCap    float64 `json:"user_monthly_cap"`
	MinAccountAgeDays int     `json:"min_account_age_days"`
	RequireRecharge   bool    `json:"require_recharge"`
}

func checkInConfigDTOFromService(cv *service.CheckInConfigValues) checkInConfigDTO {
	return checkInConfigDTO{
		Enabled:           cv.Enabled,
		MinReward:         cv.MinReward,
		MaxReward:         cv.MaxReward,
		BaseCap:           cv.BaseCap,
		WeightRecharge:    cv.WeightRecharge,
		WeightUsage:       cv.WeightUsage,
		WeightActivity:    cv.WeightActivity,
		RechargeCap:       cv.RechargeCap,
		UsageCap:          cv.UsageCap,
		StreakCap:         cv.StreakCap,
		BetaMin:           cv.BetaMin,
		BetaMax:           cv.BetaMax,
		DailyBudget:       cv.DailyBudget,
		UserMonthlyCap:    cv.UserMonthlyCap,
		MinAccountAgeDays: cv.MinAccountAgeDays,
		RequireRecharge:   cv.RequireRecharge,
	}
}

// GetConfig returns the current daily check-in configuration.
// GET /api/v1/admin/checkin/config
func (h *CheckInHandler) GetConfig(c *gin.Context) {
	cfg, err := h.checkInAdminService.GetCheckInConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, checkInConfigDTOFromService(cfg))
}

// UpdateConfig validates and persists the daily check-in configuration.
// PUT /api/v1/admin/checkin/config
func (h *CheckInHandler) UpdateConfig(c *gin.Context) {
	var req checkInConfigDTO
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	values := &service.CheckInConfigValues{
		Enabled:           req.Enabled,
		MinReward:         req.MinReward,
		MaxReward:         req.MaxReward,
		BaseCap:           req.BaseCap,
		WeightRecharge:    req.WeightRecharge,
		WeightUsage:       req.WeightUsage,
		WeightActivity:    req.WeightActivity,
		RechargeCap:       req.RechargeCap,
		UsageCap:          req.UsageCap,
		StreakCap:         req.StreakCap,
		BetaMin:           req.BetaMin,
		BetaMax:           req.BetaMax,
		DailyBudget:       req.DailyBudget,
		UserMonthlyCap:    req.UserMonthlyCap,
		MinAccountAgeDays: req.MinAccountAgeDays,
		RequireRecharge:   req.RequireRecharge,
	}
	if err := h.checkInAdminService.UpdateCheckInConfig(c.Request.Context(), values); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Return the persisted (sanitized) config so the caller sees the effective state.
	cfg, err := h.checkInAdminService.GetCheckInConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, checkInConfigDTOFromService(cfg))
}

// ---------------------------------------------------------------------------
// Analytics
// ---------------------------------------------------------------------------

// GetAnalytics returns aggregate daily check-in analytics.
// GET /api/v1/admin/checkin/analytics
func (h *CheckInHandler) GetAnalytics(c *gin.Context) {
	analytics, err := h.checkInAdminService.GetAnalytics(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"total_gifted":         analytics.TotalGifted,
		"today_gifted":         analytics.TodayGifted,
		"month_gifted":         analytics.MonthGifted,
		"total_checkins":       analytics.TotalCheckins,
		"today_checkins":       analytics.TodayCheckins,
		"distinct_users_today": analytics.DistinctUsersToday,
		"distinct_users_month": analytics.DistinctUsersMonth,
		"trend":                analytics.Trend,
	})
}

// ---------------------------------------------------------------------------
// Records listing
// ---------------------------------------------------------------------------

// checkInRecordDTO is the response shape for one individual daily check-in record.
type checkInRecordDTO struct {
	ID               int64   `json:"id"`
	UserID           int64   `json:"user_id"`
	UserEmail        string  `json:"user_email"`
	UserUsername     string  `json:"user_username"`
	CheckInDate      string  `json:"check_in_date"`
	RewardAmount     float64 `json:"reward_amount"`
	StreakCount      int     `json:"streak_count"`
	Score            float64 `json:"score"`
	RechargeSnapshot float64 `json:"recharge_snapshot"`
	UsageSnapshot    float64 `json:"usage_snapshot"`
	CreatedAt        string  `json:"created_at"`
}

func checkInRecordDTOFromService(r *service.CheckInRecordDetail) checkInRecordDTO {
	return checkInRecordDTO{
		ID:               r.ID,
		UserID:           r.UserID,
		UserEmail:        r.UserEmail,
		UserUsername:     r.UserUsername,
		CheckInDate:      r.CheckInDate.Format("2006-01-02"),
		RewardAmount:     r.RewardAmount,
		StreakCount:      r.StreakCount,
		Score:            r.Score,
		RechargeSnapshot: r.RechargeSnapshot,
		UsageSnapshot:    r.UsageSnapshot,
		CreatedAt:        r.CreatedAt.Format(time.RFC3339),
	}
}

// ListRecords returns a paginated, filterable list of individual check-in records,
// joined to users for email/username, ordered by created_at desc.
// GET /api/v1/admin/checkin/records
func (h *CheckInHandler) ListRecords(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}

	var filter service.CheckInRecordFilter

	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		filter.UserID = &userID
	}
	if raw := strings.TrimSpace(c.Query("start_date")); raw != "" {
		if _, err := time.Parse("2006-01-02", raw); err != nil {
			response.BadRequest(c, "Invalid start_date, expected YYYY-MM-DD")
			return
		}
		filter.StartDate = raw
	}
	if raw := strings.TrimSpace(c.Query("end_date")); raw != "" {
		if _, err := time.Parse("2006-01-02", raw); err != nil {
			response.BadRequest(c, "Invalid end_date, expected YYYY-MM-DD")
			return
		}
		filter.EndDate = raw
	}

	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	records, total, err := h.checkInAdminService.ListRecords(c.Request.Context(), params, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]checkInRecordDTO, 0, len(records))
	for i := range records {
		out = append(out, checkInRecordDTOFromService(&records[i]))
	}
	response.Paginated(c, out, total, page, pageSize)
}

// ---------------------------------------------------------------------------
// Reward-tier DTOs
// ---------------------------------------------------------------------------

// checkInTierDTO is the response shape for a reward tier.
type checkInTierDTO struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	MatchType      string    `json:"match_type"`
	MatchThreshold float64   `json:"match_threshold"`
	MinReward      float64   `json:"min_reward"`
	MaxReward      float64   `json:"max_reward"`
	BaseCap        float64   `json:"base_cap"`
	BetaMin        float64   `json:"beta_min"`
	BetaMax        float64   `json:"beta_max"`
	SortOrder      int       `json:"sort_order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func checkInTierDTOFromService(t *service.CheckInRewardTier) checkInTierDTO {
	return checkInTierDTO{
		ID:             t.ID,
		Name:           t.Name,
		Enabled:        t.Enabled,
		MatchType:      t.MatchType,
		MatchThreshold: t.MatchThreshold,
		MinReward:      t.MinReward,
		MaxReward:      t.MaxReward,
		BaseCap:        t.BaseCap,
		BetaMin:        t.BetaMin,
		BetaMax:        t.BetaMax,
		SortOrder:      t.SortOrder,
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
	}
}

// createCheckInTierRequest is the create payload for a reward tier.
type createCheckInTierRequest struct {
	Name           string   `json:"name" binding:"required"`
	Enabled        *bool    `json:"enabled"`
	MatchType      string   `json:"match_type" binding:"omitempty,oneof=recharge score"`
	MatchThreshold float64  `json:"match_threshold"`
	MinReward      float64  `json:"min_reward"`
	MaxReward      float64  `json:"max_reward"`
	BaseCap        float64  `json:"base_cap"`
	BetaMin        *float64 `json:"beta_min"`
	BetaMax        *float64 `json:"beta_max"`
	SortOrder      int      `json:"sort_order"`
}

// updateCheckInTierRequest is the partial-update payload for a reward tier.
type updateCheckInTierRequest struct {
	Name           *string  `json:"name"`
	Enabled        *bool    `json:"enabled"`
	MatchType      *string  `json:"match_type" binding:"omitempty,oneof=recharge score"`
	MatchThreshold *float64 `json:"match_threshold"`
	MinReward      *float64 `json:"min_reward"`
	MaxReward      *float64 `json:"max_reward"`
	BaseCap        *float64 `json:"base_cap"`
	BetaMin        *float64 `json:"beta_min"`
	BetaMax        *float64 `json:"beta_max"`
	SortOrder      *int     `json:"sort_order"`
}

// ListTiers returns all reward tiers ordered by sort_order then id.
// GET /api/v1/admin/checkin/tiers
func (h *CheckInHandler) ListTiers(c *gin.Context) {
	tiers, err := h.checkInAdminService.ListTiers(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]checkInTierDTO, 0, len(tiers))
	for i := range tiers {
		out = append(out, checkInTierDTOFromService(&tiers[i]))
	}
	response.Success(c, out)
}

// CreateTier creates a new reward tier.
// POST /api/v1/admin/checkin/tiers
func (h *CheckInHandler) CreateTier(c *gin.Context) {
	var req createCheckInTierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Apply field defaults for anything the caller left unset.
	input := &service.CreateCheckInTierInput{
		Name:           req.Name,
		Enabled:        true,
		MatchType:      service.CheckInTierMatchRecharge,
		MatchThreshold: req.MatchThreshold,
		MinReward:      req.MinReward,
		MaxReward:      req.MaxReward,
		BaseCap:        req.BaseCap,
		BetaMin:        1,
		BetaMax:        3,
		SortOrder:      req.SortOrder,
	}
	if req.Enabled != nil {
		input.Enabled = *req.Enabled
	}
	if strings.TrimSpace(req.MatchType) != "" {
		input.MatchType = req.MatchType
	}
	if req.BetaMin != nil {
		input.BetaMin = *req.BetaMin
	}
	if req.BetaMax != nil {
		input.BetaMax = *req.BetaMax
	}

	tier, err := h.checkInAdminService.CreateTier(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, checkInTierDTOFromService(tier))
}

// UpdateTier applies a partial update to a reward tier.
// PUT /api/v1/admin/checkin/tiers/:id
func (h *CheckInHandler) UpdateTier(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid tier ID")
		return
	}

	var req updateCheckInTierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &service.UpdateCheckInTierInput{
		Name:           req.Name,
		Enabled:        req.Enabled,
		MatchType:      req.MatchType,
		MatchThreshold: req.MatchThreshold,
		MinReward:      req.MinReward,
		MaxReward:      req.MaxReward,
		BaseCap:        req.BaseCap,
		BetaMin:        req.BetaMin,
		BetaMax:        req.BetaMax,
		SortOrder:      req.SortOrder,
	}

	tier, err := h.checkInAdminService.UpdateTier(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, checkInTierDTOFromService(tier))
}

// DeleteTier removes a reward tier by id.
// DELETE /api/v1/admin/checkin/tiers/:id
func (h *CheckInHandler) DeleteTier(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid tier ID")
		return
	}

	if err := h.checkInAdminService.DeleteTier(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Check-in reward tier deleted successfully"})
}
