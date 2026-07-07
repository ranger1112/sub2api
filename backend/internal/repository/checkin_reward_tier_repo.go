package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/checkinrewardtier"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type checkInRewardTierRepository struct {
	client *dbent.Client
}

// NewCheckInRewardTierRepository creates the daily check-in reward tier repository.
func NewCheckInRewardTierRepository(client *dbent.Client) service.CheckInRewardTierRepository {
	return &checkInRewardTierRepository{client: client}
}

func (r *checkInRewardTierRepository) Create(ctx context.Context, tier *service.CheckInRewardTier) error {
	client := clientFromContext(ctx, r.client)
	created, err := client.CheckInRewardTier.Create().
		SetName(tier.Name).
		SetEnabled(tier.Enabled).
		SetMatchType(tier.MatchType).
		SetMatchThreshold(tier.MatchThreshold).
		SetMinReward(tier.MinReward).
		SetMaxReward(tier.MaxReward).
		SetBaseCap(tier.BaseCap).
		SetBetaMin(tier.BetaMin).
		SetBetaMax(tier.BetaMax).
		SetSortOrder(tier.SortOrder).
		Save(ctx)
	if err != nil {
		return err
	}

	tier.ID = created.ID
	tier.CreatedAt = created.CreatedAt
	tier.UpdatedAt = created.UpdatedAt
	return nil
}

func (r *checkInRewardTierRepository) GetByID(ctx context.Context, id int64) (*service.CheckInRewardTier, error) {
	m, err := r.client.CheckInRewardTier.Query().
		Where(checkinrewardtier.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrCheckInTierNotFound
		}
		return nil, err
	}
	return checkInRewardTierEntityToService(m), nil
}

func (r *checkInRewardTierRepository) Update(ctx context.Context, tier *service.CheckInRewardTier) error {
	client := clientFromContext(ctx, r.client)
	updated, err := client.CheckInRewardTier.UpdateOneID(tier.ID).
		SetName(tier.Name).
		SetEnabled(tier.Enabled).
		SetMatchType(tier.MatchType).
		SetMatchThreshold(tier.MatchThreshold).
		SetMinReward(tier.MinReward).
		SetMaxReward(tier.MaxReward).
		SetBaseCap(tier.BaseCap).
		SetBetaMin(tier.BetaMin).
		SetBetaMax(tier.BetaMax).
		SetSortOrder(tier.SortOrder).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrCheckInTierNotFound
		}
		return err
	}

	tier.UpdatedAt = updated.UpdatedAt
	return nil
}

func (r *checkInRewardTierRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.CheckInRewardTier.Delete().Where(checkinrewardtier.IDEQ(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		// No row matched: report the same typed not-found used by GetByID/Update so the
		// handler maps it to 404 instead of a misleading success.
		return service.ErrCheckInTierNotFound
	}
	return nil
}

func (r *checkInRewardTierRepository) List(ctx context.Context) ([]service.CheckInRewardTier, error) {
	tiers, err := r.client.CheckInRewardTier.Query().
		Order(dbent.Asc(checkinrewardtier.FieldSortOrder), dbent.Asc(checkinrewardtier.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return checkInRewardTierEntitiesToService(tiers), nil
}

func (r *checkInRewardTierRepository) ListEnabled(ctx context.Context) ([]service.CheckInRewardTier, error) {
	tiers, err := r.client.CheckInRewardTier.Query().
		Where(checkinrewardtier.EnabledEQ(true)).
		Order(dbent.Asc(checkinrewardtier.FieldSortOrder), dbent.Asc(checkinrewardtier.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return checkInRewardTierEntitiesToService(tiers), nil
}

// Entity to Service conversions

func checkInRewardTierEntityToService(m *dbent.CheckInRewardTier) *service.CheckInRewardTier {
	if m == nil {
		return nil
	}
	return &service.CheckInRewardTier{
		ID:             m.ID,
		Name:           m.Name,
		Enabled:        m.Enabled,
		MatchType:      m.MatchType,
		MatchThreshold: m.MatchThreshold,
		MinReward:      m.MinReward,
		MaxReward:      m.MaxReward,
		BaseCap:        m.BaseCap,
		BetaMin:        m.BetaMin,
		BetaMax:        m.BetaMax,
		SortOrder:      m.SortOrder,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

func checkInRewardTierEntitiesToService(models []*dbent.CheckInRewardTier) []service.CheckInRewardTier {
	out := make([]service.CheckInRewardTier, 0, len(models))
	for i := range models {
		if s := checkInRewardTierEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}
