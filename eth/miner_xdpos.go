// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// XDPoS mining support - provides block minting for XDPoS consensus.

package eth

import (
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/XDPoS"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
)

// XDPoSMiner provides block minting capabilities for XDPoS consensus
type XDPoSMiner struct {
	eth       *Ethereum
	engine    *XDPoS.XDPoS
	chain     *core.BlockChain
	txpool    *txpool.TxPool
	
	mining    int32 // atomic: 1 = mining, 0 = stopped
	coinbase  common.Address
	
	mu        sync.RWMutex
	exitCh    chan struct{}
	
	// Block production timing
	period    uint64
}

// NewXDPoSMiner creates a new XDPoS miner instance
func NewXDPoSMiner(eth *Ethereum) *XDPoSMiner {
	engine, ok := eth.engine.(*XDPoS.XDPoS)
	if !ok {
		log.Error("Engine is not XDPoS, miner disabled")
		return nil
	}
	
	return &XDPoSMiner{
		eth:    eth,
		engine: engine,
		chain:  eth.blockchain,
		txpool: eth.txPool,
		period: engine.GetPeriod(),
	}
}

// Start begins the mining process
func (m *XDPoSMiner) Start(coinbase common.Address) error {
	if m == nil {
		return nil
	}
	
	m.mu.Lock()
	if atomic.LoadInt32(&m.mining) == 1 {
		m.mu.Unlock()
		return nil // Already mining
	}
	
	m.coinbase = coinbase
	m.exitCh = make(chan struct{})
	atomic.StoreInt32(&m.mining, 1)
	m.mu.Unlock()
	
	log.Info("Starting XDPoS miner", "coinbase", coinbase, "period", m.period)
	go m.mintLoop()
	return nil
}

// Stop halts the mining process
func (m *XDPoSMiner) Stop() {
	if m == nil {
		return
	}
	
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if atomic.CompareAndSwapInt32(&m.mining, 1, 0) {
		close(m.exitCh)
		log.Info("Stopped XDPoS miner")
	}
}

// Mining returns whether mining is active
func (m *XDPoSMiner) Mining() bool {
	if m == nil {
		return false
	}
	return atomic.LoadInt32(&m.mining) == 1
}

// SetCoinbase updates the coinbase address
func (m *XDPoSMiner) SetCoinbase(addr common.Address) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.coinbase = addr
	m.mu.Unlock()
}

// mintLoop is the main mining loop
func (m *XDPoSMiner) mintLoop() {
	period := time.Duration(m.period) * time.Second
	if period < time.Second {
		period = 2 * time.Second // Minimum period
	}
	
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	
	// Subscribe to new chain heads to trigger mining attempts
	chainHeadCh := make(chan core.ChainHeadEvent, 10)
	sub := m.chain.SubscribeChainHeadEvent(chainHeadCh)
	defer sub.Unsubscribe()
	
	for {
		select {
		case <-m.exitCh:
			return
		case <-ticker.C:
			if atomic.LoadInt32(&m.mining) == 1 {
				m.tryMint()
			}
		case <-chainHeadCh:
			// New chain head, check if we should mint
			if atomic.LoadInt32(&m.mining) == 1 {
				m.tryMint()
			}
		}
	}
}

// tryMint attempts to mint a new block
func (m *XDPoSMiner) tryMint() {
	m.mu.RLock()
	coinbase := m.coinbase
	m.mu.RUnlock()
	
	if coinbase == (common.Address{}) {
		return
	}
	
	parent := m.chain.CurrentBlock()
	if parent == nil {
		return
	}
	
	// Check if it's our turn
	_, _, _, isMyTurn, err := m.engine.YourTurn(m.chain, parent, coinbase)
	if err != nil {
		log.Debug("Error checking turn", "err", err)
		return
	}
	if !isMyTurn {
		return
	}
	
	log.Info("It's our turn to mint", "block", parent.Number.Uint64()+1, "coinbase", coinbase)
	
	if err := m.mintBlock(parent, coinbase); err != nil {
		log.Error("Failed to mint block", "err", err)
	}
}

