package rewards

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ipfs/go-cid"
	"github.com/rocket-pool/rocketpool-go/utils/eth"
	"github.com/rocket-pool/smartnode/shared/services/beacon"
	"github.com/rocket-pool/smartnode/shared/services/config"
	"github.com/rocket-pool/smartnode/shared/services/rewards/ssz_types"
	sszbig "github.com/rocket-pool/smartnode/shared/services/rewards/ssz_types/big"
	"github.com/rocket-pool/smartnode/shared/services/state"
	"github.com/rocket-pool/smartnode/shared/utils/log"
)

// Implementation for tree generator ruleset v9 with rolling record support
type treeGeneratorImpl_v9_v10_rolling struct {
	networkState                 *state.NetworkState
	rewardsFile                  *ssz_types.SSZFile_v1
	elSnapshotHeader             *types.Header
	snapshotEnd                  *SnapshotEnd
	log                          *log.ColorLogger
	logPrefix                    string
	rp                           RewardsExecutionClient
	previousRewardsPoolAddresses []common.Address
	bc                           RewardsBeaconClient
	opts                         *bind.CallOpts
	smoothingPoolBalance         *big.Int
	intervalDutiesInfo           *IntervalDutiesInfo
	slotsPerEpoch                uint64
	validatorIndexMap            map[string]*MinipoolInfo
	elStartTime                  time.Time
	elEndTime                    time.Time
	validNetworkCache            map[uint64]bool
	epsilon                      *big.Int
	intervalSeconds              *big.Int
	beaconConfig                 beacon.Eth2Config
	rollingRecord                *RollingRecord
	nodeDetails                  map[common.Address]*NodeSmoothingDetails
	invalidNetworkNodes          map[common.Address]uint64
	minipoolPerformanceFile      *MinipoolPerformanceFile_v2
	nodeRewards                  map[common.Address]*ssz_types.NodeReward
	networkRewards               map[ssz_types.Layer]*ssz_types.NetworkReward
}

// Create a new tree generator
func newTreeGeneratorImpl_v9_v10_rolling(rulesetVersion uint64, log *log.ColorLogger, logPrefix string, index uint64, snapshotEnd *SnapshotEnd, elSnapshotHeader *types.Header, intervalsPassed uint64, state *state.NetworkState, rollingRecord *RollingRecord) *treeGeneratorImpl_v9_v10_rolling {
	return &treeGeneratorImpl_v9_v10_rolling{
		rewardsFile: &ssz_types.SSZFile_v1{
			RewardsFileVersion: 3,
			RulesetVersion:     rulesetVersion,
			Index:              index,
			IntervalsPassed:    intervalsPassed,
			TotalRewards: &ssz_types.TotalRewards{
				ProtocolDaoRpl:               sszbig.NewUint256(0),
				TotalCollateralRpl:           sszbig.NewUint256(0),
				TotalOracleDaoRpl:            sszbig.NewUint256(0),
				TotalSmoothingPoolEth:        sszbig.NewUint256(0),
				PoolStakerSmoothingPoolEth:   sszbig.NewUint256(0),
				NodeOperatorSmoothingPoolEth: sszbig.NewUint256(0),
				TotalNodeWeight:              sszbig.NewUint256(0),
			},
			NetworkRewards: ssz_types.NetworkRewards{},
			NodeRewards:    ssz_types.NodeRewards{},
		},
		validatorIndexMap:   map[string]*MinipoolInfo{},
		elSnapshotHeader:    elSnapshotHeader,
		snapshotEnd:         snapshotEnd,
		log:                 log,
		logPrefix:           logPrefix,
		networkState:        state,
		rollingRecord:       rollingRecord,
		invalidNetworkNodes: map[common.Address]uint64{},
		minipoolPerformanceFile: &MinipoolPerformanceFile_v2{
			Index:               index,
			MinipoolPerformance: map[common.Address]*SmoothingPoolMinipoolPerformance_v2{},
		},
		nodeRewards:    map[common.Address]*ssz_types.NodeReward{},
		networkRewards: map[ssz_types.Layer]*ssz_types.NetworkReward{},
	}
}

// Get the version of the ruleset used by this generator
func (r *treeGeneratorImpl_v9_v10_rolling) getRulesetVersion() uint64 {
	return r.rewardsFile.RulesetVersion
}

