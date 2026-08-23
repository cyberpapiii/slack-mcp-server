package limiter

import (
	"time"

	"golang.org/x/time/rate"
)

// Tier is a process-wide Slack rate-limit budget. Every caller of the same
// tier shares one limiter, so concurrent tool calls split the burst instead of
// each minting a fresh one.
type Tier struct {
	lim *rate.Limiter
}

func newTier(every time.Duration, burst int) Tier {
	return Tier{lim: rate.NewLimiter(rate.Every(every), burst)}
}

func (t Tier) Limiter() *rate.Limiter {
	return t.lim
}

var (
	Tier2      = newTier(3*time.Second, 3)
	Tier2boost = newTier(300*time.Millisecond, 5)
	Tier3      = newTier(1200*time.Millisecond, 4)
)
