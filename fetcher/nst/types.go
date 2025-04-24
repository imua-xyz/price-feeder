package nst

import (
	"os"
	"path"

	"github.com/imua-xyz/price-feeder/fetcher/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"gopkg.in/yaml.v2"
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

type Source struct {
	Logger  feedertypes.LoggerInf
	Stakers *fetchertypes.Stakers
	*types.Source
}

type Config struct {
	URL   string `yaml:"url"`
	NSTID string `yaml:"nstid"`
}

type ResultConfig struct {
	Data struct {
		SlotsPerEpoch string `json:"SLOTS_PER_EPOCH"`
	} `json:"data"`
}

// func (s *Source) SetNSTStakers(sInfos fetchertypes.StakerInfos, version uint64) {
// 	s.Stakers.Locker.Lock()
// 	s.Stakers.SInfos = sInfos
// 	s.Stakers.Version = version
// 	s.Stakers.Locker.Unlock()
// }

const (
	//	envConf               = "oracle_env_beaconchain.yaml"
	//	urlQuerySlotsPerEpoch = "eth/v1/config/spec"
	HexPrefix = "0x"
)

var (
	Logger feedertypes.LoggerInf

// defaultSource *source
)

// func init() {
// 	types.SourceInitializers[types.BeaconChain] = initBeaconchain
// }
//
// func initBeaconchain(cfgPath string, l feedertypes.LoggerInf) (types.SourceInf, error) {
// 	if logger = l; logger == nil {
// 		if logger = feedertypes.GetLogger("fetcher_beaconchain"); logger == nil {
// 			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
// 		}
// 	}
// 	// init from config file
// 	cfg, err := parseConfig(cfgPath)
// 	if err != nil {
// 		// logger.Error("fail to parse config", "error", err, "path", cfgPath)
// 		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config, error:%v", err))
// 	}
// 	// beaconchain endpoint url
// 	urlEndpoint, err = url.Parse(cfg.URL)
// 	if err != nil {
// 		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse url:%s, error:%v", cfg.URL, err))
// 	}
//
// 	// parse nstID by splitting it with "_'"
// 	nstID := strings.Split(cfg.NSTID, "_")
// 	if len(nstID) != 2 {
// 		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("invalid nstID format, nstID:%s", nstID))
// 	}
// 	// the second element is the lzID of the chain, trim possible prefix_0x
// 	lzID, err := strconv.ParseUint(strings.TrimPrefix(nstID[1], hexPrefix), 16, 64)
// 	if err != nil {
// 		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse lzID:%s from nstID, error:%v", nstID[1], err))
// 	}
//
// 	// set slotsPerEpoch
// 	if slotsPerEpochKnown, ok := types.ChainToSlotsPerEpoch[lzID]; ok {
// 		slotsPerEpoch = slotsPerEpochKnown
// 	} else {
// 		// else, we need the slotsPerEpoch from beaconchain endpoint
// 		u := urlEndpoint.JoinPath(urlQuerySlotsPerEpoch)
// 		res, err := http.Get(u.String())
// 		if err != nil {
// 			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to get slotsPerEpoch from endpoint:%s, error:%v", u.String(), err))
// 		}
// 		result, err := io.ReadAll(res.Body)
// 		if err != nil {
// 			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to get slotsPerEpoch from endpoint:%s, error:%v", u.String(), err))
// 		}
// 		var re ResultConfig
// 		if err = json.Unmarshal(result, &re); err != nil {
// 			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse response from slotsPerEpoch, error:%v", err))
// 		}
// 		if slotsPerEpoch, err = strconv.ParseUint(re.Data.SlotsPerEpoch, 10, 64); err != nil {
// 			return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse response_slotsPerEoch, got:%s, rror:%v", re.Data.SlotsPerEpoch, err))
// 		}
// 	}
//
// 	// init first to get a fixed pointer for 'fetch' to refer to
// 	defaultSource = &source{}
// 	*defaultSource = source{
// 		logger:  logger,
// 		Source:  types.NewSource(logger, types.BeaconChain, defaultSource.fetch, cfgPath, defaultSource.reload),
// 		stakers: types.NewStakers(),
// 	}
//
// 	// initialize native-restaking stakers' beaconchain-validator list
//
// 	// update nst assetID to be consistent with imuad. for beaconchain it's about different lzID
// 	types.SetNativeAssetID(fetchertypes.NativeTokenETH, cfg.NSTID)
//
// 	return defaultSource, nil
// }

func ParseConfig(confPath, envConf string) (Config, error) {
	yamlFile, err := os.Open(path.Join(confPath, envConf))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{}
	if err = yaml.NewDecoder(yamlFile).Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