func (r *treeGeneratorImpl_v9_v10_rolling) generateTree(rp RewardsExecutionClient, networkName string, previousRewardsPoolAddresses []common.Address, bc RewardsBeaconClient) (*GenerateTreeResult, error) {

	r.log.Printlnf("%s Generating tree using Ruleset v%d.", r.logPrefix, r.rewardsFile.RulesetVersion)

	// Provision some struct params
	r.rp = rp
	r.previousRewardsPoolAddresses = previousRewardsPoolAddresses
	r.bc = bc
	r.validNetworkCache = map[uint64]bool{
		0: true,
	}

	// Set the network name
	r.rewardsFile.Network, _ = ssz_types.NetworkFromString(networkName)
	r.minipoolPerformanceFile.Network = networkName
	r.minipoolPerformanceFile.RewardsFileVersion = r.rewardsFile.RewardsFileVersion
	r.minipoolPerformanceFile.RulesetVersion = r.rewardsFile.RulesetVersion

	// Get the Beacon config
	r.beaconConfig = r.networkState.BeaconConfig
	r.slotsPerEpoch = r.beaconConfig.SlotsPerEpoch

	// Set the EL client call opts
	r.opts = &bind.CallOpts{
		BlockNumber: r.elSnapshotHeader.Number,
	}

	r.log.Printlnf("%s Creating tree for %d nodes", r.logPrefix, len(r.networkState.NodeDetails))

	// Get the max of node count and minipool count - this will be used for an error epsilon due to division truncation
	nodeCount := len(r.networkState.NodeDetails)
	minipoolCount := len(r.networkState.MinipoolDetails)
	if nodeCount > minipoolCount {
		r.epsilon = big.NewInt(int64(nodeCount))
	} else {
		r.epsilon = big.NewInt(int64(minipoolCount))
	}

	// Calculate the RPL rewards
	err := r.calculateRplRewards()
	if err != nil {
		return nil, fmt.Errorf("error calculating RPL rewards: %w", err)
	}

	// Calculate the ETH rewards
	err = r.calculateEthRewards(true)
	if err != nil {
		return nil, fmt.Errorf("error calculating ETH rewards: %w", err)
	}

	// Sort and assign the maps to the ssz file lists
	for nodeAddress, nodeReward := range r.nodeRewards {
		copy(nodeReward.Address[:], nodeAddress[:])
		r.rewardsFile.NodeRewards = append(r.rewardsFile.NodeRewards, nodeReward)
	}

	for layer, networkReward := range r.networkRewards {
		networkReward.Network = layer
		r.rewardsFile.NetworkRewards = append(r.rewardsFile.NetworkRewards, networkReward)
	}

	// Generate the Merkle Tree
	err = r.rewardsFile.GenerateMerkleTree()
	if err != nil {
		return nil, fmt.Errorf("error generating Merkle tree: %w", err)
	}

	// Sort all of the missed attestations so the files are always generated in the same state
	for _, minipoolInfo := range r.minipoolPerformanceFile.MinipoolPerformance {
		sort.Slice(minipoolInfo.MissingAttestationSlots, func(i, j int) bool {
			return minipoolInfo.MissingAttestationSlots[i] < minipoolInfo.MissingAttestationSlots[j]
		})
	}

	return &GenerateTreeResult{
		RewardsFile:             r.rewardsFile,
		InvalidNetworkNodes:     r.invalidNetworkNodes,
		MinipoolPerformanceFile: r.minipoolPerformanceFile,
	}, nil

}

// Quickly calculates an approximate of the staker's share of the smoothing pool balance without processing Beacon performance
// Used for approximate returns in the rETH ratio update
func (r *treeGeneratorImpl_v9_v10_rolling) approximateStakerShareOfSmoothingPool(rp RewardsExecutionClient, networkName string, bc RewardsBeaconClient) (*big.Int, error) {
	r.log.Printlnf("%s Approximating tree using Ruleset v%d.", r.logPrefix, r.rewardsFile.RulesetVersion)

	r.rp = rp
	r.bc = bc
	r.validNetworkCache = map[uint64]bool{
		0: true,
	}

	// Set the network name
	r.rewardsFile.Network, _ = ssz_types.NetworkFromString(networkName)
	r.minipoolPerformanceFile.Network = networkName
	r.minipoolPerformanceFile.RewardsFileVersion = r.rewardsFile.RewardsFileVersion
	r.minipoolPerformanceFile.RulesetVersion = r.rewardsFile.RulesetVersion

	// Get the Beacon config
	r.beaconConfig = r.networkState.BeaconConfig
	r.slotsPerEpoch = r.beaconConfig.SlotsPerEpoch

	// Set the EL client call opts
	r.opts = &bind.CallOpts{
		BlockNumber: r.elSnapshotHeader.Number,
	}

	r.log.Printlnf("%s Creating tree for %d nodes", r.logPrefix, len(r.networkState.NodeDetails))

	// Get the max of node count and minipool count - this will be used for an error epsilon due to division truncation
	nodeCount := len(r.networkState.NodeDetails)
	minipoolCount := len(r.networkState.MinipoolDetails)
	if nodeCount > minipoolCount {
		r.epsilon = big.NewInt(int64(nodeCount))
	} else {
		r.epsilon = big.NewInt(int64(minipoolCount))
	}

	// Calculate the ETH rewards
	err := r.calculateEthRewards(false)
	if err != nil {
		return nil, fmt.Errorf("error calculating ETH rewards: %w", err)
	}

	return r.rewardsFile.TotalRewards.PoolStakerSmoothingPoolEth.Int, nil
}

func (r *treeGeneratorImpl_v9_v10_rolling) calculateNodeRplRewards(
	collateralRewards *big.Int,
	nodeWeight *big.Int,
	totalNodeWeight *big.Int,
) *big.Int {

	if nodeWeight.Sign() <= 0 {
		return big.NewInt(0)
	}

	// (collateralRewards * nodeWeight / totalNodeWeight)
	rpip30Rewards := big.NewInt(0).Mul(collateralRewards, nodeWeight)
	rpip30Rewards.Quo(rpip30Rewards, totalNodeWeight)

	return rpip30Rewards
}

