//go:build !devmode

package beaconchain

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/cosmos/gogoproto/proto"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

func (s *source) fetch(token string) (*types.PriceInfo, error) {
	// check epoch, when epoch updated, update effective-balance
	if types.NSTToken(token) != types.NativeTokenETH {
		return nil, feedertypes.ErrTokenNotSupported.Wrap(fmt.Sprintf("only support native-eth-restaking %s, got:%s", types.NativeTokenETH, token))
	}

	// use 'no copy' version to avoid copying stakers
	sInfos, version := s.stakers.GetStakersNoCopy()
	if len(sInfos) == 0 {
		// return zero price when there's no stakers
		return &types.PriceInfo{}, nil
	}

	// check if finalized epoch had been updated
	epoch, stateRoot, err := getFinalizedEpoch()
	if err != nil {
		return nil, fmt.Errorf("fail to get finalized epoch from beaconchain, error:%w", err)
	}

	// epoch not updated, just return without fetching since effective-balance has not changed
	if epoch <= finalizedEpoch && version <= finalizedVersion {
		return &types.PriceInfo{
			Price: string(latestChangesBytes),
			// combine epoch and version as roundID in priceInfo
			RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
		}, nil
	}

	changedStakerBalances := make([]*oracletypes.NSTKV, 0, len(sInfos))
	s.logger.Info("fetch efb from beaconchain", "stakerList_length", len(sInfos))
	hasEFBChanged := false
	for stakerIdx, stakerInfo := range sInfos {
		validators := stakerInfo.Validators
		stakerBalance := uint64(0)
		// beaconcha.in support at most 100 validators for one request
		l := len(validators)
		i := 0
		for l > 100 {
			tmpValidatorPubkeys := validators[i : i+100]
			i += 100
			l -= 100
			validatorBalances, err := getValidators(tmpValidatorPubkeys, stateRoot)
			if err != nil {
				return nil, fmt.Errorf("failed to get validators from beaconchain, error:%w", err)
			}
			for _, validatorBalance := range validatorBalances {
				stakerBalance += uint64(validatorBalance[1])
			}
		}

		validatorBalances, err := getValidators(validators[i:], stateRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to get validators from beaconchain, error:%w", err)
		}
		for _, validatorBalance := range validatorBalances {
			// this should be initialized from imuad
			stakerBalance += validatorBalance[1]
		}
		if delta := stakerBalance - stakerInfo.Balance; delta != 0 {
			if stakerIdx > math.MaxUint32 {
				return nil, fmt.Errorf("staker index %d is larger than max uint32", stakerIdx)
			}
			changedStakerBalances = append(changedStakerBalances, &oracletypes.NSTKV{
				StakerIndex: uint32(stakerIdx),
				Balance:     stakerBalance,
			})
			s.logger.Info("fetched efb from beaconchain", "staker_index", stakerIdx, "balance_change", delta, "validators_count", l)
			hasEFBChanged = true
		}
	}
	if !hasEFBChanged && len(sInfos) > 0 {
		s.logger.Info("fetch efb from beaconchain, all efbs of validators remains to 32 without any change")
	}
	sort.Slice(changedStakerBalances, func(i, j int) bool {
		return changedStakerBalances[i].StakerIndex < changedStakerBalances[j].StakerIndex
	})

	finalizedEpoch = epoch
	finalizedVersion = version

	nstBS := oracletypes.RawDataNST{
		Version:           version,
		NstBalanceChanges: changedStakerBalances,
	}
	bz, err := proto.Marshal(&nstBS)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal nstBalanceChanges, error:%w", err)
	}
	latestChangesBytes = bz

	return &types.PriceInfo{
		Price:   string(latestChangesBytes),
		RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
	}, nil
}
