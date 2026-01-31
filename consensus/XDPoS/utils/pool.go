// Copyright (c) 2018 XDPoSChain
// Vote and Timeout pool for XDPoS V2

package utils

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// PoolObj interface for objects that can be pooled
type PoolObj interface {
	Hash() common.Hash
	PoolKey() string
	GetSigner() common.Address
}

// Pool manages a collection of pool objects by key
type Pool struct {
	objList map[string]map[common.Hash]PoolObj
	lock    sync.RWMutex
}

// NewPool creates a new pool
func NewPool() *Pool {
	return &Pool{
		objList: make(map[string]map[common.Hash]PoolObj),
	}
}

// Get returns all objects in the pool
func (p *Pool) Get() map[string]map[common.Hash]PoolObj {
	return p.objList
}

// Add adds an object to the pool, returns count and objects for that key
func (p *Pool) Add(obj PoolObj) (int, map[common.Hash]PoolObj) {
	p.lock.Lock()
	defer p.lock.Unlock()

	poolKey := obj.PoolKey()
	objListKeyed, ok := p.objList[poolKey]
	if !ok {
		p.objList[poolKey] = make(map[common.Hash]PoolObj)
		objListKeyed = p.objList[poolKey]
	}
	objListKeyed[obj.Hash()] = obj
	return len(objListKeyed), objListKeyed
}

// Size returns the number of objects for a given key
func (p *Pool) Size(obj PoolObj) int {
	poolKey := obj.PoolKey()
	objListKeyed, ok := p.objList[poolKey]
	if !ok {
		return 0
	}
	return len(objListKeyed)
}

// PoolObjKeysList returns all keys in the pool
func (p *Pool) PoolObjKeysList() []string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	var keyList []string
	for key := range p.objList {
		keyList = append(keyList, key)
	}
	return keyList
}

// ClearPoolKeyByObj clears all objects with the same pool key
func (p *Pool) ClearPoolKeyByObj(obj PoolObj) {
	p.lock.Lock()
	defer p.lock.Unlock()
	delete(p.objList, obj.PoolKey())
}

// ClearByPoolKey clears objects by pool key
func (p *Pool) ClearByPoolKey(poolKey string) {
	p.lock.Lock()
	defer p.lock.Unlock()
	delete(p.objList, poolKey)
}

// Clear clears all objects
func (p *Pool) Clear() {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.objList = make(map[string]map[common.Hash]PoolObj)
}

// GetObjsByKey returns all objects for a key
func (p *Pool) GetObjsByKey(poolKey string) []PoolObj {
	p.lock.Lock()
	defer p.lock.Unlock()

	objListKeyed, ok := p.objList[poolKey]
	if !ok {
		return []PoolObj{}
	}
	objList := make([]PoolObj, len(objListKeyed))
	cnt := 0
	for _, obj := range objListKeyed {
		objList[cnt] = obj
		cnt++
	}
	return objList
}
