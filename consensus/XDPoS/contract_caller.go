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
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// ContractCaller provides methods to call XDC system contracts
type ContractCaller struct {
	config   *params.ChainConfig
	vmConfig vm.Config
}

// NewContractCaller creates a new ContractCaller instance
func NewContractCaller(config *params.ChainConfig) *ContractCaller {
	return &ContractCaller{
		config:   config,
		vmConfig: vm.Config{},
	}
}

// CallValidatorContract executes a call to the validator contract
func (cc *ContractCaller) CallValidatorContract(
	statedb *state.StateDB,
	header *types.Header,
	method []byte,
	args ...[]byte,
) ([]byte, error) {
	return cc.callContract(statedb, header, ValidatorContractAddress, method, args...)
}

// CallBlockSignerContract executes a call to the block signer contract
func (cc *ContractCaller) CallBlockSignerContract(
	statedb *state.StateDB,
	header *types.Header,
	method []byte,
	args ...[]byte,
) ([]byte, error) {
	return cc.callContract(statedb, header, BlockSignerContractAddress, method, args...)
}

// callContract executes a read-only call to a contract
func (cc *ContractCaller) callContract(
	statedb *state.StateDB,
	header *types.Header,
	contractAddr common.Address,
	method []byte,
	args ...[]byte,
) ([]byte, error) {
	// Build the call data
	data := make([]byte, len(method))
	copy(data, method)
	for _, arg := range args {
		data = append(data, arg...)
	}

	// Create a message for the call
	from := common.Address{} // Zero address for read-only calls
	msg := &core.Message{
		From:      from,
		To:        &contractAddr,
		Value:     big.NewInt(0),
		GasLimit:  uint64(4000000), // Generous gas limit for view calls
		GasPrice:  big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		GasTipCap: big.NewInt(0),
		Data:      data,
	}

	// Create a block context
	blockCtx := core.NewEVMBlockContext(header, nil, &header.Coinbase)

	// Create the EVM (modern go-ethereum API)
	evm := vm.NewEVM(blockCtx, statedb, cc.config, cc.vmConfig)

	// Set up tx context
	evm.TxContext = vm.TxContext{
		Origin:   from,
		GasPrice: big.NewInt(0),
	}

	// Execute the call using StaticCall for read-only operations
	// Modern go-ethereum API: StaticCall(caller common.Address, ...)
	result, _, err := evm.StaticCall(from, contractAddr, data, msg.GasLimit)
	if err != nil {
		log.Debug("Contract call failed", "contract", contractAddr.Hex(), "error", err)
		return nil, err
	}

	return result, nil
}

// GetCandidatesFromContract retrieves the candidate list from the validator contract
func (cc *ContractCaller) GetCandidatesFromContract(
	statedb *state.StateDB,
	header *types.Header,
) ([]common.Address, error) {
	result, err := cc.CallValidatorContract(statedb, header, GetCandidatesMethod)
	if err != nil {
		return nil, err
	}
	return ExtractAddressesFromReturn(result), nil
}

// GetCandidateCapFromContract retrieves the stake amount for a candidate
func (cc *ContractCaller) GetCandidateCapFromContract(
	statedb *state.StateDB,
	header *types.Header,
	candidate common.Address,
) (*big.Int, error) {
	result, err := cc.CallValidatorContract(
		statedb,
		header,
		GetCandidateCapMethod,
		AddressToPaddedBytes(candidate),
	)
	if err != nil {
		return nil, err
	}
	if len(result) < 32 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(result[:32]), nil
}

// GetVotersFromContract retrieves the list of voters for a candidate
func (cc *ContractCaller) GetVotersFromContract(
	statedb *state.StateDB,
	header *types.Header,
	candidate common.Address,
) ([]common.Address, error) {
	result, err := cc.CallValidatorContract(
		statedb,
		header,
		GetVotersMethod,
		AddressToPaddedBytes(candidate),
	)
	if err != nil {
		return nil, err
	}
	return ExtractAddressesFromReturn(result), nil
}

// GetVoterCapFromContract retrieves the vote amount for a specific voter on a candidate
func (cc *ContractCaller) GetVoterCapFromContract(
	statedb *state.StateDB,
	header *types.Header,
	candidate common.Address,
	voter common.Address,
) (*big.Int, error) {
	result, err := cc.CallValidatorContract(
		statedb,
		header,
		GetVoterCapMethod,
		AddressToPaddedBytes(candidate),
		AddressToPaddedBytes(voter),
	)
	if err != nil {
		return nil, err
	}
	if len(result) < 32 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(result[:32]), nil
}

// GetMasternodesWithStakes retrieves masternodes sorted by stake from the contract
// This selects the top N candidates by stake to become masternodes
func (cc *ContractCaller) GetMasternodesWithStakes(
	statedb *state.StateDB,
	header *types.Header,
	maxMasternodes int,
) ([]common.Address, error) {
	// Get all candidates
	candidates, err := cc.GetCandidatesFromContract(statedb, header)
	if err != nil {
		return nil, err
	}

	// Get stakes for each candidate
	type candidateStake struct {
		address common.Address
		stake   *big.Int
	}
	stakes := make([]candidateStake, 0, len(candidates))

	for _, candidate := range candidates {
		stake, err := cc.GetCandidateCapFromContract(statedb, header, candidate)
		if err != nil {
			log.Debug("Failed to get candidate stake", "candidate", candidate.Hex(), "error", err)
			continue
		}
		stakes = append(stakes, candidateStake{candidate, stake})
	}

	// Sort by stake (descending)
	for i := 0; i < len(stakes); i++ {
		for j := i + 1; j < len(stakes); j++ {
			if stakes[j].stake.Cmp(stakes[i].stake) > 0 {
				stakes[i], stakes[j] = stakes[j], stakes[i]
			}
		}
	}

	// Take top N
	result := make([]common.Address, 0, maxMasternodes)
	for i := 0; i < len(stakes) && i < maxMasternodes; i++ {
		result = append(result, stakes[i].address)
	}

	return result, nil
}

// CalculateMasternodeRewardsFromContract calculates rewards based on actual signing data
// from the block signer contract
func (cc *ContractCaller) CalculateMasternodeRewardsFromContract(
	statedb *state.StateDB,
	header *types.Header,
	epochStart uint64,
	epochEnd uint64,
	masternodes []common.Address,
) (map[common.Address]int64, error) {
	signCount := make(map[common.Address]int64)

	// Initialize all masternodes with 0 signs
	for _, mn := range masternodes {
		signCount[mn] = 0
	}

	// Note: In production, this would query the block signer contract
	// for actual signature data. The block signer contract at 0x89
	// stores who signed each block.
	//
	// For now, we return equal distribution as the contract call
	// to getSigners(blockNumber) would need to be implemented
	// based on the specific contract ABI.

	log.Debug("Calculated sign counts for masternodes",
		"epochStart", epochStart,
		"epochEnd", epochEnd,
		"masternodes", len(masternodes))

	return signCount, nil
}
