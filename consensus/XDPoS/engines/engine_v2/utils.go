// Copyright (c) 2024 XDC Network
// Utility functions for XDPoS 2.0

package engine_v2

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

// signSignature signs a hash with the node's private key
func (x *XDPoS_v2) signSignature(signingHash common.Hash) (types.Signature, error) {
	x.signLock.RLock()
	signer, signFn := x.signer, x.signFn
	x.signLock.RUnlock()

	if signFn == nil {
		return nil, errors.New("signFn is nil")
	}

	signedHash, err := signFn(accounts.Account{Address: signer}, signingHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("error signing hash: %v", err)
	}
	return signedHash, nil
}

// verifyMsgSignature verifies a signature against a list of masternodes
func (x *XDPoS_v2) verifyMsgSignature(signedHashToBeVerified common.Hash, signature types.Signature, masternodes []common.Address) (bool, common.Address, error) {
	var signerAddress common.Address

	if len(masternodes) == 0 {
		return false, signerAddress, errors.New("empty masternode list")
	}

	// Recover public key
	pubkey, err := crypto.Ecrecover(signedHashToBeVerified.Bytes(), signature)
	if err != nil {
		return false, signerAddress, fmt.Errorf("ecrecover error: %v", err)
	}

	copy(signerAddress[:], crypto.Keccak256(pubkey[1:])[12:])

	// Check if signer is in masternode list
	for _, mn := range masternodes {
		if mn == signerAddress {
			return true, signerAddress, nil
		}
	}

	log.Warn("[verifyMsgSignature] Signer not in masternode list",
		"signer", signerAddress,
		"masternodes", len(masternodes))
	return false, signerAddress, nil
}

// RecoverUniqueSigners recovers unique signers from a list of signatures
func RecoverUniqueSigners(signedHash common.Hash, signatureList []types.Signature) ([]types.Signature, []types.Signature, error) {
	if signedHash == (common.Hash{}) {
		return nil, nil, errors.New("signedHash cannot be empty")
	}
	if len(signatureList) == 0 {
		return []types.Signature{}, []types.Signature{}, nil
	}

	type Message struct {
		pubkey common.Address
		sig    types.Signature
	}

	result := make(chan Message, len(signatureList))
	errCh := make(chan error, len(signatureList))
	var wg sync.WaitGroup
	wg.Add(len(signatureList))

	for _, signature := range signatureList {
		go func(sig types.Signature) {
			defer wg.Done()
			pubkey, err := crypto.Ecrecover(signedHash.Bytes(), sig)
			if err != nil {
				log.Error("[RecoverUniqueSigners] ecrecover error",
					"error", err,
					"signature", common.Bytes2Hex(sig))
				errCh <- err
				return
			}
			var signerAddress common.Address
			copy(signerAddress[:], crypto.Keccak256(pubkey[1:])[12:])
			result <- Message{pubkey: signerAddress, sig: sig}
		}(signature)
	}
	wg.Wait()
	close(result)
	close(errCh)

	if len(errCh) > 0 {
		return nil, nil, <-errCh
	}

	keys := make(map[string]struct{})
	uniqueSigners := make([]types.Signature, 0, len(result))
	duplicates := make([]types.Signature, 0)

	for r := range result {
		pubkeyHex := r.pubkey.Hex()
		if _, ok := keys[pubkeyHex]; !ok {
			keys[pubkeyHex] = struct{}{}
			uniqueSigners = append(uniqueSigners, r.sig)
		} else {
			log.Warn("[RecoverUniqueSigners] Duplicate signature found",
				"pubkey", pubkeyHex,
				"signedHash", signedHash.Hex())
			duplicates = append(duplicates, r.sig)
		}
	}

	return uniqueSigners, duplicates, nil
}

