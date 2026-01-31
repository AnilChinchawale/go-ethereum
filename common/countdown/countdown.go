// Copyright (c) 2018 XDPoSChain
// A countdown timer for XDPoS V2 consensus timeout handling

package countdown

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// TimeoutDurationHelper calculates timeout duration based on rounds
type TimeoutDurationHelper interface {
	GetTimeoutDuration(currentRound, highestRound types.Round) time.Duration
	SetParams(duration time.Duration, base float64, maxExponent uint8) error
}

// ResetInfo contains round info for timer reset
type ResetInfo struct {
	CurrentRound types.Round
	HighestRound types.Round
}

// CountdownTimer manages the BFT timeout countdown
type CountdownTimer struct {
	lock           sync.RWMutex
	resetc         chan ResetInfo
	quitc          chan chan struct{}
	initilised     bool
	durationHelper TimeoutDurationHelper
	// OnTimeoutFn is called when countdown reaches zero
	OnTimeoutFn func(time time.Time, i interface{}) error
}

// NewExpCountDown creates a countdown timer with exponential backoff
func NewExpCountDown(duration time.Duration, base float64, maxExponent uint8) (*CountdownTimer, error) {
	durationHelper, err := NewExpTimeoutDuration(duration, base, maxExponent)
	if err != nil {
		return nil, err
	}
	return &CountdownTimer{
		resetc:         make(chan ResetInfo),
		quitc:          make(chan chan struct{}),
		initilised:     false,
		durationHelper: durationHelper,
	}, nil
}

// StopTimer completely stops the countdown timer
func (t *CountdownTimer) StopTimer() {
	q := make(chan struct{})
	t.quitc <- q
	<-q
}

// SetParams updates the timer parameters
func (t *CountdownTimer) SetParams(duration time.Duration, base float64, maxExponent uint8) error {
	return t.durationHelper.SetParams(duration, base, maxExponent)
}

// Reset starts or resets the countdown timer
func (t *CountdownTimer) Reset(i interface{}, currentRound, highestRound types.Round) {
	if !t.isInitilised() {
		t.setInitilised(true)
		go t.startTimer(i, currentRound, highestRound)
	} else {
		t.resetc <- ResetInfo{currentRound, highestRound}
	}
}

// startTimer runs the countdown loop
func (t *CountdownTimer) startTimer(i interface{}, currentRound, highestRound types.Round) {
	defer t.setInitilised(false)
	timer := time.NewTimer(t.durationHelper.GetTimeoutDuration(currentRound, highestRound))

	for {
		select {
		case q := <-t.quitc:
			log.Debug("Quit countdown timer")
			close(q)
			return
		case <-timer.C:
			log.Debug("Countdown time reached!")
			go func() {
				err := t.OnTimeoutFn(time.Now(), i)
				if err != nil {
					log.Error("OnTimeoutFn error", "error", err)
				}
			}()
			timer.Reset(t.durationHelper.GetTimeoutDuration(currentRound, highestRound))
		case info := <-t.resetc:
			currentRound = info.CurrentRound
			highestRound = info.HighestRound
			duration := t.durationHelper.GetTimeoutDuration(currentRound, highestRound)
			log.Debug("Reset countdown timer", "duration", duration, "currentRound", currentRound, "highestRound", highestRound)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(duration)
		}
	}
}

func (t *CountdownTimer) setInitilised(value bool) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.initilised = value
}

func (t *CountdownTimer) isInitilised() bool {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.initilised
}
