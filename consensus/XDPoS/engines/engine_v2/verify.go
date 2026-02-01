// Copyright (c) 2018 XDPoSChain
// XDPoS V2 verification functions

package engine_v2

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// verifyQC verifies a quorum certificate
func (x *XDPoS_v2) verifyQC(blockChainReader consensus.ChainReader, quorumCert *types.QuorumCert, parentHeader *types.Header) error {
	if quorumCert == nil {
		log.Warn("[verifyQC] QC is Nil")
		return utils.ErrInvalidQC
	}

	epochInfo, err := x.getEpochSwitchInfo(blockChainReader, parentHeader, quorumCert.ProposedBlockInfo.Hash)
	if err != nil {
		log.Error("[verifyQC] Error getting epoch switch Info to verify QC", "Error", err)
		return errors.New("fail to verify QC due to failure in getting epoch switch info")
	}

	signatures, duplicates := UniqueSignatures(quorumCert.Signatures)
	if len(duplicates) != 0 {
		for _, d := range duplicates {
			log.Warn("[verifyQC] duplicated signature in QC", "duplicate", common.Bytes2Hex(d))
		}
	}

	qcRound := quorumCert.ProposedBlockInfo.Round
	certThreshold := x.getCertThreshold()

	if (qcRound > 0) && (signatures == nil || float64(len(signatures)) < float64(epochInfo.MasternodesLen)*certThreshold) {
		log.Warn("[verifyQC] Invalid QC Signature count", "QCNumber", quorumCert.ProposedBlockInfo.Number, "LenSignatures", len(signatures), "CertThreshold", float64(epochInfo.MasternodesLen)*certThreshold)
		return utils.ErrInvalidQCSignatures
	}

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(len(signatures))
	var haveError error

	for _, signature := range signatures {
		go func(sig types.Signature) {
			defer wg.Done()
			verified, _, err := x.verifyMsgSignature(types.VoteSigHash(&types.VoteForSign{
				ProposedBlockInfo: quorumCert.ProposedBlockInfo,
				GapNumber:         quorumCert.GapNumber,
			}), sig, epochInfo.Masternodes)
			if err != nil {
				log.Error("[verifyQC] Error verifying QC message signatures", "Error", err)
				haveError = errors.New("error while verifying QC message signatures")
				return
			}
			if !verified {
				log.Warn("[verifyQC] Signature not verified doing QC verification", "QC", quorumCert)
				haveError = errors.New("fail to verify QC due to signature mis-match")
				return
			}
		}(signature)
	}
	wg.Wait()

	elapsed := time.Since(start)
	log.Debug("[verifyQC] time verify message signatures of qc", "elapsed", elapsed)

	if haveError != nil {
		return haveError
	}

	// Verify gap number
	epochSwitchNumber := epochInfo.EpochSwitchBlockInfo.Number.Uint64()
	gapNumber := epochSwitchNumber - epochSwitchNumber%x.config.Epoch - x.config.Gap
	// Prevent overflow
	if epochSwitchNumber-epochSwitchNumber%x.config.Epoch < x.config.Gap {
		gapNumber = 0
	}
	if gapNumber != quorumCert.GapNumber {
		log.Error("[verifyQC] QC gap number mismatch", "epochSwitchNumber", epochSwitchNumber, "BlockNum", quorumCert.ProposedBlockInfo.Number, "BlockInfoHash", quorumCert.ProposedBlockInfo.Hash, "Gap", quorumCert.GapNumber, "GapShouldBe", gapNumber)
		return fmt.Errorf("gap number mismatch QC Gap %d, shouldBe %d", quorumCert.GapNumber, gapNumber)
	}

	return x.VerifyBlockInfo(blockChainReader, quorumCert.ProposedBlockInfo, parentHeader)
}

