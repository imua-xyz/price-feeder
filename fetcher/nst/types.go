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

const (
	HexPrefix = "0x"
)

var (
	Logger feedertypes.LoggerInf
)

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
