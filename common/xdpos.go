// Copyright 2024 The go-ethereum Authors
// XDC Network helper functions

package common

// StoreRewardFolder is the folder path for storing reward information
// Set via command line flag or environment variable
var StoreRewardFolder string

// ExtractAddressFromBytes extracts addresses from a byte slice
func ExtractAddressFromBytes(b []byte) []Address {
	if len(b) == 0 {
		return []Address{}
	}
	count := len(b) / AddressLength
	addresses := make([]Address, count)
	for i := 0; i < count; i++ {
		copy(addresses[i][:], b[i*AddressLength:(i+1)*AddressLength])
	}
	return addresses
}

// RemoveItemFromArray removes items in toRemove from array
func RemoveItemFromArray(array []Address, toRemove []Address) []Address {
	removeMap := make(map[Address]bool)
	for _, r := range toRemove {
		removeMap[r] = true
	}
	result := make([]Address, 0, len(array))
	for _, a := range array {
		if !removeMap[a] {
			result = append(result, a)
		}
	}
	return result
}
