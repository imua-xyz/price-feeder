package beaconchain

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ExocoreNetwork/price-feeder/fetcher/types"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/imroc/biu"
)

type stakerList struct {
	StakerAddrs []string
}

const (
	defaultBalance = 32
	divisor        = 1000000000
	maxChange      = -16
)

var (
	lock sync.RWMutex
	// updated from oracle, deposit/withdraw
	// TEST only. debug
	stakerValidators = map[int][]string{2: {"97", "199"}}
	// latest finalized epoch we've got balances summarized for stakers
	finalizedEpoch uint64
	// latest stakerBalanceChanges
	latestChangesBytes = make([]byte, 0)

	// errors
	errTokenNotSupported = errors.New("token not supported")
	errBalanceNotChanged = errors.New("balance not changed")
)

type DataValidatorBalance []struct {
	ValidatorIndex   uint64 `json:"validatorindex"`
	EffectiveBalance uint64 `json:"effectivebalance"`
}

type DataEpoch struct {
	Epoch uint64 `json:"epoch"`
}

type ResultEpoch struct {
	DataEpoch `json:"data"`
}

type ResultValidatorBalances struct {
	DataValidatorBalance `json:"data"`
}

const (
	envConf = ""
)

var (
	urlQueryValidatorBalances, _ = url.Parse("https://beaconcha.in/api/v1/validator")
	queryValue                   = url.Values(map[string][]string{"offset": {"0"}, "limit": {"1"}})
)

func init() {
	types.InitFetchers[types.BeaconChain] = Init
}

func Init(_ string) error {
	types.Fetchers[types.BeaconChain] = Fetch
	return nil
}

func UpdateStakerValidators(stakerIdx int, validatorIdx uint64, deposit bool) {
	validatorIdxStr := strconv.FormatUint(validatorIdx, 10)
	lock.Lock()
	// add a new valdiator for the staker
	if deposit {
		if validators, ok := stakerValidators[stakerIdx]; ok {
			stakerValidators[stakerIdx] = append(validators, validatorIdxStr)
		} else {
			stakerValidators[stakerIdx] = []string{validatorIdxStr}
		}
	} else {
		// remove the existing validatorIndex for the corresponding staker
		if validators, ok := stakerValidators[stakerIdx]; ok {
			for idx, v := range validators {
				if v == validatorIdxStr {
					if len(validators) == 1 {
						delete(stakerValidators, stakerIdx)
						break
					}
					stakerValidators[stakerIdx] = append(validators[:idx], validators[idx+1:]...)
					break
				}
			}
		}
	}
	lock.Unlock()
}

func Fetch(token string) (*types.PriceInfo, error) {
	// check epoch, when epoch updated, update effective-balance
	if token != types.NativeTokenETH {
		return nil, errTokenNotSupported
	}
	// check if finalized epoch had been updated
	epoch, err := GetFinalizedEpoch()
	if err != nil {
		return nil, err
	}

	// epoch not updated, just return without fetching since effective-balance has not changed
	if epoch <= finalizedEpoch {
		// TODO: return current price or {nil, err}?
		//	return nil, errEpochNotUpdated
		return &types.PriceInfo{
			Price:   string(latestChangesBytes),
			RoundID: strconv.FormatUint(finalizedEpoch, 10),
		}, nil

	}

	stakerChanges := make([][]int, 0, len(stakerValidators))

	lock.RLock()
	for stakerIdx, validatorIdxs := range stakerValidators {
		stakerBalance := 0
		// beaconcha.in support at most 100 validators for one request
		for len(validatorIdxs) > 100 {
			tmpValidatorIdx := validatorIdxs[:100]
			validatorIdxs = validatorIdxs[100:]

			validatorBalances, err := GetValidators(tmpValidatorIdx, epoch)
			if err != nil {
				return nil, err
			}
			for _, validatorBalance := range validatorBalances {
				stakerBalance += int(validatorBalance[1])
			}
		}

		validatorBalances, err := GetValidators(validatorIdxs, epoch)
		if err != nil {
			return nil, err
		}
		for _, validatorBalance := range validatorBalances {
			// this should be initialized from exocored
			stakerBalance += int(validatorBalance[1])
		}
		if delta := stakerBalance - defaultBalance*len(validatorIdxs); delta != 0 {
			if delta < maxChange {
				delta = maxChange
			}
			stakerChanges = append(stakerChanges, []int{stakerIdx, delta})
		}
	}
	lock.RUnlock()

	finalizedEpoch = epoch
	if len(stakerChanges) == 0 {
		if len(latestChangesBytes) > 0 {
			latestChangesBytes = make([]byte, 0)
			finalizedEpoch = epoch
		}
		return nil, errBalanceNotChanged
	}

	latestChangesBytes = convertBalanceChangeToBytes(stakerChanges)

	return &types.PriceInfo{
		Price:   string(latestChangesBytes),
		RoundID: strconv.FormatUint(finalizedEpoch, 10),
	}, nil
}

