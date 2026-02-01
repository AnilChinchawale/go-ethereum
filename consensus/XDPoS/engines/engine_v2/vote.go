// Copyright (c) 2018 XDPoSChain
// XDPoS V2 vote handling

package engine_v2

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// sendVote creates and sends a vote for the given block
func (x *XDPoS_v2) sendVote(chainReader consensus.ChainReader, blockInfo *types.BlockInfo) error {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chainReader, nil, blockInfo.Hash)
	if err != nil {
		log.Error("getEpochSwitchInfo when sending Vote", "BlockInfoHash", blockInfo.Hash, "Error", err)
		return err
	}

	epochSwitchNumber := epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()
	gapNumber := epochSwitchNumber - epochSwitchNumber%x.config.Epoch - x.config.Gap
	// Prevent overflow
	if epochSwitchNumber-epochSwitchNumber%x.config.Epoch < x.config.Gap {
		gapNumber = 0
	}

	signedHash, err := x.signSignature(types.VoteSigHash(&types.VoteForSign{
		ProposedBlockInfo: blockInfo,
		GapNumber:         gapNumber,
	}))
	if err != nil {
		log.Error("signSignature when sending Vote", "BlockInfoHash", blockInfo.Hash, "Error", err)
		return err
	}

	x.highestVotedRound = x.currentRound
	voteMsg := &types.Vote{
		ProposedBlockInfo: blockInfo,
		Signature:         signedHash,
		GapNumber:         gapNumber,
	}

	err = x.voteHandler(chainReader, voteMsg)
	if err != nil {
		log.Error("sendVote error", "BlockInfoHash", blockInfo.Hash, "Error", err)
		return err
	}
	x.broadcastToBftChannel(voteMsg)
	return nil
}

// VerifyVoteMessage verifies an incoming vote message
func (x *XDPoS_v2) VerifyVoteMessage(chain consensus.ChainReader, vote *types.Vote) (bool, error) {
	if vote.ProposedBlockInfo.Round < x.currentRound {
		log.Debug("[VerifyVoteMessage] Disqualified vote message", "voteHash", vote.Hash(), "voteRound", vote.ProposedBlockInfo.Round, "currentRound", x.currentRound)
		return false, nil
	}

	snapshot, err := x.getSnapshot(chain, vote.GapNumber, true)
	if err != nil {
		log.Error("[VerifyVoteMessage] fail to get snapshot", "blockNum", vote.ProposedBlockInfo.Number, "blockHash", vote.ProposedBlockInfo.Hash, "voteHash", vote.Hash(), "error", err.Error())
		return false, err
	}

	verified, signer, err := x.verifyMsgSignature(types.VoteSigHash(&types.VoteForSign{
		ProposedBlockInfo: vote.ProposedBlockInfo,
		GapNumber:         vote.GapNumber,
	}), vote.Signature, snapshot.NextEpochCandidates)
	if err != nil {
		for i, mn := range snapshot.NextEpochCandidates {
			log.Warn("[VerifyVoteMessage] Master node", "index", i, "address", mn.Hex())
		}
		log.Warn("[VerifyVoteMessage] Error verifying vote", "votedBlockNum", vote.ProposedBlockInfo.Number.Uint64(), "votedBlockHash", vote.ProposedBlockInfo.Hash.Hex(), "voteHash", vote.Hash(), "error", err.Error())
		return false, err
	}
	vote.SetSigner(signer)

	return verified, nil
}

// VoteHandler is the consensus entry point for processing vote messages
func (x *XDPoS_v2) VoteHandler(chain consensus.ChainReader, voteMsg *types.Vote) error {
	x.lock.Lock()
	defer x.lock.Unlock()
	return x.voteHandler(chain, voteMsg)
}

