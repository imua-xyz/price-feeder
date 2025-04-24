//go:build devmode

// this snippet is not used in devmode build to test nst balance changes
package beaconchain

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/cosmos/gogoproto/proto"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

var (
	lastNSTPrice  string
	stakerChanges = map[uint32]*oracletypes.NSTKV{
		0: {StakerIndex: 0, Balance: 1239},
		1: {StakerIndex: 1, Balance: 9977},
		2: {StakerIndex: 2, Balance: 5566},
		3: {StakerIndex: 3, Balance: 1998292},
		4: {StakerIndex: 4, Balance: 32},
		5: {StakerIndex: 5, Balance: 910},
	}
)

func (s *source) fetch(token string) (*types.PriceInfo, error) {
	// check epoch, when epoch updated, update effective-balance
	if types.NSTToken(token) != types.NativeTokenETH {
		return nil, feedertypes.ErrTokenNotSupported.Wrap(fmt.Sprintf("only support native-eth-restaking %s, got:%s", types.NativeTokenETH, token))
	}

	// stakerValidators, version := defaultStakerValidators.getStakerValidators()
	stakerValidators, version := s.stakers.GetStakersNoCopy()
	if len(stakerValidators) == 0 {
		// return zero price when there's no stakers
		return &types.PriceInfo{}, nil
	}
	if version <= finalizedVersion {
		return &types.PriceInfo{
			Price: lastNSTPrice,
			// combine epoch and version as roundID in priceInfo
			RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
		}, nil

	}
	changes := make([]*oracletypes.NSTKV, 0, len(stakerValidators))
	for stakerIndex, _ := range stakerValidators {
		if stakerIndex > 5 {
			continue
		}
		changes = append(changes, stakerChanges[uint32(stakerIndex)])
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].StakerIndex < changes[j].StakerIndex
	})
	nstBC := oracletypes.RawDataNST{
		Version:           version,
		NstBalanceChanges: changes,
	}
	bz, err := proto.Marshal(&nstBC)
	if err != nil {
		return &types.PriceInfo{
			Price: lastNSTPrice,
			// combine epoch and version as roundID in priceInfo
			RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
		}, nil
	}
	return &types.PriceInfo{
		Price: string(bz),
	}, nil
}