// Calculates the RPL rewards for the given interval
func (r *treeGeneratorImpl_v9_v10_rolling) calculateRplRewards() error {
	pendingRewards := r.networkState.NetworkDetails.PendingRPLRewards
	r.log.Printlnf("%s Pending RPL rewards: %s (%.3f)", r.logPrefix, pendingRewards.String(), eth.WeiToEth(pendingRewards))
	if pendingRewards.Cmp(common.Big0) == 0 {
		return fmt.Errorf("there are no pending RPL rewards, so this interval cannot be used for rewards submission")
	}

	// Get baseline Protocol DAO rewards
	pDaoPercent := r.networkState.NetworkDetails.ProtocolDaoRewardsPercent
	pDaoRewards := big.NewInt(0)
	pDaoRewards.Mul(pendingRewards, pDaoPercent)
	pDaoRewards.Div(pDaoRewards, oneEth)
	r.log.Printlnf("%s Expected Protocol DAO rewards: %s (%.3f)", r.logPrefix, pDaoRewards.String(), eth.WeiToEth(pDaoRewards))

	// Get node operator rewards
	nodeOpPercent := r.networkState.NetworkDetails.NodeOperatorRewardsPercent
	totalNodeRewards := big.NewInt(0)
	totalNodeRewards.Mul(pendingRewards, nodeOpPercent)
	totalNodeRewards.Div(totalNodeRewards, oneEth)
	r.log.Printlnf("%s Approx. total collateral RPL rewards: %s (%.3f)", r.logPrefix, totalNodeRewards.String(), eth.WeiToEth(totalNodeRewards))

	// Calculate the RPIP-30 weight of each node, scaling by their participation in this interval
	nodeWeights, totalNodeWeight, err := r.networkState.CalculateNodeWeights()
	if err != nil {
		return fmt.Errorf("error calculating node weights: %w", err)
	}

	// Operate normally if any node has rewards
	if totalNodeWeight.Sign() > 0 {
		// Make sure to record totalNodeWeight in the rewards file
		r.rewardsFile.TotalRewards.TotalNodeWeight.Set(totalNodeWeight)

		r.log.Printlnf("%s Calculating individual collateral rewards...", r.logPrefix)
		for i, nodeDetails := range r.networkState.NodeDetails {
			// Get how much RPL goes to this node
			nodeRplRewards := r.calculateNodeRplRewards(
				totalNodeRewards,
				nodeWeights[nodeDetails.NodeAddress],
				totalNodeWeight,
			)

			// If there are pending rewards, add it to the map
			if nodeRplRewards.Sign() == 1 {
				rewardsForNode, exists := r.nodeRewards[nodeDetails.NodeAddress]
				if !exists {
					// Get the network the rewards should go to
					network := r.networkState.NodeDetails[i].RewardNetwork.Uint64()
					validNetwork, err := r.validateNetwork(network)
					if err != nil {
						return err
					}
					if !validNetwork {
						r.invalidNetworkNodes[nodeDetails.NodeAddress] = network
						network = 0
					}

					rewardsForNode = ssz_types.NewNodeReward(
						network,
						ssz_types.AddressFromBytes(nodeDetails.NodeAddress.Bytes()),
					)
					r.nodeRewards[nodeDetails.NodeAddress] = rewardsForNode
				}
				rewardsForNode.CollateralRpl.Add(rewardsForNode.CollateralRpl.Int, nodeRplRewards)

				// Add the rewards to the running total for the specified network
				rewardsForNetwork, exists := r.networkRewards[rewardsForNode.Network]
				if !exists {
					rewardsForNetwork = ssz_types.NewNetworkReward(rewardsForNode.Network)
					r.networkRewards[rewardsForNode.Network] = rewardsForNetwork
				}
				rewardsForNetwork.CollateralRpl.Add(rewardsForNetwork.CollateralRpl.Int, nodeRplRewards)
			}
		}

		// Sanity check to make sure we arrived at the correct total
		delta := big.NewInt(0)
		totalCalculatedNodeRewards := big.NewInt(0)
		for _, networkRewards := range r.networkRewards {
			totalCalculatedNodeRewards.Add(totalCalculatedNodeRewards, networkRewards.CollateralRpl.Int)
		}
		delta.Sub(totalNodeRewards, totalCalculatedNodeRewards).Abs(delta)
		if delta.Cmp(r.epsilon) == 1 {
			return fmt.Errorf("error calculating collateral RPL: total was %s, but expected %s; error was too large", totalCalculatedNodeRewards.String(), totalNodeRewards.String())
		}
		r.rewardsFile.TotalRewards.TotalCollateralRpl.Int.Set(totalCalculatedNodeRewards)
		r.log.Printlnf("%s Calculated rewards:           %s (error = %s wei)", r.logPrefix, totalCalculatedNodeRewards.String(), delta.String())
		pDaoRewards.Sub(pendingRewards, totalCalculatedNodeRewards)
	} else {
		// In this situation, none of the nodes in the network had eligible rewards so send it all to the pDAO
		pDaoRewards.Add(pDaoRewards, totalNodeRewards)
		r.log.Printlnf("%s None of the nodes were eligible for collateral rewards, sending everything to the pDAO; now at %s (%.3f)", r.logPrefix, pDaoRewards.String(), eth.WeiToEth(pDaoRewards))
	}

	// Handle Oracle DAO rewards
	oDaoPercent := r.networkState.NetworkDetails.TrustedNodeOperatorRewardsPercent
	totalODaoRewards := big.NewInt(0)
	totalODaoRewards.Mul(pendingRewards, oDaoPercent)
	totalODaoRewards.Div(totalODaoRewards, oneEth)
	r.log.Printlnf("%s Total Oracle DAO RPL rewards: %s (%.3f)", r.logPrefix, totalODaoRewards.String(), eth.WeiToEth(totalODaoRewards))

	oDaoDetails := r.networkState.OracleDaoMemberDetails

	// Calculate the true effective time of each oDAO node based on their participation in this interval
	totalODaoNodeTime := big.NewInt(0)
	trueODaoNodeTimes := map[common.Address]*big.Int{}
	for _, details := range oDaoDetails {
		// Get the timestamp of the node joining the oDAO
		joinTime := details.JoinedTime

		// Get the actual effective time, scaled based on participation
		intervalDuration := r.networkState.NetworkDetails.IntervalDuration
		intervalDurationBig := big.NewInt(int64(intervalDuration.Seconds()))
		participationTime := big.NewInt(0).Set(intervalDurationBig)
		snapshotBlockTime := time.Unix(int64(r.elSnapshotHeader.Time), 0)
		eligibleDuration := snapshotBlockTime.Sub(joinTime)
		if eligibleDuration < intervalDuration {
			participationTime = big.NewInt(int64(eligibleDuration.Seconds()))
		}
		trueODaoNodeTimes[details.Address] = participationTime

		// Add it to the total
		totalODaoNodeTime.Add(totalODaoNodeTime, participationTime)
	}

	for _, details := range oDaoDetails {
		address := details.Address

		// Calculate the oDAO rewards for the node: (participation time) * (total oDAO rewards) / (total participation time)
		individualOdaoRewards := big.NewInt(0)
		individualOdaoRewards.Mul(trueODaoNodeTimes[address], totalODaoRewards)
		individualOdaoRewards.Div(individualOdaoRewards, totalODaoNodeTime)

		rewardsForNode, exists := r.nodeRewards[address]
		if !exists {
			// Get the network the rewards should go to
			network := r.networkState.NodeDetailsByAddress[address].RewardNetwork.Uint64()
			validNetwork, err := r.validateNetwork(network)
			if err != nil {
				return err
			}
			if !validNetwork {
				r.invalidNetworkNodes[address] = network
				network = 0
			}

			rewardsForNode = ssz_types.NewNodeReward(
				network,
				ssz_types.AddressFromBytes(address.Bytes()),
			)
			r.nodeRewards[address] = rewardsForNode

		}
		rewardsForNode.OracleDaoRpl.Add(rewardsForNode.OracleDaoRpl.Int, individualOdaoRewards)

		// Add the rewards to the running total for the specified network
		rewardsForNetwork, exists := r.networkRewards[rewardsForNode.Network]
		if !exists {
			rewardsForNetwork = ssz_types.NewNetworkReward(rewardsForNode.Network)
			r.networkRewards[rewardsForNode.Network] = rewardsForNetwork
		}
		rewardsForNetwork.OracleDaoRpl.Add(rewardsForNetwork.OracleDaoRpl.Int, individualOdaoRewards)
	}

	// Sanity check to make sure we arrived at the correct total
	totalCalculatedOdaoRewards := big.NewInt(0)
	delta := big.NewInt(0)
	for _, networkRewards := range r.networkRewards {
		totalCalculatedOdaoRewards.Add(totalCalculatedOdaoRewards, networkRewards.OracleDaoRpl.Int)
	}
	delta.Sub(totalODaoRewards, totalCalculatedOdaoRewards).Abs(delta)
	if delta.Cmp(r.epsilon) == 1 {
		return fmt.Errorf("error calculating ODao RPL: total was %s, but expected %s; error was too large", totalCalculatedOdaoRewards.String(), totalODaoRewards.String())
	}
	r.rewardsFile.TotalRewards.TotalOracleDaoRpl.Int.Set(totalCalculatedOdaoRewards)
	r.log.Printlnf("%s Calculated rewards:           %s (error = %s wei)", r.logPrefix, totalCalculatedOdaoRewards.String(), delta.String())

	// Get actual protocol DAO rewards
	pDaoRewards.Sub(pDaoRewards, totalCalculatedOdaoRewards)
	r.rewardsFile.TotalRewards.ProtocolDaoRpl = sszbig.NewUint256(0)
	r.rewardsFile.TotalRewards.ProtocolDaoRpl.Set(pDaoRewards)
	r.log.Printlnf("%s Actual Protocol DAO rewards:   %s to account for truncation", r.logPrefix, pDaoRewards.String())

	return nil

}

