// Copyright (c) 2018 XDPoSChain
// XDPoS V2 utility functions

package engine_v2

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

// sigHash returns the hash to be signed for a block header
func sigHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()

	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
		header.MixDigest,
		header.Nonce,
		header.Validators,
		header.Penalties,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

// ecrecover extracts the Ethereum address from a signed header
func ecrecover(header *types.Header, sigcache *utils.SigLRU) (common.Address, error) {
	hash := header.Hash()
	if address, known := sigcache.Get(hash); known {
		return address, nil
	}

	pubkey, err := crypto.Ecrecover(sigHash(header).Bytes(), header.Validator)
	if err != nil {
		return common.Address{}, err
	}

	var signer common.Address
	copy(signer[:], crypto.Keccak256(pubkey[1:])[12:])

	sigcache.Add(hash, signer)
	return signer, nil
}

// decodeMasternodesFromHeaderExtra decodes masternodes from V1 header extra
func decodeMasternodesFromHeaderExtra(checkpointHeader *types.Header) []common.Address {
	masternodes := make([]common.Address, (len(checkpointHeader.Extra)-utils.ExtraVanity-utils.ExtraSeal)/common.AddressLength)
	for i := 0; i < len(masternodes); i++ {
		copy(masternodes[i][:], checkpointHeader.Extra[utils.ExtraVanity+i*common.AddressLength:])
	}
	return masternodes
}

// UniqueSignatures filters out duplicate signatures
func UniqueSignatures(signatureSlice []types.Signature) ([]types.Signature, []types.Signature) {
	keys := make(map[string]bool)
	list := []types.Signature{}
	duplicates := []types.Signature{}

	for _, signature := range signatureSlice {
		hexOfSig := common.Bytes2Hex(signature)
		if _, value := keys[hexOfSig]; !value {
			keys[hexOfSig] = true
			list = append(list, signature)
		} else {
			duplicates = append(duplicates, signature)
		}
	}
	return list, duplicates
}

// signSignature signs a hash using the node's credentials
func (x *XDPoS_v2) signSignature(signingHash common.Hash) (types.Signature, error) {
	x.signLock.RLock()
	signer, signFn := x.signer, x.signFn
	x.signLock.RUnlock()

	signedHash, err := signFn(accounts.Account{Address: signer}, signingHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("error signing hash: %v", err)
	}
	return signedHash, nil
}

// verifyMsgSignature verifies a BFT message signature
func (x *XDPoS_v2) verifyMsgSignature(signedHashToBeVerified common.Hash, signature types.Signature, masternodes []common.Address) (bool, common.Address, error) {
	var signerAddress common.Address
	if len(masternodes) == 0 {
		return false, signerAddress, errors.New("empty masternode list")
	}

	pubkey, err := crypto.Ecrecover(signedHashToBeVerified.Bytes(), signature)
	if err != nil {
		return false, signerAddress, fmt.Errorf("error verifying signature: %v", err)
	}

	copy(signerAddress[:], crypto.Keccak256(pubkey[1:])[12:])

	for _, mn := range masternodes {
		if mn == signerAddress {
			return true, signerAddress, nil
		}
	}

	log.Warn("[verifyMsgSignature] signer not in masternode list", "signer", signerAddress)
	return false, signerAddress, nil
}

// getExtraFields decodes the extra fields from a V2 header
func (x *XDPoS_v2) getExtraFields(header *types.Header) (*types.QuorumCert, types.Round, []common.Address, error) {
	var masternodes []common.Address
	switchBlock := x.config.V2.SwitchBlock

	// V1 last block
	if header.Number.Cmp(switchBlock) == 0 {
		masternodes = decodeMasternodesFromHeaderExtra(header)
		return nil, types.Round(0), masternodes, nil
	}

	// V2 block
	masternodes = x.GetMasternodesFromEpochSwitchHeader(header)
	var decodedExtraField types.ExtraFields_v2
	err := utils.DecodeBytesExtraFields(header.Extra, &decodedExtraField)
	if err != nil {
		log.Error("[getExtraFields] decode error", "err", err, "extra", header.Extra)
		return nil, types.Round(0), masternodes, err
	}
	return decodedExtraField.QuorumCert, decodedExtraField.Round, masternodes, nil
}

// GetRoundNumber returns the round number from a header
func (x *XDPoS_v2) GetRoundNumber(header *types.Header) (types.Round, error) {
	switchBlock := x.config.V2.SwitchBlock
	if header.Number.Cmp(switchBlock) <= 0 {
		return types.Round(0), nil
	}

	var decodedExtraField types.ExtraFields_v2
	err := utils.DecodeBytesExtraFields(header.Extra, &decodedExtraField)
	if err != nil {
		return types.Round(0), err
	}
	return decodedExtraField.Round, nil
}

