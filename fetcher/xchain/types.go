package xchain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"gopkg.in/yaml.v2"
)

const envConf = "oracle_env_xchain.yaml"

type TokenConfig struct {
	RPC           string `yaml:"rpc"`
	Gateway       string `yaml:"gateway"`
	SrcChainID    uint64 `yaml:"src_chain_id"`
	StartBlock    uint64 `yaml:"start_block"`
	StartNonce    uint64 `yaml:"start_nonce"`
	StartBatchSeq uint64 `yaml:"start_batch_seq"`
	Confirmations uint64 `yaml:"confirmations"`
	MaxMessages   int    `yaml:"max_messages"`
	MaxBytes      int    `yaml:"max_bytes"`
	MaxBlocks     uint64 `yaml:"max_blocks"`
}

type Config struct {
	ABIPath   string                 `yaml:"abi_path"`
	EventName string                 `yaml:"event_name"`
	Tokens    map[string]TokenConfig `yaml:"tokens"`
}

type tokenState struct {
	nextBlock uint64
	nextNonce uint64
	batchSeq  uint64
	lastPrice *types.PriceInfo
}

type source struct {
	logger feedertypes.LoggerInf
	*types.Source
	locker  *sync.Mutex
	clients map[string]*ethclient.Client
	config  Config
	states  map[string]*tokenState
	abi     abi.ABI
	event   abi.Event
}

var (
	logger        feedertypes.LoggerInf
	defaultSource *source
)

func init() {
	types.SourceInitializers[types.XChain] = initXChain
}

func initXChain(cfgPath string, l feedertypes.LoggerInf) (types.SourceInf, error) {
	if logger = l; logger == nil {
		if logger = feedertypes.GetLogger("fetcher_xchain"); logger == nil {
			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
		}
	}
	cfg, err := parseConfig(cfgPath)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config file, path:%s, error:%s", cfgPath, err))
	}
	if cfg.ABIPath == "" {
		return nil, feedertypes.ErrInitFail.Wrap("abi_path is required for xchain fetcher")
	}
	if cfg.EventName == "" {
		return nil, feedertypes.ErrInitFail.Wrap("event_name is required for xchain fetcher")
	}

	abiFile := cfg.ABIPath
	if !filepath.IsAbs(abiFile) {
		abiFile = filepath.Join(cfgPath, cfg.ABIPath)
	}
	abiBytes, err := os.ReadFile(abiFile)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to read abi file: %s, error:%v", abiFile, err))
	}
	parsedABI, err := abi.JSON(bytesReader(abiBytes))
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse abi json: %s", err))
	}
	event, ok := parsedABI.Events[cfg.EventName]
	if !ok {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("event not found in abi: %s", cfg.EventName))
	}

	defaultSource = &source{
		logger:  logger,
		locker:  new(sync.Mutex),
		clients: make(map[string]*ethclient.Client),
		config:  cfg,
		states:  make(map[string]*tokenState),
		abi:     parsedABI,
		event:   event,
	}
	defaultSource.Source = types.NewSource(logger, types.XChain, defaultSource.fetch, cfgPath, defaultSource.reload)
	return defaultSource, nil
}

func parseConfig(cfgPath string) (Config, error) {
	yamlFile, err := os.Open(filepath.Join(cfgPath, envConf))
	if err != nil {
		return Config{}, err
	}
	defer yamlFile.Close()

	cfg := Config{}
	if err = yaml.NewDecoder(yamlFile).Decode(&cfg); err != nil {
		return Config{}, err
	}
	if len(cfg.Tokens) == 0 {
		return Config{}, errors.New("xchain tokens config is empty")
	}
	return cfg, nil
}

func (s *source) getTokenConfig(token string) (TokenConfig, bool) {
	cfg, ok := s.config.Tokens[token]
	return cfg, ok
}

func (s *source) getState(token string, cfg TokenConfig) *tokenState {
	s.locker.Lock()
	defer s.locker.Unlock()

	state := s.states[token]
	if state == nil {
		batchSeq := cfg.StartBatchSeq
		if batchSeq == 0 {
			batchSeq = 1
		}
		nextNonce := cfg.StartNonce
		if nextNonce == 0 {
			nextNonce = 1
		}
		state = &tokenState{
			nextBlock: cfg.StartBlock,
			nextNonce: nextNonce,
			batchSeq:  batchSeq,
		}
		s.states[token] = state
	}
	return state
}

func (s *source) getClient(token string, cfg TokenConfig) (*ethclient.Client, error) {
	s.locker.Lock()
	defer s.locker.Unlock()

	if cfg.RPC == "" {
		return nil, errors.New("rpc is empty")
	}
	client := s.clients[token]
	if client != nil {
		return client, nil
	}
	c, err := ethclient.Dial(cfg.RPC)
	if err != nil {
		return nil, err
	}
	s.clients[token] = c
	return c, nil
}

func (s *source) reload(cfgPath, token string) error {
	cfg, err := parseConfig(cfgPath)
	if err != nil {
		return err
	}
	if _, ok := cfg.Tokens[token]; !ok {
		return feedertypes.ErrSourceTokenNotConfigured.Wrap(fmt.Sprintf("token %s not found in config", token))
	}
	s.locker.Lock()
	s.config = cfg
	s.locker.Unlock()
	return nil
}

// bytesReader returns an io.Reader for a byte slice without importing bytes everywhere.
func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}
