// Copyright (c) 2018 XDCchain
// Copyright 2024 The go-ethereum Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package XDPoS

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

// Note: RewardMasterPercent, RewardVoterPercent, RewardFoundationPercent are defined in constants.go

// RewardLog stores signing count and reward for a signer
type RewardLog struct {
	Sign   uint64
	Reward *big.Int
}

// BlockReader provides access to block headers for reward calculation.
// Full blocks are read directly from the database since Finalize receives
// ChainHeaderReader which doesn't have GetBlock.
type BlockReader interface {
	consensus.ChainHeaderReader
}

// GetRewardForCheckpoint calculates the signing rewards for the checkpoint epoch.
// It reads signing transactions from blocks to determine which masternodes signed which blocks.
// The scan range is from block 1 to (checkpoint - 1), looking for signing txs that reference
// the reward epoch blocks.
func (c *XDPoS) GetRewardForCheckpoint(
	chain BlockReader,
	header *types.Header,
	rCheckpoint uint64,
) (map[common.Address]*RewardLog, uint64, error) {
	number := header.Number.Uint64()

	// Match v2.6.8's formula:
	// prevCheckpoint = number - (rCheckpoint * 2)
	// startBlockNumber = prevCheckpoint + 1
	// endBlockNumber = startBlockNumber + rCheckpoint - 1
	// Scan blocks: (prevCheckpoint + rCheckpoint*2 - 1) down to startBlockNumber
	
	prevCheckpoint := number - (rCheckpoint * 2)
	startBlockNumber := prevCheckpoint + 1
	endBlockNumber := startBlockNumber + rCheckpoint - 1
	scanEndBlock := number - 1 // Scan up to block before current checkpoint

	// For block 1800: prevCheckpoint=0, start=1, end=900, scanEnd=1799
	// For block 900: prevCheckpoint would be negative, skip
	// Block 1800 is the FIRST reward checkpoint (rewards for epoch 0, blocks 1-900)
	if number < rCheckpoint*2 {
		log.Debug("Skipping rewards - before first reward checkpoint", "number", number)
		return nil, 0, nil
	}

	signers := make(map[common.Address]*RewardLog)
	var totalSigner uint64

	// Get masternodes from the epoch's starting checkpoint (block prevCheckpoint)
	epochCheckpoint := prevCheckpoint
	if epochCheckpoint == 0 {
		epochCheckpoint = 0
	}
	
	epochHeader := chain.GetHeaderByNumber(epochCheckpoint)
	if epochHeader == nil {
		log.Warn("Failed to get epoch header for reward calculation", "number", epochCheckpoint)
		return signers, totalSigner, nil
	}

	epoch := epochCheckpoint / rCheckpoint
	masternodes := c.GetMasternodesFromCheckpointHeader(epochHeader, epochCheckpoint, epoch)
	masternodeMap := make(map[common.Address]bool)
	for _, mn := range masternodes {
		masternodeMap[mn] = true
	}

	// Build block hash map for the reward epoch (blocks startBlockNumber to endBlockNumber)
	blockHashMap := make(map[uint64]common.Hash)
	for i := startBlockNumber; i <= endBlockNumber; i++ {
		h := chain.GetHeaderByNumber(i)
		if h != nil {
			blockHashMap[i] = h.Hash()
		}
	}

	// Collect signing data from ALL blocks up to checkpoint
	// Map: blockHash -> list of signers who signed that block
	blockSigners := make(map[common.Hash][]common.Address)

	log.Info("Scanning blocks for signing transactions",
		"from", scanEndBlock, "to", startBlockNumber, "rewardBlocks", endBlockNumber-startBlockNumber+1)

	// Scan blocks from scanEndBlock down to startBlockNumber (matching v2.6.8)
	txCount := 0
	signingTxCount := 0
	for i := scanEndBlock; i >= startBlockNumber; i-- {
		blockHeader := chain.GetHeaderByNumber(i)
		if blockHeader == nil {
			continue
		}
		
		// Read block directly from database since chain only provides headers
		block := rawdb.ReadBlock(c.db, blockHeader.Hash(), i)
		if block == nil {
			log.Debug("Failed to get block for reward calculation", "number", i)
			continue
		}

		// Find signing transactions in this block
		txs := block.Transactions()
		txCount += len(txs)
		for _, tx := range txs {
			if tx.IsSigningTransaction() {
				signingTxCount++
				// Extract the block hash being signed from tx data
				// Format: methodId (4 bytes) + blockNumber (32 bytes) + blockHash (32 bytes)
				data := tx.Data()
				if len(data) >= 68 {
					signedBlockHash := common.BytesToHash(data[len(data)-32:])
					
					// Get the sender of this signing tx
					signer, err := types.Sender(types.LatestSignerForChainID(big.NewInt(50)), tx)
					if err != nil {
						log.Debug("Failed to get signing tx sender", "err", err)
						continue
					}
					
					// Only count if signer is a masternode
					if masternodeMap[signer] {
						blockSigners[signedBlockHash] = append(blockSigners[signedBlockHash], signer)
					}
				}
			}
		}
	}
	
	log.Info("Scanned blocks for signing transactions",
		"totalTxs", txCount, "signingTxs", signingTxCount, "blockSignerEntries", len(blockSigners))

	// Count signatures per signer
	for i := startBlockNumber; i <= endBlockNumber; i++ {
		blockHeader := chain.GetHeaderByNumber(i)
		if blockHeader == nil {
			continue
		}
		
		addrs := blockSigners[blockHeader.Hash()]
		if len(addrs) > 0 {
			// Track unique signers for this block
			seen := make(map[common.Address]bool)
			for _, addr := range addrs {
				if !seen[addr] && masternodeMap[addr] {
					seen[addr] = true
					
					if _, exists := signers[addr]; exists {
						signers[addr].Sign++
					} else {
						signers[addr] = &RewardLog{Sign: 1, Reward: new(big.Int)}
					}
					totalSigner++
				}
			}
		}
	}

	log.Info("Calculated signers for checkpoint",
		"checkpoint", number,
		"startBlock", startBlockNumber,
		"endBlock", endBlockNumber,
		"totalSigners", totalSigner,
		"uniqueSigners", len(signers))

	return signers, totalSigner, nil
}