// verifyQC verifies a quorum certificate
func (x *XDPoS_v2) verifyQC(chain consensus.ChainReader, quorumCert *types.QuorumCert, parentHeader *types.Header) error {
	if quorumCert == nil {
		log.Warn("[verifyQC] QC is nil")
		return utils.ErrInvalidQC
	}

	// Get epoch info
	epochInfo, err := x.getEpochSwitchInfo(chain, parentHeader, quorumCert.ProposedBlockInfo.Hash)
	if err != nil {
		log.Error("[verifyQC] Failed to get epoch info", "error", err)
		return errors.New("failed to get epoch switch info for QC verification")
	}

	// Verify signature hash
	signedVoteObj := types.VoteSigHash(&types.VoteForSign{
		ProposedBlockInfo: quorumCert.ProposedBlockInfo,
		GapNumber:         quorumCert.GapNumber,
	})

	// Recover unique signers
	signatures, duplicates, err := RecoverUniqueSigners(signedVoteObj, quorumCert.Signatures)
	if err != nil {
		log.Error("[verifyQC] Failed to recover signers",
			"blockNum", quorumCert.ProposedBlockInfo.Number,
			"error", err)
		return err
	}

	if len(duplicates) > 0 {
		for _, d := range duplicates {
			log.Warn("[verifyQC] Duplicate signature in QC",
				"signature", common.Bytes2Hex(d))
		}
	}

	// Check threshold
	qcRound := quorumCert.ProposedBlockInfo.Round
	certThreshold := x.config.V2.CurrentConfig.CertThreshold
	if qcRound > 0 && (signatures == nil || float64(len(signatures)) < float64(epochInfo.MasternodesLen)*certThreshold) {
		log.Warn("[verifyQC] Not enough signatures",
			"signatures", len(signatures),
			"threshold", float64(epochInfo.MasternodesLen)*certThreshold)
		return utils.ErrInvalidQCSignatures
	}

	// Verify each signature
	var wg sync.WaitGroup
	var mutex sync.Mutex
	var verifyError error

	wg.Add(len(signatures))
	for _, sig := range signatures {
		go func(signature types.Signature) {
			defer wg.Done()
			verified, _, err := x.verifyMsgSignature(signedVoteObj, signature, epochInfo.Masternodes)
			if err != nil {
				mutex.Lock()
				if verifyError == nil {
					log.Error("[verifyQC] Signature verification error", "error", err)
					verifyError = errors.New("QC signature verification error")
				}
				mutex.Unlock()
				return
			}
			if !verified {
				mutex.Lock()
				if verifyError == nil {
					log.Warn("[verifyQC] Signature not verified")
					verifyError = errors.New("QC signature verification failed")
				}
				mutex.Unlock()
			}
		}(sig)
	}
	wg.Wait()

	if verifyError != nil {
		return verifyError
	}

	// Verify gap number
	epochSwitchNumber := epochInfo.EpochSwitchBlockInfo.Number.Uint64()
	gapNumber := epochSwitchNumber - epochSwitchNumber%x.config.Epoch
	if gapNumber > x.config.Gap {
		gapNumber -= x.config.Gap
	} else {
		gapNumber = 0
	}
	if gapNumber != quorumCert.GapNumber {
		log.Error("[verifyQC] Gap number mismatch",
			"expected", gapNumber,
			"got", quorumCert.GapNumber)
		return fmt.Errorf("gap number mismatch: expected %d, got %d", gapNumber, quorumCert.GapNumber)
	}

	// Verify block info
	return x.VerifyBlockInfo(chain, quorumCert.ProposedBlockInfo, parentHeader)
}

// processQC processes a quorum certificate
func (x *XDPoS_v2) processQC(chain consensus.ChainReader, incomingQuorumCert *types.QuorumCert) error {
	log.Trace("[processQC] Processing", "highestQC", x.highestQuorumCert)

	// Update highest QC
	if incomingQuorumCert.ProposedBlockInfo.Round > x.highestQuorumCert.ProposedBlockInfo.Round {
		log.Debug("[processQC] Updating highest QC",
			"blockNum", incomingQuorumCert.ProposedBlockInfo.Number,
			"round", incomingQuorumCert.ProposedBlockInfo.Round,
			"hash", incomingQuorumCert.ProposedBlockInfo.Hash)
		x.highestQuorumCert = incomingQuorumCert
	}

	// Get proposed block header
	proposedBlockHeader := chain.GetHeaderByHash(incomingQuorumCert.ProposedBlockInfo.Hash)
	if proposedBlockHeader == nil {
		log.Error("[processQC] Block not found",
			"hash", incomingQuorumCert.ProposedBlockInfo.Hash,
			"number", incomingQuorumCert.ProposedBlockInfo.Number)
		return fmt.Errorf("block not found: %s", incomingQuorumCert.ProposedBlockInfo.Hash.Hex())
	}

	// Update lock QC for blocks after V2 switch
	if proposedBlockHeader.Number.Cmp(x.config.V2.SwitchBlock) > 0 {
		proposedBlockQuorumCert, round, _, err := x.getExtraFields(proposedBlockHeader)
		if err != nil {
			return err
		}
		if x.lockQuorumCert == nil || (proposedBlockQuorumCert != nil && proposedBlockQuorumCert.ProposedBlockInfo.Round > x.lockQuorumCert.ProposedBlockInfo.Round) {
			x.lockQuorumCert = proposedBlockQuorumCert
		}

		// Commit blocks (3-chain rule)
		_, err = x.commitBlocks(chain, proposedBlockHeader, &round, incomingQuorumCert)
		if err != nil {
			log.Error("[processQC] commitBlocks error", "round", round)
			return err
		}
	}

	// Advance round
	if incomingQuorumCert.ProposedBlockInfo.Round >= x.currentRound {
		x.setNewRound(chain, incomingQuorumCert.ProposedBlockInfo.Round+1)
	}

	log.Trace("[processQC] Complete", "highestQC", x.highestQuorumCert)
	return nil
}

