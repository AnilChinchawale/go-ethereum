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
	"encoding/base64"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

// API is a user facing RPC API to allow controlling the signer and voting
// mechanisms of the XDPoS consensus engine.
type API struct {
	chain consensus.ChainHeaderReader
	xdpos *XDPoS
}

// V2BlockInfo contains information about a V2 block
type V2BlockInfo struct {
	Hash       common.Hash `json:"hash"`
	Round      uint64      `json:"round"`
	Number     *big.Int    `json:"number"`
	ParentHash common.Hash `json:"parentHash"`
	Committed  bool        `json:"committed"`
	Miner      common.Hash `json:"miner"`
	Timestamp  *big.Int    `json:"timestamp"`
	EncodedRLP string      `json:"encodedRLP"`
	Error      string      `json:"error,omitempty"`
}

// NetworkInformation contains network configuration info
type NetworkInformation struct {
	NetworkId                  *big.Int          `json:"networkId"`
	XDCValidatorAddress        common.Address    `json:"xdcValidatorAddress"`
	RelayerRegistrationAddress common.Address    `json:"relayerRegistrationAddress"`
	XDCXListingAddress         common.Address    `json:"xdcxListingAddress"`
	XDCZAddress                common.Address    `json:"xdczAddress"`
	LendingAddress             common.Address    `json:"lendingAddress"`
	ConsensusConfigs           params.XDPoSConfig `json:"consensusConfigs"`
}

// MasternodesStatus contains detailed masternode info at a block
type MasternodesStatus struct {
	Epoch           uint64           `json:"epoch"`
	Number          uint64           `json:"number"`
	Round           uint64           `json:"round"`
	MasternodesLen  int              `json:"masternodesLen"`
	Masternodes     []common.Address `json:"masternodes"`
	PenaltyLen      int              `json:"penaltyLen"`
	Penalty         []common.Address `json:"penalty"`
	StandbynodesLen int              `json:"standbynodesLen"`
	Standbynodes    []common.Address `json:"standbynodes"`
	Error           string           `json:"error,omitempty"`
}

// SignerStatus contains information about if this node is a signer
type SignerStatus struct {
	IsSigner       bool           `json:"isSigner"`
	SignerAddress  common.Address `json:"signerAddress"`
	InMasternodes  bool           `json:"inMasternodes"`
	CurrentBlock   uint64         `json:"currentBlock"`
	TotalSigners   int            `json:"totalSigners"`
}

// EpochInfo contains epoch-related information
type EpochInfo struct {
	EpochNumber      uint64   `json:"epochNumber"`
	EpochStartBlock  uint64   `json:"epochStartBlock"`
	EpochEndBlock    uint64   `json:"epochEndBlock"`
	CurrentBlock     uint64   `json:"currentBlock"`
	BlocksRemaining  uint64   `json:"blocksRemaining"`
	EpochLength      uint64   `json:"epochLength"`
}

// GapInfo contains gap block information
type GapInfo struct {
	GapNumber       uint64 `json:"gapNumber"`
	CurrentBlock    uint64 `json:"currentBlock"`
	EpochLength     uint64 `json:"epochLength"`
	Gap             uint64 `json:"gap"`
	IsInGapPeriod   bool   `json:"isInGapPeriod"`
}

// GetSnapshot retrieves the state snapshot at a given block.
func (api *API) GetSnapshot(number *rpc.BlockNumber) (*Snapshot, error) {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.xdpos.GetSnapshot(api.chain, header)
}

// GetSnapshotAtHash retrieves the state snapshot at a given block hash.
func (api *API) GetSnapshotAtHash(hash common.Hash) (*Snapshot, error) {
	header := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.xdpos.GetSnapshot(api.chain, header)
}

// GetSigners retrieves the list of authorized signers at the specified block.
func (api *API) GetSigners(number *rpc.BlockNumber) ([]common.Address, error) {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.xdpos.GetSnapshot(api.chain, header)
	if err != nil {
		return nil, err
	}
	return snap.GetSigners(), nil
}

// GetSignersAtHash retrieves the list of authorized signers at the specified block hash.
func (api *API) GetSignersAtHash(hash common.Hash) ([]common.Address, error) {
	header := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.xdpos.GetSnapshot(api.chain, header)
	if err != nil {
		return nil, err
	}
	return snap.GetSigners(), nil
}

// GetMasternodes retrieves the list of masternodes at the current block.
func (api *API) GetMasternodes(number *rpc.BlockNumber) ([]common.Address, error) {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.xdpos.GetMasternodes(api.chain, header), nil
}

