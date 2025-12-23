package bsc

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	nsttypes "github.com/imua-xyz/price-feeder/fetcher/nst/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const (
	envConf = "oracle_env_bsc.yaml"

	multicallABIJSON = `[
	{
	  "inputs": [
		{
		  "components": [
			{"internalType": "address","name": "target","type": "address"},
			{"internalType": "bytes","name": "callData","type": "bytes"}
		  ],
		  "internalType": "struct Call[]",
		  "name": "calls",
		  "type": "tuple[]"
		}
	  ],
	  "name": "aggregate",
	  "outputs": [
		{"internalType": "uint256","name": "blockNumber","type": "uint256"},
		{"internalType": "bytes[]","name": "returnData","type": "bytes[]"}
	  ],
	  "stateMutability": "nonpayable",
	  "type": "function"
	}
  ]`

	capsuleABIJSON = `[
  {
    "inputs": [],
    "name": "getPooledAndLockedBNBs",
    "outputs": [
      {"internalType": "uint256","name": "pooledBNB","type": "uint256"},
      {"internalType": "uint256","name": "lockedBNB","type": "uint256"}
    ],
    "stateMutability": "view",
    "type": "function"
  }
]`
)

var (
	logger        feedertypes.LoggerInf
	defaultSource *source
	errInvalidJob = fmt.Errorf("invalid worker job")
)

type config struct {
	MuilticallAddr string `yaml:"multicall_addr"`
	URLs           struct {
		Bsc string `yaml:"bsc"`
	} `yaml:"urls"`
}

type source struct {
	*nsttypes.Source
	client ethClientInf

	multicallAddr common.Address

	multicallABI abi.ABI
	capsuleABI   abi.ABI

	pool *workerPool
}

// ethClientInf allows mocking eth client in tests.
type ethClientInf interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	BlockNumber(ctx context.Context) (uint64, error)
}

func init() {
	fetchertypes.SourceInitializers[fetchertypes.Bsc] = initBSC
}

func initBSC(cfgPath string, l feedertypes.LoggerInf) (fetchertypes.SourceInf, error) {
	if logger = l; logger == nil {
		if logger = feedertypes.GetLogger("fetcher_bsc"); logger == nil {
			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
		}
	}

	cfg, err := nsttypes.ParseConfig[config](cfgPath, envConf)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config, error:%v", err))
	}

	multicallABI, err := abi.JSON(strings.NewReader(multicallABIJSON))
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse multicall ABI, error:%v", err))
	}

	capsuleABI, err := abi.JSON(strings.NewReader(capsuleABIJSON))
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse capsule ABI, error:%v", err))
	}

	client, err := ethclient.Dial(cfg.URLs.Bsc)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to dial BSC, error:%v", err))
	}

	defaultSource = &source{}

	*defaultSource = source{
		Source:        nsttypes.NewSource(logger, fetchertypes.Bsc, defaultSource.fetch, cfgPath, defaultSource.reload),
		client:        client,
		multicallAddr: common.HexToAddress(cfg.MuilticallAddr),
		multicallABI:  multicallABI,
		capsuleABI:    capsuleABI,
		pool: startNewWorkerPool(defaultSource, workerPoolConfig{
			numWorkers: 10,
			jobBuffer:  100,
		}),
	}

	return defaultSource, nil
}
