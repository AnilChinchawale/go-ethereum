// Copyright (c) 2018 XDPoSChain
// XDPoS V2 timeout handling

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

// VerifyTimeoutMessage verifies an incoming timeout message
func (x *XDPoS_v2) VerifyTimeoutMessage(chain consensus.ChainReader, timeoutMsg *types.Timeout) (bool, error) {
	if timeoutMsg.Round < x.currentRound {
		log.Debug("[VerifyTimeoutMessage] Disqualified timeout message", "timeoutHash", timeoutMsg.Hash(), "timeoutRound", timeoutMsg.Round, "currentRound", x.currentRound)
		return false, nil
	}

	snap, err := x.getSnapshot(chain, timeoutMsg.GapNumber, true)
	if err != nil || snap == nil {
		log.Error("[VerifyTimeoutMessage] Fail to get snapshot", "messageGapNumber", timeoutMsg.GapNumber, "err", err)
		return false, err
	}

	if len(snap.NextEpochCandidates) == 0 {
		log.Error("[VerifyTimeoutMessage] cannot find NextEpochCandidates from snapshot", "messageGapNumber", timeoutMsg.GapNumber)
		return false, errors.New("empty master node lists from snapshot")
	}

	verified, signer, err := x.verifyMsgSignature(types.TimeoutSigHash(&types.TimeoutForSign{
		Round:     timeoutMsg.Round,
		GapNumber: timeoutMsg.GapNumber,
	}), timeoutMsg.Signature, snap.NextEpochCandidates)

	if err != nil {
		log.Warn("[VerifyTimeoutMessage] cannot verify timeout signature", "err", err)
		return false, err
	}

	timeoutMsg.SetSigner(signer)
	return verified, nil
}

// TimeoutHandler is the entry point for handling timeout messages
func (x *XDPoS_v2) TimeoutHandler(blockChainReader consensus.ChainReader, timeout *types.Timeout) error {
	x.lock.Lock()
	defer x.lock.Unlock()
	return x.timeoutHandler(blockChainReader, timeout)
}

func (x *XDPoS_v2) timeoutHandler(blockChainReader consensus.ChainReader, timeout *types.Timeout) error {
	// Check round number
	if timeout.Round != x.currentRound {
		return &utils.ErrIncomingMessageRoundNotEqualCurrentRound{
			Type:          "timeout",
			IncomingRound: timeout.Round,
			CurrentRound:  x.currentRound,
		}
	}

	// Collect timeout, generate TC
	numberOfTimeoutsInPool, pooledTimeouts := x.timeoutPool.Add(timeout)
	log.Debug("[timeoutHandler] collect timeout", "number", numberOfTimeoutsInPool)

	epochInfo, err := x.getEpochSwitchInfo(blockChainReader, blockChainReader.CurrentHeader(), blockChainReader.CurrentHeader().Hash())
	if err != nil {
		log.Error("[timeoutHandler] Error getting epoch switch Info", "error", err)
		return fmt.Errorf("fail on timeoutHandler due to failure in getting epoch switch info, %s", err)
	}

	// Check threshold
	certThreshold := x.getCertThreshold()

	isThresholdReached := float64(numberOfTimeoutsInPool) >= float64(epochInfo.MasternodesLen)*certThreshold
	if isThresholdReached {
		log.Info(fmt.Sprintf("Timeout pool threshold reached: %v, number of items: %v", isThresholdReached, numberOfTimeoutsInPool))
		err := x.onTimeoutPoolThresholdReached(blockChainReader, pooledTimeouts, timeout, timeout.GapNumber)
		if err != nil {
			return err
		}
	}
	return nil
}

// onTimeoutPoolThresholdReached is called when timeout pool reaches threshold
func (x *XDPoS_v2) onTimeoutPoolThresholdReached(blockChainReader consensus.ChainReader, pooledTimeouts map[common.Hash]utils.PoolObj, currentTimeoutMsg utils.PoolObj, gapNumber uint64) error {
	signatures := []types.Signature{}
	for _, v := range pooledTimeouts {
		signatures = append(signatures, v.(*types.Timeout).Signature)
	}

	// Generate TC
	timeoutCert := &types.TimeoutCert{
		Round:      currentTimeoutMsg.(*types.Timeout).Round,
		Signatures: signatures,
		GapNumber:  gapNumber,
	}

	// Process TC
	err := x.processTC(blockChainReader, timeoutCert)
	if err != nil {
		log.Error("Error processing TC in Timeout handler", "TcRound", timeoutCert.Round, "NumberOfTcSig", len(timeoutCert.Signatures), "GapNumber", gapNumber, "Error", err)
		return err
	}

	// Generate and broadcast syncInfo
	syncInfo := x.getSyncInfo()
	x.broadcastToBftChannel(syncInfo)

	log.Info("Successfully processed timeout and produced TC & SyncInfo!", "QcRound", syncInfo.HighestQuorumCert.ProposedBlockInfo.Round, "QcBlockNum", syncInfo.HighestQuorumCert.ProposedBlockInfo.Number, "TcRound", timeoutCert.Round, "NumberOfTcSig", len(timeoutCert.Signatures))
	return nil
}

