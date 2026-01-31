// Copyright (c) 2018 XDPoSChain
// XDPoS V2 error definitions

package utils

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// V2 specific errors
var (
	ErrInvalidQC           = errors.New("invalid quorum certificate")
	ErrInvalidTC           = errors.New("invalid timeout certificate")
	ErrInvalidQCSignatures = errors.New("invalid QC signatures")
	ErrInvalidTCSignatures = errors.New("invalid TC signatures")
	ErrNotReadyToMine      = errors.New("not ready to mine")
	ErrUnknownBlock        = errors.New("unknown block")
)

// ErrIncomingMessageRoundNotEqualCurrentRound is returned when message round doesn't match
type ErrIncomingMessageRoundNotEqualCurrentRound struct {
	Type          string
	IncomingRound types.Round
	CurrentRound  types.Round
}

func (e *ErrIncomingMessageRoundNotEqualCurrentRound) Error() string {
	return fmt.Sprintf("%s message round %d does not match current round %d",
		e.Type, e.IncomingRound, e.CurrentRound)
}

// ErrIncomingMessageRoundTooFarFromCurrentRound is returned when message round is too far
type ErrIncomingMessageRoundTooFarFromCurrentRound struct {
	Type          string
	IncomingRound types.Round
	CurrentRound  types.Round
}

func (e *ErrIncomingMessageRoundTooFarFromCurrentRound) Error() string {
	return fmt.Sprintf("%s message round %d is too far from current round %d",
		e.Type, e.IncomingRound, e.CurrentRound)
}

// ErrIncomingMessageBlockNotFound is returned when the block referenced by message is not found
type ErrIncomingMessageBlockNotFound struct {
	Type                string
	IncomingBlockHash   common.Hash
	IncomingBlockNumber *big.Int
	Err                 error
}

func (e *ErrIncomingMessageBlockNotFound) Error() string {
	return fmt.Sprintf("%s message references block %d (%s) not found: %v",
		e.Type, e.IncomingBlockNumber, e.IncomingBlockHash.Hex(), e.Err)
}
