package beaconchain

import (
	"context"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ABI for all needed methods on BootstrapStorage
const (
	bootstrapStorageABI = `[
{"constant":true,"inputs":[{"name":"owner","type":"address"}],"name":"ownerToCapsule","outputs":[{"name":"","type":"address"}],"payable":false,"stateMutability":"view","type":"function"},
  {"constant":true,"inputs":[{"name":"","type":"uint256"}],"name":"whitelistTokens","outputs":[{"name":"","type":"address"}],"payable":false,"stateMutability":"view","type":"function"},
  {"constant":true,"inputs":[],"name":"getWhitelistedTokensCount","outputs":[{"name":"","type":"uint256"}],"payable":false,"stateMutability":"view","type":"function"}
]`
	capsuleABI = `[
{"constant":true,"inputs":[],"name":"inWithdrawProgress","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"},
{"constant":true,"inputs":[],"name":"withdrawableBalance","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}
	]`
)

// CallOwnerToCapsule queries the ownerToCapsule(address) method on the proxy contract
func CallOwnerToCapsule(
	ethClient *ethclient.Client,
	proxyAddress common.Address,
	abiJSON string,
	stakerAddress common.Address,
	blockNumber *big.Int,
) (common.Address, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return common.Address{}, err
	}
	input, err := parsedABI.Pack("ownerToCapsule", stakerAddress)
	if err != nil {
		return common.Address{}, err
	}
	callMsg := ethereum.CallMsg{
		To:   &proxyAddress,
		Data: input,
	}
	output, err := ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return common.Address{}, err
	}
	var capsuleAddress common.Address
	err = parsedABI.UnpackIntoInterface(&capsuleAddress, "ownerToCapsule", output)
	if err != nil {
		return common.Address{}, err
	}
	return capsuleAddress, nil
}

// CallWhitelistTokenAt queries the whitelistTokens(uint256) getter on the proxy contract for a given index
func CallWhitelistTokenAt(
	ethClient *ethclient.Client,
	proxyAddress common.Address,
	abiJSON string,
	index *big.Int,
	blockNumber *big.Int,
) (common.Address, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return common.Address{}, err
	}
	input, err := parsedABI.Pack("whitelistTokens", index)
	if err != nil {
		return common.Address{}, err
	}
	callMsg := ethereum.CallMsg{
		To:   &proxyAddress,
		Data: input,
	}
	output, err := ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return common.Address{}, err
	}
	var token common.Address
	err = parsedABI.UnpackIntoInterface(&token, "whitelistTokens", output)
	if err != nil {
		return common.Address{}, err
	}
	return token, nil
}

// CallWhitelistTokensLength queries a length getter for the whitelistTokens array on the proxy contract.
// This requires the contract to expose a function like getWhitelistedTokensCount() or whitelistTokensLength().
// The ABI should match the function signature: function getWhitelistedTokensCount() public view returns (uint256)
func CallWhitelistTokensLength(
	ethClient *ethclient.Client,
	proxyAddress common.Address,
	abiJSON string,
	blockNumber *big.Int,
) (*big.Int, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, err
	}
	input, err := parsedABI.Pack("getWhitelistedTokensCount") // or "whitelistTokensLength" if that's the function name
	if err != nil {
		return nil, err
	}
	callMsg := ethereum.CallMsg{
		To:   &proxyAddress,
		Data: input,
	}
	output, err := ethClient.CallContract(context.Background(), callMsg, blockNumber)
	if err != nil {
		return nil, err
	}
	var length *big.Int
	err = parsedABI.UnpackIntoInterface(&length, "getWhitelistedTokensCount", output)
	if err != nil {
		return nil, err
	}
	return length, nil
}