// Calculates the ETH rewards for the given interval
func (r *treeGeneratorImpl_v9_v10_rolling) calculateEthRewards(checkBeaconPerformance bool) error {

	// Get the Smoothing Pool contract's balance
	r.smoothingPoolBalance = r.networkState.NetworkDetails.SmoothingPoolBalance
	r.log.Printlnf("%s Smoothing Pool Balance: %s (%.3f)", r.logPrefix, r.smoothingPoolBalance.String(), eth.WeiToEth(r.smoothingPoolBalance))

	// Ignore the ETH calculation if there are no rewards
	if r.smoothingPoolBalance.Cmp(common.Big0) == 0 {
		return nil
	}

	if r.rewardsFile.Index == 0 {
		// This is the first interval, Smoothing Pool rewards are ignored on the first interval since it doesn't have a discrete start time
		return nil
	}

	startElBlockHeader, err := r.getBlocksAndTimesForInterval()
	if err != nil {
		return err
	}

	r.elStartTime = time.Unix(int64(startElBlockHeader.Time), 0)
	r.elEndTime = time.Unix(int64(r.elSnapshotHeader.Time), 0)
	r.intervalSeconds = big.NewInt(int64(r.elEndTime.Sub(r.elStartTime) / time.Second))

	// Process the attestation performance for each minipool during this interval
	r.intervalDutiesInfo = &IntervalDutiesInfo{
		Index: r.rewardsFile.Index,
		Slots: map[uint64]*SlotInfo{},
	}

	// Determine how much ETH each node gets and how much the pool stakers get
	poolStakerETH, nodeOpEth, bonusScalar, err := r.calculateNodeRewards()
	if err != nil {
		return err
	}
	if r.rewardsFile.RulesetVersion >= 10 {
		r.minipoolPerformanceFile.BonusScalar = QuotedBigIntFromBigInt(bonusScalar)
	}

	// Update the rewards maps
	for nodeAddress, nodeInfo := range r.nodeDetails {
		if nodeInfo.SmoothingPoolEth.Cmp(common.Big0) > 0 {
			rewardsForNode, exists := r.nodeRewards[nodeAddress]
			if !exists {
				network := nodeInfo.RewardsNetwork
				validNetwork, err := r.validateNetwork(network)
				if err != nil {
					return err
				}
				if !validNetwork {
					r.invalidNetworkNodes[nodeAddress] = network
					network = 0
				}

				rewardsForNode = ssz_types.NewNodeReward(
					network,
					ssz_types.AddressFromBytes(nodeAddress.Bytes()),
				)
				r.nodeRewards[nodeAddress] = rewardsForNode
			}
			rewardsForNode.SmoothingPoolEth.Add(rewardsForNode.SmoothingPoolEth.Int, nodeInfo.SmoothingPoolEth)

			// Add minipool rewards to the JSON
			for _, minipoolInfo := range nodeInfo.Minipools {
				successfulAttestations := uint64(minipoolInfo.AttestationCount)
				missingAttestations := uint64(len(minipoolInfo.MissingAttestationSlots))
				performance := &SmoothingPoolMinipoolPerformance_v2{
					Pubkey:                  minipoolInfo.ValidatorPubkey.Hex(),
					SuccessfulAttestations:  successfulAttestations,
					MissedAttestations:      missingAttestations,
					AttestationScore:        minipoolInfo.AttestationScore,
					EthEarned:               QuotedBigIntFromBigInt(minipoolInfo.MinipoolShare),
					BonusEthEarned:          QuotedBigIntFromBigInt(minipoolInfo.MinipoolBonus),
					ConsensusIncome:         minipoolInfo.ConsensusIncome,
					EffectiveCommission:     QuotedBigIntFromBigInt(minipoolInfo.TotalFee),
					MissingAttestationSlots: []uint64{},
				}
				if successfulAttestations+missingAttestations == 0 {
					// Don't include minipools that have zero attestations
					continue
				}
				for slot := range minipoolInfo.MissingAttestationSlots {
					performance.MissingAttestationSlots = append(performance.MissingAttestationSlots, slot)
				}
				r.minipoolPerformanceFile.MinipoolPerformance[minipoolInfo.Address] = performance
			}

			// Add the rewards to the running total for the specified network
			rewardsForNetwork, exists := r.networkRewards[rewardsForNode.Network]
			if !exists {
				rewardsForNetwork = ssz_types.NewNetworkReward(rewardsForNode.Network)
				r.networkRewards[rewardsForNode.Network] = rewardsForNetwork
			}
			rewardsForNetwork.SmoothingPoolEth.Add(rewardsForNetwork.SmoothingPoolEth.Int, nodeInfo.SmoothingPoolEth)
		}
	}

	// Set the totals
	r.rewardsFile.TotalRewards.PoolStakerSmoothingPoolEth.Set(poolStakerETH)
	r.rewardsFile.TotalRewards.NodeOperatorSmoothingPoolEth.Set(nodeOpEth)
	r.rewardsFile.TotalRewards.TotalSmoothingPoolEth.Set(r.smoothingPoolBalance)
	return nil

}

