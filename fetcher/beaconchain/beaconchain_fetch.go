//go:build !devmode

package beaconchain

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const (
	zeroAddressHex = "0x0000000000000000000000000000000000000000"
)

// Helper to get capsule balance
// returns: balance(balance-withdrawableBalance, valid, error); valid indicates whether the capsule is in withdrawal progress
func getCapsuleValidBalance(ethClient *ethclient.Client, capsuleAddr string, blockNumber *big.Int) (*big.Int, bool, error) {
	address := common.HexToAddress(capsuleAddr)
	var valid bool
	parsedABI, err := abi.JSON(strings.NewReader(capsuleABI))
	if err != nil {
		return nil, valid, fmt.Errorf("failed to parse capsuleABI, err:%w", err)
	}

	callData, err := parsedABI.Pack("isInClaimProgress")
	if err != nil {
		return nil, valid, fmt.Errorf("failed to pack calldata_isInClaimProgress, err:%w", err)
	}

	callMsg := ethereum.CallMsg{
		To:   &address,
		Data: callData,
	}
	output, err := ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return nil, valid, fmt.Errorf("failed to call contract to query isInClaimProgress, err:%w", err)
	}

	var inWithdrawProgress bool
	err = parsedABI.UnpackIntoInterface(&inWithdrawProgress, "isInClaimProgress", output)
	if err != nil || inWithdrawProgress {
		return nil, valid, fmt.Errorf("capsule %s is in withdrawal progress or unpack error: %w", capsuleAddr, err)
	}

	valid = true
	callData, err = parsedABI.Pack("withdrawableBalance")
	if err != nil {
		return nil, valid, fmt.Errorf("failed to pack calldata_withdrawableBalance, err:%w", err)
	}

	callMsg = ethereum.CallMsg{To: &address, Data: callData}
	output, err = ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return nil, valid, fmt.Errorf("failed to call contract to query withdrawableBalance, err:%w", err)
	}

	var withdrawable *big.Int
	err = parsedABI.UnpackIntoInterface(&withdrawable, "withdrawableBalance", output)
	if err != nil {
		return nil, valid, fmt.Errorf("failed to unpack withdrawableBalance, err:%w", err)
	}

	balance, err := ethClient.BalanceAt(context.Background(), address, blockNumber)
	if err != nil {
		return nil, valid, fmt.Errorf("failed to get capsule balance, err:%w", err)
	}
	if balance.Cmp(withdrawable) < 0 {
		return nil, valid, fmt.Errorf("capsule balance %s is less than withdrawable balance %s", balance.String(), withdrawable.String())
	}
	balance.Sub(balance, withdrawable)
	return balance, valid, nil
}

// getCapsuleAddressForStaker queries the ownerToCapsule(address) method on the proxy contract
func getCapsuleAddressForStaker(ethClient *ethclient.Client, stakerAddr string, blockNumber *big.Int, proxyAddressHex string) (string, error) {
	if stakerAddr == "" {
		return "", nil
	}
	parsedABI, err := abi.JSON(strings.NewReader(bootstrapStorageABI))
	if err != nil {
		return "", err
	}
	owner := common.HexToAddress(stakerAddr)
	input, err := parsedABI.Pack("ownerToCapsule", owner)
	if err != nil {
		return "", err
	}
	proxyAddress := common.HexToAddress(proxyAddressHex)
	callMsg := ethereum.CallMsg{
		To:   &proxyAddress,
		Data: input,
	}
	output, err := ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return "", err
	}
	if len(output) == 0 {
		return "", nil
	}
	var capsuleAddress common.Address
	err = parsedABI.UnpackIntoInterface(&capsuleAddress, "ownerToCapsule", output)
	if err != nil {
		return "", err
	}
	return capsuleAddress.Hex(), nil
}

