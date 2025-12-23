package bsc

import (
	"context"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	nsttypes "github.com/imua-xyz/price-feeder/fetcher/nst/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type mockEthClient struct {
	block uint64

	multicallABI  abi.ABI
	capsuleABI    abi.ABI
	capsuleValues map[common.Address][2]*big.Int // pooled, locked

	callCount int
}

func (m *mockEthClient) BlockNumber(ctx context.Context) (uint64, error) {
	return m.block, nil
}

func (m *mockEthClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	m.callCount++
	// We only expect multicall aggregate calls in these tests.
	// Decode input to learn the target list order.
	if len(msg.Data) < 4 {
		return nil, nil
	}
	method := m.multicallABI.Methods["aggregate"]
	args, err := method.Inputs.Unpack(msg.Data[4:])
	if err != nil {
		return nil, err
	}
	// args[0] is the tuple[] calls
	callsV := args[0]

	// Reflect over the slice of calls to extract .Target fields in order.
	// Each element is a struct with fields Target and CallData.
	rv := reflect.ValueOf(callsV)
	returnData := make([][]byte, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i)
		if item.Kind() == reflect.Pointer {
			item = item.Elem()
		}
		target := item.FieldByName("Target").Interface().(common.Address)
		val := m.capsuleValues[target]
		pooled := val[0]
		locked := val[1]
		outBz, err := m.capsuleABI.Methods["getPooledAndLockedBNBs"].Outputs.Pack(pooled, locked)
		if err != nil {
			return nil, err
		}
		returnData = append(returnData, outBz)
	}

	// multicall aggregate outputs: (uint256 blockNumber, bytes[] returnData)
	return method.Outputs.Pack(new(big.Int).SetUint64(m.block), returnData)
}

func TestBSCFetch_NoStakers_ReturnsZeroChanges(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 0, 0, 0
	latestChangesBytes = fetchertypes.NSTZeroChanges

	logger := feedertypes.NewLogger(feedertypes.LogConf{})
	s := &source{
		Source: nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
	}
	// Empty stakers
	s.SetNSTStakers(fetchertypes.StakerInfos{}, 1, 0)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pi == nil {
		t.Fatalf("expected price info, got nil")
	}
	if pi.Price != string(fetchertypes.NSTZeroChanges) {
		t.Fatalf("expected NSTZeroChanges, got len=%d", len(pi.Price))
	}
}

func TestBSCFetch_CapsuleOnly_MulticallProducesBalanceChanges(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 0, 0, 0
	latestChangesBytes = fetchertypes.NSTZeroChanges

	logger := feedertypes.NewLogger(feedertypes.LogConf{})

	mcABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		t.Fatalf("parse multicall abi: %v", err)
	}
	capABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		t.Fatalf("parse capsule abi: %v", err)
	}

	c1 := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	c2 := common.HexToAddress("0x00000000000000000000000000000000000000c2")

	mock := &mockEthClient{
		block:        12345,
		multicallABI: mcABI,
		capsuleABI:   capABI,
		capsuleValues: map[common.Address][2]*big.Int{
			c1: {big.NewInt(1e9), big.NewInt(2e9)}, // total 3e9 => 3 gwei
			c2: {big.NewInt(0), big.NewInt(0)},     // 0
		},
	}

	s := &source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
		client:        mock,
		multicallAddr: common.HexToAddress("0x00000000000000000000000000000000000000aa"),
		multicallABI:  mcABI,
		capsuleABI:    capABI,
	}
	s.pool = startNewWorkerPool(s, workerPoolConfig{numWorkers: 1, jobBuffer: 10})

	// staker0 old balance 0 -> new 3
	// staker1 old balance 1 -> new 0
	sInfos := fetchertypes.StakerInfos{
		0: &fetchertypes.StakerInfo{Validators: []string{c1.Hex()}, Balance: 0},
		1: &fetchertypes.StakerInfo{Validators: []string{c2.Hex()}, Balance: 1},
	}
	s.SetNSTStakers(sInfos, 1, 0)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var raw oracletypes.RawDataNST
	if err := proto.Unmarshal([]byte(pi.Price), &raw); err != nil {
		t.Fatalf("unmarshal RawDataNST: %v", err)
	}
	if raw.Version != 1 {
		t.Fatalf("expected version 1, got %d", raw.Version)
	}
	if len(raw.NstBalanceChanges) != 2 {
		t.Fatalf("expected 2 balance changes, got %d", len(raw.NstBalanceChanges))
	}
	// Expect staker0=3, staker1=0
	if raw.NstBalanceChanges[0].StakerIndex != 0 || raw.NstBalanceChanges[0].Balance != 3 {
		t.Fatalf("unexpected change[0]: %+v", raw.NstBalanceChanges[0])
	}
	if raw.NstBalanceChanges[1].StakerIndex != 1 || raw.NstBalanceChanges[1].Balance != 0 {
		t.Fatalf("unexpected change[1]: %+v", raw.NstBalanceChanges[1])
	}
}