// GetMasternodesByNumber retrieves detailed masternode status at a block number.
func (api *API) GetMasternodesByNumber(number *rpc.BlockNumber) MasternodesStatus {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	
	if header == nil {
		return MasternodesStatus{
			Error: "block not found",
		}
	}

	masternodes := api.xdpos.GetMasternodes(api.chain, header)
	epoch := api.xdpos.config.Epoch
	epochNum := header.Number.Uint64() / epoch

	info := MasternodesStatus{
		Epoch:           epochNum,
		Number:          header.Number.Uint64(),
		Round:           0, // V1 doesn't have rounds
		MasternodesLen:  len(masternodes),
		Masternodes:     masternodes,
		PenaltyLen:      0,
		Penalty:         []common.Address{},
		StandbynodesLen: 0,
		Standbynodes:    []common.Address{},
	}
	return info
}

// GetCandidates returns the current masternode candidates (same as signers in V1)
func (api *API) GetCandidates(number *rpc.BlockNumber) ([]common.Address, error) {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errUnknownBlock
	}
	snap, err := api.xdpos.GetSnapshot(api.chain, header)
	if err != nil {
		return nil, err
	}
	return snap.GetSigners(), nil
}

// GetEpoch returns the current epoch number.
func (api *API) GetEpoch() *EpochInfo {
	header := api.chain.CurrentHeader()
	if header == nil {
		return nil
	}
	
	blockNum := header.Number.Uint64()
	epoch := api.xdpos.config.Epoch
	epochNum := blockNum / epoch
	epochStart := epochNum * epoch
	epochEnd := epochStart + epoch - 1
	
	return &EpochInfo{
		EpochNumber:     epochNum,
		EpochStartBlock: epochStart,
		EpochEndBlock:   epochEnd,
		CurrentBlock:    blockNum,
		BlocksRemaining: epochEnd - blockNum,
		EpochLength:     epoch,
	}
}

// GetEpochByNumber returns epoch info at a given block number.
func (api *API) GetEpochByNumber(number *rpc.BlockNumber) *EpochInfo {
	var blockNum uint64
	if number == nil || *number == rpc.LatestBlockNumber {
		header := api.chain.CurrentHeader()
		if header == nil {
			return nil
		}
		blockNum = header.Number.Uint64()
	} else {
		blockNum = uint64(number.Int64())
	}
	
	epoch := api.xdpos.config.Epoch
	epochNum := blockNum / epoch
	epochStart := epochNum * epoch
	epochEnd := epochStart + epoch - 1
	
	return &EpochInfo{
		EpochNumber:     epochNum,
		EpochStartBlock: epochStart,
		EpochEndBlock:   epochEnd,
		CurrentBlock:    blockNum,
		BlocksRemaining: epochEnd - blockNum,
		EpochLength:     epoch,
	}
}

// GetGapNumber returns gap block information.
func (api *API) GetGapNumber() *GapInfo {
	header := api.chain.CurrentHeader()
	if header == nil {
		return nil
	}
	
	blockNum := header.Number.Uint64()
	epoch := api.xdpos.config.Epoch
	gap := api.xdpos.config.Gap
	
	// Calculate current epoch and gap period
	epochNum := blockNum / epoch
	epochStart := epochNum * epoch
	gapStart := epochStart + epoch - gap
	
	return &GapInfo{
		GapNumber:     gapStart,
		CurrentBlock:  blockNum,
		EpochLength:   epoch,
		Gap:           gap,
		IsInGapPeriod: blockNum >= gapStart,
	}
}

// GetV2BlockByNumber retrieves V2 block info by number.
func (api *API) GetV2BlockByNumber(number *rpc.BlockNumber) *V2BlockInfo {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	
	if header == nil {
		num := int64(0)
		if number != nil {
			num = number.Int64()
		}
		return &V2BlockInfo{
			Number: big.NewInt(num),
			Error:  "block not found",
		}
	}

	return api.getV2BlockInfo(header, false)
}

// GetV2BlockByHash retrieves V2 block info by hash.
func (api *API) GetV2BlockByHash(blockHash common.Hash) *V2BlockInfo {
	header := api.chain.GetHeaderByHash(blockHash)
	if header == nil {
		return &V2BlockInfo{
			Hash:  blockHash,
			Error: "block not found",
		}
	}

	// Check if this is on the main chain
	chainHeader := api.chain.GetHeaderByNumber(header.Number.Uint64())
	uncle := chainHeader != nil && header.Hash() != chainHeader.Hash()

	return api.getV2BlockInfo(header, uncle)
}

// getV2BlockInfo builds V2BlockInfo from a header
func (api *API) getV2BlockInfo(header *types.Header, uncle bool) *V2BlockInfo {
	committed := !uncle // Simplified: assume all non-uncle blocks are committed
	
	encodeBytes, err := rlp.EncodeToBytes(header)
	if err != nil {
		return &V2BlockInfo{
			Hash:  header.Hash(),
			Error: err.Error(),
		}
	}

	return &V2BlockInfo{
		Hash:       header.Hash(),
		ParentHash: header.ParentHash,
		Number:     header.Number,
		Round:      0, // V1 doesn't have rounds
		Committed:  committed,
		Miner:      common.BytesToHash(header.Coinbase.Bytes()),
		Timestamp:  new(big.Int).SetUint64(header.Time),
		EncodedRLP: base64.StdEncoding.EncodeToString(encodeBytes),
	}
}