func (s *source) fetch(token string) (*types.PriceInfo, error) {
	// check epoch, when epoch updated, update effective-balance
	if types.NSTToken(token) != types.NativeTokenETH {
		return nil, feedertypes.ErrTokenNotSupported.Wrap(fmt.Sprintf("only support native-eth-restaking %s, got:%s", types.NativeTokenETH, token))
	}

	// use 'no copy' version to avoid copying stakers
	sInfos, version, withdrawVersion := s.stakers.GetStakersNoCopy()
	if len(sInfos) == 0 {
		// return zero price when there's no stakers
		return &types.PriceInfo{}, nil
	}
	// --- CL/EL synchronization ---
	elBlockNumber, clSlot, stateRoot, err := getFinalizedELBlockNumber()
	if err != nil {
		return nil, fmt.Errorf("fail to get finalized EL block number, error:%w", err)
	}
	blockNumber := big.NewInt(elBlockNumber)
	// Calculate epoch from slot
	epoch := clSlot / slotsPerEpoch
	// --- End CL/EL synchronization ---
	// epoch not updated, just return without fetching since effective-balance has not changed
	if epoch <= finalizedEpoch && version <= finalizedVersion && withdrawVersion <= finalizedWithdrawVersion {
		s.logger.Info("fetch efb from beaconchain, no change in epoch or version, return latestChangesBytes", "epoch", epoch, "version", version, "withdrawVersion", withdrawVersion)
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
		l := len(validators)
		if l == 0 {
			continue
		}
		stakerBalance := uint64(0)
		// --- Capsule balance integration ---
		capsuleAddr, err := getCapsuleAddressForStaker(s.ethClient, stakerInfo.Address, blockNumber, s.bootstrapAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to get capsule address, staker_index:%d, staker:%s, err:%w", stakerIdx, stakerInfo.Address, err)
		}

		if len(capsuleAddr) == 0 || capsuleAddr == zeroAddressHex || s.ethClient == nil {
			return nil, fmt.Errorf("capsule address is empty:%t or ethClient is nil:%t, staker_index:%d, staker:%s", (len(capsuleAddr) == 0 || capsuleAddr == zeroAddressHex), s.ethClient == nil, stakerIdx, stakerInfo.Address)
		}

		capsuleBalance, valid, err := getCapsuleValidBalance(s.ethClient, capsuleAddr, blockNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to get capsule balance, staker_index:%d, capsule:%s, err:%w", stakerIdx, capsuleAddr, err)
		}
		if !valid {
			// skip this staker
			continue
		}

		capsuleBalanceGwei := new(big.Int).Div(capsuleBalance, big.NewInt(1e9))
		stakerBalance += capsuleBalanceGwei.Uint64()
		// --- End capsule balance integration ---

		// beaconcha.in support at most 100 validators for one request
		i := 0
		for l > 100 {
			tmpValidatorPubkeys := validators[i : i+100]
			i += 100
			l -= 100
			validatorBalances, err := getValidators(tmpValidatorPubkeys, stateRoot, epoch)
			if err != nil {
				return nil, fmt.Errorf("failed to get validators from beaconchain, error:%w", err)
			}
			for _, validatorBalance := range validatorBalances {
				stakerBalance += uint64(validatorBalance[1])
			}
		}

		validatorBalances, err := getValidators(validators[i:], stateRoot, epoch)
		if err != nil {
			return nil, fmt.Errorf("failed to get validators from beaconchain, error:%w", err)
		}
		for _, validatorBalance := range validatorBalances {
			stakerBalance += validatorBalance[1]
		}

		if stakerBalance != stakerInfo.Balance || stakerInfo.WithdrawVersion > finalizedWithdrawVersion {
			changedStakerBalances = append(changedStakerBalances, &oracletypes.NSTKV{
				StakerIndex: uint32(stakerIdx),
				Balance:     stakerBalance,
			})
			s.logger.Info("fetched efb from beaconchain", "staker_index", stakerIdx, "prev_balance", stakerInfo.Balance, "latest_balance", stakerBalance, "validators_count", l)
			hasEFBChanged = true
		}
	}
	// when balance change or withdrawVersion is updated(since last feed might skip some balance change), we update the same price again
	if hasEFBChanged {
		s.logger.Info("fetched efb from beaconchain, some efbs of validators have changed")
		sort.Slice(changedStakerBalances, func(i, j int) bool {
			return changedStakerBalances[i].StakerIndex < changedStakerBalances[j].StakerIndex
		})
		nstBC := oracletypes.RawDataNST{
			Version:           version,
			WithdrawVersion:   withdrawVersion,
			NstBalanceChanges: changedStakerBalances,
		}
		bz, err := proto.Marshal(&nstBC)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nstBalanceChanges, error:%w", err)
		}

		latestChangesBytes = bz
	} else {
		s.logger.Info("fetched efb from beaconchain, all efbs remain unchanged")
		latestChangesBytes = fetchertypes.NSTZeroChanges
	}
	finalizedEpoch = epoch
	finalizedVersion = version
	finalizedWithdrawVersion = withdrawVersion

	return &types.PriceInfo{
		Price:   string(latestChangesBytes),
		RoundID: fmt.Sprintf("%s|%s|%s", strconv.FormatUint(finalizedEpoch, 10), strconv.FormatUint(version, 10), strconv.FormatUint(withdrawVersion, 10)),
	}, nil
}
