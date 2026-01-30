// Copyright (c) 2024 XDC Network
// Timeout handling for XDPoS 2.0 BFT consensus

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

// VerifyTimeoutMessage verifies a timeout message from a peer
func (x *XDPoS_v2) VerifyTimeoutMessage(chain consensus.ChainReader, timeoutMsg *types.Timeout) (bool, error) {
	// Check if timeout round is current
	if timeoutMsg.Round < x.currentRound {
		log.Debug("[VerifyTimeoutMessage] Timeout round too old",
			"timeoutRound", timeoutMsg.Round,
			"currentRound", x.currentRound)
		return false, nil
	}

	// Get snapshot
	snap, err := x.getSnapshot(chain, timeoutMsg.GapNumber, true)
	if err != nil || snap == nil {
		log.Error("[VerifyTimeoutMessage] Failed to get snapshot",
			"gapNumber", timeoutMsg.GapNumber,
			"error", err)
		return false, err
	}

	if len(snap.NextEpochCandidates) == 0 {
		log.Error("[VerifyTimeoutMessage] Empty masternode list",
			"gapNumber", timeoutMsg.GapNumber)
		return false, errors.New("empty masternode list")
	}

	// Verify signature
	verified, signer, err := x.verifyMsgSignature(
		types.TimeoutSigHash(&types.TimeoutForSign{
			Round:     timeoutMsg.Round,
			GapNumber: timeoutMsg.GapNumber,
		}),
		timeoutMsg.Signature,
		snap.NextEpochCandidates,
	)
	if err != nil {
		log.Warn("[VerifyTimeoutMessage] Signature verification failed", "error", err)
		return false, err
	}

	timeoutMsg.SetSigner(signer)
	return verified, nil
}

// TimeoutHandler processes a timeout message
func (x *XDPoS_v2) TimeoutHandler(chain consensus.ChainReader, timeout *types.Timeout) error {
	x.lock.Lock()
	defer x.lock.Unlock()
	return x.timeoutHandler(chain, timeout)
}

func (x *XDPoS_v2) timeoutHandler(chain consensus.ChainReader, timeout *types.Timeout) error {
	// Check round
	if timeout.Round != x.currentRound {
		return &utils.ErrIncomingMessageRoundNotEqualCurrentRound{
			Type:          "timeout",
			IncomingRound: timeout.Round,
			CurrentRound:  x.currentRound,
		}
	}

	// Add to pool
	numberOfTimeouts, pooledTimeouts := x.timeoutPool.Add(timeout)
	log.Debug("[timeoutHandler] collected timeout", "count", numberOfTimeouts)

	// Get epoch info
	epochInfo, err := x.getEpochSwitchInfo(chain, chain.CurrentHeader(), chain.CurrentHeader().Hash())
	if err != nil {
		log.Error("[timeoutHandler] Failed to get epoch info", "error", err)
		return fmt.Errorf("failed to get epoch switch info: %s", err)
	}

	// Check threshold
	certThreshold := x.config.V2.CurrentConfig.CertThreshold
	isThresholdReached := float64(numberOfTimeouts) >= float64(epochInfo.MasternodesLen)*certThreshold

	if isThresholdReached {
		log.Info("[timeoutHandler] Timeout threshold reached",
			"count", numberOfTimeouts,
			"threshold", float64(epochInfo.MasternodesLen)*certThreshold)

		if err := x.onTimeoutPoolThresholdReached(chain, pooledTimeouts, timeout, timeout.GapNumber); err != nil {
			return err
		}
	}

	return nil
}

// onTimeoutPoolThresholdReached generates a TC when enough timeouts are collected
func (x *XDPoS_v2) onTimeoutPoolThresholdReached(chain consensus.ChainReader, pooledTimeouts map[common.Hash]utils.PoolObj, currentTimeoutMsg utils.PoolObj, gapNumber uint64) error {
	// Collect signatures
	signatures := make([]types.Signature, 0, len(pooledTimeouts))
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
	if err := x.processTC(chain, timeoutCert); err != nil {
		log.Error("[onTimeoutPoolThresholdReached] Failed to process TC",
			"round", timeoutCert.Round,
			"signatures", len(timeoutCert.Signatures),
			"error", err)
		return err
	}

	// Broadcast SyncInfo
	syncInfo := x.getSyncInfo()
	x.broadcastToBftChannel(syncInfo)

	log.Info("[onTimeoutPoolThresholdReached] TC processed successfully",
		"round", timeoutCert.Round,
		"signatures", len(timeoutCert.Signatures))
	return nil
}