// NetworkInformation returns XDPoS network configuration.
func (api *API) NetworkInformation() NetworkInformation {
	info := NetworkInformation{}
	info.NetworkId = api.chain.Config().ChainID
	info.XDCValidatorAddress = common.HexToAddress("0x0000000000000000000000000000000000000088")
	info.LendingAddress = common.HexToAddress("0x0000000000000000000000000000000000000055")
	info.RelayerRegistrationAddress = common.HexToAddress("0x0000000000000000000000000000000000000002")
	info.XDCXListingAddress = common.HexToAddress("0x000000000000000000000000000000000000000A")
	info.XDCZAddress = common.HexToAddress("0x0000000000000000000000000000000000000089")
	info.ConsensusConfigs = *api.xdpos.config

	return info
}

// SignerStatus returns if this node is configured as a signer.
func (api *API) SignerStatus() *SignerStatus {
	api.xdpos.lock.RLock()
	signer := api.xdpos.signer
	api.xdpos.lock.RUnlock()
	
	header := api.chain.CurrentHeader()
	if header == nil {
		return &SignerStatus{
			IsSigner:      false,
			SignerAddress: signer,
		}
	}
	
	masternodes := api.xdpos.GetMasternodes(api.chain, header)
	inMasternodes := false
	for _, mn := range masternodes {
		if mn == signer {
			inMasternodes = true
			break
		}
	}
	
	return &SignerStatus{
		IsSigner:      signer != (common.Address{}),
		SignerAddress: signer,
		InMasternodes: inMasternodes,
		CurrentBlock:  header.Number.Uint64(),
		TotalSigners:  len(masternodes),
	}
}

// Proposals returns the current proposals the node tries to uphold and vote on.
func (api *API) Proposals() map[common.Address]bool {
	api.xdpos.lock.RLock()
	defer api.xdpos.lock.RUnlock()

	proposals := make(map[common.Address]bool)
	for address, auth := range api.xdpos.proposals {
		proposals[address] = auth
	}
	return proposals
}

// Propose injects a new authorization proposal that the signer will attempt to push through.
func (api *API) Propose(address common.Address, auth bool) {
	api.xdpos.lock.Lock()
	defer api.xdpos.lock.Unlock()
	api.xdpos.proposals[address] = auth
}

// Discard drops a currently running proposal.
func (api *API) Discard(address common.Address) {
	api.xdpos.lock.Lock()
	defer api.xdpos.lock.Unlock()
	delete(api.xdpos.proposals, address)
}

// GetValidator returns the validator address for a given block signer
func (api *API) GetValidator(address common.Address, number *rpc.BlockNumber) (common.Address, error) {
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return common.Address{}, errUnknownBlock
	}
	
	return api.xdpos.GetValidator(address, api.chain, header)
}

// GetRound returns the current round (0 for V1, actual round for V2)
func (api *API) GetRound() uint64 {
	header := api.chain.CurrentHeader()
	if header == nil {
		return 0
	}
	
	// V2 check
	if api.xdpos.config.V2 != nil && api.xdpos.config.V2.SwitchBlock != nil {
		if header.Number.Uint64() >= api.xdpos.config.V2.SwitchBlock.Uint64() {
			// For V2, round could be extracted from header extra data
			// For now return block number as approximation
			return header.Number.Uint64()
		}
	}
	return 0
}

// GetSyncInfo returns sync status info (placeholder for V2)
func (api *API) GetSyncInfo() map[string]interface{} {
	header := api.chain.CurrentHeader()
	if header == nil {
		return map[string]interface{}{
			"error": "no current header",
		}
	}
	
	return map[string]interface{}{
		"currentBlock":  header.Number.Uint64(),
		"currentHash":   header.Hash().Hex(),
		"epoch":         header.Number.Uint64() / api.xdpos.config.Epoch,
		"epochLength":   api.xdpos.config.Epoch,
		"gap":           api.xdpos.config.Gap,
		"period":        api.xdpos.config.Period,
	}
}

// GetVotes returns votes for a block (placeholder - V2 specific)
func (api *API) GetVotes(blockHash common.Hash) map[string]interface{} {
	return map[string]interface{}{
		"blockHash": blockHash.Hex(),
		"message":   "votes tracking not available in V1 consensus",
	}
}

// GetQC returns quorum certificate for a block (placeholder - V2 specific)
func (api *API) GetQC(blockHash common.Hash) map[string]interface{} {
	return map[string]interface{}{
		"blockHash": blockHash.Hex(),
		"message":   "quorum certificates not available in V1 consensus",
	}
}
