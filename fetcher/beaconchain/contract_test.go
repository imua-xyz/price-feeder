// contract_test.go
//
// This file contains integration tests for cross-layer contract and beacon chain queries.
// It demonstrates how to:
//   - Query the finalized EL block number, CL slot, and stateRoot from a beacon node
//   - Use the EL block number for contract calls (e.g., ownerToCapsule, whitelistTokens)
//   - Use the CL slot/stateRoot for consensus-layer queries (e.g., validator effective balance)
//
// These tests are intended for end-to-end validation of cross-layer workflows in the price-feeder project.

package beaconchain

import (
	"fmt"
	"math/big"
	"net/url"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	// Proxy contract address for BootstrapStorage on Holesky
	testProxyAddressHex = "0x38674073a3713dd2C46892f1d2C5Dadc5Bb14172"
	// Public Holesky Ethereum endpoint
	testEthRpcUrl = "https://ethereum-holesky.publicnode.com"
)

// TestCallWhitelistTokenAt demonstrates how to call the whitelistTokens(uint256) getter
// on the proxy contract at a specific EL block number.
func TestCallWhitelistTokenAt(t *testing.T) {
	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}

	proxyAddress := common.HexToAddress(testProxyAddressHex)
	blockNumber := big.NewInt(0) // latest
	index := big.NewInt(0)       // test the first token

	// If you want to use a specific block, set blockNumber = big.NewInt(elBlockNumber)
	blockNumber = nil
	token, err := CallWhitelistTokenAt(client, proxyAddress, bootstrapStorageABI, index, blockNumber)
	if err != nil {
		t.Fatalf("Failed to call whitelistTokens getter: %v", err)
	}

	t.Logf("Whitelist token at index %d: %s", index.Int64(), token.Hex())
}

// TestGetFinalizedELBlockNumber demonstrates how to fetch the finalized EL block number,
// CL slot, and stateRoot from the beacon node.
func TestGetFinalizedELBlockNumber(t *testing.T) {
	// Set the beacon endpoint for Holesky
	urlEndpoint, _ = url.Parse("https://rpc.ankr.com/premium-http/eth_holesky_beacon/a5d2626c4027e6d924c870d2558bc774bc995ce917a17a654a92856b3279a586")

	elBlockNumber, clSlot, stateRoot, err := getFinalizedELBlockNumber()
	if err != nil {
		t.Fatalf("Failed to get finalized EL block number: %v", err)
	}
	if elBlockNumber == 0 || clSlot == 0 {
		t.Fatalf("Invalid block number or slot: elBlockNumber=%d, clSlot=%d", elBlockNumber, clSlot)
	}
	t.Logf("Finalized EL block number: %d, CL slot: %d, stateRoot: %s", elBlockNumber, clSlot, stateRoot)
}

// TestCompleteCL_EL_Workflow demonstrates the full cross-layer workflow:
//  1. Fetch finalized EL block number, CL slot, and stateRoot
//  2. Use EL block number for contract calls (whitelistTokens)
//  3. Use CL slot/stateRoot for consensus-layer queries (validator effective balance)
func TestCompleteCL_EL_Workflow(t *testing.T) {
	// Set the beacon endpoint for Holesky
	urlEndpoint, _ = url.Parse("https://rpc.ankr.com/premium-http/eth_holesky_beacon/a5d2626c4027e6d924c870d2558bc774bc995ce917a17a654a92856b3279a586")

	// 1. Get finalized EL block number, CL slot, and stateRoot
	elBlockNumber, clSlot, stateRoot, err := getFinalizedELBlockNumber()
	if err != nil {
		t.Fatalf("Failed to get finalized EL block number: %v", err)
	}
	if elBlockNumber == 0 || clSlot == 0 {
		t.Fatalf("Invalid block number or slot: elBlockNumber=%d, clSlot=%d", elBlockNumber, clSlot)
	}
	t.Logf("Finalized EL block number: %d, CL slot: %d, stateRoot: %s", elBlockNumber, clSlot, stateRoot)

	// 2. Use the block number for EL queries (contract calls)
	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}
	proxyAddress := common.HexToAddress(testProxyAddressHex)
	blockNumber := big.NewInt(elBlockNumber)

	index := big.NewInt(0) // test the first token
	blockNumber = nil

	token, err := CallWhitelistTokenAt(client, proxyAddress, bootstrapStorageABI, index, blockNumber)
	if err != nil {
		t.Fatalf("Failed to call whitelistTokens getter (EL): %v", err)
	}
	t.Logf("[EL] Whitelist token at index %d: %s", index.Int64(), token.Hex())

	// 3. Use stateRoot and clSlot for a CL query: get efb for validator 1
	// epoch := clSlot / slotsPerEpoch
	epoch := clSlot / 32
	validatorIdx := "1"
	validators := []string{validatorIdx}
	validatorBalances, err := getValidators(validators, stateRoot, epoch)
	if err != nil {
		t.Fatalf("Failed to get validator efb (CL): %v", err)
	}
	if len(validatorBalances) > 0 {
		t.Logf("[CL] Validator %s effective balance: %v", validatorIdx, validatorBalances[0])
	} else {
		t.Logf("[CL] No balance returned for validator %s", validatorIdx)
	}

	// Optionally, compare with a direct finalized epoch query
	slotsPerEpoch = 32
	epoch, stateRoot, _ = getFinalizedEpoch()
	validatorBalances, err = getValidators(validators, stateRoot, epoch)
	fmt.Println("debug2->", validatorBalances)
}
