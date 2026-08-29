package provider

import "context"

// UsageWindow mirrors LiveUsageWindow from src/types/account.ts.
type UsageWindow struct {
	Label            string   `json:"label"`
	RemainingPercent int      `json:"remainingPercent"`
	ResetsAtMs       *float64 `json:"resetsAtMs,omitempty"`
}

// LiveUsage mirrors LiveUsage.
type LiveUsage struct {
	Windows []UsageWindow `json:"windows"`
}

// LiveUsageProvider is implemented by providers that can read quota/rate-limit
// from their upstream API using the current live login (no snapshot needed).
type LiveUsageProvider interface {
	LiveUsage(ctx context.Context) (*LiveUsage, error)
}