// sendTimeout sends a timeout message
func (x *XDPoS_v2) sendTimeout(chain consensus.ChainReader) error {
	// Calculate gap number
	var gapNumber uint64
	currentBlockHeader := chain.CurrentHeader()

	isEpochSwitch, epochNum, err := x.isEpochSwitchAtRound(x.currentRound, currentBlockHeader)
	if err != nil {
		log.Error("[sendTimeout] isEpochSwitchAtRound error",
			"currentRound", x.currentRound,
			"error", err)
		return err
	}

	if isEpochSwitch {
		currentNumber := currentBlockHeader.Number.Uint64() + 1
		gapNumber = currentNumber - currentNumber%x.config.Epoch
		if gapNumber > x.config.Gap {
			gapNumber -= x.config.Gap
		} else {
			gapNumber = 0
		}
		log.Debug("[sendTimeout] epoch switch",
			"currentNumber", currentNumber,
			"gapNumber", gapNumber)
	} else {
		epochSwitchInfo, err := x.getEpochSwitchInfo(chain, currentBlockHeader, currentBlockHeader.Hash())
		if err != nil {
			log.Error("[sendTimeout] Failed to get epoch switch info",
				"currentRound", x.currentRound,
				"epochNum", epochNum,
				"error", err)
			return err
		}
		gapNumber = epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64() - epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()%x.config.Epoch
		if gapNumber > x.config.Gap {
			gapNumber -= x.config.Gap
		} else {
			gapNumber = 0
		}
	}

	// Sign timeout
	signedHash, err := x.signSignature(types.TimeoutSigHash(&types.TimeoutForSign{
		Round:     x.currentRound,
		GapNumber: gapNumber,
	}))
	if err != nil {
		log.Error("[sendTimeout] Failed to sign", "error", err)
		return err
	}

	timeoutMsg := &types.Timeout{
		Round:     x.currentRound,
		Signature: signedHash,
		GapNumber: gapNumber,
	}
	timeoutMsg.SetSigner(x.signer)

	log.Warn("[sendTimeout] Timeout message generated",
		"round", timeoutMsg.Round,
		"gapNumber", timeoutMsg.GapNumber,
		"whosTurn", x.whosTurn)

	// Process locally
	if err := x.timeoutHandler(chain, timeoutMsg); err != nil {
		log.Error("[sendTimeout] Local handler error", "error", err)
		return err
	}

	// Broadcast
	x.broadcastToBftChannel(timeoutMsg)
	return nil
}

// OnCountdownTimeout is called when the countdown timer expires
func (x *XDPoS_v2) OnCountdownTimeout(t time.Time, chain interface{}) error {
	x.lock.Lock()
	defer x.lock.Unlock()

	chainReader := chain.(consensus.ChainReader)

	// Check if we're a masternode
	if !x.allowedToSend(chainReader, chainReader.CurrentHeader(), "timeout") {
		return nil
	}

	// Send timeout
	if err := x.sendTimeout(chainReader); err != nil {
		log.Error("[OnCountdownTimeout] Failed to send timeout",
			"time", t,
			"error", err)
		return err
	}

	x.timeoutCount++

	// Send SyncInfo periodically
	if x.timeoutCount%x.config.V2.CurrentConfig.TimeoutSyncThreshold == 0 {
		log.Warn("[OnCountdownTimeout] Timeout sync threshold reached, sending SyncInfo")
		syncInfo := x.getSyncInfo()
		x.broadcastToBftChannel(syncInfo)
	}

	return nil
}