// CalculateRewardForSigner calculates the reward amount for each signer
// based on their signing activity.
// Uses v2.6.8 calculation order: (chainReward / totalSigner) * sign
func CalculateRewardForSigner(
	chainReward *big.Int,
	signers map[common.Address]*RewardLog,
	totalSigner uint64,
) map[common.Address]*big.Int {
	resultSigners := make(map[common.Address]*big.Int)

	if totalSigner == 0 {
		return resultSigners
	}

	for signer, rLog := range signers {
		// Match v2.6.8: divide first, then multiply
		calcReward := new(big.Int).Set(chainReward)
		calcReward.Div(calcReward, new(big.Int).SetUint64(totalSigner))
		calcReward.Mul(calcReward, new(big.Int).SetUint64(rLog.Sign))
		rLog.Reward = calcReward
		resultSigners[signer] = calcReward
	}

	return resultSigners
}

// CalculateRewardForHolders distributes the signer's reward among the masternode owner and voters.
// - Owner gets RewardMasterPercent (90%)
// - Voters share RewardVoterPercent (0% currently)
// - Foundation gets RewardFoundationPercent (10%) - handled separately
func CalculateRewardForHolders(
	foundationWallet common.Address,
	statedb *state.StateDB,
	signer common.Address,
	calcReward *big.Int,
	blockNumber uint64,
) map[common.Address]*big.Int {
	balances := make(map[common.Address]*big.Int)

	if calcReward == nil || calcReward.Sign() <= 0 {
		return balances
	}

	// Get the owner of this masternode
	owner := state.GetCandidateOwner(statedb, signer)
	if owner == (common.Address{}) {
		owner = signer // Fallback to signer if no owner found
	}

	// Calculate owner portion (90% of the signer's reward)
	rewardMaster := new(big.Int).Mul(calcReward, big.NewInt(RewardMasterPercent))
	rewardMaster.Div(rewardMaster, big.NewInt(100))
	balances[owner] = rewardMaster

	// Voter rewards are 0% currently, infrastructure kept for future
	if RewardVoterPercent > 0 {
		voters := state.GetVoters(statedb, signer)
		if len(voters) > 0 {
			totalVoterReward := new(big.Int).Mul(calcReward, big.NewInt(RewardVoterPercent))
			totalVoterReward.Div(totalVoterReward, big.NewInt(100))

			totalCap := big.NewInt(0)
			voterCaps := make(map[common.Address]*big.Int)

			for _, voter := range voters {
				if _, exists := voterCaps[voter]; exists {
					continue
				}
				voterCap := state.GetVoterCap(statedb, signer, voter)
				if voterCap.Sign() > 0 {
					totalCap.Add(totalCap, voterCap)
					voterCaps[voter] = voterCap
				}
			}

			if totalCap.Sign() > 0 {
				for voter, voterCap := range voterCaps {
					reward := new(big.Int).Mul(totalVoterReward, voterCap)
					reward.Div(reward, totalCap)

					if balances[voter] != nil {
						balances[voter].Add(balances[voter], reward)
					} else {
						balances[voter] = reward
					}
				}
			}
		}
	}

	return balances
}