func GetFinalizedEpoch() (uint64, error) {
	res, err := http.Get("https://beaconcha.in/api/v1/epoch/finalized")
	if err != nil {
		return 0, err
	}
	result, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	r := &ResultEpoch{}
	_ = json.Unmarshal(result, r)
	return r.Epoch, nil
}

func GetValidators(indexs []string, epoch uint64) ([][]uint64, error) {
	var err error
	if epoch == 0 {
		epoch, err = GetFinalizedEpoch()
		if err != nil {
			return nil, err
		}
	}

	finalizedEpochStr := strconv.FormatUint(epoch, 10)

	base := urlQueryValidatorBalances.JoinPath(strings.Join(indexs, ","))

	value := queryValue
	value.Add("latest_epoch", finalizedEpochStr)

	base.RawQuery = value.Encode()

	response, err := http.Get(base.String())
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	rBytes, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	response.Body.Close()

	r := &ResultValidatorBalances{}
	_ = json.Unmarshal(rBytes, r)
	fmt.Println(r, r.DataValidatorBalance[1].ValidatorIndex, r.DataValidatorBalance[1].EffectiveBalance)
	ret := make([][]uint64, 0, len(r.DataValidatorBalance))
	for _, value := range r.DataValidatorBalance {
		ret = append(ret, []uint64{value.ValidatorIndex, value.EffectiveBalance / divisor})
	}
	return ret, nil
}

