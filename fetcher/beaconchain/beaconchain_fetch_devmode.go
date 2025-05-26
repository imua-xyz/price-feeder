//go:build devmode

// this snippet is used in devmode build to test nst balance changes
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
		0:  {StakerIndex: 0, Balance: 1239},
		1:  {StakerIndex: 1, Balance: 9977},
		2:  {StakerIndex: 2, Balance: 5566},
		3:  {StakerIndex: 3, Balance: 1998292},
		4:  {StakerIndex: 4, Balance: 320},
		5:  {StakerIndex: 5, Balance: 910},
		6:  {StakerIndex: 6, Balance: 919},
		7:  {StakerIndex: 7, Balance: 510},
		8:  {StakerIndex: 8, Balance: 810},
		9:  {StakerIndex: 9, Balance: 670},
		10: {StakerIndex: 10, Balance: 777},
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
	if len(stakerValidators) > 7 {
		fmt.Println("trigger withdraw")
		// 0,1,2,3,4,5,6,7,8 -> 0,_,2,_,_,5,6,7
		// 0,_,2,_,_,5,6,7 -> 0,7,2,6,5
		// mapping:
		// 1->7, 3->6, 4->5
		stakerChanges[1].Balance = 0
		stakerChanges[3].Balance = 0
		stakerChanges[4].Balance = 0
	}
	for stakerIndex, _ := range stakerValidators {
		if stakerIndex > 10 {
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
		Price:   string(bz),
		RoundID: fmt.Sprintf("%s_%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10)),
	}, nil
}
