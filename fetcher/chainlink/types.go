package chainlink

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	aggregatorv3 "github.com/imua-xyz/price-feeder/fetcher/chainlink/aggregatorv3"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"gopkg.in/yaml.v2"
)

type source struct {
	logger feedertypes.LoggerInf
	*types.Source
	chainlinkProxy *proxy
	clients        map[string]*ethclient.Client
}

type Config struct {
	URLs     map[string]string `yaml:"urls"`
	Tokens   map[string]string `yaml:"tokens"`
	TokenNet []string          `yaml:"tokenNet"`
}

type proxy struct {
	locker      *sync.RWMutex
	clients     map[string]*ethclient.Client
	aggregators map[string]*aggregatorv3.AggregatorV3Interface
}

func newProxy() *proxy {
	return &proxy{
		locker:      new(sync.RWMutex),
		aggregators: make(map[string]*aggregatorv3.AggregatorV3Interface),
		clients:     make(map[string]*ethclient.Client),
	}
}

func (p *proxy) addToken(tokens map[string]string) error {
	p.locker.Lock()
	defer p.locker.Unlock()
	for token, address := range tokens {
		addrParsed := strings.Split(strings.TrimSpace(address), "_")
		if ok := isContractAddress(addrParsed[0], p.clients[addrParsed[1]]); !ok {
			return fmt.Errorf("invalid contract: address=%s chain=%s", addrParsed[0], addrParsed[1])
		}
		var err error
		if p.aggregators[strings.ToLower(token)], err = aggregatorv3.NewAggregatorV3Interface(common.HexToAddress(addrParsed[0]), p.clients[addrParsed[1]]); err != nil {
			return fmt.Errorf("failed to create aggregator: address=%s chain=%s error=%w",
				common.HexToAddress(addrParsed[0]), addrParsed[1], err)
		}
	}
	return nil
}

// addClient adds an ethClient, it will skip if the network exists in current clients
// does not need to be guard by lock
func (p *proxy) addClient(network, url string) error {
	var err error
	if _, ok := p.clients[network]; !ok {
		if len(url) == 0 {
			return errors.New("url is empty")
		}
		p.clients[network], err = ethclient.Dial(url)
	}
	return err
}

func (p *proxy) get(token string) (*aggregatorv3.AggregatorV3Interface, bool) {
	p.locker.RLock()
	aggregator, ok := p.aggregators[token]
	p.locker.RUnlock()
	return aggregator, ok
}

const envConf = "oracle_env_chainlink.yaml"

var (
	defaultSource *source
	logger        feedertypes.LoggerInf
)

func init() {
	types.SourceInitializers[types.Chainlink] = initChainlink
}

func initChainlink(cfgPath string, l feedertypes.LoggerInf) (types.SourceInf, error) {
	if logger = l; logger == nil {
		if logger = feedertypes.GetLogger("fetcher_chainlink"); logger == nil {
			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
		}
	}
	cfg, err := parseConfig(cfgPath)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config file, path;%s, error:%s", cfgPath, err))
	}
	// init defaultSource
	defaultSource = &source{
		logger:         logger,
		clients:        make(map[string]*ethclient.Client),
		chainlinkProxy: newProxy(),
	}
	// add ethClients
	for network, url := range cfg.URLs {
		if len(url) == 0 {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("rpcURL from config is empty, config_file:%s", envConf))
		}
		network = strings.ToLower(network)
		err = defaultSource.chainlinkProxy.addClient(network, url)
		if err != nil {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("fail to initialize ethClient, url:%s, error:%s", url, err))
		}
	}

	// add proxy for tokens
	if err = defaultSource.chainlinkProxy.addToken(cfg.Tokens); err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to add chainlinkPriceFeedProxy for token:%s, error:%v", cfg.Tokens, err))
	}

	defaultSource.Source = types.NewSource(logger, types.Chainlink, defaultSource.fetch, cfgPath, defaultSource.reload)
	return defaultSource, nil
}

func parseConfig(cfgPath string) (Config, error) {
	yamlFile, err := os.Open(path.Join(cfgPath, envConf))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err = yaml.NewDecoder(yamlFile).Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