// commitBlocks implements the 3-chain commit rule
func (x *XDPoS_v2) commitBlocks(chain consensus.ChainReader, proposedBlockHeader *types.Header, proposedBlockRound *types.Round, incomingQc *types.QuorumCert) (bool, error) {
	// Skip if too close to V2 switch
	if proposedBlockHeader.Number.Int64()-2 <= x.config.V2.SwitchBlock.Int64() {
		return false, nil
	}

	// Get parent block
	parentBlock := chain.GetHeaderByHash(proposedBlockHeader.ParentHash)
	if parentBlock == nil {
		log.Error("[commitBlocks] Parent not found", "hash", proposedBlockHeader.ParentHash)
		return false, fmt.Errorf("parent not found: %s", proposedBlockHeader.ParentHash.Hex())
	}

	_, round, _, err := x.getExtraFields(parentBlock)
	if err != nil {
		log.Error("[commitBlocks] Failed to decode parent extra", "hash", proposedBlockHeader.Hash())
		return false, err
	}

	// Check if parent round is continuous
	if *proposedBlockRound-1 != round {
		log.Info("[commitBlocks] Parent round not continuous",
			"proposedRound", *proposedBlockRound,
			"parentRound", round)
		return false, nil
	}

	// Get grandparent block
	grandParentBlock := chain.GetHeaderByHash(parentBlock.ParentHash)
	if grandParentBlock == nil {
		log.Error("[commitBlocks] Grandparent not found", "hash", parentBlock.ParentHash)
		return false, fmt.Errorf("grandparent not found: %s", parentBlock.ParentHash.Hex())
	}

	_, round, _, err = x.getExtraFields(grandParentBlock)
	if err != nil {
		log.Error("[commitBlocks] Failed to decode grandparent extra", "hash", parentBlock.Hash())
		return false, err
	}

	// Check if grandparent round is continuous
	if *proposedBlockRound-2 != round {
		log.Info("[commitBlocks] Grandparent round not continuous",
			"proposedRound", *proposedBlockRound,
			"grandparentRound", round)
		return false, nil
	}

	// Check if already committed
	if x.highestCommitBlock != nil &&
		(x.highestCommitBlock.Round >= round || x.highestCommitBlock.Number.Cmp(grandParentBlock.Number) >= 0) {
		return false, nil
	}

	// Commit grandparent
	x.highestCommitBlock = &types.BlockInfo{
		Number: grandParentBlock.Number,
		Hash:   grandParentBlock.Hash(),
		Round:  round,
	}
	log.Info("Block committed (3-chain rule)",
		"number", x.highestCommitBlock.Number,
		"round", x.highestCommitBlock.Round,
		"hash", x.highestCommitBlock.Hash)

	return true, nil
}

// VerifyBlockInfo verifies block info against the chain
func (x *XDPoS_v2) VerifyBlockInfo(chain consensus.ChainReader, blockInfo *types.BlockInfo, blockHeader *types.Header) error {
	if blockHeader == nil {
		blockHeader = chain.GetHeaderByHash(blockInfo.Hash)
		if blockHeader == nil {
			log.Warn("[VerifyBlockInfo] Header not found",
				"hash", blockInfo.Hash,
				"number", blockInfo.Number)
			return fmt.Errorf("header not found: %s", blockInfo.Hash.Hex())
		}
	} else {
		if blockHeader.Hash() != blockInfo.Hash {
			log.Warn("[VerifyBlockInfo] Hash mismatch",
				"blockInfoHash", blockInfo.Hash,
				"headerHash", blockHeader.Hash())
			return errors.New("header hash mismatch")
		}
	}

	// Verify block number
	if blockHeader.Number.Cmp(blockInfo.Number) != 0 {
		log.Warn("[VerifyBlockInfo] Number mismatch",
			"blockInfoNumber", blockInfo.Number,
			"headerNumber", blockHeader.Number)
		return fmt.Errorf("block number mismatch")
	}

	// V2 switch block has round 0
	if blockInfo.Number.Cmp(x.config.V2.SwitchBlock) == 0 {
		if blockInfo.Round != 0 {
			log.Error("[VerifyBlockInfo] Switch block round not 0",
				"round", blockInfo.Round)
			return errors.New("switch block round must be 0")
		}
		return nil
	}

	// Verify round
	_, round, _, err := x.getExtraFields(blockHeader)
	if err != nil {
		log.Error("[VerifyBlockInfo] Failed to decode extra", "error", err)
		return err
	}
	if round != blockInfo.Round {
		log.Warn("[VerifyBlockInfo] Round mismatch",
			"blockInfoRound", blockInfo.Round,
			"headerRound", round)
		return fmt.Errorf("round mismatch: expected %d, got %d", blockInfo.Round, round)
	}

	return nil
}