func (r *treeGeneratorImpl_v9_v10_rolling) calculateNodeBonuses() (*big.Int, error) {
	totalConsensusBonus := big.NewInt(0)
	for _, nsd := range r.nodeDetails {
		if !nsd.IsEligible {
			continue
		}

		// Get the nodeDetails from the network state
		nodeDetails := r.networkState.NodeDetailsByAddress[nsd.Address]
		eligibleBorrowedEth := r.networkState.GetEligibleBorrowedEth(nodeDetails)
		_, percentOfBorrowedEth := r.networkState.GetStakedRplValueInEthAndPercentOfBorrowedEth(eligibleBorrowedEth, nodeDetails.RplStake)
		for _, mpd := range nsd.Minipools {
			mpi := r.networkState.MinipoolDetailsByAddress[mpd.Address]
			fee := mpi.NodeFee
			if !mpd.IsEligibleForBonuses() {
				mpd.MinipoolBonus = nil
				mpd.ConsensusIncome = nil
				continue
			}
			// fee = max(fee, 0.10 Eth + (0.04 Eth * min(10 Eth, percentOfBorrowedETH) / 10 Eth))
			_min := big.NewInt(0).Set(tenEth)
			if _min.Cmp(percentOfBorrowedEth) > 0 {
				_min.Set(percentOfBorrowedEth)
			}
			dividend := _min.Mul(_min, pointOhFourEth)
			divResult := dividend.Div(dividend, tenEth)
			feeWithBonus := divResult.Add(divResult, pointOneEth)
			if fee.Cmp(feeWithBonus) >= 0 {
				// This minipool won't get any bonuses, so skip it
				mpd.MinipoolBonus = nil
				mpd.ConsensusIncome = nil
				continue
			}
			// This minipool will get a bonus
			// It is safe to populate the optional fields from here on.

			fee = feeWithBonus
			// Save fee as totalFee for the Minipool
			mpd.TotalFee = fee

			// Total fee for a minipool with a bonus shall never exceed 14%
			if fee.Cmp(fourteenPercentEth) > 0 {
				r.log.Printlnf("WARNING: Minipool %s has a fee of %s, which is greater than the maximum allowed of 14%", mpd.Address.Hex(), fee.String())
				r.log.Printlnf("WARNING: Aborting.")
				return nil, fmt.Errorf("minipool %s has a fee of %s, which is greater than the maximum allowed of 14%%", mpd.Address.Hex(), fee.String())
			}
			bonusFee := big.NewInt(0).Set(fee)
			bonusFee.Sub(bonusFee, mpi.NodeFee)
			consensusIncome := big.NewInt(0).Set(&mpd.ConsensusIncome.Int)
			bonusShare := bonusFee.Mul(bonusFee, big.NewInt(0).Sub(thirtyTwoEth, mpi.NodeDepositBalance))
			bonusShare.Div(bonusShare, thirtyTwoEth)
			minipoolBonus := consensusIncome.Mul(consensusIncome, bonusShare)
			minipoolBonus.Div(minipoolBonus, oneEth)
			if minipoolBonus.Sign() == -1 {
				minipoolBonus = big.NewInt(0)
			}
			mpd.MinipoolBonus = minipoolBonus
			totalConsensusBonus.Add(totalConsensusBonus, minipoolBonus)
			nsd.BonusEth.Add(nsd.BonusEth, minipoolBonus)
		}
	}
	return totalConsensusBonus, nil
}