// verifyTC verifies a timeout certificate
func (x *XDPoS_v2) verifyTC(chain consensus.ChainReader, timeoutCert *types.TimeoutCert) error {
	if timeoutCert == nil || timeoutCert.Signatures == nil {
		log.Warn("[verifyTC] TC or signatures is nil")
		return utils.ErrInvalidTC
	}

	// Get snapshot
	snap, err := x.getSnapshot(chain, timeoutCert.GapNumber, true)
	if err != nil {
		log.Error("[verifyTC] Failed to get snapshot",
			"gapNumber", timeoutCert.GapNumber,
			"error", err)
		return fmt.Errorf("unable to get snapshot: %s", err)
	}

	if snap == nil || len(snap.NextEpochCandidates) == 0 {
		log.Error("[verifyTC] Empty snapshot",
			"gapNumber", timeoutCert.GapNumber)
		return errors.New("empty masternode list")
	}

	// Recover unique signers
	signedTimeoutObj := types.TimeoutSigHash(&types.TimeoutForSign{
		Round:     timeoutCert.Round,
		GapNumber: timeoutCert.GapNumber,
	})
	signatures, _, err := RecoverUniqueSigners(signedTimeoutObj, timeoutCert.Signatures)
	if err != nil {
		log.Error("[verifyTC] Failed to recover signers",
			"round", timeoutCert.Round,
			"error", err)
		return err
	}

	// Get epoch info
	epochInfo, err := x.getEpochSwitchInfo(chain, chain.CurrentHeader(), chain.CurrentHeader().Hash())
	if err != nil {
		return err
	}

	// Check threshold
	certThreshold := x.config.V2.CurrentConfig.CertThreshold
	if float64(len(signatures)) < float64(epochInfo.MasternodesLen)*certThreshold {
		log.Warn("[verifyTC] Not enough signatures",
			"signatures", len(signatures),
			"threshold", float64(epochInfo.MasternodesLen)*certThreshold)
		return utils.ErrInvalidTCSignatures
	}

	// Verify signatures in parallel
	var wg sync.WaitGroup
	var mutex sync.Mutex
	var verifyError error

	wg.Add(len(signatures))
	for _, signature := range signatures {
		go func(sig types.Signature) {
			defer wg.Done()
			verified, _, err := x.verifyMsgSignature(signedTimeoutObj, sig, snap.NextEpochCandidates)
			if err != nil || !verified {
				mutex.Lock()
				if verifyError == nil {
					if err != nil {
						verifyError = fmt.Errorf("signature verification error: %s", err)
					} else {
						verifyError = errors.New("signature verification failed")
					}
				}
				mutex.Unlock()
			}
		}(signature)
	}
	wg.Wait()

	return verifyError
}

// processTC processes a timeout certificate
func (x *XDPoS_v2) processTC(chain consensus.ChainReader, timeoutCert *types.TimeoutCert) error {
	// Update highest TC
	if timeoutCert.Round > x.highestTimeoutCert.Round {
		x.highestTimeoutCert = timeoutCert
	}

	// Advance round if needed
	if timeoutCert.Round >= x.currentRound {
		x.setNewRound(chain, timeoutCert.Round+1)
	}

	return nil
}

// isEpochSwitchAtRound checks if a round is an epoch switch
func (x *XDPoS_v2) isEpochSwitchAtRound(round types.Round, header *types.Header) (bool, uint64, error) {
	epochNum := uint64(round) / x.config.Epoch
	isSwitch := uint64(round)%x.config.Epoch == 0
	return isSwitch, epochNum, nil
}

// hygieneTimeoutPool cleans up old timeouts
func (x *XDPoS_v2) hygieneTimeoutPool() {
	x.lock.RLock()
	currentRound := x.currentRound
	x.lock.RUnlock()

	timeoutPoolKeys := x.timeoutPool.PoolObjKeysList()

	for _, k := range timeoutPoolKeys {
		keyedRound, err := strconv.ParseInt(strings.Split(k, ":")[0], 10, 64)
		if err != nil {
			log.Error("[hygieneTimeoutPool] Parse error", "error", err)
			continue
		}
		if keyedRound < int64(currentRound)-PoolHygieneRound {
			log.Debug("[hygieneTimeoutPool] Cleaning timeout pool",
				"round", keyedRound,
				"currentRound", currentRound)
			x.timeoutPool.ClearByPoolKey(k)
		}
	}
}

// ReceivedTimeouts returns all received timeouts
func (x *XDPoS_v2) ReceivedTimeouts() map[string]map[common.Hash]utils.PoolObj {
	return x.timeoutPool.Get()
}