// VerifySyncInfoMessage verifies a sync info message
func (x *XDPoS_v2) VerifySyncInfoMessage(chain consensus.ChainReader, syncInfo *types.SyncInfo) (bool, error) {
	// Check if we need to update
	if x.highestQuorumCert.ProposedBlockInfo.Round >= syncInfo.HighestQuorumCert.ProposedBlockInfo.Round &&
		x.highestTimeoutCert.Round >= syncInfo.HighestTimeoutCert.Round {
		log.Debug("[VerifySyncInfoMessage] SyncInfo not newer",
			"localQCRound", x.highestQuorumCert.ProposedBlockInfo.Round,
			"incomingQCRound", syncInfo.HighestQuorumCert.ProposedBlockInfo.Round,
			"localTCRound", x.highestTimeoutCert.Round,
			"incomingTCRound", syncInfo.HighestTimeoutCert.Round)
		return false, nil
	}

	// Verify QC
	if err := x.verifyQC(chain, syncInfo.HighestQuorumCert, nil); err != nil {
		log.Warn("[VerifySyncInfoMessage] QC verification failed",
			"blockNum", syncInfo.HighestQuorumCert.ProposedBlockInfo.Number,
			"error", err)
		return false, err
	}

	// Verify TC
	if err := x.verifyTC(chain, syncInfo.HighestTimeoutCert); err != nil {
		log.Warn("[VerifySyncInfoMessage] TC verification failed",
			"round", syncInfo.HighestTimeoutCert.Round,
			"error", err)
		return false, err
	}

	return true, nil
}

// SyncInfoHandler processes a sync info message
func (x *XDPoS_v2) SyncInfoHandler(chain consensus.ChainReader, syncInfo *types.SyncInfo) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	// Process QC
	if err := x.processQC(chain, syncInfo.HighestQuorumCert); err != nil {
		return err
	}

	// Process TC
	return x.processTC(chain, syncInfo.HighestTimeoutCert)
}

// ProposedBlockHandler processes a proposed block
func (x *XDPoS_v2) ProposedBlockHandler(chain consensus.ChainReader, blockHeader *types.Header) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	// Get QC and round from header
	quorumCert, round, _, err := x.getExtraFields(blockHeader)
	if err != nil {
		return err
	}

	// Generate block info
	blockInfo := &types.BlockInfo{
		Hash:   blockHeader.Hash(),
		Round:  round,
		Number: blockHeader.Number,
	}

	// Process QC
	if err := x.processQC(chain, quorumCert); err != nil {
		log.Error("[ProposedBlockHandler] processQC error",
			"round", quorumCert.ProposedBlockInfo.Round,
			"hash", quorumCert.ProposedBlockInfo.Hash)
		return err
	}

	// Check if we can vote
	if !x.allowedToSend(chain, blockHeader, "vote") {
		return nil
	}

	// Verify voting rule
	verified, err := x.verifyVotingRule(chain, blockInfo, quorumCert)
	if err != nil {
		return err
	}
	if verified {
		return x.sendVote(chain, blockInfo)
	}

	return nil
}

// GetRoundNumber returns the round number from a header
func (x *XDPoS_v2) GetRoundNumber(header *types.Header) (types.Round, error) {
	if header.Number.Cmp(x.config.V2.SwitchBlock) <= 0 {
		return types.Round(0), nil
	}
	var decodedExtra types.ExtraFields_v2
	if err := DecodeExtraFields(header.Extra, &decodedExtra); err != nil {
		return types.Round(0), err
	}
	return decodedExtra.Round, nil
}

// GetSignersFromSnapshot returns signers from the snapshot
func (x *XDPoS_v2) GetSignersFromSnapshot(chain consensus.ChainReader, header *types.Header) ([]common.Address, error) {
	snap, err := x.getSnapshot(chain, header.Number.Uint64(), false)
	if err != nil {
		return nil, err
	}
	return snap.NextEpochCandidates, nil
}
