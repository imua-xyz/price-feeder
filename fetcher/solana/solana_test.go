package nstsol

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

func TestGetEpoch(t *testing.T) {
	vidxStr, err := convertHexToIntStr("0x00000000000000000000000000000000000000000000000000000000001CDD66")
	if err != nil {
		panic(err)
	}
	//	GetValidators([]string{"1", "5", "9"}, 0)
	validators := []string{
		//	"0xa1d1ad0714035353258038e964ae9675dc0252ee22cea896825c01458e1807bfad2f9969338798548d9858a571f7425c",
		//	"0xb2ff4716ed345b05dd1dfc6a5a9fa70856d8c75dcc9e881dd2f766d5f891326f0d10e96f3a444ce6c912b69c22c6754d",
		//		"1",
		//		"5",
		//		"9",
		vidxStr,
	}
	// urlEndpoint, _ = url.Parse("https://rpc.ankr.com/premium-http/eth_beacon/a5d2626154027e423423d924c870d2558bc774bc995ce917a17a654a92856b1119a586")
	urlEndpoint, _ = "https://rpc.ankr.com/premium-http/eth_beacon/a5d2626154027e423423d924c870d2558bc774bc995ce917a17a654a92856b1119a586"
	slotsPerEpoch = 32

	_, stateRoot, _ := getFinalizedEpoch()

	//GetValidatorsAnkr([]string{"0xa1d1ad0714035353258038e964ae9675dc0252ee22cea896825c01458e1807bfad2f9969338798548d9858a571f7425c"}, stateRoot)
	//GetValidatorsAnkr([]string{"0xb2ff4716ed345b05dd1dfc6a5a9fa70856d8c75dcc9e881dd2f766d5f891326f0d10e96f3a444ce6c912b69c22c6754d"}, stateRoot)
	getValidators(validators, stateRoot)
}

func convertHexToIntStr(hexStr string) (string, error) {
	vBytes, err := hexutil.Decode(hexStr)
	if err != nil {
		return "", err
	}
	return new(big.Int).SetBytes(vBytes).String(), nil

}
