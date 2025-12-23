package bsc

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// we use gwei as unit of amount (uint64 should be enough)

var _ scannerInf = &source{}

var divisor = big.NewInt(1e9)

type multicall struct {
	Target   common.Address `abi:"target"`
	CallData []byte         `abi:"callData"`
}

// batchQueryCapsules queries capsule.getPooledAndLockedBNBs() for each capsule address via multicall.
// It returns pooled+locked (in wei) per capsule.
func (s *source) batchQueryCapsules(ctx context.Context, capsules []common.Address, blockNumber *big.Int) (map[common.Address]*big.Int, error) {
	calls := make([]multicall, 0, len(capsules))
	for _, capAddr := range capsules {
		callData, err := s.capsuleABI.Pack("getPooledAndLockedBNBs")
		if err != nil {
			return nil, err
		}
		calls = append(calls, multicall{Target: capAddr, CallData: callData})
	}

	calldata, err := s.multicallABI.Pack("aggregate", calls)
	if err != nil {
		return nil, err
	}
	res, err := s.client.CallContract(ctx, ethereum.CallMsg{
		To:   &s.multicallAddr,
		Data: calldata,
	}, blockNumber)
	if err != nil {
		return nil, err
	}
	unpackedRes, err := s.multicallABI.Unpack("aggregate", res)
	if err != nil {
		return nil, err
	}

	// unpackedRes[0] is blockNumber (*big.Int) - returned by multicall, can be ignored
	// unpackedRes[1] is returnData ([][]byte)
	if len(unpackedRes) != 2 {
		return nil, fmt.Errorf("unexpected unpack result length: %d", len(unpackedRes))
	}

	_ = unpackedRes[0].(*big.Int) // blockNumber from multicall response (we use the provided blockNumber parameter)
	returnData := unpackedRes[1].([][]byte)

	if len(returnData) != len(capsules) {
		return nil, fmt.Errorf("unexpected returnData length: %d, expected %d", len(returnData), len(capsules))
	}

	result := make(map[common.Address]*big.Int)
	for i, capAddr := range capsules {
		out, err := s.capsuleABI.Unpack("getPooledAndLockedBNBs", returnData[i])
		if err != nil {
			return nil, fmt.Errorf("failed to unpack getPooledAndLockedBNBs for capsule %s: %w", capAddr.Hex(), err)
		}
		if len(out) != 2 {
			return nil, fmt.Errorf("unexpected getPooledAndLockedBNBs output length: %d", len(out))
		}

		pooled := out[0].(*big.Int)
		locked := out[1].(*big.Int)
		result[capAddr] = new(big.Int).Add(pooled, locked)
	}

	return result, nil
}

func (s *source) getCurrentHeight() uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	height, err := s.client.BlockNumber(ctx)
	if err != nil {
		if logger := s.Logger(); logger != nil {
			logger.Error("failed to fetch current BSC height", "error", err)
		}
		return 0
	}
	return height
}
