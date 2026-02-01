// Copyright (c) 2018 XDPoSChain
// XDPoS constants

package utils

const (
	// Extra field constants
	ExtraVanity  = 32 // Fixed number of extra-data prefix bytes reserved for signer vanity
	ExtraSeal    = 65 // Fixed number of extra-data suffix bytes reserved for signer seal
	M2ByteLength = 4  // Length of M2 validator index bytes

	// Cache sizes
	InmemorySnapshots   = 128  // Number of recent vote snapshots to keep in memory
	InmemorySignatures  = 4096 // Number of recent block signatures to keep in memory
	InmemoryEpochs      = 10   // Number of recent epoch info to keep in memory
	InmemoryRound2Epochs = 900 // Number of round to epoch block info mappings

	// Pool hygiene
	PoolHygieneRound   = 10 // Clean up pool entries older than this many rounds
	PeriodicJobPeriod  = 10 // Seconds between periodic cleanup jobs
)
