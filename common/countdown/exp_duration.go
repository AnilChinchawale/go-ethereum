// Copyright (c) 2018 XDPoSChain
// Exponential timeout duration calculation

package countdown

import (
	"errors"
	"math"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// ExpTimeoutDuration calculates exponential backoff timeout
type ExpTimeoutDuration struct {
	baseDuration time.Duration
	base         float64
	maxExponent  uint8
}

// NewExpTimeoutDuration creates a new exponential timeout calculator
func NewExpTimeoutDuration(duration time.Duration, base float64, maxExponent uint8) (*ExpTimeoutDuration, error) {
	if base < 1.0 {
		return nil, errors.New("base must be >= 1.0")
	}
	return &ExpTimeoutDuration{
		baseDuration: duration,
		base:         base,
		maxExponent:  maxExponent,
	}, nil
}

// SetParams updates the timeout parameters
func (e *ExpTimeoutDuration) SetParams(duration time.Duration, base float64, maxExponent uint8) error {
	if base < 1.0 {
		return errors.New("base must be >= 1.0")
	}
	e.baseDuration = duration
	e.base = base
	e.maxExponent = maxExponent
	return nil
}

// GetTimeoutDuration calculates timeout based on round difference
func (e *ExpTimeoutDuration) GetTimeoutDuration(currentRound, highestRound types.Round) time.Duration {
	// Calculate how many rounds behind the current round is
	var exponent uint64
	if currentRound > highestRound {
		exponent = uint64(currentRound - highestRound)
	} else {
		exponent = 0
	}

	// Cap the exponent
	if exponent > uint64(e.maxExponent) {
		exponent = uint64(e.maxExponent)
	}

	// Calculate multiplier: base^exponent
	multiplier := math.Pow(e.base, float64(exponent))

	return time.Duration(float64(e.baseDuration) * multiplier)
}
