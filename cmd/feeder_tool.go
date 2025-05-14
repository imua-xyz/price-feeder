package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/imua-xyz/price-feeder/fetcher"
	"github.com/imua-xyz/price-feeder/imuaclient"
	"github.com/imua-xyz/price-feeder/types"

	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/fetcher/beaconchain"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type RetryConfig struct {
	MaxAttempts int
	Interval    time.Duration
}

// DefaultRetryConfig provides default retry settings
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 43200, // defaultMaxRetry
	Interval:    2 * time.Second,
}

// var updateConfig sync.Mutex

// RunPriceFeeder runs price feeder to fetching price and feed to imuachain
func RunPriceFeeder(conf *feedertypes.Config, logger feedertypes.LoggerInf, mnemonic string, sourcesPath string, standalone bool) {
	// init logger
	if logger = feedertypes.SetLogger(logger); logger == nil {
		panic("logger is not initialized")
	}
	// init logger, fetchers, imuaclient
	if err := initComponents(logger, conf, sourcesPath, standalone); err != nil {
		logger.Error("failed to initialize components")
		panic(err)
	}
	// initComponents(logger, conf, standalone)

	f, _ := fetcher.GetFetcher()
	// start fetching on all supported sources and tokens
	logger.Info("start fetching prices from all sources")
	if err := f.Start(); err != nil {
		panic(fmt.Sprintf("failed to start Fetcher, error:%v", err))
	}

	ecClient, _ := imuaclient.GetClient()
	defer ecClient.Close()
	// initialize oracle params by querying from imua
	oracleP, err := getOracleParamsWithMaxRetry(DefaultRetryConfig.MaxAttempts, ecClient, logger)
	if err != nil {
		panic(fmt.Sprintf("failed to get initial oracle params: %v", err))
	}

	ecClient.Subscribe()

	feeders := NewFeeders(feedertypes.GetLogger("feeders"), f, ecClient)
	// we don't check empty tokenfeeders list
	maxNonce := oracleP.MaxNonce
	for feederID, feeder := range oracleP.TokenFeeders {
		if feederID == 0 {
			continue
		}
		tokenName := strings.ToLower(oracleP.Tokens[feeder.TokenID].Name)
		decimal := oracleP.Tokens[feeder.TokenID].Decimal
		source := fetchertypes.Chainlink
		if fetchertypes.IsNSTToken(tokenName) {
			nstToken := fetchertypes.NSTToken(tokenName)
			if source = fetchertypes.GetNSTSource(nstToken); len(source) == 0 {
				panic(fmt.Sprintf("source of nst:%s is not set", tokenName))
			}
		} else if !strings.HasSuffix(tokenName, fetchertypes.BaseCurrency) {
			// NOTE: this is for V1 only
			tokenName += fetchertypes.BaseCurrency
		}

		feeders.SetupFeeder(feeder, feederID, source, tokenName, maxNonce, decimal)
	}
	feeders.Start()

	for event := range ecClient.EventsCh() {
		switch e := event.(type) {
		case *imuaclient.EventNewBlock:
			if paramsUpdate := e.ParamsUpdate(); paramsUpdate {
				oracleP, err = getOracleParamsWithMaxRetry(DefaultRetryConfig.MaxAttempts, ecClient, logger)
				if err != nil {
					logger.Error(fmt.Sprintf("Failed to get oracle params with maxRetry when params update detected, price-feeder will exit, error:%v", err))
					return
				}
				feeders.UpdateOracleParams(oracleP)
			}
			feeders.Trigger(e.Height(), e.FeederIDs())
		case *imuaclient.EventUpdatePrice:
			finalPrices := make([]*finalPrice, 0, len(e.Prices()))
			var syncPriceInfo string
			for _, price := range e.Prices() {
				feederIDList := oracleP.GetFeederIDsByTokenID(uint64(price.TokenID()))
				l := len(feederIDList)
				if l == 0 {
					logger.Error("Failed to get feederIDs by tokenID when try to updata local price for feeders on event_updatePrice", "tokenID", price.TokenID())
					continue
				}
				feederID := feederIDList[l-1]
				finalPrices = append(finalPrices, &finalPrice{
					feederID: int64(feederID),
					price:    price.Price(),
					decimal:  price.Decimal(),
					roundID:  price.RoundID(),
				})
				syncPriceInfo += fmt.Sprintf("feederID:%d, price:%s, decimal:%d, roundID:%s\n", feederID, price.Price(), price.Decimal(), price.RoundID())
			}
			logger.Info("sync local price from event", "prices", syncPriceInfo)
			feeders.UpdatePrice(e.TxHeight(), finalPrices)
		case imuaclient.EventUpdateNSTs:
			addList, removeList, nextVersion, latestVersion := e.Parse()
			// NOTE: for v1, beaconchain only:
			var err error
			addList, err = beaconchain.ConvertHexToIntStrForMap(addList)
			if err != nil {
				logger.Error("failed to convert hex to int for staker's validator list", "error", err)
				break
			}
			removeList, err = beaconchain.ConvertHexToIntStrForMap(removeList)
			if err != nil {
				logger.Error("failed to convert hex to int for staker's validator list", "error", err)
				break
			}
			if err := beaconchain.UpdateStakerValidators(addList, removeList, nextVersion, latestVersion); err != nil {
				logger.Error("failed to update staker's validator list", "nextVersion", nextVersion, "latestVersion", latestVersion, "addList", addList, "removeList", removeList, "error", err)
				// try to reset all validatorList
				if err := ResetAllStakerValidators(ecClient, logger); err != nil {
					logger.Error("failed to reset all staker's validators for native-restaking-eth", "error", err)
				} else {
					logger.Info("reset all staker's validators for native-restaking-eth")
				}
			} else {
				logger.Info("updated Staker validator list for beaconchain fetcher", "stakerID", addList, "nextVersion", nextVersion, "latestVersion", latestVersion)
			}
		}
	}
}