func convertBalanceChangeToBytes(stakerChanges [][]int) []byte {
	if len(stakerChanges) == 0 {
		return nil
	}
	str := ""
	index := 0
	changeBytesList := make([][]byte, 0, len(stakerChanges))
	bitsList := make([]int, 0, len(stakerChanges))
	for _, stakerChange := range stakerChanges {
		str += strings.Repeat("0", stakerChange[0]-index) + "1"
		index = stakerChange[0] + 1

		// change amount -> bytes
		change := stakerChange[1]
		var changeBytes []byte
		symbol := 1
		if change < 0 {
			symbol = -1
			change *= -1
		}
		change--
		bits := 0
		if change == 0 {
			bits = 1
			changeBytes = []byte{byte(0)}
		} else {
			tmpChange := change
			for tmpChange > 0 {
				bits++
				tmpChange /= 2
			}
			if change < 256 {
				// 1 byte
				changeBytes = []byte{byte(change)}
				changeBytes[0] <<= (8 - bits)
			} else {
				// 2 byte
				changeBytes = make([]byte, 2)
				binary.BigEndian.PutUint16(changeBytes, uint16(change))
				moveLength := 16 - bits
				changeBytes[0] <<= moveLength
				tmp := changeBytes[1] >> (8 - moveLength)
				changeBytes[0] |= tmp
				changeBytes[1] <<= moveLength
			}
		}

		// use lower 4 bits to represent the length of valid change value in bits format
		bitsLengthBytes := []byte{byte(bits)}
		bitsLengthBytes[0] <<= 4
		if symbol < 0 {
			bitsLengthBytes[0] |= 8
		}

		tmp := changeBytes[0] >> 5
		bitsLengthBytes[0] |= tmp
		if bits <= 3 {
			changeBytes = nil
		} else {
			changeBytes[0] <<= 3
		}

		if len(changeBytes) == 2 {
			tmp = changeBytes[1] >> 5
			changeBytes[0] |= tmp
			if bits <= 11 {
				changeBytes = changeBytes[:1]
			} else {
				changeBytes[1] <<= 3
			}
		}
		bitsLengthBytes = append(bitsLengthBytes, changeBytes...)
		changeBytesList = append(changeBytesList, bitsLengthBytes)
		bitsList = append(bitsList, bits)
	}

	l := len(bitsList)
	changeResult := changeBytesList[l-1]
	bitsList[len(bitsList)-1] = bitsList[len(bitsList)-1] + 5
	for i := l - 2; i >= 0; i-- {
		prev := changeBytesList[i]

		byteLength := 8 * len(prev)
		bitsLength := bitsList[i] + 5
		// delta must <8
		delta := byteLength - bitsLength
		if delta == 0 {
			changeResult = append(prev, changeResult...)
			bitsList[i] = bitsLength + bitsList[i+1]
		} else {
			// delta : (0,8)
			tmp := changeResult[0] >> (8 - delta)
			prev[len(prev)-1] |= tmp
			if len(changeResult) > 1 {
				for j := 1; j < len(changeResult); j++ {
					changeResult[j-1] <<= delta
					tmp := changeResult[j] >> (8 - delta)
					changeResult[j-1] |= tmp
				}
			}
			changeResult[len(changeResult)-1] <<= delta
			left := bitsList[i+1] % 8
			if bitsList[i+1] > 0 && left == 0 {
				left = 8
			}
			if left <= delta {
				changeResult = changeResult[:len(changeResult)-1]
			}
			changeResult = append(prev, changeResult...)
			bitsList[i] = bitsLength + bitsList[i+1]
		}
	}
	str += strings.Repeat("0", 256-index)
	bytesIndex := biu.BinaryStringToBytes(str)

	result := append(bytesIndex, changeResult...)
	return result
}

func parseBalanceChange(rawData []byte, sl stakerList) (map[string]int, error) {
	indexs := rawData[:32]
	changes := rawData[32:]
	index := -1
	byteIndex := 0
	bitOffset := 0
	lengthBits := 5
	stakerChanges := make(map[string]int)
	for _, b := range indexs {
		for i := 7; i >= 0; i-- {
			index++
			if (b>>i)&1 == 1 {
				lenValue := changes[byteIndex] << bitOffset
				bitsLeft := 8 - bitOffset
				lenValue >>= (8 - lengthBits)
				if bitsLeft < lengthBits {
					byteIndex++
					lenValue |= changes[byteIndex] >> (8 - lengthBits + bitsLeft)
					bitOffset = lengthBits - bitsLeft
				} else {
					if bitOffset += lengthBits; bitOffset == 8 {
						bitOffset = 0
					}
					if bitsLeft == lengthBits {
						byteIndex++
					}
				}

				symbol := lenValue & 1
				lenValue >>= 1
				if lenValue <= 0 {
					return stakerChanges, errors.New("length of change value must be at least 1 bit")
				}

				bitsExtracted := 0
				stakerChange := 0
				for bitsExtracted < int(lenValue) {
					bitsLeft := 8 - bitOffset
					byteValue := changes[byteIndex] << bitOffset
					if (int(lenValue) - bitsExtracted) < bitsLeft {
						bitsLeft = int(lenValue) - bitsExtracted
						bitOffset += bitsLeft
					} else {
						byteIndex++
						bitOffset = 0
					}
					byteValue >>= (8 - bitsLeft)
					stakerChange = (stakerChange << bitsLeft) | int(byteValue)
					bitsExtracted += bitsLeft
				}
				stakerChange++
				if symbol == 1 {
					stakerChange *= -1
				}
				stakerChanges[sl.StakerAddrs[index]] = stakerChange
			}
		}
	}
	return stakerChanges, nil
}
