//go:build !devmode

package nstsol

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/cosmos/gogoproto/proto"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

func (s *source) fetch(token string) (*types.PriceInfo, error) {
	// check epoch, when epoch updated, update effective-balance
	if types.NSTToken(token) != types.NativeTokenSOL {
		return nil, feedertypes.ErrTokenNotSupported.Wrap(fmt.Sprintf("only support native-sol-restaking %s, got:%s", types.NativeTokenSOL, token))
	}

	// use 'no copy' version to avoid copying stakers
	sInfos, version := s.Stakers.GetStakersNoCopy()
	if len(sInfos) == 0 {
		// return zero price when there's no stakers
		return &types.PriceInfo{}, nil
	}

	// check if finalized epoch had been updated
	epoch, startSlot, endSlot, _, err := getFinalizedEpoch()
	if err != nil {
		return nil, fmt.Errorf("fail to get finalized epoch from solana, error:%w", err)
	}

	// epoch not updated, just return without fetching since effective-balance has not changed
	if epoch <= finalizedEpoch && version <= finalizedVersion {
		s.Logger.Info("fetch active stake from solana, no change in epoch or version, return latestChangesBytes", "epoch", epoch, "version", version)
		return &types.PriceInfo{
			Price: string(latestChangesBytes),
			// combine epoch and version as roundID in priceInfo
			RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
		}, nil
	}

	s.Logger.Info("fetch active stake from solana", "stakerList_length", len(sInfos), "epoch", epoch, "version", version)
	changedStakerBalances, err := fetchStakerBalanceChanges(sInfos, startSlot, endSlot, s.Logger)
	for err != nil {
		if !errors.Is(err, errExceedsMaxSlot) {
			return nil, err
		}
		s.Logger.Info("fetch active stake from solana, epoch increased during fetching, try latestEpoch, ", "prevEpoch", epoch, "version", version)
		epoch, startSlot, endSlot, _, err = getFinalizedEpoch()
		if err != nil {
			return nil, fmt.Errorf("fail to get finalized epoch from solana, error:%w", err)
		}
		// this should not happen
		if epoch <= finalizedEpoch && version <= finalizedVersion {
			s.Logger.Info("fetch active stake from solana, no change in epoch or version, return latestChangesBytes", "epoch", epoch, "version", version)
			return &types.PriceInfo{
				Price: string(latestChangesBytes),
				// combine epoch and version as roundID in priceInfo
				RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
			}, nil
		}

		s.Logger.Info("retry fetching active stake from solana", "stakerList_length", len(sInfos), "epoch", epoch, "version", version)
		changedStakerBalances, err = fetchStakerBalanceChanges(sInfos, startSlot, endSlot, s.Logger)
	}

	if len(changedStakerBalances) > 0 {
		s.Logger.Info("fetch active stake from solana, some active stake of validators have changed")
		sort.Slice(changedStakerBalances, func(i, j int) bool {
			return changedStakerBalances[i].StakerIndex < changedStakerBalances[j].StakerIndex
		})
		nstBS := oracletypes.RawDataNST{
			Version:           version,
			NstBalanceChanges: changedStakerBalances,
		}
		bz, err := proto.Marshal(&nstBS)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nstBalanceChanges, error:%w", err)
		}
		latestChangesBytes = bz
	} else {
		latestChangesBytes = fetchertypes.NSTZeroChanges
	}
	finalizedEpoch = epoch
	finalizedVersion = version

	return &types.PriceInfo{
		Price:   string(latestChangesBytes),
		RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
	}, nil
}

func fetchStakerBalanceChanges(sInfos fetchertypes.StakerInfos, startSlot, endSlot uint64, logger feedertypes.LoggerInf) ([]*oracletypes.NSTKV, error) {
	changedStakerBalances := make([]*oracletypes.NSTKV, 0, len(sInfos))
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
			validatorBalances, err := getStakeAccounts(tmpValidatorPubkeys, startSlot, endSlot)
			if err != nil {
				return nil, err
			}

			for _, validatorBalance := range validatorBalances {
				stakerBalance += validatorBalance
			}
		}

		validatorBalances, err := getStakeAccounts(validators[i:], startSlot, endSlot)
		if err != nil {
			return nil, err
		}
		for _, validatorBalance := range validatorBalances {
			// this should be initialized from imuad
			stakerBalance += validatorBalance
		}
		if delta := stakerBalance - stakerInfo.Balance; delta != 0 {
			changedStakerBalances = append(changedStakerBalances, &oracletypes.NSTKV{
				StakerIndex: uint32(stakerIdx),
				Balance:     stakerBalance,
			})
			logger.Info("fetched active stake from solana", "staker_index", stakerIdx, "balance_change", delta, "latest_balance", stakerBalance, "validators_count", l)
		}
	}
	return changedStakerBalances, nil
}