// verifyTC verifies a timeout certificate
func (x *XDPoS_v2) verifyTC(chain consensus.ChainReader, timeoutCert *types.TimeoutCert) error {
	if timeoutCert == nil || timeoutCert.Signatures == nil {
		log.Warn("[verifyTC] TC or TC signatures is Nil")
		return utils.ErrInvalidTC
	}

	snap, err := x.getSnapshot(chain, timeoutCert.GapNumber, true)
	if err != nil {
		log.Error("[verifyTC] Fail to get snapshot", "tcGapNumber", timeoutCert.GapNumber)
		return fmt.Errorf("[verifyTC] Unable to get snapshot, %s", err)
	}

	if snap == nil || len(snap.NextEpochCandidates) == 0 {
		log.Error("[verifyTC] Something wrong with snapshot", "messageGapNumber", timeoutCert.GapNumber, "snapshot", snap)
		return errors.New("empty master node lists from snapshot")
	}

	signatures, duplicates := UniqueSignatures(timeoutCert.Signatures)
	if len(duplicates) != 0 {
		for _, d := range duplicates {
			log.Warn("[verifyTC] duplicated signature in TC", "duplicate", common.Bytes2Hex(d))
		}
	}

	epochInfo, err := x.getTCEpochInfo(chain, timeoutCert)
	if err != nil {
		return err
	}

	certThreshold := x.getCertThreshold()

	if float64(len(signatures)) < float64(epochInfo.MasternodesLen)*certThreshold {
		log.Warn("[verifyTC] Invalid TC Signature count", "tcRound", timeoutCert.Round, "tcGapNumber", timeoutCert.GapNumber, "tcSignLen", len(timeoutCert.Signatures), "certThreshold", float64(epochInfo.MasternodesLen)*certThreshold)
		return utils.ErrInvalidTCSignatures
	}

	var wg sync.WaitGroup
	wg.Add(len(signatures))

	var mutex sync.Mutex
	var haveError error

	signedTimeoutObj := types.TimeoutSigHash(&types.TimeoutForSign{
		Round:     timeoutCert.Round,
		GapNumber: timeoutCert.GapNumber,
	})

	for _, signature := range signatures {
		go func(sig types.Signature) {
			defer wg.Done()
			verified, _, err := x.verifyMsgSignature(signedTimeoutObj, sig, snap.NextEpochCandidates)
			if err != nil || !verified {
				log.Error("[verifyTC] Error or verification failure", "signature", sig, "error", err)
				mutex.Lock()
				if haveError == nil {
					if err != nil {
						log.Error("[verifyTC] Error verifying TC message signatures", "tcRound", timeoutCert.Round, "tcGapNumber", timeoutCert.GapNumber, "tcSignLen", len(signatures), "error", err)
						haveError = fmt.Errorf("error while verifying TC message signatures, %s", err)
					} else {
						log.Warn("[verifyTC] Signature not verified", "tcRound", timeoutCert.Round, "tcGapNumber", timeoutCert.GapNumber, "tcSignLen", len(signatures))
						haveError = errors.New("fail to verify TC due to signature mis-match")
					}
				}
				mutex.Unlock()
			}
		}(signature)
	}
	wg.Wait()
	if haveError != nil {
		return haveError
	}
	return nil
}

// getTCEpochInfo gets epoch info for verifying TC
func (x *XDPoS_v2) getTCEpochInfo(chain consensus.ChainReader, timeoutCert *types.TimeoutCert) (*types.EpochSwitchInfo, error) {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, chain.CurrentHeader(), chain.CurrentHeader().Hash())
	if err != nil {
		log.Error("[getTCEpochInfo] Error getting epoch switch info", "error", err)
		return nil, fmt.Errorf("fail on getTCEpochInfo due to failure in getting epoch switch info, %s", err)
	}

	epochRound := epochSwitchInfo.EpochSwitchBlockInfo.Round
	tempTCEpoch := x.getSwitchEpoch() + uint64(epochRound)/x.config.Epoch

	epochBlockInfo := &types.BlockInfo{
		Hash:   epochSwitchInfo.EpochSwitchBlockInfo.Hash,
		Round:  epochRound,
		Number: epochSwitchInfo.EpochSwitchBlockInfo.Number,
	}
	log.Info("[getTCEpochInfo] Init epochInfo", "number", epochBlockInfo.Number, "round", epochRound, "tcRound", timeoutCert.Round, "tcEpoch", tempTCEpoch)

	for epochBlockInfo.Round > timeoutCert.Round {
		tempTCEpoch--
		epochBlockInfo, err = x.GetBlockByEpochNumber(chain, tempTCEpoch)
		if err != nil {
			log.Error("[getTCEpochInfo] Error getting epoch block info by tc round", "error", err)
			return nil, fmt.Errorf("fail on getTCEpochInfo due to failure in getting epoch block info tc round, %s", err)
		}
		log.Debug("[getTCEpochInfo] Loop to get right epochInfo", "number", epochBlockInfo.Number, "round", epochBlockInfo.Round, "tcRound", timeoutCert.Round, "tcEpoch", tempTCEpoch)
	}
	tcEpoch := tempTCEpoch
	log.Info("[getTCEpochInfo] Final TC epochInfo", "number", epochBlockInfo.Number, "round", epochBlockInfo.Round, "tcRound", timeoutCert.Round, "tcEpoch", tcEpoch)

	epochInfo, err := x.getEpochSwitchInfo(chain, nil, epochBlockInfo.Hash)
	if err != nil {
		log.Error("[getTCEpochInfo] Error getting epoch switch info", "error", err)
		return nil, fmt.Errorf("fail on getTCEpochInfo due to failure in getting epoch switch info, %s", err)
	}
	return epochInfo, nil
}

