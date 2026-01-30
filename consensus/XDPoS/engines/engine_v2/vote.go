// Copyright (c) 2024 XDC Network
// Vote handling for XDPoS 2.0 BFT consensus

package engine_v2

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// VerifyVoteMessage verifies a vote message from a peer
func (x *XDPoS_v2) VerifyVoteMessage(chain consensus.ChainReader, vote *types.Vote) (bool, error) {
	// Check if vote round is valid
	if vote.ProposedBlockInfo.Round < x.currentRound {
		log.Debug("[VerifyVoteMessage] Vote round too old",
			"voteRound", vote.ProposedBlockInfo.Round,
			"currentRound", x.currentRound)
		return false, nil
	}

	// Get snapshot for vote's gap number
	snapshot, err := x.getSnapshot(chain, vote.GapNumber, true)
	if err != nil {
		log.Error("[VerifyVoteMessage] Failed to get snapshot",
			"blockNum", vote.ProposedBlockInfo.Number,
			"error", err)
		return false, err
	}

	// Verify signature
	verified, signer, err := x.verifyMsgSignature(
		types.VoteSigHash(&types.VoteForSign{
			ProposedBlockInfo: vote.ProposedBlockInfo,
			GapNumber:         vote.GapNumber,
		}),
		vote.Signature,
		snapshot.NextEpochCandidates,
	)
	if err != nil {
		log.Warn("[VerifyVoteMessage] Signature verification failed",
			"blockNum", vote.ProposedBlockInfo.Number,
			"error", err)
		return false, err
	}
	vote.SetSigner(signer)

	return verified, nil
}

// VoteHandler processes a vote message
func (x *XDPoS_v2) VoteHandler(chain consensus.ChainReader, voteMsg *types.Vote) error {
	x.lock.Lock()
	defer x.lock.Unlock()
	return x.voteHandler(chain, voteMsg)
}

func (x *XDPoS_v2) voteHandler(chain consensus.ChainReader, voteMsg *types.Vote) error {
	// Check round
	if voteMsg.ProposedBlockInfo.Round != x.currentRound && voteMsg.ProposedBlockInfo.Round != x.currentRound+1 {
		return &utils.ErrIncomingMessageRoundTooFarFromCurrentRound{
			Type:          "vote",
			IncomingRound: voteMsg.ProposedBlockInfo.Round,
			CurrentRound:  x.currentRound,
		}
	}

	if x.votePoolCollectionTime.IsZero() {
		log.Info("[voteHandler] set vote pool time", "round", x.currentRound)
		x.votePoolCollectionTime = time.Now()
	}

	// Add to pool
	numberOfVotes, pooledVotes := x.votePool.Add(voteMsg)
	log.Debug("[voteHandler] collected vote", "count", numberOfVotes)

	// Get epoch info
	epochInfo, err := x.getEpochSwitchInfo(chain, nil, voteMsg.ProposedBlockInfo.Hash)
	if err != nil {
		return &utils.ErrIncomingMessageBlockNotFound{
			Type:                "vote",
			IncomingBlockHash:   voteMsg.ProposedBlockInfo.Hash,
			IncomingBlockNumber: voteMsg.ProposedBlockInfo.Number,
			Err:                 err,
		}
	}

	// Check threshold
	certThreshold := x.config.V2.CurrentConfig.CertThreshold
	thresholdReached := float64(numberOfVotes) >= float64(epochInfo.MasternodesLen)*certThreshold

	if thresholdReached {
		log.Info("[voteHandler] Vote threshold reached",
			"votes", numberOfVotes,
			"threshold", float64(epochInfo.MasternodesLen)*certThreshold)

		// Check if block exists
		proposedBlockHeader := chain.GetHeaderByHash(voteMsg.ProposedBlockInfo.Hash)
		if proposedBlockHeader == nil {
			log.Info("[voteHandler] Block not found, waiting for next vote",
				"blockNum", voteMsg.ProposedBlockInfo.Number,
				"hash", voteMsg.ProposedBlockInfo.Hash)
			return nil
		}

		// Verify block info
		if err := x.VerifyBlockInfo(chain, voteMsg.ProposedBlockInfo, nil); err != nil {
			return err
		}

		// Verify all votes
		x.verifyVotes(chain, pooledVotes, proposedBlockHeader)

		// Generate QC
		if err := x.onVotePoolThresholdReached(chain, pooledVotes, voteMsg, proposedBlockHeader); err != nil {
			return err
		}

		elapsed := time.Since(x.votePoolCollectionTime)
		log.Info("[voteHandler] QC created", "elapsed", elapsed)
		x.votePoolCollectionTime = time.Time{}
	}

	return nil
}