// VerifyBlockInfo verifies block info against the local chain
func (x *XDPoS_v2) VerifyBlockInfo(blockChainReader consensus.ChainReader, blockInfo *types.BlockInfo, blockHeader *types.Header) error {
	if blockHeader == nil {
		blockHeader = blockChainReader.GetHeaderByHash(blockInfo.Hash)
		if blockHeader == nil {
			log.Warn("[VerifyBlockInfo] No such header in the chain", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockInfoNum", blockInfo.Number, "BlockInfoRound", blockInfo.Round, "currentHeaderNum", blockChainReader.CurrentHeader().Number)
			return fmt.Errorf("[VerifyBlockInfo] header doesn't exist for the received blockInfo at hash: %v", blockInfo.Hash.Hex())
		}
	} else {
		// If blockHeader present, it should be consistent with blockInfo
		if blockHeader.Hash() != blockInfo.Hash {
			log.Warn("[VerifyBlockInfo] BlockHeader and blockInfo mismatch", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockHeaderHash", blockHeader.Hash())
			return errors.New("[VerifyBlockInfo] Provided blockheader does not match what's in the blockInfo")
		}
	}

	if blockHeader.Number.Cmp(blockInfo.Number) != 0 {
		log.Warn("[VerifyBlockInfo] Block Number mismatch", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockInfoNum", blockInfo.Number, "BlockInfoRound", blockInfo.Round, "blockHeaderNum", blockHeader.Number)
		return fmt.Errorf("[VerifyBlockInfo] chain header number does not match for the received blockInfo at hash: %v", blockInfo.Hash.Hex())
	}

	// Switch block is a v1 block, there is no valid extra to decode
	if blockInfo.Number.Cmp(x.config.V2.SwitchBlock) == 0 {
		if blockInfo.Round != 0 {
			log.Error("[VerifyBlockInfo] Switch block round is not 0", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockInfoNum", blockInfo.Number, "BlockInfoRound", blockInfo.Round, "blockHeaderNum", blockHeader.Number)
			return errors.New("[VerifyBlockInfo] switch block round have to be 0")
		}
		return nil
	}

	// Check round
	_, round, _, err := x.getExtraFields(blockHeader)
	if err != nil {
		log.Error("[VerifyBlockInfo] Fail to decode extra field", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockInfoNum", blockInfo.Number, "BlockInfoRound", blockInfo.Round, "blockHeaderNum", blockHeader.Number)
		return err
	}
	if round != blockInfo.Round {
		log.Warn("[VerifyBlockInfo] Block extra round mismatch with blockInfo", "BlockInfoHash", blockInfo.Hash.Hex(), "BlockInfoNum", blockInfo.Number, "BlockInfoRound", blockInfo.Round, "blockHeaderNum", blockHeader.Number, "blockRound", round)
		return fmt.Errorf("[VerifyBlockInfo] chain block's round does not match from blockInfo at hash: %v and block round: %v, blockInfo Round: %v", blockInfo.Hash.Hex(), round, blockInfo.Round)
	}

	return nil
}

// VerifySyncInfoMessage verifies a sync info message
func (x *XDPoS_v2) VerifySyncInfoMessage(chain consensus.ChainReader, syncInfo *types.SyncInfo) (bool, error) {
	// Check QC and TC against highest QC TC. Skip if none of them need to be updated
	if (x.highestQuorumCert.ProposedBlockInfo.Round >= syncInfo.HighestQuorumCert.ProposedBlockInfo.Round) && (x.highestTimeoutCert.Round >= syncInfo.HighestTimeoutCert.Round) {
		log.Debug("[VerifySyncInfoMessage] Round from incoming syncInfo message is no longer qualified", "Highest QC Round", x.highestQuorumCert.ProposedBlockInfo.Round, "Incoming SyncInfo QC Round", syncInfo.HighestQuorumCert.ProposedBlockInfo.Round, "highestTimeoutCert Round", x.highestTimeoutCert.Round, "Incoming syncInfo TC Round", syncInfo.HighestTimeoutCert.Round)
		return false, nil
	}

	err := x.verifyQC(chain, syncInfo.HighestQuorumCert, nil)
	if err != nil {
		log.Warn("[VerifySyncInfoMessage] SyncInfo message verification failed due to QC", "blockNum", syncInfo.HighestQuorumCert.ProposedBlockInfo.Number, "round", syncInfo.HighestQuorumCert.ProposedBlockInfo.Round, "error", err)
		return false, err
	}

	err = x.verifyTC(chain, syncInfo.HighestTimeoutCert)
	if err != nil {
		log.Warn("[VerifySyncInfoMessage] SyncInfo message verification failed due to TC", "gapNum", syncInfo.HighestTimeoutCert.GapNumber, "round", syncInfo.HighestTimeoutCert.Round, "error", err)
		return false, err
	}

	return true, nil
}

// SyncInfoHandler handles incoming sync info messages
func (x *XDPoS_v2) SyncInfoHandler(chain consensus.ChainReader, syncInfo *types.SyncInfo) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	err := x.processQC(chain, syncInfo.HighestQuorumCert)
	if err != nil {
		return err
	}
	return x.processTC(chain, syncInfo.HighestTimeoutCert)
}

// processQC processes a quorum certificate
func (x *XDPoS_v2) processQC(blockChainReader consensus.ChainReader, incomingQuorumCert *types.QuorumCert) error {
	log.Trace("[processQC][Before]", "HighQC", x.highestQuorumCert)

	// 1. Update HighestQC
	if incomingQuorumCert.ProposedBlockInfo.Round > x.highestQuorumCert.ProposedBlockInfo.Round {
		log.Debug("[processQC] update x.highestQuorumCert", "blockNum", incomingQuorumCert.ProposedBlockInfo.Number, "round", incomingQuorumCert.ProposedBlockInfo.Round, "hash", incomingQuorumCert.ProposedBlockInfo.Hash)
		x.highestQuorumCert = incomingQuorumCert
	}

	// 2. Get QC from header and update lockQuorumCert
	proposedBlockHeader := blockChainReader.GetHeaderByHash(incomingQuorumCert.ProposedBlockInfo.Hash)
	if proposedBlockHeader == nil {
		log.Error("[processQC] Block not found using the QC", "quorumCert.ProposedBlockInfo.Hash", incomingQuorumCert.ProposedBlockInfo.Hash, "incomingQuorumCert.ProposedBlockInfo.Number", incomingQuorumCert.ProposedBlockInfo.Number)
		return fmt.Errorf("block not found, number: %v, hash: %v", incomingQuorumCert.ProposedBlockInfo.Number, incomingQuorumCert.ProposedBlockInfo.Hash)
	}

	if proposedBlockHeader.Number.Cmp(x.config.V2.SwitchBlock) > 0 {
		// Extra field contains parent information
		proposedBlockQuorumCert, round, _, err := x.getExtraFields(proposedBlockHeader)
		if err != nil {
			return err
		}
		if x.lockQuorumCert == nil || proposedBlockQuorumCert.ProposedBlockInfo.Round > x.lockQuorumCert.ProposedBlockInfo.Round {
			x.lockQuorumCert = proposedBlockQuorumCert
		}

		proposedBlockRound := &round

		// 3. Update commit block info
		_, err = x.commitBlocks(blockChainReader, proposedBlockHeader, proposedBlockRound, incomingQuorumCert)
		if err != nil {
			log.Error("[processQC] Error while committing blocks", "proposedBlockRound", proposedBlockRound)
			return err
		}
	}

	// 4. Set new round
	if incomingQuorumCert.ProposedBlockInfo.Round >= x.currentRound {
		x.setNewRound(blockChainReader, incomingQuorumCert.ProposedBlockInfo.Round+1)
	}

	log.Trace("[processQC][After]", "HighQC", x.highestQuorumCert)
	return nil
}

// commitBlocks commits blocks based on 3-chain commit rule
func (x *XDPoS_v2) commitBlocks(blockChainReader consensus.ChainReader, proposedBlockHeader *types.Header, proposedBlockRound *types.Round, incomingQc *types.QuorumCert) (bool, error) {
	// XDPoS v1.0 switch to v2.0, skip commit
	switchBlock := x.config.V2.SwitchBlock
	if proposedBlockHeader.Number.Int64()-2 <= switchBlock.Int64() {
		return false, nil
	}

	// Find the last two parent blocks and check their rounds are continuous
	parentBlock := blockChainReader.GetHeaderByHash(proposedBlockHeader.ParentHash)

	_, round, _, err := x.getExtraFields(parentBlock)
	if err != nil {
		log.Error("Fail to execute first DecodeBytesExtraFields for committing block", "ProposedBlockHash", proposedBlockHeader.Hash())
		return false, err
	}
	if *proposedBlockRound-1 != round {
		log.Info("[commitBlocks] Rounds not continuous(parent) found when committing block", "proposedBlockRound", *proposedBlockRound, "decodedExtraField.Round", round, "proposedBlockHeaderHash", proposedBlockHeader.Hash())
		return false, nil
	}

	// If parent round is continuous, check grandparent
	grandParentBlock := blockChainReader.GetHeaderByHash(parentBlock.ParentHash)
	_, round, _, err = x.getExtraFields(grandParentBlock)
	if err != nil {
		log.Error("Fail to execute second DecodeBytesExtraFields for committing block", "parentBlockHash", parentBlock.Hash())
		return false, err
	}
	if *proposedBlockRound-2 != round {
		log.Info("[commitBlocks] Rounds not continuous(grand parent) found when committing block", "proposedBlockRound", *proposedBlockRound, "decodedExtraField.Round", round, "proposedBlockHeaderHash", proposedBlockHeader.Hash())
		return false, nil
	}

	if x.highestCommitBlock != nil && (x.highestCommitBlock.Round >= round || x.highestCommitBlock.Number.Cmp(grandParentBlock.Number) == 1) {
		return false, nil
	}

	// Process Commit
	x.highestCommitBlock = &types.BlockInfo{
		Number: grandParentBlock.Number,
		Hash:   grandParentBlock.Hash(),
		Round:  round,
	}
	log.Info("Successfully commit and confirm block from continuous 3 blocks", "num", x.highestCommitBlock.Number, "round", x.highestCommitBlock.Round, "hash", x.highestCommitBlock.Hash)

	// Perform forensics related operation
	headerQcToBeCommitted := []types.Header{*parentBlock, *proposedBlockHeader}
	go x.ForensicsProcessor.ForensicsMonitoring(blockChainReader, x, headerQcToBeCommitted, *incomingQc)

	return true, nil
}
