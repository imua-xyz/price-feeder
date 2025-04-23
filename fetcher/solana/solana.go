package nstsol

import (
	"encoding/json"
	"errors"
	"strconv"

	"context"
	"fmt"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/imua-xyz/price-feeder/fetcher/types"
)

type StakeAccountRes struct {
	Parsed struct {
		Info struct {
			Stake struct {
				Delegation struct {
					Stake string `json:"stake"`
				} `json:"delegation"`
			} `json:"stake"`
		} `json:"info"`
	} `json:"parsed"`
}

var (
	// endpoint = "https://rpc.ankr.com/solana/a5d2626c4027e6d924c870d2558bc774bc995ce917a17a654a92856b3279a586"
	// client   = rpc.New(endpoint)

	errExceedsMaxSlot = errors.New("slot is greater than max slot")

	// latest finalized epoch we've got balances summarized for stakers
	finalizedEpoch   uint64
	finalizedVersion uint64

	latestChangesBytes = types.NSTZeroChanges

	// urlEndpoint   *url.URL
	urlEndpoint   string
	slotsPerEpoch uint64
)

// reload does nothing since beaconchain source only used to update the balance change for nsteth
func (s *source) reload(token, cfgPath string) error {
	return nil
}

func getFinalizedEpoch() (epoch, startSlot, endSlot, currentSlot uint64, err error) {
	var out *rpc.GetEpochInfoResult
	client := rpc.New(urlEndpoint)
	defer client.Close()
	out, err = client.GetEpochInfo(

		context.TODO(),
		rpc.CommitmentFinalized,
	)

	if err != nil {
		panic(err)
	}

	fmt.Println(out.AbsoluteSlot, out.SlotIndex, out.Epoch, out.SlotsInEpoch)
	startSlot = out.AbsoluteSlot - out.SlotIndex
	endSlot = startSlot + (out.SlotsInEpoch - 1)
	currentSlot = out.AbsoluteSlot
	epoch = out.Epoch
	return
}

func getStakeAccounts(pubKeyStrs []string, minSlot, maxSlot uint64) (map[string]uint64, error) {
	ret := make(map[string]uint64)
	var pubKeys []solana.PublicKey

	for _, pubKeyStr := range pubKeyStrs {
		pubKey := solana.MustPublicKeyFromBase58(pubKeyStr)
		pubKeys = append(pubKeys, pubKey)
	}

	client := rpc.New(urlEndpoint)
	defer client.Close()

	respM, err := client.GetMultipleAccountsWithOpts(

		context.TODO(),
		pubKeys,
		&rpc.GetMultipleAccountsOpts{
			Commitment:     rpc.CommitmentFinalized,
			Encoding:       solana.EncodingJSONParsed,
			MinContextSlot: &minSlot,
		},
	)

	if err != nil {
		return nil, err
	}

	if respM.RPCContext.Context.Slot > maxSlot {
		return nil, errExceedsMaxSlot
	}

	for idx, acc := range respM.Value {
		if acc == nil {
			ret[pubKeyStrs[idx]] = 0
			continue
		}
		var stake StakeAccountRes
		if err := json.Unmarshal(acc.Data.GetRawJSON(), &stake); err != nil {
			return nil, err
		}
		stakeAmount, _ := strconv.ParseUint(stake.Parsed.Info.Stake.Delegation.Stake, 10, 64)
		ret[pubKeyStrs[idx]] = stakeAmount
	}

	return ret, nil
}