// sendVote sends a vote for a block
func (x *XDPoS_v2) sendVote(chain consensus.ChainReader, blockInfo *types.BlockInfo) error {
	// Get epoch info for gap number
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, nil, blockInfo.Hash)
	if err != nil {
		log.Error("[sendVote] Failed to get epoch switch info", "error", err)
		return err
	}

	epochSwitchNumber := epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()
	gapNumber := epochSwitchNumber - epochSwitchNumber%x.config.Epoch
	if gapNumber > x.config.Gap {
		gapNumber -= x.config.Gap
	} else {
		gapNumber = 0
	}

	// Sign vote
	signedHash, err := x.signSignature(types.VoteSigHash(&types.VoteForSign{
		ProposedBlockInfo: blockInfo,
		GapNumber:         gapNumber,
	}))
	if err != nil {
		log.Error("[sendVote] Failed to sign", "error", err)
		return err
	}

	x.highestVotedRound = x.currentRound
	voteMsg := &types.Vote{
		ProposedBlockInfo: blockInfo,
		Signature:         signedHash,
		GapNumber:         gapNumber,
	}

	// Process locally
	if err := x.voteHandler(chain, voteMsg); err != nil {
		log.Error("[sendVote] Local handler error", "error", err)
		return err
	}

	// Broadcast
	x.broadcastToBftChannel(voteMsg)
	return nil
}

// verifyVotes verifies all votes in the pool
func (x *XDPoS_v2) verifyVotes(chain consensus.ChainReader, votes map[common.Hash]utils.PoolObj, header *types.Header) {
	masternodes := x.GetMasternodes(chain, header)
	emptySigner := common.Address{}

	var wg sync.WaitGroup
	wg.Add(len(votes))

	for h, vote := range votes {
		go func(hash common.Hash, v *types.Vote) {
			defer wg.Done()

			signerAddress := v.GetSigner()
			if signerAddress != emptySigner {
				// Already verified, check if still in masternode list
				for _, mn := range masternodes {
					if mn == signerAddress {
						return
					}
				}
				// Not in current masternodes
				v.SetSigner(emptySigner)
				return
			}

			// Verify signature
			signedVote := types.VoteSigHash(&types.VoteForSign{
				ProposedBlockInfo: v.ProposedBlockInfo,
				GapNumber:         v.GapNumber,
			})
			verified, masterNode, err := x.verifyMsgSignature(signedVote, v.Signature, masternodes)
			if err != nil || !verified {
				log.Warn("[verifyVotes] Vote verification failed", "error", err)
				return
			}
			v.SetSigner(masterNode)
		}(h, vote.(*types.Vote))
	}
	wg.Wait()
}

// onVotePoolThresholdReached generates a QC when enough votes are collected
func (x *XDPoS_v2) onVotePoolThresholdReached(chain consensus.ChainReader, pooledVotes map[common.Hash]utils.PoolObj, currentVoteMsg utils.PoolObj, header *types.Header) error {
	// Collect valid signatures
	var validSignatures []types.Signature
	emptySigner := common.Address{}

	for _, vote := range pooledVotes {
		if vote.GetSigner() != emptySigner {
			validSignatures = append(validSignatures, vote.(*types.Vote).Signature)
		}
	}

	// Get epoch info
	epochInfo, err := x.getEpochSwitchInfo(chain, nil, currentVoteMsg.(*types.Vote).ProposedBlockInfo.Hash)
	if err != nil {
		log.Error("[onVotePoolThresholdReached] Failed to get epoch info", "error", err)
		return errors.New("failed to get epoch switch info")
	}

	// Check if enough valid signatures
	certThreshold := x.config.V2.CurrentConfig.CertThreshold
	if float64(len(validSignatures)) < float64(epochInfo.MasternodesLen)*certThreshold {
		log.Warn("[onVotePoolThresholdReached] Not enough valid signatures",
			"valid", len(validSignatures),
			"needed", float64(epochInfo.MasternodesLen)*certThreshold)
		return nil
	}

	// Generate QC
	quorumCert := &types.QuorumCert{
		ProposedBlockInfo: currentVoteMsg.(*types.Vote).ProposedBlockInfo,
		Signatures:        validSignatures,
		GapNumber:         currentVoteMsg.(*types.Vote).GapNumber,
	}

	// Process QC
	if err := x.processQC(chain, quorumCert); err != nil {
		log.Error("[onVotePoolThresholdReached] Failed to process QC", "error", err)
		return err
	}

	log.Info("Successfully created QC",
		"round", quorumCert.ProposedBlockInfo.Round,
		"signatures", len(quorumCert.Signatures),
		"hash", quorumCert.ProposedBlockInfo.Hash)
	return nil
}

