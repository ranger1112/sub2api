//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountRepoStubForPlanGroups struct {
	accountRepoStub
	nextID      int64
	created     *Account
	updated     *Account
	byID        map[int64]*Account
	boundGroups map[int64][]int64
}

func (s *accountRepoStubForPlanGroups) Create(_ context.Context, account *Account) error {
	if s.nextID != 0 && account.ID == 0 {
		account.ID = s.nextID
	}
	s.created = account
	if s.byID == nil {
		s.byID = map[int64]*Account{}
	}
	cp := *account
	s.byID[account.ID] = &cp
	return nil
}

func (s *accountRepoStubForPlanGroups) Update(_ context.Context, account *Account) error {
	s.updated = account
	if s.byID == nil {
		s.byID = map[int64]*Account{}
	}
	cp := *account
	s.byID[account.ID] = &cp
	return nil
}

func (s *accountRepoStubForPlanGroups) GetByID(_ context.Context, id int64) (*Account, error) {
	if account, ok := s.byID[id]; ok {
		cp := *account
		return &cp, nil
	}
	return nil, ErrAccountNotFound
}

func (s *accountRepoStubForPlanGroups) BindGroups(_ context.Context, accountID int64, groupIDs []int64) error {
	if s.boundGroups == nil {
		s.boundGroups = map[int64][]int64{}
	}
	s.boundGroups[accountID] = append([]int64(nil), groupIDs...)
	return nil
}

func openAIPlanGroupTestRepo() *groupRepoStubForAdmin {
	return &groupRepoStubForAdmin{
		listActiveByPlatformGroups: []Group{
			{ID: 3, Name: "codex-plus", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
			{ID: 4, Name: "中包", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			{ID: 5, Name: "小包", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			{ID: 6, Name: "大包", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			{ID: 7, Name: "codex-free", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
		},
	}
}

func TestAdminServiceCreateAccount_DefaultOpenAIPlusGroups(t *testing.T) {
	repo := &accountRepoStubForPlanGroups{nextID: 101}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: openAIPlanGroupTestRepo()}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:        "plus@example.com",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus"},
	})

	require.NoError(t, err)
	require.Equal(t, int64(101), account.ID)
	require.ElementsMatch(t, []int64{3, 4, 5, 6}, repo.boundGroups[101])
}

func TestAdminServiceCreateAccount_DefaultOpenAIFreeGroups(t *testing.T) {
	repo := &accountRepoStubForPlanGroups{nextID: 102}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: openAIPlanGroupTestRepo()}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:        "free@example.com",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "free"},
	})

	require.NoError(t, err)
	require.Equal(t, int64(102), account.ID)
	require.Equal(t, []int64{7}, repo.boundGroups[102])
}

func TestAdminServiceCreateAccount_ExplicitGroupsOverridePlanDefaults(t *testing.T) {
	repo := &accountRepoStubForPlanGroups{nextID: 103}
	svc := &adminServiceImpl{
		accountRepo: repo,
		groupRepo: &groupRepoStubForAdmin{
			getByID: &Group{ID: 99, Name: "manual", Platform: PlatformOpenAI, Status: StatusActive},
		},
	}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "manual@example.com",
		Platform:              PlatformOpenAI,
		Type:                  AccountTypeOAuth,
		Credentials:           map[string]any{"plan_type": "plus"},
		GroupIDs:              []int64{99},
		SkipMixedChannelCheck: true,
	})

	require.NoError(t, err)
	require.Equal(t, int64(103), account.ID)
	require.Equal(t, []int64{99}, repo.boundGroups[103])
}

func TestAdminServiceCreateAccount_ExplicitEmptyGroupsDisablePlanDefaults(t *testing.T) {
	repo := &accountRepoStubForPlanGroups{nextID: 104}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: openAIPlanGroupTestRepo()}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:        "empty@example.com",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus"},
		GroupIDs:    []int64{},
	})

	require.NoError(t, err)
	require.Equal(t, int64(104), account.ID)
	require.Empty(t, repo.boundGroups[104])
}

func TestAdminServiceUpdateAccount_DefaultGroupsWhenCredentialsPlanChanges(t *testing.T) {
	repo := &accountRepoStubForPlanGroups{
		byID: map[int64]*Account{
			201: {
				ID:          201,
				Name:        "was-free@example.com",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Credentials: map[string]any{"plan_type": "free"},
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: openAIPlanGroupTestRepo()}

	updated, err := svc.UpdateAccount(context.Background(), 201, &UpdateAccountInput{
		Credentials: map[string]any{"plan_type": "plus"},
	})

	require.NoError(t, err)
	require.Equal(t, int64(201), updated.ID)
	require.ElementsMatch(t, []int64{3, 4, 5, 6}, repo.boundGroups[201])
}
