package bsc

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum/common"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const batchSize = 100

var (
	finalizedBlock           uint64
	finalizedVersion         uint64
	finalizedWithdrawVersion uint64

	latestChangesBytes = fetchertypes.NSTZeroChanges
)

func (s *source) fetch(token string) (*fetchertypes.PriceInfo, error) {
	if fetchertypes.NSTToken(token) != fetchertypes.NativeTokenBSC {
		return nil, feedertypes.ErrTokenNotSupported.Wrap(fmt.Sprintf("only support native-eth-restaking %s, got:%s", fetchertypes.NativeTokenBSC, token))
	}
	sInfos, v, wV := s.Stakers.GetStakersNoCopy()
	// return zero price when there's no stakers
	if len(sInfos) == 0 {
		latestChangesBytes = fetchertypes.NSTZeroChanges
		return &fetchertypes.PriceInfo{
			Price: string(latestChangesBytes),
			// combine height and versions as roundID in priceInfo
			RoundID: fmt.Sprintf("%s|%s|%s", strconv.FormatUint(finalizedBlock, 10), strconv.FormatUint(finalizedVersion, 10), strconv.FormatUint(finalizedWithdrawVersion, 10)),
		}, nil
	}
	// Round block height down to nearest 100 for consistency with batch querying
	height := s.getCurrentHeight() / 100 * 100

	// Only skip when all are unchanged (same semantics as other NST fetchers)
	if height <= finalizedBlock && v <= finalizedVersion && wV <= finalizedWithdrawVersion {
		s.Logger().Info("fetch delegators from beaconchain, no change in height(round to 100) or version, return latestChangesBytes", "height", height, "version", finalizedVersion, "withdrawVersion", finalizedWithdrawVersion)
		return &fetchertypes.PriceInfo{
			Price: string(latestChangesBytes),
			// combine height and versions as roundID in priceInfo
			RoundID: fmt.Sprintf("%s|%s|%s", strconv.FormatUint(finalizedBlock, 10), strconv.FormatUint(finalizedVersion, 10), strconv.FormatUint(finalizedWithdrawVersion, 10)),
		}, nil
	}

	// TODO: 20 should be set as a value that less than fetching interval
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	capsules := []common.Address{}
	capsule2Idx := make(map[common.Address]uint32)
	for stakerIdx, sInfo := range sInfos {
		// oracle events now set Validators[0] as capsule address for BSC
		if len(sInfo.Validators) != 1 {
			s.Logger().Error("invalid staker validators length for bsc", "staker_index", stakerIdx, "validators_length", len(sInfo.Validators))
			continue
		}
		capAddr := common.HexToAddress(sInfo.Validators[0])
		capsules = append(capsules, capAddr)
		capsule2Idx[capAddr] = stakerIdx
	}

	jobs := make([]job, 0, (len(capsules)+batchSize-1)/batchSize)
	for aIdx := 0; aIdx < len(capsules); aIdx += batchSize {
		bIdx := aIdx + batchSize
		if bIdx > len(capsules) {
			bIdx = len(capsules)
		}
		jobs = append(jobs, job{
			kind:     jobKindBatchCapsules,
			capsules: capsules[aIdx:bIdx],
			block:    height,
		})
	}
	res, err := s.pool.runBatch(ctx, jobs)
	if err != nil {
		return nil, err
	}
	changedStakerBalances := make([]*oracletypes.NSTKV, 0, len(sInfos))
	for _, capAddr := range capsules {
		sIdx := capsule2Idx[capAddr]
		sInfo := sInfos[sIdx]
		if sInfo.Balance != res[capAddr] || sInfo.WithdrawVersion > finalizedWithdrawVersion {
			changedStakerBalances = append(changedStakerBalances, &oracletypes.NSTKV{
				StakerIndex: sIdx,
				Balance:     res[capAddr],
			})
		}
	}

	if len(changedStakerBalances) > 0 {
		s.Logger().Info("fetched delegator amounts from bsc, some amounts of delegators have changed")
		sort.Slice(changedStakerBalances, func(i, j int) bool {
			return changedStakerBalances[i].StakerIndex < changedStakerBalances[j].StakerIndex
		})
		nstBC := oracletypes.RawDataNST{
			Version:           v,
			WithdrawVersion:   wV,
			NstBalanceChanges: changedStakerBalances,
		}
		bz, err := proto.Marshal(&nstBC)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nstBalanceChanges, error:%w", err)
		}
		latestChangesBytes = bz
	} else {
		s.Logger().Info("fetched delegator amounts from bsc, all amounts remain unchanged")
		latestChangesBytes = fetchertypes.NSTZeroChanges
	}

	finalizedBlock = height
	finalizedVersion = v
	finalizedWithdrawVersion = wV

	return &fetchertypes.PriceInfo{
		Price:   string(latestChangesBytes),
		RoundID: fmt.Sprintf("%s|%s|%s", strconv.FormatUint(finalizedBlock, 10), strconv.FormatUint(v, 10), strconv.FormatUint(wV, 10)),
	}, nil
}

func (s *source) reload(config, token string) error {
	return nil
}
