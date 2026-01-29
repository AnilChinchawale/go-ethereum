# XDPoS Port Roadmap & Timeline

## Current Status

| Component | Status | Blocks Supported |
|-----------|--------|------------------|
| V1 Consensus | ✅ Complete | 0 - ~3,000,000 |
| Reward Distribution | ✅ Complete | All checkpoints |
| Block Verification | ✅ Complete | V1 blocks |
| State Root Matching | ✅ Verified | All tested blocks |
| V2 Consensus | ❌ Not Started | ~3M - current |
| Block Minting | ❌ Not Started | - |

---

## Timeline Estimate for Full Mainnet Sync

### Phase 1: V2 Consensus (Estimated: 2-3 weeks)

**Goal:** Sync past block 3,000,000 to current mainnet head

| Task | Complexity | Time Estimate |
|------|------------|---------------|
| Port V2 Engine Structure | Medium | 2-3 days |
| Implement Round Management | High | 3-4 days |
| Implement Vote Collection | High | 3-4 days |
| Implement Timeout Handling | Medium | 2-3 days |
| Fork Choice Rules | High | 2-3 days |
| Testing & Debugging | High | 3-5 days |

**Files to Port:**
```
consensus/XDPoS/engines/engine_v2/
├── engine.go           (~800 lines)
├── verifyHeader.go     (~400 lines)
├── vote.go             (~600 lines)
├── timeout.go          (~300 lines)
└── utils.go            (~200 lines)
```

### Phase 2: Penalty System (Estimated: 1 week)

**Goal:** Proper validator penalty handling

| Task | Complexity | Time Estimate |
|------|------------|---------------|
| HookPenalty Implementation | Medium | 2 days |
| HookPenaltyTIPSigning | Medium | 2 days |
| Testing | Medium | 2-3 days |

### Phase 3: Block Minting (Estimated: 2 weeks)

**Goal:** Validators can produce blocks

| Task | Complexity | Time Estimate |
|------|------------|---------------|
| Seal() Implementation | High | 3-4 days |
| Block Proposal Logic | High | 3-4 days |
| Signing Integration | Medium | 2-3 days |
| Masternode Selection | Medium | 2 days |
| Testing on Testnet | High | 3-4 days |

### Phase 4: Full Integration (Estimated: 1 week)

| Task | Complexity | Time Estimate |
|------|------------|---------------|
| Integration Testing | High | 3-4 days |
| Performance Optimization | Medium | 2-3 days |
| Documentation | Low | 1-2 days |

---

## Total Timeline Summary

| Phase | Duration | Cumulative |
|-------|----------|------------|
| Phase 1: V2 Consensus | 2-3 weeks | 2-3 weeks |
| Phase 2: Penalty System | 1 week | 3-4 weeks |
| Phase 3: Block Minting | 2 weeks | 5-6 weeks |
| Phase 4: Integration | 1 week | 6-7 weeks |

**Estimated Total: 6-7 weeks for full mainnet sync + block minting**

---

## PR Structure Recommendation

### Option A: Single Large PR (Current - PR #5)
❌ **Not Recommended**

**Pros:**
- All changes in one place
- Single merge

**Cons:**
- Hard to review (1000+ lines)
- Difficult to debug issues
- Risk of merge conflicts

### Option B: Multiple Focused PRs ✅ **Recommended**

Break into logical, reviewable chunks:

```
PR #5  - Base XDPoS + V1 Consensus (CURRENT - READY)
         ├── consensus/XDPoS/xdpos.go
         ├── consensus/XDPoS/reward.go
         ├── consensus/XDPoS/constants.go
         └── core/state helpers

PR #6  - V2 Consensus Engine
         ├── consensus/XDPoS/engine_v2.go
         ├── consensus/XDPoS/vote.go
         └── consensus/XDPoS/timeout.go

PR #7  - Penalty System
         ├── consensus/XDPoS/penalty.go
         └── Related state changes

PR #8  - Block Production/Minting
         ├── consensus/XDPoS/seal.go
         ├── Miner integration
         └── Masternode selection

PR #9  - Full Integration & Optimization
         └── Final integration, tests, docs
```

### Recommended Approach

1. **Merge PR #5 NOW** - V1 consensus is complete and verified
2. **Create PR #6** - Start V2 consensus work
3. **Parallel work** - Penalty system can be done alongside V2
4. **Sequential** - Block minting depends on V2

---

## Immediate Next Steps

### This Week:
1. ✅ Merge PR #5 (V1 consensus)
2. 🔄 Create PR #6 branch for V2 consensus
3. 📝 Document V2 consensus requirements

### Next Week:
1. Port V2 engine structure
2. Implement basic round management
3. Test against blocks 3M-3.1M

---

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|------------|
| V2 complexity higher than expected | High | Start with minimal implementation |
| State incompatibilities | Medium | Continuous verification against v2.6.8 |
| Performance issues | Medium | Profile and optimize incrementally |
| Network protocol differences | Low | Use same bootnodes as v2.6.8 |

---

## Success Criteria

### For Full Mainnet Sync:
- [ ] Sync to current mainnet head (~98M blocks)
- [ ] All state roots match v2.6.8
- [ ] Stable peer connections
- [ ] No crashes during sync

### For Block Minting:
- [ ] Successfully produce blocks on testnet
- [ ] Blocks accepted by other nodes
- [ ] Rewards distributed correctly
- [ ] Participate in consensus rounds

---

## Resources Required

- **Developer Time:** 1-2 developers, 6-7 weeks
- **Testing Infrastructure:** 
  - XDC mainnet node (v2.6.8) for verification
  - XDC testnet for block minting tests
- **Hardware:** Standard server (8+ cores, 16GB+ RAM, 500GB+ SSD)