func (x *XDPoS_v2) voteHandler(chain consensus.ChainReader, voteMsg *types.Vote) error {
	// Check round number
	if (voteMsg.ProposedBlockInfo.Round != x.currentRound) && (voteMsg.ProposedBlockInfo.Round != x.currentRound+1) {
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

	// Collect vote
	numberOfVotesInPool, pooledVotes := x.votePool.Add(voteMsg)
	log.Debug("[voteHandler] collect votes", "number", numberOfVotesInPool)

	// Process forensics asynchronously
	go x.ForensicsProcessor.DetectEquivocationInVotePool(voteMsg, x.votePool)
	go x.ForensicsProcessor.ProcessVoteEquivocation(chain, x, voteMsg)

	epochInfo, err := x.getEpochSwitchInfo(chain, nil, voteMsg.ProposedBlockInfo.Hash)
	if err != nil {
		return &utils.ErrIncomingMessageBlockNotFound{
			Type:                "vote",
			IncomingBlockHash:   voteMsg.ProposedBlockInfo.Hash,
			IncomingBlockNumber: voteMsg.ProposedBlockInfo.Number,
			Err:                 err,
		}
	}

	certThreshold := x.getCertThreshold()

	thresholdReached := float64(numberOfVotesInPool) >= float64(epochInfo.MasternodesLen)*certThreshold
	if thresholdReached {
		log.Info(fmt.Sprintf("[voteHandler] Vote pool threshold reached: %v, number of items: %v", thresholdReached, numberOfVotesInPool))

		// Check if the block already exists
		proposedBlockHeader := chain.GetHeaderByHash(voteMsg.ProposedBlockInfo.Hash)
		if proposedBlockHeader == nil {
			log.Info("[voteHandler] The proposed block does not exist yet, wait for next vote", "blockNum", voteMsg.ProposedBlockInfo.Number, "Hash", voteMsg.ProposedBlockInfo.Hash, "Round", voteMsg.ProposedBlockInfo.Round)
			return nil
		}

		err := x.VerifyBlockInfo(chain, voteMsg.ProposedBlockInfo, nil)
		if err != nil {
			return err
		}

		x.verifyVotes(chain, pooledVotes, proposedBlockHeader)

		err = x.onVotePoolThresholdReached(chain, pooledVotes, voteMsg, proposedBlockHeader)
		if err != nil {
			return err
		}

		elapsed := time.Since(x.votePoolCollectionTime)
		log.Info("[voteHandler] time cost from receive first vote under QC create", "elapsed", elapsed)
		x.votePoolCollectionTime = time.Time{}
	}

	return nil
}

// verifyVotes verifies all votes in the pool
func (x *XDPoS_v2) verifyVotes(chain consensus.ChainReader, votes map[common.Hash]utils.PoolObj, header *types.Header) {
	masternodes := x.GetMasternodes(chain, header)
	start := time.Now()
	emptySigner := common.Address{}

	// Filter out non-masternode signatures
	var wg sync.WaitGroup
	wg.Add(len(votes))
	for h, vote := range votes {
		go func(hash common.Hash, v *types.Vote) {
			defer wg.Done()
			signerAddress := v.GetSigner()
			if signerAddress != emptySigner {
				// Verify signer belongs to masternodes
				if len(masternodes) == 0 {
					log.Error("[verifyVotes] empty masternode list")
				}
				for _, mn := range masternodes {
					if mn == signerAddress {
						return
					}
				}
				// Signer not in masternodes, remove signer
				v.SetSigner(emptySigner)
				log.Debug("[verifyVotes] vote signer not in masternodes", "signer", signerAddress)
				return
			}

			signedVote := types.VoteSigHash(&types.VoteForSign{
				ProposedBlockInfo: v.ProposedBlockInfo,
				GapNumber:         v.GapNumber,
			})
			verified, masterNode, err := x.verifyMsgSignature(signedVote, v.Signature, masternodes)
			if err != nil {
				log.Warn("[verifyVotes] error verifying vote signature", "error", err.Error())
				return
			}

			if !verified {
				log.Warn("[verifyVotes] non-verified vote signature", "verified", verified)
				return
			}
			v.SetSigner(masterNode)
		}(h, vote.(*types.Vote))
	}
	wg.Wait()
	elapsed := time.Since(start)
	log.Debug("[verifyVotes] verify message signatures took", "elapsed", elapsed)
}

// onVotePoolThresholdReached is called when vote pool reaches threshold
func (x *XDPoS_v2) onVotePoolThresholdReached(chain consensus.ChainReader, pooledVotes map[common.Hash]utils.PoolObj, currentVoteMsg utils.PoolObj, proposedBlockHeader *types.Header) error {
	// Filter to only valid signatures
	var validSignatures []types.Signature
	emptySigner := common.Address{}
	for _, vote := range pooledVotes {
		if vote.GetSigner() != emptySigner {
			validSignatures = append(validSignatures, vote.(*types.Vote).Signature)
		}
	}

	epochInfo, err := x.getEpochSwitchInfo(chain, nil, currentVoteMsg.(*types.Vote).ProposedBlockInfo.Hash)
	if err != nil {
		log.Error("[voteHandler] Error getting epoch switch Info", "error", err)
		return errors.New("fail on voteHandler due to failure in getting epoch switch info")
	}

	// Check if we have enough valid votes
	certThreshold := x.getCertThreshold()

	if float64(len(validSignatures)) < float64(epochInfo.MasternodesLen)*certThreshold {
		log.Warn("[onVotePoolThresholdReached] Not enough valid signatures to generate QC", "ValidVotes", len(validSignatures), "TotalVotes", len(pooledVotes))
		return nil
	}

	// Generate QC
	quorumCert := &types.QuorumCert{
		ProposedBlockInfo: currentVoteMsg.(*types.Vote).ProposedBlockInfo,
		Signatures:        validSignatures,
		GapNumber:         currentVoteMsg.(*types.Vote).GapNumber,
	}

	err = x.processQC(chain, quorumCert)
	if err != nil {
		log.Error("Error processing QC in Vote handler", "err", err)
		return err
	}

	log.Info("Successfully processed the vote and produced QC!", "QcRound", quorumCert.ProposedBlockInfo.Round, "QcNumOfSig", len(quorumCert.Signatures), "QcHash", quorumCert.ProposedBlockInfo.Hash, "QcNumber", quorumCert.ProposedBlockInfo.Number.Uint64())
	return nil
}

// verifyVotingRule checks if node is eligible to vote for the received block
func (x *XDPoS_v2) verifyVotingRule(blockChainReader consensus.ChainReader, blockInfo *types.BlockInfo, quorumCert *types.QuorumCert) (bool, error) {
	// Make sure this node has not voted for this round
	if x.currentRound <= x.highestVotedRound {
		log.Info("Failed voting rule verification, currentRound <= highestVotedRound", "x.currentRound", x.currentRound, "x.highestVotedRound", x.highestVotedRound)
		return false, nil
	}

	// HotStuff Voting rule:
	// header's round == local current round, AND (one of the following two:)
	// header's block extends lockQuorumCert's ProposedBlockInfo, OR
	// header's QC's ProposedBlockInfo.Round > lockQuorumCert's ProposedBlockInfo.Round

	if blockInfo.Round != x.currentRound {
		log.Info("Failed voting rule verification, blockRound != currentRound", "x.currentRound", x.currentRound, "blockInfo.Round", blockInfo.Round)
		return false, nil
	}

	// First V2 block or no lock QC yet
	if x.lockQuorumCert == nil {
		return true, nil
	}

	if quorumCert.ProposedBlockInfo.Round > x.lockQuorumCert.ProposedBlockInfo.Round {
		return true, nil
	}

	isExtended, err := x.isExtendingFromAncestor(blockChainReader, blockInfo, x.lockQuorumCert.ProposedBlockInfo)
	if err != nil {
		log.Error("Failed voting rule verification, error on isExtendingFromAncestor", "err", err, "blockInfo", blockInfo, "lockQC", x.lockQuorumCert.ProposedBlockInfo)
		return false, err
	}

	if !isExtended {
		log.Warn("Failed voting rule verification, block is not extended from ancestor", "blockInfo", blockInfo, "lockQC", x.lockQuorumCert.ProposedBlockInfo)
		return false, nil
	}

	return true, nil
}

// isExtendingFromAncestor checks if currentBlock extends from ancestorBlock
func (x *XDPoS_v2) isExtendingFromAncestor(blockChainReader consensus.ChainReader, currentBlock *types.BlockInfo, ancestorBlock *types.BlockInfo) (bool, error) {
	blockNumDiff := int(big.NewInt(0).Sub(currentBlock.Number, ancestorBlock.Number).Int64())

	nextBlockHash := currentBlock.Hash
	for i := 0; i < blockNumDiff; i++ {
		parentBlock := blockChainReader.GetHeaderByHash(nextBlockHash)
		if parentBlock == nil {
			return false, fmt.Errorf("could not find parent block when checking whether currentBlock %v with hash %v is extending from ancestorBlock %v", currentBlock.Number, currentBlock.Hash, ancestorBlock.Number)
		}
		nextBlockHash = parentBlock.ParentHash
		log.Debug("[isExtendingFromAncestor] Found parent block", "CurrentBlockHash", currentBlock.Hash, "ParentHash", nextBlockHash)
	}

	if nextBlockHash == ancestorBlock.Hash {
		return true, nil
	}
	return false, nil
}

// ProposedBlockHandler handles an incoming proposed block
func (x *XDPoS_v2) ProposedBlockHandler(chain consensus.ChainReader, blockHeader *types.Header) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	// Get QC and Round from Extra
	quorumCert, round, _, err := x.getExtraFields(blockHeader)
	if err != nil {
		return err
	}

	// Generate blockInfo
	blockInfo := &types.BlockInfo{
		Hash:   blockHeader.Hash(),
		Round:  round,
		Number: blockHeader.Number,
	}

	err = x.processQC(chain, quorumCert)
	if err != nil {
		log.Error("[ProposedBlockHandler] Fail to processQC", "QC round", quorumCert.ProposedBlockInfo.Round, "QC hash", quorumCert.ProposedBlockInfo.Hash)
		return err
	}

	allow := x.allowedToSend(chain, blockHeader, "vote")
	if !allow {
		return nil
	}

	verified, err := x.verifyVotingRule(chain, blockInfo, quorumCert)
	if err != nil {
		return err
	}
	if verified {
		return x.sendVote(chain, blockInfo)
	}

	return nil
}
