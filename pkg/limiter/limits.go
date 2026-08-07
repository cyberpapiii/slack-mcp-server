package limiter

import (
	"time"

	"golang.org/x/time/rate"
)

type tier struct {
	t time.Duration
	b int
}

func (t tier) Limiter() *rate.Limiter {
	return rate.NewLimiter(rate.Every(t.t), t.b)
}

var (
	Tier2      = tier{t: 3 * time.Second, b: 3}
	Tier2boost = tier{t: 300 * time.Millisecond, b: 5}
	Tier3      = tier{t: 1200 * time.Millisecond, b: 4}
)