func TestBSCFetch_OnlyChangedStakerIncluded(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 0, 0, 0
	latestChangesBytes = fetchertypes.NSTZeroChanges

	logger := feedertypes.NewLogger(feedertypes.LogConf{})

	mcABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		t.Fatalf("parse multicall abi: %v", err)
	}
	capABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		t.Fatalf("parse capsule abi: %v", err)
	}

	c1 := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	c2 := common.HexToAddress("0x00000000000000000000000000000000000000c2")

	// c1 total 5 gwei, c2 total 7 gwei
	mock := &mockEthClient{
		block:        12300,
		multicallABI: mcABI,
		capsuleABI:   capABI,
		capsuleValues: map[common.Address][2]*big.Int{
			c1: {big.NewInt(5e9), big.NewInt(0)},
			c2: {big.NewInt(7e9), big.NewInt(0)},
		},
	}

	s := &source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
		client:        mock,
		multicallAddr: common.HexToAddress("0x00000000000000000000000000000000000000aa"),
		multicallABI:  mcABI,
		capsuleABI:    capABI,
	}
	s.pool = startNewWorkerPool(s, workerPoolConfig{numWorkers: 1, jobBuffer: 10})

	// staker0 old 5 -> new 5 (unchanged), staker1 old 1 -> new 7 (changed)
	sInfos := fetchertypes.StakerInfos{
		0: &fetchertypes.StakerInfo{Validators: []string{c1.Hex()}, Balance: 5, WithdrawVersion: 0},
		1: &fetchertypes.StakerInfo{Validators: []string{c2.Hex()}, Balance: 1, WithdrawVersion: 0},
	}
	s.SetNSTStakers(sInfos, 1, 0)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var raw oracletypes.RawDataNST
	if err := proto.Unmarshal([]byte(pi.Price), &raw); err != nil {
		t.Fatalf("unmarshal RawDataNST: %v", err)
	}
	if len(raw.NstBalanceChanges) != 1 {
		t.Fatalf("expected 1 balance change, got %d", len(raw.NstBalanceChanges))
	}
	if raw.NstBalanceChanges[0].StakerIndex != 1 || raw.NstBalanceChanges[0].Balance != 7 {
		t.Fatalf("unexpected change: %+v", raw.NstBalanceChanges[0])
	}
}

func TestBSCFetch_NoChangeWhenHeightAndVersionsUnchanged_ReturnsCached(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 12300, 1, 5
	latestChangesBytes = []byte("cached-bytes")

	logger := feedertypes.NewLogger(feedertypes.LogConf{})

	mcABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		t.Fatalf("parse multicall abi: %v", err)
	}
	capABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		t.Fatalf("parse capsule abi: %v", err)
	}

	mock := &mockEthClient{
		block:        12300,
		multicallABI: mcABI,
		capsuleABI:   capABI,
		capsuleValues: map[common.Address][2]*big.Int{
			common.HexToAddress("0x00000000000000000000000000000000000000c1"): {big.NewInt(0), big.NewInt(0)},
		},
	}

	s := &source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
		client:        mock,
		multicallAddr: common.HexToAddress("0x00000000000000000000000000000000000000aa"),
		multicallABI:  mcABI,
		capsuleABI:    capABI,
	}
	s.pool = startNewWorkerPool(s, workerPoolConfig{numWorkers: 1, jobBuffer: 10})

	// version=1, withdrawVersion=5 match finalized ones, height equals finalizedBlock
	sInfos := fetchertypes.StakerInfos{
		0: &fetchertypes.StakerInfo{Validators: []string{"0x00000000000000000000000000000000000000c1"}, Balance: 0, WithdrawVersion: 0},
	}
	s.SetNSTStakers(sInfos, 1, 5)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pi.Price != string(latestChangesBytes) {
		t.Fatalf("expected cached bytes, got %q", pi.Price)
	}
	if mock.callCount != 0 {
		t.Fatalf("expected no CallContract when unchanged, got %d", mock.callCount)
	}
}

