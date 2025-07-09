package beaconchain

import (
	"fmt"
	"math/big"
	"net/url"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	testProxyAddressHex = "0x38674073a3713dd2C46892f1d2C5Dadc5Bb14172"
	testEthRpcUrl       = "https://ethereum-holesky.publicnode.com"
)

func TestGetEpoch(t *testing.T) {
	//	vidxStr, err := convertHexToIntStr("0x00000000000000000000000000000000000000000000000000000000001CDD66")
	//	if err != nil {
	//		panic(err)
	//	}
	//	GetValidators([]string{"1", "5", "9"}, 0)
	validators := []string{
		//	"0xa1d1ad0714035353258038e964ae9675dc0252ee22cea896825c01458e1807bfad2f9969338798548d9858a571f7425c",
		//	"0xb2ff4716ed345b05dd1dfc6a5a9fa70856d8c75dcc9e881dd2f766d5f891326f0d10e96f3a444ce6c912b69c22c6754d",
		"1",
		//		"5",
		//		"9",
		// vidxStr,
	}
	ankrApiKey := os.Getenv("ANKR_API_KEY")
	if ankrApiKey == "" {
		t.Skip("ANKR_API_KEY not set")
	}
	urlEndpoint, _ = url.Parse("https://rpc.ankr.com/premium-http/eth_beacon/${ANKR_API_KEY}")

	slotsPerEpoch = 32

	epoch, stateRoot, err := getFinalizedEpoch()
	fmt.Println("stateRoot:", stateRoot, err)

	//GetValidatorsAnkr([]string{"0xa1d1ad0714035353258038e964ae9675dc0252ee22cea896825c01458e1807bfad2f9969338798548d9858a571f7425c"}, stateRoot)
	//GetValidatorsAnkr([]string{"0xb2ff4716ed345b05dd1dfc6a5a9fa70856d8c75dcc9e881dd2f766d5f891326f0d10e96f3a444ce6c912b69c22c6754d"}, stateRoot)
	res, err := getValidators(validators, stateRoot, epoch)
	fmt.Println("result:", res, err)
}

func convertHexToIntStr(hexStr string) (string, error) {
	vBytes, err := hexutil.Decode(hexStr)
	if err != nil {
		return "", err
	}
	return new(big.Int).SetBytes(vBytes).String(), nil

}

func TestOwnerToCapsuleCall(t *testing.T) {
	stakerAddress := os.Getenv("TEST_STAKER_ADDRESS")
	if stakerAddress == "" {
		stakerAddress = "0x0000000000000000000000000000000000000000"
	}

	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}

	proxyAddress := common.HexToAddress(testProxyAddressHex)
	owner := common.HexToAddress(stakerAddress)
	blockNumber := big.NewInt(0) // latest

	blockNumber = nil
	capsuleAddress, err := CallOwnerToCapsule(client, proxyAddress, bootstrapStorageABI, owner, blockNumber)
	if err != nil {
		t.Fatalf("Failed to call contract: %v", err)
	}

	t.Logf("Capsule address for staker %s: %s", stakerAddress, capsuleAddress.Hex())
}

func TestCallWhitelistTokenAt(t *testing.T) {
	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}

	proxyAddress := common.HexToAddress(testProxyAddressHex)
	blockNumber := big.NewInt(0) // latest
	index := big.NewInt(0)       // test the first token

	blockNumber = nil
	token, err := CallWhitelistTokenAt(client, proxyAddress, bootstrapStorageABI, index, blockNumber)
	if err != nil {
		t.Fatalf("Failed to call whitelistTokens getter: %v", err)
	}

	t.Logf("Whitelist token at index %d: %s", index.Int64(), token.Hex())
}

func TestGetFinalizedELBlockNumber(t *testing.T) {
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

func TestCompleteCL_EL_Workflow(t *testing.T) {
	// Set the beacon endpoint for getFinalizedELBlockNumber
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

	// 2. Use the block number for EL queries
	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}
	proxyAddress := common.HexToAddress(testProxyAddressHex)
	blockNumber := big.NewInt(elBlockNumber)

	index := big.NewInt(0) // test the first token

	token, err := CallWhitelistTokenAt(client, proxyAddress, bootstrapStorageABI, index, blockNumber)
	if err != nil {
		t.Fatalf("Failed to call whitelistTokens getter (EL): %v", err)
	}
	t.Logf("[EL] Whitelist token at index %d: %s", index.Int64(), token.Hex())

	// 3. Use stateRoot and clSlot for a CL query: get efb for validator 1
	slotsPerEpoch = 32
	epoch := clSlot / slotsPerEpoch
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
}

func TestCapsuleQueries(t *testing.T) {
	client, err := ethclient.Dial(testEthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to Ethereum node: %v", err)
	}
	v, valid, err := getCapsuleValidBalance(client, "0x90618D1cDb01bF37c24FC012E70029DA20fCe971", nil)
	fmt.Println(v, valid, err)
}
