package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CheckInHandler handles daily check-in reward requests.
type CheckInHandler struct {
	checkInService *service.CheckInService
}

// NewCheckInHandler creates a new CheckInHandler.
func NewCheckInHandler(checkInService *service.CheckInService) *CheckInHandler {
	return &CheckInHandler{checkInService: checkInService}
}

// checkInHistoryDTO is one row of check-in history.
type checkInHistoryDTO struct {
	Date   string  `json:"date"`
	Reward float64 `json:"reward"`
	Streak int     `json:"streak"`
}

// checkInStatusDTO is the GET /checkin response payload.
type checkInStatusDTO struct {
	Enabled         bool                `json:"enabled"`
	CanCheckIn      bool                `json:"can_check_in"`
	CheckedInToday  bool                `json:"checked_in_today"`
	Streak          int                 `json:"streak"`
	LastReward      *float64            `json:"last_reward,omitempty"`
	LastCheckInDate *string             `json:"last_check_in_date"`
	TotalReward     float64             `json:"total_reward"`
	NextAvailableAt *time.Time          `json:"next_available_at"`
	MinReward       float64             `json:"min_reward"`
	MaxReward       float64             `json:"max_reward"`
	History         []checkInHistoryDTO `json:"history"`
}

// checkInClaimDTO is the POST /checkin response payload.
type checkInClaimDTO struct {
	RewardAmount float64 `json:"reward_amount"`
	NewBalance   float64 `json:"new_balance"`
	Streak       int     `json:"streak"`
	CheckInDate  string  `json:"check_in_date"`
}

// GetStatus returns the current user's check-in status and recent history.
// GET /api/v1/checkin
func (h *CheckInHandler) GetStatus(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	status, err := h.checkInService.GetStatus(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	history := make([]checkInHistoryDTO, 0, len(status.History))
	for _, item := range status.History {
		history = append(history, checkInHistoryDTO{
			Date:   item.Date,
			Reward: item.Reward,
			Streak: item.Streak,
		})
	}

	response.Success(c, checkInStatusDTO{
		Enabled:         status.Enabled,
		CanCheckIn:      status.CanCheckIn,
		CheckedInToday:  status.CheckedInToday,
		Streak:          status.Streak,
		LastReward:      status.LastReward,
		LastCheckInDate: status.LastCheckInDate,
		TotalReward:     status.TotalReward,
		NextAvailableAt: status.NextAvailableAt,
		MinReward:       status.MinReward,
		MaxReward:       status.MaxReward,
		History:         history,
	})
}

// Claim performs today's check-in for the current user.
// POST /api/v1/checkin
func (h *CheckInHandler) Claim(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	result, err := h.checkInService.Claim(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, checkInClaimDTO{
		RewardAmount: result.RewardAmount,
		NewBalance:   result.NewBalance,
		Streak:       result.Streak,
		CheckInDate:  result.CheckInDate,
	})
}
