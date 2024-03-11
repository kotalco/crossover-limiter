package crossover_limiter

import (
	"context"
	"errors"
	"fmt"
	"github.com/kotalco/resp"
	"strconv"
)

type IPlanCache interface {
	setUserPlan(ctx context.Context, userId string, plan int) error
	getUserPlan(ctx context.Context, userId string) (int, error)
	limit(ctx context.Context, key string, limit int, window int) (bool, error)
}
type PlanCache struct {
	resp resp.IClient
}

func NewPlanCache(client resp.IClient) IPlanCache {
	return &PlanCache{
		resp: client,
	}
}

func (s *PlanCache) setUserPlan(ctx context.Context, userId string, plan int) error {
	return s.resp.Set(ctx, userId, strconv.Itoa(plan))
}

func (s *PlanCache) getUserPlan(ctx context.Context, userId string) (int, error) {
	userPlan, err := s.resp.Get(ctx, userId)
	if err != nil {
		return 0, err
	}
	if userPlan == "" {
		return 0, nil
	}
	// Parse the user plan.
	userPlanInt, err := strconv.Atoi(userPlan)
	if err != nil {
		return 0, errors.New(fmt.Sprintf("can't parse userPlan: %s, got error: %s", userPlan, err.Error()))
	}

	return userPlanInt, nil
}

func (s *PlanCache) limit(ctx context.Context, key string, limit int, window int) (bool, error) {
	// Increment the counter for the given key.
	count, err := s.resp.Incr(ctx, key)
	if err != nil {
		return false, err
	}
	if count == 1 {
		// If the key is new or expired (i.e., count == 1), set the expiration.
		_, err = s.resp.Expire(ctx, key, window)
		if err != nil {
			return false, err
		}
	}

	// Check against the limit.
	return count <= limit, nil
}