// Calculate the distribution of Smoothing Pool ETH to each node
func (r *treeGeneratorImpl_v9_v10_rolling) calculateNodeRewards() (*big.Int, *big.Int, *big.Int, error) {
	var err error
	bonusScalar := big.NewInt(0).Set(oneEth)

	// Get the list of cheaters
	cheaters := r.getCheaters()

	// Get the latest scores from the rolling record
	minipools, totalScore, attestationCount := r.rollingRecord.GetScores(cheaters)

	// If there weren't any successful attestations, everything goes to the pool stakers
	if totalScore.Cmp(common.Big0) == 0 || attestationCount == 0 {
		r.log.Printlnf("WARNING: Total attestation score = %s, successful attestations = %d... sending the whole smoothing pool balance to the pool stakers.", totalScore.String(), attestationCount)
		return r.smoothingPoolBalance, big.NewInt(0), bonusScalar, nil
	}

	totalEthForMinipools := big.NewInt(0)
	totalNodeOpShare := big.NewInt(0)
	totalNodeOpShare.Mul(r.smoothingPoolBalance, totalScore)
	totalNodeOpShare.Div(totalNodeOpShare, big.NewInt(int64(attestationCount)))
	totalNodeOpShare.Div(totalNodeOpShare, oneEth)

	r.nodeDetails = map[common.Address]*NodeSmoothingDetails{}
	for _, minipool := range minipools {
		nnd := r.networkState.NodeDetailsByAddress[minipool.NodeAddress]
		nmd := r.networkState.MinipoolDetailsByAddress[minipool.Address]
		// Get the node amount
		nodeInfo, exists := r.nodeDetails[minipool.NodeAddress]
		nodeIsCheater := cheaters[minipool.NodeAddress]
		if !exists {
			nodeInfo = &NodeSmoothingDetails{
				Address:          nnd.NodeAddress,
				IsEligible:       !nodeIsCheater,
				Minipools:        []*MinipoolInfo{},
				SmoothingPoolEth: big.NewInt(0),
				BonusEth:         big.NewInt(0),
				RewardsNetwork:   nnd.RewardNetwork.Uint64(),
			}
			nodeInfo.IsOptedIn = nnd.SmoothingPoolRegistrationState
			statusChangeTimeBig := nnd.SmoothingPoolRegistrationChanged
			statusChangeTime := time.Unix(statusChangeTimeBig.Int64(), 0)
			if nodeInfo.IsOptedIn {
				nodeInfo.OptInTime = statusChangeTime
				nodeInfo.OptOutTime = time.Unix(farFutureTimestamp, 0)
			} else {
				nodeInfo.OptOutTime = statusChangeTime
				nodeInfo.OptInTime = time.Unix(farPastTimestamp, 0)
			}
			r.nodeDetails[minipool.NodeAddress] = nodeInfo
		}
		// Populate the minipool NodeOperatorBond
		minipool.NodeOperatorBond = nmd.NodeDepositBalance
		nodeInfo.Minipools = append(nodeInfo.Minipools, minipool)

		// Add the minipool's score to the total node score
		minipoolEth := big.NewInt(0).Set(totalNodeOpShare)
		minipoolEth.Mul(minipoolEth, &minipool.AttestationScore.Int)
		minipoolEth.Div(minipoolEth, totalScore)
		minipool.MinipoolShare = minipoolEth
		nodeInfo.SmoothingPoolEth.Add(nodeInfo.SmoothingPoolEth, minipoolEth)
	}

	// Add the node amounts to the total
	for _, nodeInfo := range r.nodeDetails {
		totalEthForMinipools.Add(totalEthForMinipools, nodeInfo.SmoothingPoolEth)
	}

	if r.rewardsFile.RulesetVersion >= 10 {
		// Calculate the minipool bonuses
		isEligibleInterval := true // TODO - check on-chain for saturn 1
		var totalConsensusBonus *big.Int
		if isEligibleInterval {
			totalConsensusBonus, err = r.calculateNodeBonuses()
			if err != nil {
				return nil, nil, nil, err
			}
		}

		remainingBalance := big.NewInt(0).Sub(r.smoothingPoolBalance, totalEthForMinipools)
		if remainingBalance.Cmp(totalConsensusBonus) < 0 {
			r.log.Printlnf("WARNING: Remaining balance is less than total consensus bonus... Balance = %s, total consensus bonus = %s", remainingBalance.String(), totalConsensusBonus.String())
			// Scale bonuses down to fit the remaining balance
			bonusScalar.Div(big.NewInt(0).Mul(remainingBalance, oneEth), totalConsensusBonus)
			for _, nsd := range r.nodeDetails {
				nsd.BonusEth.Mul(nsd.BonusEth, remainingBalance)
				nsd.BonusEth.Div(nsd.BonusEth, totalConsensusBonus)
				// Calculate the reduced bonus for each minipool
				// Because of integer division, this will be less than the actual bonus by up to 1 wei
				for _, mpd := range nsd.Minipools {
					mpd.MinipoolBonus.Mul(mpd.MinipoolBonus, remainingBalance)
					mpd.MinipoolBonus.Div(mpd.MinipoolBonus, totalConsensusBonus)
				}
			}
		}
	}

	// Sanity check the totalNodeOpShare before bonuses are awarded
	delta := big.NewInt(0).Sub(totalEthForMinipools, totalNodeOpShare)
	delta.Abs(delta)
	if delta.Cmp(r.epsilon) == 1 {
		return nil, nil, nil, fmt.Errorf("error calculating smoothing pool ETH: total was %s, but expected %s; error was too large (%s wei)", totalEthForMinipools.String(), totalNodeOpShare.String(), delta.String())
	}

	// Finally, award the bonuses
	if r.rewardsFile.RulesetVersion >= 10 {
		for _, nsd := range r.nodeDetails {
			nsd.SmoothingPoolEth.Add(nsd.SmoothingPoolEth, nsd.BonusEth)
			totalEthForMinipools.Add(totalEthForMinipools, nsd.BonusEth)
		}
	}

	// This is how much actually goes to the pool stakers - it should ideally be equal to poolStakerShare but this accounts for any cumulative floating point errors
	truePoolStakerAmount := big.NewInt(0).Sub(r.smoothingPoolBalance, totalEthForMinipools)

	// Calculate the staking pool share and the node op share
	poolStakerShareBeforeBonuses := big.NewInt(0).Sub(r.smoothingPoolBalance, totalNodeOpShare)

	r.log.Printlnf("%s Pool staker ETH before bonuses:    %s (%.3f)", r.logPrefix, poolStakerShareBeforeBonuses.String(), eth.WeiToEth(poolStakerShareBeforeBonuses))
	r.log.Printlnf("%s Pool staker ETH after bonuses:     %s (%.3f)", r.logPrefix, truePoolStakerAmount.String(), eth.WeiToEth(truePoolStakerAmount))
	r.log.Printlnf("%s Node Op ETH before bonuses:        %s (%.3f)", r.logPrefix, totalNodeOpShare.String(), eth.WeiToEth(totalNodeOpShare))
	r.log.Printlnf("%s Node Op ETH after bonuses:         %s (%.3f)", r.logPrefix, totalEthForMinipools.String(), eth.WeiToEth(totalEthForMinipools))
	r.log.Printlnf("%s (error = %s wei)", r.logPrefix, delta.String())
	r.log.Printlnf("%s Adjusting pool staker ETH to %s to account for truncation", r.logPrefix, truePoolStakerAmount.String())

	return truePoolStakerAmount, totalEthForMinipools, bonusScalar, nil

}

