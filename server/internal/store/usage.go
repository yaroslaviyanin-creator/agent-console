package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"agent-console/server/internal/provider"
)

const (
	fiveHours = 5 * time.Hour
	sevenDays = 7 * 24 * time.Hour
)

func (s Subscription) Limits() (int64, int64) {
	if s.PlanTier != "pro" && s.PlanTier != "2x_pro" {
		return 0, 0
	}
	five := planLimit("PRO_LIMIT_5H_CENTS", 1500)
	week := planLimit("PRO_LIMIT_7D_CENTS", 15000)
	if s.PlanTier == "2x_pro" {
		return 2 * five, 2 * week
	}
	return five, week
}

func planLimit(key string, fallback int64) int64 {
	if limit, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil && limit >= 0 && limit <= math.MaxInt64/2 {
		return limit
	}
	return fallback
}

func priceUsage(usage *provider.Usage, model, prompt, text, thinking string) (provider.Usage, int64) {
	counts := provider.Usage{InputTokens: max(1, int64(len(prompt)/4)), OutputTokens: max(1, int64((len(text)+len(thinking))/4))}
	if usage != nil {
		counts = provider.Usage{
			InputTokens:       max(0, usage.InputTokens),
			CachedInputTokens: max(0, usage.CachedInputTokens),
			OutputTokens:      max(0, usage.OutputTokens),
		}
	}
	if counts == (provider.Usage{}) {
		return counts, 0
	}

	var inputRate, cachedRate, outputRate int64 = 300, 30, 1500
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "opus"):
		inputRate, cachedRate, outputRate = 2*inputRate, 2*cachedRate, 2*outputRate
	case strings.Contains(model, "mini"), strings.Contains(model, "haiku"):
		inputRate, cachedRate, outputRate = inputRate/3, cachedRate/3, outputRate/3
	}
	const million = 1_000_000
	input := max(0, counts.InputTokens-counts.CachedInputTokens)
	cached, output := counts.CachedInputTokens, counts.OutputTokens
	// Keep whole millions separate to avoid overflow before converting to cents.
	whole := input/million*inputRate + cached/million*cachedRate + output/million*outputRate
	remainder := input%million*inputRate + cached%million*cachedRate + output%million*outputRate
	return counts, max(1, whole+(remainder+million/2)/million)
}

const windowSum = `(SELECT COALESCE(SUM(cost_cents),0) FROM usage_events WHERE user_id=owner.id AND created_at>?%d)`

const windowReset = `(SELECT COALESCE(MIN(created_at)+%d,0) FROM usage_events WHERE user_id=owner.id AND created_at>?%d)`

type usageTotals struct {
	fiveHour, sevenDay   int64
	fiveReset, weekReset int64
}

func usageColumns(five, week int) string {
	return fmt.Sprintf(windowSum+", "+windowSum+", "+windowReset+", "+windowReset,
		five, week, fiveHours.Milliseconds(), five, sevenDays.Milliseconds(), week)
}

func (s *Store) UserUsage(ctx context.Context, userID string) (int64, int64, error) {
	usage, err := userUsage(ctx, s.db, userID, time.Now())
	return usage.fiveHour, usage.sevenDay, err
}

func userUsage(ctx context.Context, db rowQuerier, userID string, now time.Time) (usageTotals, error) {
	var usage usageTotals
	query := "SELECT " + usageColumns(1, 2) + " FROM users owner WHERE owner.id=?3"
	err := db.QueryRowContext(ctx, query, now.Add(-fiveHours).UnixMilli(), now.Add(-sevenDays).UnixMilli(), userID).
		Scan(&usage.fiveHour, &usage.sevenDay, &usage.fiveReset, &usage.weekReset)
	return usage, err
}

type QuotaWindow struct {
	UsedCents   int64   `json:"usedCents"`
	LimitCents  int64   `json:"limitCents"`
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    int64   `json:"resetsAt"`
}

type Quota struct {
	Subscription
	FiveHour QuotaWindow `json:"fiveHour"`
	SevenDay QuotaWindow `json:"sevenDay"`
}

func quotaWindow(used, limit, reset int64) QuotaWindow {
	result := QuotaWindow{UsedCents: used, LimitCents: limit, ResetsAt: reset}
	if limit > 0 {
		result.UsedPercent = 100 * float64(used) / float64(limit)
	}
	return result
}

func (s *Store) CurrentQuota(ctx context.Context, user User) (Quota, error) {
	usage, err := userUsage(ctx, s.db, user.ID, time.Now())
	five, week := user.Limits()
	return Quota{
		Subscription: user.Subscription,
		FiveHour:     quotaWindow(usage.fiveHour, five, usage.fiveReset),
		SevenDay:     quotaWindow(usage.sevenDay, week, usage.weekReset),
	}, err
}