// processTC processes a timeout certificate
func (x *XDPoS_v2) processTC(blockChainReader consensus.ChainReader, timeoutCert *types.TimeoutCert) error {
	if timeoutCert.Round > x.highestTimeoutCert.Round {
		x.highestTimeoutCert = timeoutCert
	}
	if timeoutCert.Round >= x.currentRound {
		x.setNewRound(blockChainReader, timeoutCert.Round+1)
	}
	return nil
}

// sendTimeout generates and sends a timeout message
func (x *XDPoS_v2) sendTimeout(chain consensus.ChainReader) error {
	// Construct the gapNumber
	var gapNumber uint64
	currentBlockHeader := chain.CurrentHeader()

	isEpochSwitch, epochNum, err := x.isEpochSwitchAtRound(x.currentRound, currentBlockHeader)
	if err != nil {
		log.Error("[sendTimeout] Error checking if currentBlock is epoch switch", "currentRound", x.currentRound, "currentBlockNum", currentBlockHeader.Number, "currentBlockHash", currentBlockHeader.Hash(), "epochNum", epochNum)
		return err
	}

	if isEpochSwitch {
		// +1 because we expect a block that's child of currentHeader
		currentNumber := currentBlockHeader.Number.Uint64() + 1
		gapNumber = currentNumber - currentNumber%x.config.Epoch - x.config.Gap
		// Prevent overflow
		if currentNumber-currentNumber%x.config.Epoch < x.config.Gap {
			gapNumber = 0
		}
		log.Debug("[sendTimeout] is epoch switch when sending timeout message", "currentNumber", currentNumber, "gapNumber", gapNumber)
	} else {
		epochSwitchInfo, err := x.getEpochSwitchInfo(chain, currentBlockHeader, currentBlockHeader.Hash())
		if err != nil {
			log.Error("[sendTimeout] Error getting epoch switch info for non-epoch block", "currentRound", x.currentRound, "currentBlockNum", currentBlockHeader.Number, "currentBlockHash", currentBlockHeader.Hash(), "epochNum", epochNum)
			return err
		}
		gapNumber = epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64() - epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()%x.config.Epoch - x.config.Gap
		// Prevent overflow
		if epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()-epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()%x.config.Epoch < x.config.Gap {
			gapNumber = 0
		}
		log.Debug("[sendTimeout] non-epoch-switch block gapNumber", "epochSwitchBlockNum", epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64(), "gapNumber", gapNumber)
	}

	signedHash, err := x.signSignature(types.TimeoutSigHash(&types.TimeoutForSign{
		Round:     x.currentRound,
		GapNumber: gapNumber,
	}))
	if err != nil {
		log.Error("[sendTimeout] signSignature error", "Error", err, "round", x.currentRound, "gap", gapNumber)
		return err
	}

	timeoutMsg := &types.Timeout{
		Round:     x.currentRound,
		Signature: signedHash,
		GapNumber: gapNumber,
	}

	timeoutMsg.SetSigner(x.signer)
	log.Warn("[sendTimeout] Timeout message generated, ready to send!", "timeoutMsgRound", timeoutMsg.Round, "timeoutMsgGapNumber", timeoutMsg.GapNumber, "whosTurn", x.whosTurn)

	err = x.timeoutHandler(chain, timeoutMsg)
	if err != nil {
		log.Error("TimeoutHandler error", "TimeoutRound", timeoutMsg.Round, "Error", err)
		return err
	}
	x.broadcastToBftChannel(timeoutMsg)
	return nil
}

// OnCountdownTimeout is called by timer when countdown reaches zero
func (x *XDPoS_v2) OnCountdownTimeout(time time.Time, chain interface{}) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	// Check if we are in the masternode list
	allow := x.allowedToSend(chain.(consensus.ChainReader), chain.(consensus.ChainReader).CurrentHeader(), "timeout")
	if !allow {
		return nil
	}

	err := x.sendTimeout(chain.(consensus.ChainReader))
	if err != nil {
		log.Error("Error sending timeout message", "time", time, "err", err)
		return err
	}

	x.timeoutCount++

	// Check if we should send sync info
	timeoutSyncThreshold := x.getTimeoutSyncThreshold()

	if x.timeoutCount%timeoutSyncThreshold == 0 {
		log.Warn("[OnCountdownTimeout] timeout sync threshold reached, send syncInfo message")
		syncInfo := x.getSyncInfo()
		x.broadcastToBftChannel(syncInfo)
	}

	return nil
}