// verifyVotingRule checks HotStuff voting rules
func (x *XDPoS_v2) verifyVotingRule(chain consensus.ChainReader, blockInfo *types.BlockInfo, quorumCert *types.QuorumCert) (bool, error) {
	// Haven't voted this round yet?
	if x.currentRound <= x.highestVotedRound {
		log.Info("[verifyVotingRule] Already voted this round",
			"currentRound", x.currentRound,
			"highestVotedRound", x.highestVotedRound)
		return false, nil
	}

	// Block round matches current round?
	if blockInfo.Round != x.currentRound {
		log.Info("[verifyVotingRule] Round mismatch",
			"currentRound", x.currentRound,
			"blockRound", blockInfo.Round)
		return false, nil
	}

	// First V2 block or no lock QC?
	if x.lockQuorumCert == nil {
		return true, nil
	}

	// QC round > lock QC round?
	if quorumCert.ProposedBlockInfo.Round > x.lockQuorumCert.ProposedBlockInfo.Round {
		return true, nil
	}

	// Block extends from lock QC?
	isExtended, err := x.isExtendingFromAncestor(chain, blockInfo, x.lockQuorumCert.ProposedBlockInfo)
	if err != nil {
		log.Error("[verifyVotingRule] isExtendingFromAncestor error", "error", err)
		return false, err
	}

	if !isExtended {
		log.Warn("[verifyVotingRule] Block doesn't extend from lock QC",
			"blockInfo", blockInfo,
			"lockQC", x.lockQuorumCert.ProposedBlockInfo)
		return false, nil
	}

	return true, nil
}

// isExtendingFromAncestor checks if current block extends from ancestor
func (x *XDPoS_v2) isExtendingFromAncestor(chain consensus.ChainReader, currentBlock *types.BlockInfo, ancestorBlock *types.BlockInfo) (bool, error) {
	blockNumDiff := int(currentBlock.Number.Int64() - ancestorBlock.Number.Int64())

	nextBlockHash := currentBlock.Hash
	for i := 0; i < blockNumDiff; i++ {
		parentBlock := chain.GetHeaderByHash(nextBlockHash)
		if parentBlock == nil {
			return false, fmt.Errorf("parent block not found: %s", nextBlockHash.Hex())
		}
		nextBlockHash = parentBlock.ParentHash
	}

	return nextBlockHash == ancestorBlock.Hash, nil
}

// hygieneVotePool cleans up old votes
func (x *XDPoS_v2) hygieneVotePool() {
	x.lock.RLock()
	round := x.currentRound
	x.lock.RUnlock()

	votePoolKeys := x.votePool.PoolObjKeysList()

	for _, k := range votePoolKeys {
		keyedRound, err := strconv.ParseInt(strings.Split(k, ":")[0], 10, 64)
		if err != nil {
			log.Error("[hygieneVotePool] Parse error", "error", err)
			continue
		}
		if keyedRound < int64(round)-PoolHygieneRound {
			log.Debug("[hygieneVotePool] Cleaning vote pool", "round", keyedRound, "currentRound", round)
			x.votePool.ClearByPoolKey(k)
		}
	}
}

// ReceivedVotes returns all received votes
func (x *XDPoS_v2) ReceivedVotes() map[string]map[common.Hash]utils.PoolObj {
	return x.votePool.Get()
}
