package beaconchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/imua-xyz/price-feeder/fetcher/types"
)

type ResultValidators struct {
	Data []struct {
		Index     string `json:"index"`
		Validator struct {
			Pubkey            string `json:"pubkey"`
			EffectiveBalance  string `json:"effective_balance"`
			WithdrawableEpoch string `json:"withdrawable_epoch"`
		} `json:"validator"`
	} `json:"data"`
}

type ResultHeader struct {
	Data struct {
		Header struct {
			Message struct {
				Slot      string `json:"slot"`
				StateRoot string `json:"state_root"`
			} `json:"message"`
		} `json:"header"`
	} `json:"data"`
}

type ValidatorPostRequest struct {
	IDs []string `json:"ids"`
}

const (
	// the response from beaconchain represents ETH with 10^9 decimals
	divisor = 1000000000

	urlQueryHeader          = "eth/v1/beacon/headers"
	urlQueryHeaderFinalized = "eth/v1/beacon/headers/finalized"

	getValidatorsPath = "eth/v1/beacon/states/%s/validators"
)

var (
	// updated from oracle, deposit/withdraw
	// TEST only. debug
	//	validatorsTmp = []string{
	//		"0xa1d1ad0714035353258038e964ae9675dc0252ee22cea896825c01458e1807bfad2f9969338798548d9858a571f7425c",
	//		"0xb2ff4716ed345b05dd1dfc6a5a9fa70856d8c75dcc9e881dd2f766d5f891326f0d10e96f3a444ce6c912b69c22c6754d",
	//	}
	//
	//	stakerValidators = map[int]*validatorList{2: {0, validatorsTmp}}
	// stakerValidators  = make(map[int]*validatorList)
	//	defaultStakerValidators = newStakerVList()
	//	defaultStakers          = newStakers()

	// latest finalized epoch we've got balances summarized for stakers
	finalizedEpoch   uint64
	finalizedVersion uint64

	latestChangesBytes = types.NSTZeroChanges

	urlEndpoint   *url.URL
	slotsPerEpoch uint64
)

// reload does nothing since beaconchain source only used to update the balance change for nsteth
func (s *source) reload(token, cfgPath string) error {
	return nil
}

func getValidators(validators []string, stateRoot string, epoch uint64) ([][]uint64, error) {
	reqBody := ValidatorPostRequest{
		IDs: validators,
	}
	body, _ := json.Marshal(reqBody)
	u := urlEndpoint.JoinPath(fmt.Sprintf(getValidatorsPath, stateRoot))
	res, err := http.Post(u.String(), "application/json", bytes.NewBuffer(body))
	if err != nil {
		logger.Error("failed to get validators from beaconchain", "error", err)
		return nil, err
	}
	defer res.Body.Close()
	result, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	re := ResultValidators{}
	if err := json.Unmarshal(result, &re); err != nil {
		logger.Error("failed to parse GetValidators response", "error", err)
		return nil, err
	}
	ret := make([][]uint64, 0, len(re.Data))
	for _, value := range re.Data {
		efb, _ := strconv.ParseUint(value.Validator.EffectiveBalance, 10, 64)
		index, _ := strconv.ParseUint(value.Index, 10, 64)
		ret = append(ret, []uint64{index, efb / divisor})
	}
	return ret, nil
}

func getFinalizedEpoch() (epoch uint64, stateRoot string, err error) {
	u := urlEndpoint.JoinPath(urlQueryHeaderFinalized)
	var res *http.Response
	res, err = http.Get(u.String())
	if err != nil {
		return
	}
	result, _ := io.ReadAll(res.Body)
	defer res.Body.Close()
	re := ResultHeader{}
	if err = json.Unmarshal(result, &re); err != nil {
		return
	}
	slot, _ := strconv.ParseUint(re.Data.Header.Message.Slot, 10, 64)
	epoch = slot / slotsPerEpoch
	if slot%slotsPerEpoch > 0 {
		u = urlEndpoint.JoinPath(urlQueryHeader, strconv.FormatUint(epoch*slotsPerEpoch, 10))
		res, err = http.Get(u.String())
		if err != nil {
			return
		}
		result, _ = io.ReadAll(res.Body)
		res.Body.Close()
		if err = json.Unmarshal(result, &re); err != nil {
			return
		}
	}
	stateRoot = re.Data.Header.Message.StateRoot
	return
}

// Struct for parsing finalized block with execution payload
// See: https://ethereum.github.io/beacon-APIs/#/Beacon/getBlockV2
// and https://rpc.ankr.com/premium-http/eth_beacon

type ResultFinalizedBlock struct {
	Data struct {
		Message struct {
			Slot      string `json:"slot"`
			StateRoot string `json:"state_root"`
			Body      struct {
				ExecutionPayload struct {
					BlockNumber string `json:"block_number"`
				} `json:"execution_payload"`
			} `json:"body"`
			Header struct {
				StateRoot string `json:"state_root"`
			} `json:"header"`
		} `json:"message"`
	} `json:"data"`
}

// Returns (elBlockNumber, clSlot, stateRoot, error)
func getFinalizedELBlockNumber() (int64, uint64, string, error) {
	u := urlEndpoint.JoinPath("eth/v2/beacon/blocks/finalized")
	res, err := http.Get(u.String())
	if err != nil {
		return 0, 0, "", err
	}
	defer res.Body.Close()
	result, _ := io.ReadAll(res.Body)
	var re ResultFinalizedBlock
	if err = json.Unmarshal(result, &re); err != nil {
		return 0, 0, "", err
	}
	clSlot, _ := strconv.ParseUint(re.Data.Message.Slot, 10, 64)
	elBlockNumber, _ := strconv.ParseInt(re.Data.Message.Body.ExecutionPayload.BlockNumber, 10, 64)
	// stateRoot := re.Data.Message.Header.StateRoot
	stateRoot := re.Data.Message.StateRoot
	return elBlockNumber, clSlot, stateRoot, nil
}