// mintBlock creates and seals a new block
func (m *XDPoSMiner) mintBlock(parent *types.Header, coinbase common.Address) error {
	number := new(big.Int).Add(parent.Number, big.NewInt(1))
	
	// Calculate timestamp
	timestamp := uint64(time.Now().Unix())
	if timestamp <= parent.Time {
		timestamp = parent.Time + 1
	}
	
	// Prepare header
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     number,
		GasLimit:   core.CalcGasLimit(parent.GasLimit, m.eth.config.Miner.GasCeil),
		Time:       timestamp,
		Coinbase:   coinbase,
		Extra:      m.eth.config.Miner.ExtraData,
	}
	
	// Ensure extra data has space for vanity and seal
	if len(header.Extra) < 32 {
		header.Extra = append(header.Extra, make([]byte, 32-len(header.Extra))...)
	}
	// Add space for signature (65 bytes)
	header.Extra = append(header.Extra, make([]byte, 65)...)
	
	// Let consensus engine prepare the header
	if err := m.engine.Prepare(m.chain, header); err != nil {
		return err
	}
	
	// Get state for block assembly
	state, err := m.chain.StateAt(parent.Root)
	if err != nil {
		return err
	}
	
	// Get pending transactions
	pending := m.txpool.Pending(txpool.PendingFilter{})
	
	// Fill transactions
	var txs types.Transactions
	gasPool := new(core.GasPool).AddGas(header.GasLimit)
	var receipts types.Receipts
	var usedGas uint64
	
	// Create EVM context
	blockContext := core.NewEVMBlockContext(header, m.chain, &coinbase)
	evm := vm.NewEVM(blockContext, state, m.chain.Config(), vm.Config{})
	
	for _, batch := range pending {
		for _, lazyTx := range batch {
			// Resolve lazy transaction to actual transaction
			tx := lazyTx.Resolve()
			if tx == nil {
				continue
			}
			
			// Check gas limit
			if gasPool.Gas() < tx.Gas() {
				continue
			}
			
			// Apply transaction
			state.SetTxContext(tx.Hash(), len(txs))
			receipt, err := core.ApplyTransaction(evm, gasPool, state, header, tx, &usedGas)
			if err != nil {
				continue
			}
			
			txs = append(txs, tx)
			receipts = append(receipts, receipt)
		}
	}
	
	header.GasUsed = usedGas
	
	// Finalize and assemble block
	block, err := m.engine.FinalizeAndAssemble(m.chain, header, state, 
		&types.Body{Transactions: txs}, receipts)
	if err != nil {
		return err
	}
	
	// Seal the block
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	
	if err := m.engine.Seal(m.chain, block, results, stop); err != nil {
		return err
	}
	
	// Wait for seal result
	select {
	case sealed := <-results:
		if sealed != nil {
			log.Info("Successfully minted block", 
				"number", sealed.Number(), 
				"hash", sealed.Hash(),
				"txs", len(sealed.Transactions()),
				"gas", sealed.GasUsed())
			
			// Insert into blockchain
			if _, err := m.chain.InsertChain([]*types.Block{sealed}); err != nil {
				return err
			}
		}
	case <-time.After(10 * time.Second):
		close(stop)
		return nil
	}
	
	return nil
}

// StartMining starts the XDPoS block minting process with the given coinbase.
// This should be called on the Ethereum backend.
func (s *Ethereum) StartMining(coinbase common.Address) error {
	// Get account manager for signing
	am := s.AccountManager()
	if am == nil {
		return fmt.Errorf("account manager not available")
	}
	
	// Find wallet containing the coinbase
	var wallet accounts.Wallet
	for _, w := range am.Wallets() {
		if w.Contains(accounts.Account{Address: coinbase}) {
			wallet = w
			break
		}
	}
	if wallet == nil {
		return fmt.Errorf("coinbase account %s not found in wallets", coinbase)
	}
	
	// Check if XDPoS engine
	engine, ok := s.engine.(*XDPoS.XDPoS)
	if !ok {
		return fmt.Errorf("mining only supported for XDPoS consensus")
	}
	
	// Create signing function
	signFn := func(acc accounts.Account, mimeType string, data []byte) ([]byte, error) {
		return wallet.SignData(acc, mimeType, data)
	}
	
	// Authorize the engine
	engine.Authorize(coinbase, signFn)
	
	log.Info("Authorized XDPoS engine for mining", "coinbase", coinbase)
	
	// Create and start XDPoS miner
	miner := NewXDPoSMiner(s)
	if miner != nil {
		return miner.Start(coinbase)
	}
	return fmt.Errorf("failed to create XDPoS miner")
}
