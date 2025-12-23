package types

import (
	"os"
	"path"

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
	// Logger  feedertypes.LoggerInf
	Stakers *fetchertypes.Stakers
	*fetchertypes.Source
}

func NewSource(logger feedertypes.LoggerInf, name string, fetch fetchertypes.SourceFetchFunc, cfgPath string, reload fetchertypes.SourceReloadConfigFunc) *Source {
	return &Source{
		Stakers: fetchertypes.NewStakers(),
		Source:  fetchertypes.NewSource(logger, name, fetch, cfgPath, reload),
	}
}

func (s *Source) SetNSTStakers(sInfos fetchertypes.StakerInfos, version, withdrawVersion uint64) {
	s.Stakers.Locker.Lock()
	s.Stakers.SInfos = sInfos
	s.Stakers.Version = version
	s.Stakers.WithdrawVersion = withdrawVersion
	s.Stakers.Locker.Unlock()
}

var _ fetchertypes.SourceNSTInf = &Source{}

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

var Logger feedertypes.LoggerInf

func ParseConfig[T any](confPath, envConf string) (cfg T, err error) {
	var yamlFile *os.File
	yamlFile, err = os.Open(path.Join(confPath, envConf))
	if err != nil {
		return
	}
	err = yaml.NewDecoder(yamlFile).Decode(&cfg)
	return
}
