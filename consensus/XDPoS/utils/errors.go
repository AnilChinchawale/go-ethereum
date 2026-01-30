// Copyright (c) 2024 XDC Network
// Error definitions for XDPoS 2.0

package utils

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Common errors
var (
	ErrUnknownBlock       = errors.New("unknown block")
	ErrInvalidQC          = errors.New("invalid quorum certificate")
	ErrInvalidQCSignatures = errors.New("invalid QC signatures")
	ErrInvalidTC          = errors.New("invalid timeout certificate")
	ErrInvalidTCSignatures = errors.New("invalid TC signatures")
	ErrInvalidVote        = errors.New("invalid vote")
	ErrInvalidTimeout     = errors.New("invalid timeout")
	ErrNotReadyToMine     = errors.New("not ready to mine")
	ErrNotReadyToPropose  = errors.New("not ready to propose")
)

// ErrIncomingMessageRoundTooFarFromCurrentRound is returned when a message's round
// is too far from the current round
type ErrIncomingMessageRoundTooFarFromCurrentRound struct {
	Type          string
	IncomingRound types.Round
	CurrentRound  types.Round
}

func (e *ErrIncomingMessageRoundTooFarFromCurrentRound) Error() string {
	return fmt.Sprintf("%s message round %d too far from current round %d",
		e.Type, e.IncomingRound, e.CurrentRound)
}

// ErrIncomingMessageRoundNotEqualCurrentRound is returned when a message's round
// doesn't match the current round
type ErrIncomingMessageRoundNotEqualCurrentRound struct {
	Type          string
	IncomingRound types.Round
	CurrentRound  types.Round
}

func (e *ErrIncomingMessageRoundNotEqualCurrentRound) Error() string {
	return fmt.Sprintf("%s message round %d not equal to current round %d",
		e.Type, e.IncomingRound, e.CurrentRound)
}

// ErrIncomingMessageBlockNotFound is returned when the block referenced by a message
// is not found
type ErrIncomingMessageBlockNotFound struct {
	Type                string
	IncomingBlockHash   common.Hash
	IncomingBlockNumber *big.Int
	Err                 error
}

func (e *ErrIncomingMessageBlockNotFound) Error() string {
	return fmt.Sprintf("%s message references unknown block %s (#%s): %v",
		e.Type, e.IncomingBlockHash.Hex(), e.IncomingBlockNumber, e.Err)
}

func (e *ErrIncomingMessageBlockNotFound) Unwrap() error {
	return e.Err
}