// GetMasternodesFromEpochSwitchHeader extracts masternodes from an epoch switch header
func (x *XDPoS_v2) GetMasternodesFromEpochSwitchHeader(epochSwitchHeader *types.Header) []common.Address {
	if epochSwitchHeader == nil {
		log.Error("[GetMasternodesFromEpochSwitchHeader] nil header")
		return []common.Address{}
	}
	masternodes := make([]common.Address, len(epochSwitchHeader.Validators)/common.AddressLength)
	for i := 0; i < len(masternodes); i++ {
		copy(masternodes[i][:], epochSwitchHeader.Validators[i*common.AddressLength:])
	}
	return masternodes
}

// GetMasternodes returns masternodes for the epoch containing the given header
func (x *XDPoS_v2) GetMasternodes(chain consensus.ChainReader, header *types.Header) []common.Address {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, header, header.Hash())
	if err != nil {
		log.Error("[GetMasternodes] getEpochSwitchInfo error", "err", err)
		return []common.Address{}
	}
	return epochSwitchInfo.Masternodes
}

// GetPenalties returns penalized nodes for the epoch
func (x *XDPoS_v2) GetPenalties(chain consensus.ChainReader, header *types.Header) []common.Address {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, header, header.Hash())
	if err != nil {
		log.Error("[GetPenalties] getEpochSwitchInfo error", "err", err)
		return []common.Address{}
	}
	return epochSwitchInfo.Penalties
}

// GetStandbynodes returns standby nodes for the epoch
func (x *XDPoS_v2) GetStandbynodes(chain consensus.ChainReader, header *types.Header) []common.Address {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, header, header.Hash())
	if err != nil {
		log.Error("[GetStandbynodes] getEpochSwitchInfo error", "err", err)
		return []common.Address{}
	}
	return epochSwitchInfo.Standbynodes
}

// GetSignersFromSnapshot returns signers from the snapshot
func (x *XDPoS_v2) GetSignersFromSnapshot(chain consensus.ChainReader, header *types.Header) ([]common.Address, error) {
	snap, err := x.getSnapshot(chain, header.Number.Uint64(), false)
	if err != nil {
		return nil, err
	}
	return snap.NextEpochCandidates, nil
}

// calcMasternodes calculates masternodes for a new epoch
func (x *XDPoS_v2) calcMasternodes(chain consensus.ChainReader, blockNum *big.Int, parentHash common.Hash, round types.Round) ([]common.Address, []common.Address, error) {
	maxMasternodes := 108 // Default
	if false {
		maxMasternodes = 108
	}

	snap, err := x.getSnapshot(chain, blockNum.Uint64(), false)
	if err != nil {
		log.Error("[calcMasternodes] getSnapshot error", "err", err)
		return nil, nil, err
	}
	candidates := snap.NextEpochCandidates

	switchBlock := x.config.V2.SwitchBlock
	if blockNum.Uint64() == switchBlock.Uint64()+1 {
		log.Info("[calcMasternodes] first V2 block")
		if len(candidates) > maxMasternodes {
			candidates = candidates[:maxMasternodes]
		}
		return candidates, []common.Address{}, nil
	}

	if x.HookPenalty == nil {
		if len(candidates) > maxMasternodes {
			candidates = candidates[:maxMasternodes]
		}
		return candidates, []common.Address{}, nil
	}

	penalties, err := x.HookPenalty(chain, blockNum, parentHash, candidates)
	if err != nil {
		log.Error("[calcMasternodes] HookPenalty error", "err", err)
		return nil, nil, err
	}

	masternodes := common.RemoveItemFromArray(candidates, penalties)
	if len(masternodes) > maxMasternodes {
		masternodes = masternodes[:maxMasternodes]
	}

	return masternodes, penalties, nil
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
			log.Error("[hygieneVotePool] parse error", "Error", err)
			continue
		}
		if keyedRound < int64(round)-utils.PoolHygieneRound {
			log.Debug("[hygieneVotePool] cleaning", "round", keyedRound, "currentRound", round)
			x.votePool.ClearByPoolKey(k)
		}
	}
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
			log.Error("[hygieneTimeoutPool] parse error", "Error", err)
			continue
		}
		if keyedRound < int64(currentRound)-utils.PoolHygieneRound {
			log.Debug("[hygieneTimeoutPool] cleaning", "round", keyedRound, "currentRound", currentRound)
			x.timeoutPool.ClearByPoolKey(k)
		}
	}
}