func TestBSCFetch_WithdrawVersionForcesInclusionEvenIfBalanceUnchanged(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 0, 0, 1
	latestChangesBytes = fetchertypes.NSTZeroChanges

	logger := feedertypes.NewLogger(feedertypes.LogConf{})

	mcABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		t.Fatalf("parse multicall abi: %v", err)
	}
	capABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		t.Fatalf("parse capsule abi: %v", err)
	}

	c1 := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	mock := &mockEthClient{
		block:        12300,
		multicallABI: mcABI,
		capsuleABI:   capABI,
		capsuleValues: map[common.Address][2]*big.Int{
			c1: {big.NewInt(5e9), big.NewInt(0)}, // 5 gwei
		},
	}

	s := &source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
		client:        mock,
		multicallAddr: common.HexToAddress("0x00000000000000000000000000000000000000aa"),
		multicallABI:  mcABI,
		capsuleABI:    capABI,
	}
	s.pool = startNewWorkerPool(s, workerPoolConfig{numWorkers: 1, jobBuffer: 10})

	// Balance unchanged at 5, but withdrawVersion increased beyond finalizedWithdrawVersion => should include.
	sInfos := fetchertypes.StakerInfos{
		0: &fetchertypes.StakerInfo{Validators: []string{c1.Hex()}, Balance: 5, WithdrawVersion: 2},
	}
	s.SetNSTStakers(sInfos, 1, 2)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var raw oracletypes.RawDataNST
	if err := proto.Unmarshal([]byte(pi.Price), &raw); err != nil {
		t.Fatalf("unmarshal RawDataNST: %v", err)
	}
	if len(raw.NstBalanceChanges) != 1 {
		t.Fatalf("expected 1 balance change, got %d", len(raw.NstBalanceChanges))
	}
	if raw.NstBalanceChanges[0].StakerIndex != 0 || raw.NstBalanceChanges[0].Balance != 5 {
		t.Fatalf("unexpected change: %+v", raw.NstBalanceChanges[0])
	}
}

func TestBSCFetch_VersionIncreasesButNoOtherChanges_ReturnsZeroChanges(t *testing.T) {
	// reset globals for isolation
	finalizedBlock, finalizedVersion, finalizedWithdrawVersion = 12300, 1, 5
	latestChangesBytes = fetchertypes.NSTZeroChanges

	logger := feedertypes.NewLogger(feedertypes.LogConf{})

	mcABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		t.Fatalf("parse multicall abi: %v", err)
	}
	capABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		t.Fatalf("parse capsule abi: %v", err)
	}

	c1 := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	mock := &mockEthClient{
		block:        12300, // same height bucket
		multicallABI: mcABI,
		capsuleABI:   capABI,
		capsuleValues: map[common.Address][2]*big.Int{
			c1: {big.NewInt(5e9), big.NewInt(0)}, // 5 gwei, unchanged
		},
	}

	s := &source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, nil, "", func(string, string) error { return nil }),
		client:        mock,
		multicallAddr: common.HexToAddress("0x00000000000000000000000000000000000000aa"),
		multicallABI:  mcABI,
		capsuleABI:    capABI,
	}
	s.pool = startNewWorkerPool(s, workerPoolConfig{numWorkers: 1, jobBuffer: 10})

	// version increased (2 > finalizedVersion), but wV unchanged and balance unchanged => expect zero changes
	sInfos := fetchertypes.StakerInfos{
		0: &fetchertypes.StakerInfo{Validators: []string{c1.Hex()}, Balance: 5, WithdrawVersion: 5},
	}
	s.SetNSTStakers(sInfos, 2, 5)

	pi, err := s.fetch(string(fetchertypes.NativeTokenBSC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pi.Price != string(fetchertypes.NSTZeroChanges) {
		t.Fatalf("expected NSTZeroChanges, got %q", pi.Price)
	}
	if mock.callCount == 0 {
		t.Fatalf("expected multicall to be executed when version increased")
	}
}
