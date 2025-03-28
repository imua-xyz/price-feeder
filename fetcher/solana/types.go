package nstsol

import (
	"fmt"
	"strings"

	nsttypes "github.com/imua-xyz/price-feeder/fetcher/nst"
	"github.com/imua-xyz/price-feeder/fetcher/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type source nsttypes.Source

func (s *source) SetNSTStakers(sInfos fetchertypes.StakerInfos, version uint64) {
	s.Stakers.Locker.Lock()
	s.Stakers.SInfos = sInfos
	s.Stakers.Version = version
	s.Stakers.Locker.Unlock()
}

var _ fetchertypes.SourceNSTInf = &source{}

const (
	envConf   = "oracle_env_solana.yaml"
	hexPrefix = "0x"
)

var (
	logger        feedertypes.LoggerInf
	defaultSource *source
)

func init() {
	types.SourceInitializers[types.Solana] = initSolana
}

func initSolana(cfgPath string, l feedertypes.LoggerInf) (types.SourceInf, error) {
	if logger = l; logger == nil {
		if logger = feedertypes.GetLogger("fetcher_solana"); logger == nil {
			return nil, feedertypes.ErrInitFail.Wrap("logger is not initialized")
		}
	}
	// init from config file
	cfg, err := nsttypes.ParseConfig(cfgPath, envConf)
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse config, error:%v", err))
	}
	urlEndpoint = cfg.URL
	if err != nil {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse url:%s, error:%v", cfg.URL, err))
	}

	// parse nstID by splitting it with "_'"
	nstID := strings.Split(cfg.NSTID, "_")
	if len(nstID) != 2 {
		return nil, feedertypes.ErrInitFail.Wrap(fmt.Sprintf("invalid nstID format, nstID:%s", nstID))
	}

	// init first to get a fixed pointer for 'fetch' to refer to
	defaultSource = &source{}
	*defaultSource = source{
		Logger:  logger,
		Source:  types.NewSource(logger, types.Solana, defaultSource.fetch, cfgPath, defaultSource.reload),
		Stakers: types.NewStakers(),
	}

	types.SetNativeAssetID(fetchertypes.NativeTokenSOL, cfg.NSTID)

	return defaultSource, nil
}
