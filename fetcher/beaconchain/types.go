package beaconchain

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/ethclient"
	nsttypes "github.com/imua-xyz/price-feeder/fetcher/nst/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

/**
limitation of imuachainv1:
	10 NST
	200,000  stakers per NST
	20 validators per staker

=> 4,000,000 validators per NST
=> total validatoList size memory usage for price-feeder
	4,000,000 * 10 * 20 = 800,000,000 -> 800MB
   total 'staker memory usage for price-feeder
   	200,000 * 10 * (4(uint32_index) + 8*(uint64_balance)) = 24MB
   with other metaData, the total memory usage for theses information could be limited under 1GB(which is for 40,000,000 validators)

**/

type source struct {
	*nsttypes.Source
	ethClient        *ethclient.Client
	bootstrapAddress string
}

// type source struct {
// 	logger  feedertypes.LoggerInf
// 	stakers *fetchertypes.Stakers
// 	*fetchertypes.Source
// 	ethClient        *ethclient.Client
// 	bootstrapAddress string // the address of the bootstrap contract, used to get capsule address for stakers
// }

type config struct {
	URLs struct {
		Beaconchain string `yaml:"beaconchain"`
		ETH         string `yaml:"eth"`
	} `yaml:"urls"`
	NSTID     string `yaml:"nstid"`
	Bootstrap string `yaml:"bootstrap"` // the address of the bootstrap contract, used to get capsule address for stakers
}

type ResultConfig struct {
	Data struct {
		SlotsPerEpoch string `json:"SLOTS_PER_EPOCH"`
	} `json:"data"`
}

// func (s *source) SetNSTStakers(sInfos fetchertypes.StakerInfos, version, withdrawVersion uint64) {
// 	s.Stakers.Locker.Lock()
// 	s.Stakers.SInfos = sInfos
// 	s.Stakers.Version = version
// 	s.Stakers.WithdrawVersion = withdrawVersion
// 	s.Stakers.Locker.Unlock()
// }

const (
	envConf               = "oracle_env_beaconchain.yaml"
	urlQuerySlotsPerEpoch = "eth/v1/config/spec"
	hexPrefix             = "0x"
)

var (
	logger        feedertypes.LoggerInf
	defaultSource *source
)

func init() {
	fetchertypes.SourceInitializers[fetchertypes.BeaconChain] = initBeaconchain
}

func initBeaconchain(cfgPath string, l feedertypes.LoggerInf) (fetchertypes.SourceInf, error) {
	if logger = l; logger == nil {
		if logger = feedertypes.GetLogger("fetcher_beaconchain"); logger == nil {
			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
		}
	}
	// init from config file
	// cfg, err := parseConfig(cfgPath)
	cfg, err := nsttypes.ParseConfig[config](cfgPath, envConf)
	if err != nil {
		// logger.Error("fail to parse config", "error", err, "path", cfgPath)
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config, error:%v", err))
	}
	// beaconchain endpoint url
	urlEndpoint, err = url.Parse(cfg.URLs.Beaconchain)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse url:%s, error:%v", cfg.URLs.Beaconchain, err))
	}

	// parse nstID by splitting it with "_'"
	nstID := strings.Split(cfg.NSTID, "_")
	if len(nstID) != 2 {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("invalid nstID format, nstID:%s", nstID))
	}
	// the second element is the lzID of the chain, trim possible prefix_0x
	lzID, err := strconv.ParseUint(strings.TrimPrefix(nstID[1], hexPrefix), 16, 64)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse lzID:%s from nstID, error:%v", nstID[1], err))
	}

	// set slotsPerEpoch
	if slotsPerEpochKnown, ok := fetchertypes.ChainToSlotsPerEpoch[lzID]; ok {
		slotsPerEpoch = slotsPerEpochKnown
	} else {
		// else, we need the slotsPerEpoch from beaconchain endpoint
		u := urlEndpoint.JoinPath(urlQuerySlotsPerEpoch)
		res, err := http.Get(u.String())
		if err != nil {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to get slotsPerEpoch from endpoint:%s, error:%v", u.String(), err))
		}
		result, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to get slotsPerEpoch from endpoint:%s, error:%v", u.String(), err))
		}
		var re ResultConfig
		if err = json.Unmarshal(result, &re); err != nil {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse response from slotsPerEpoch, error:%v", err))
		}
		if slotsPerEpoch, err = strconv.ParseUint(re.Data.SlotsPerEpoch, 10, 64); err != nil {
			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse response_slotsPerEoch, got:%s, rror:%v", re.Data.SlotsPerEpoch, err))
		}
	}

	client, err := ethclient.Dial(cfg.URLs.ETH)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to connect to Ethereum node: %v", err))
	}

	if cfg.Bootstrap == "" {
		return nil, feedertypes.ErrInitFail.Wrap("bootstrap address is not set")
	}
	if !fetchertypes.IsContractAddress(cfg.Bootstrap, client, logger) {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("bootstrap address is not a contract address: %s", cfg.Bootstrap))
	}
	// init first to get a fixed pointer for 'fetch' to refer to
	defaultSource = &source{}

	*defaultSource = source{
		Source:           nsttypes.NewSource(logger, fetchertypes.BeaconChain, defaultSource.fetch, cfgPath, defaultSource.reload),
		ethClient:        client,
		bootstrapAddress: cfg.Bootstrap,
	}

	// update nst assetID to be consistent with imuad. for beaconchain it's about different lzID
	fetchertypes.SetNativeAssetID(fetchertypes.NativeTokenETH, cfg.NSTID)

	return defaultSource, nil
}

// func parseConfig(confPath string) (config, error) {
// 	yamlFile, err := os.Open(path.Join(confPath, envConf))
// 	if err != nil {
// 		return config{}, err
// 	}
// 	cfg := config{}
// 	if err = yaml.NewDecoder(yamlFile).Decode(&cfg); err != nil {
// 		return config{}, err
// 	}
// 	return cfg, nil
// }