// ApplyRewards distributes rewards at checkpoint blocks.
func (c *XDPoS) ApplyRewards(
	chain BlockReader,
	statedb *state.StateDB,
	parentState *state.StateDB,
	header *types.Header,
) (map[string]interface{}, error) {
	rewards := make(map[string]interface{})
	number := header.Number.Uint64()

	rCheckpoint := c.config.RewardCheckpoint
	if rCheckpoint == 0 {
		rCheckpoint = c.config.Epoch
	}

	foundationWallet := c.config.FoudationWalletAddr
	if foundationWallet == (common.Address{}) {
		log.Error("Foundation wallet address is empty")
		return rewards, nil
	}

	// Only calculate rewards starting from second checkpoint
	if number <= rCheckpoint {
		log.Debug("Skipping rewards - at or before first checkpoint", "number", number)
		return rewards, nil
	}

	// Get the chain reward
	chainReward := new(big.Int).Mul(
		new(big.Int).SetUint64(c.config.Reward),
		big.NewInt(1e18),
	)

	// Get signers for this checkpoint
	signers, totalSigner, err := c.GetRewardForCheckpoint(chain, header, rCheckpoint)
	if err != nil {
		log.Error("Failed to get reward checkpoint", "err", err)
		return rewards, err
	}

	if totalSigner == 0 {
		log.Warn("No signers found for reward calculation", "number", number)
		return rewards, nil
	}

	// Calculate rewards per signer
	signerRewards := CalculateRewardForSigner(chainReward, signers, totalSigner)

	// Use parentState for reading voter/owner info if available
	readState := parentState
	if readState == nil {
		readState = statedb
	}

	// Only distribute rewards if there are signers
	// Foundation reward is part of holder rewards in v2.6.8
	voterResults := make(map[common.Address]interface{})
	totalDistributed := big.NewInt(0)

	// Foundation reward is accumulated per-signer to match v2.6.8's rounding behavior
	totalFoundationReward := big.NewInt(0)

	if len(signerRewards) > 0 {
		for signer, signerReward := range signerRewards {
			holderRewards := CalculateRewardForHolders(foundationWallet, readState, signer, signerReward, number)

			for holder, reward := range holderRewards {
				if reward.Sign() > 0 {
					log.Debug("Distributing holder reward",
						"signer", signer.Hex(),
						"holder", holder.Hex(),
						"reward", reward.String())
					rewardU256, _ := uint256.FromBig(reward)
					statedb.AddBalance(holder, rewardU256, tracing.BalanceIncreaseRewardMineBlock)
					totalDistributed.Add(totalDistributed, reward)
				}
			}
			voterResults[signer] = holderRewards

			// Calculate foundation reward per-signer (matching v2.6.8's rounding)
			signerFoundationReward := new(big.Int).Mul(signerReward, big.NewInt(RewardFoundationPercent))
			signerFoundationReward.Div(signerFoundationReward, big.NewInt(100))
			totalFoundationReward.Add(totalFoundationReward, signerFoundationReward)
		}

		// Distribute accumulated foundation reward
		if totalFoundationReward.Sign() > 0 {
			log.Debug("Distributing foundation reward",
				"foundation", foundationWallet.Hex(),
				"reward", totalFoundationReward.String())
			foundationU256, _ := uint256.FromBig(totalFoundationReward)
			statedb.AddBalance(foundationWallet, foundationU256, tracing.BalanceIncreaseRewardMineBlock)
			totalDistributed.Add(totalDistributed, totalFoundationReward)
		}

		log.Info("Rewards distributed",
			"block", number,
			"totalSigners", totalSigner,
			"uniqueSigners", len(signers),
			"totalDistributed", totalDistributed.String())
	} else {
		log.Debug("No signers found, skipping rewards", "block", number)
	}

	rewards["signers"] = signers
	rewards["rewards"] = voterResults
	rewards["totalDistributed"] = totalDistributed.String()

	return rewards, nil
}

// CreateDefaultHookReward creates the reward hook function.
// The hook receives ChainHeaderReader, and we read full blocks directly from the database.
func (c *XDPoS) CreateDefaultHookReward() func(chain consensus.ChainHeaderReader, state *state.StateDB, header *types.Header) (map[string]interface{}, error) {
	return func(chain consensus.ChainHeaderReader, statedb *state.StateDB, header *types.Header) (map[string]interface{}, error) {
		// BlockReader embeds ChainHeaderReader, so we can pass chain directly
		return c.ApplyRewards(chain, statedb, nil, header)
	}
}
