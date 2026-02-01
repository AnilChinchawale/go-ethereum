// Package hooks provides consensus hooks for XDPoS engine integration
// The full implementations are in engine_v2_hooks.go

package hooks

import (
	"github.com/ethereum/go-ethereum/consensus/XDPoS"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"
)

// AttachConsensusV1Hooks attaches V1 consensus hooks to XDPoS engine
// Note: V1 hooks use the same mechanism as V2 for the unified engine
func AttachConsensusV1Hooks(adaptor *XDPoS.XDPoS, bc *core.BlockChain, chainConfig *params.ChainConfig) {
	// V1 hooks are handled by the same HookReward in the unified XDPoS engine
	// The engine internally dispatches to V1 or V2 logic based on block number
}