// Validates that the provided network is legal
func (r *treeGeneratorImpl_v9_v10_rolling) validateNetwork(network uint64) (bool, error) {
	valid, exists := r.validNetworkCache[network]
	if !exists {
		var err error
		valid, err = r.rp.GetNetworkEnabled(big.NewInt(int64(network)), r.opts)
		if err != nil {
			return false, err
		}
		r.validNetworkCache[network] = valid
	}

	return valid, nil
}

// Gets the EL header for the given interval's start block
func (r *treeGeneratorImpl_v9_v10_rolling) getBlocksAndTimesForInterval() (*types.Header, error) {

	// Get the Beacon block for the start slot of the record
	r.rewardsFile.ConsensusStartBlock = r.rollingRecord.StartSlot
	r.minipoolPerformanceFile.ConsensusStartBlock = r.rollingRecord.StartSlot
	beaconBlock, exists, err := r.bc.GetBeaconBlock(fmt.Sprint(r.rollingRecord.StartSlot))
	if err != nil {
		return nil, fmt.Errorf("error verifying block from interval start: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("couldn't retrieve CL block from interval start (slot %d); this likely means you checkpoint sync'd your Beacon Node and it has not backfilled to the previous interval yet so it cannot be used for tree generation", r.rollingRecord.StartSlot)
	}

	// Get the EL block for that Beacon block
	elBlockNumber := beaconBlock.ExecutionBlockNumber
	r.rewardsFile.ExecutionStartBlock = elBlockNumber
	r.minipoolPerformanceFile.ExecutionStartBlock = r.rewardsFile.ExecutionStartBlock
	startElHeader, err := r.rp.HeaderByNumber(context.Background(), big.NewInt(int64(elBlockNumber)))
	if err != nil {
		return nil, fmt.Errorf("error getting EL header for block %d: %w", elBlockNumber, err)
	}

	r.rewardsFile.ConsensusEndBlock = r.snapshotEnd.ConsensusBlock
	r.minipoolPerformanceFile.ConsensusEndBlock = r.snapshotEnd.ConsensusBlock

	r.rewardsFile.ExecutionEndBlock = r.snapshotEnd.ExecutionBlock
	r.minipoolPerformanceFile.ExecutionEndBlock = r.snapshotEnd.ExecutionBlock

	// rollingRecord.StartSlot is the first non-missing slot, so it isn't suitable for startTime, but can be used for startBlock
	// it can safely be assumed to be in the same epoch, due to the implementation of GetStartSlotForInterval.
	// Calculate the time of the first slot in that epoch.
	startTime := r.beaconConfig.GetSlotTime((r.rollingRecord.StartSlot / r.beaconConfig.SlotsPerEpoch) * r.beaconConfig.SlotsPerEpoch)

	r.rewardsFile.StartTime = startTime
	r.minipoolPerformanceFile.StartTime = startTime

	endTime := r.beaconConfig.GetSlotTime(r.snapshotEnd.Slot)
	r.rewardsFile.EndTime = endTime
	r.minipoolPerformanceFile.EndTime = endTime

	return startElHeader, nil
}

// Detect and flag any cheaters
func (r *treeGeneratorImpl_v9_v10_rolling) getCheaters() map[common.Address]bool {
	cheatingNodes := map[common.Address]bool{}
	three := big.NewInt(3)

	for _, nd := range r.networkState.NodeDetails {
		for _, mpd := range r.networkState.MinipoolDetailsByNode[nd.NodeAddress] {
			if mpd.PenaltyCount.Cmp(three) >= 0 {
				// If any minipool has 3+ penalties, ban the entire node
				cheatingNodes[nd.NodeAddress] = true
				break
			}
		}
	}

	return cheatingNodes
}

func (r *treeGeneratorImpl_v9_v10_rolling) saveFiles(smartnode *config.SmartnodeConfig, treeResult *GenerateTreeResult, nodeTrusted bool) (cid.Cid, map[string]cid.Cid, error) {
	return saveRewardsArtifacts(smartnode, treeResult, nodeTrusted)
}
