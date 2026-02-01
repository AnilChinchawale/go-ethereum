// Copyright (c) 2018 XDCchain
// Copyright 2024 The go-ethereum Authors
//
// This file adds GetValidator and M1M2 mapping for double validation in XDPoS.

package XDPoS

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// GetValidator returns the validator (M2) assigned to a creator (M1) for double validation.
// In XDPoS, each block creator has an assigned validator who must also sign the block.
// This implements the M1-M2 pairing where the validator rotates based on block position in epoch.
func (c *XDPoS) GetValidator(creator common.Address, chain consensus.ChainHeaderReader, header *types.Header) (common.Address, error) {
	epoch := c.config.Epoch
	no := header.Number.Uint64()
	
	// Calculate checkpoint block number
	cpNo := no
	if no%epoch != 0 {
		cpNo = no - (no % epoch)
	}
	if cpNo == 0 {
		// First epoch has no double validation
		return common.Address{}, nil
	}
	
	cpHeader := chain.GetHeaderByNumber(cpNo)
	if cpHeader == nil {
		if no%epoch == 0 {
			// We are at checkpoint, use current header
			cpHeader = header
		} else {
			return common.Address{}, fmt.Errorf("couldn't find checkpoint header at %d", cpNo)
		}
	}
	
	m, err := c.GetM1M2FromCheckpointHeader(cpHeader, header)
	if err != nil {
		return common.Address{}, err
	}
	return m[creator], nil
}

// GetM1M2FromCheckpointHeader returns the mapping of block creators (M1) to their 
// validators (M2) based on the checkpoint header's validator assignments.
func (c *XDPoS) GetM1M2FromCheckpointHeader(checkpointHeader *types.Header, currentHeader *types.Header) (map[common.Address]common.Address, error) {
	epoch := c.config.Epoch
	
	if checkpointHeader.Number.Uint64()%epoch != 0 {
		return nil, errors.New("this block is not a checkpoint block")
	}
	
	// Get masternodes from checkpoint header
	masternodes := c.GetMasternodesFromCheckpointHeader(checkpointHeader, 
		checkpointHeader.Number.Uint64(), epoch)
	
	// Extract validator indices from header.Validators field
	// Validators field contains M2ByteLength bytes per masternode indicating their validator index
	validators := ExtractValidatorsFromBytes(checkpointHeader.Validators)
	
	m1m2, _, err := getM1M2Mapping(masternodes, validators, currentHeader, c.config, epoch)
	if err != nil {
		return map[common.Address]common.Address{}, err
	}
	return m1m2, nil
}

// getM1M2Mapping computes the M1->M2 mapping with rotation based on block number.
// The rotation ensures different validators are assigned over time within an epoch.
func getM1M2Mapping(masternodes []common.Address, validators []int64, currentHeader *types.Header, config *params.XDPoSConfig, epoch uint64) (map[common.Address]common.Address, uint64, error) {
	m1m2 := map[common.Address]common.Address{}
	maxMNs := len(masternodes)
	moveM2 := uint64(0)
	
	if len(validators) < maxMNs {
		log.Debug("Validators list shorter than masternodes", "validators", len(validators), "masternodes", maxMNs)
		// Fall back to self-validation for early blocks or incomplete validator lists
		for _, mn := range masternodes {
			m1m2[mn] = mn
		}
		return m1m2, moveM2, nil
	}
	
	if maxMNs > 0 {
		// Calculate rotation based on position within epoch
		// This ensures different M2 validators over time
		moveM2 = ((currentHeader.Number.Uint64() % epoch) / uint64(maxMNs)) % uint64(maxMNs)
		
		for i, m1 := range masternodes {
			m2Index := uint64(validators[i] % int64(maxMNs))
			m2Index = (m2Index + moveM2) % uint64(maxMNs)
			m1m2[m1] = masternodes[m2Index]
		}
	}
	return m1m2, moveM2, nil
}