// getOracleParamsWithMaxRetry, get oracle params with max retry
// blocked
func getOracleParamsWithMaxRetry(maxRetry int, ecClient imuaclient.ImuaClientInf, logger feedertypes.LoggerInf) (oracleP *oracletypes.Params, err error) {
	if maxRetry <= 0 {
		maxRetry = DefaultRetryConfig.MaxAttempts
	}
	for i := 0; i < maxRetry; i++ {
		oracleP, err = ecClient.GetParams()
		if err == nil {
			return
		}
		logger.Error("Failed to get oracle params, retrying...", "count", i, "max", maxRetry, "error", err)
		time.Sleep(DefaultRetryConfig.Interval)
	}
	return
}

func ResetAllStakerValidators(ec imuaclient.ImuaClientInf, logger feedertypes.LoggerInf) error {
	stakerInfos, version, err := ec.GetStakerInfos(fetchertypes.GetNSTAssetID(fetchertypes.NativeTokenETH))
	if err != nil {
		return fmt.Errorf("failed to get stakerInfos for native-restaking-eth, error:%w", err)
	}
	if len(stakerInfos) > 0 {
		if err := beaconchain.ResetStakerValidators(stakerInfos, version, true); err != nil {
			return fmt.Errorf("failed to set stakerInfs for native-restaking-eth, error:%w", err)
		}
	}
	return nil
}

// // initComponents, initialize fetcher, imuaclient, it will panic if any initialization fialed
func initComponents(logger types.LoggerInf, conf *types.Config, sourcesPath string, standalone bool) error {
	count := 0
	for count < DefaultRetryConfig.MaxAttempts {
		count++
		// init fetcher, start fetchers to get prices from sources
		err := fetcher.Init(conf.Tokens, sourcesPath)
		if err != nil {
			return fmt.Errorf("failed to init fetcher, error:%w", err)
		}

		// init imuaclient
		err = imuaclient.Init(conf, mnemonic, privFile, false, standalone)
		if err != nil {
			if errors.Is(err, feedertypes.ErrInitConnectionFail) {
				logger.Info("retry initComponents due to connectionfailed", "count", count, "maxRetry", DefaultRetryConfig.MaxAttempts, "error", err)
				time.Sleep(DefaultRetryConfig.Interval)
				continue
			}
			return fmt.Errorf("failed to init imuaclient, error;%w", err)
		}

		ec, _ := imuaclient.GetClient()

		_, err = getOracleParamsWithMaxRetry(DefaultRetryConfig.MaxAttempts, ec, logger)
		if err != nil {
			return fmt.Errorf("failed to get oracle params on start, error:%w", err)
		}

		// init native stakerlist for nstETH(beaconchain)
		if err := ResetAllStakerValidators(ec, logger); err != nil {
			return fmt.Errorf("failed in initialize nst:%w", err)
		}

		logger.Info("Initialization for price-feeder done")
		break
	}
	return nil
}
