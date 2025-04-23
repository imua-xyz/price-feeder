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
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type RetryConfig struct {
	MaxAttempts int
	Attempts    int
	Interval    time.Duration
}

// DefaultRetryConfig provides default retry settings
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 43200, // defaultMaxRetry
	Attempts:    5,
	Interval:    2 * time.Second,
}

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
	pieceSize := oracleP.PieceSizeByte
	twoPhasesFeeders := make(map[int]string)
	for feederID, feeder := range oracleP.TokenFeeders {
		if feederID == 0 {
			continue
		}
		tokenName := strings.ToLower(oracleP.Tokens[feeder.TokenID].Name)
		decimal := oracleP.Tokens[feeder.TokenID].Decimal
		sourceName := fetchertypes.Chainlink
		// TODO(leonz): unify with Rule check
		if fetchertypes.IsNSTToken(tokenName) {
			nstToken := fetchertypes.NSTToken(tokenName)
			if sourceName = fetchertypes.GetNSTSource(nstToken); len(sourceName) == 0 {
				panic(fmt.Sprintf("source of nst:%s is not set", tokenName))
			}
			twoPhasesFeeders[feederID] = fetchertypes.GetNSTAssetID(nstToken)
		} else if !strings.HasSuffix(tokenName, fetchertypes.BaseCurrency) {
			// NOTE: this is for V1 only
			tokenName += fetchertypes.BaseCurrency
		}
		feeders.SetupFeeder(feeder, feederID, sourceName, tokenName, maxNonce, decimal, pieceSize, oracleP.IsRule2PhasesByFeederID(uint64(feederID)))
	}
	feeders.Start()

	// The InitComponents had done, which means then conenction between price-feeder and imuachain is established, so we don't need too many retries
	// this is processed before nst-events handling, so it's the only source of chainging for a specific feeder
	for feederID, assetID := range twoPhasesFeeders {
		feeder := feeders.feederMap[feederID]
		if err := ResetNSTStakers(ecClient, assetID, logger, feeder, true); err != nil {
			panic(fmt.Sprintf("failed to init nst stakers, assetID:%s, error:%s", assetID, err.Error()))
		}
		sInfos, version := feeder.stakers.GetStakerInfos()
		feeder.fetcherNST.SetNSTStakers(feeder.source, sInfos, version)
	}

	for event := range ecClient.EventsCh() {
		switch e := event.(type) {
		case *imuaclient.EventNewBlock:
			if e.ParamsUpdate() {
				oracleP, err = getOracleParamsWithMaxRetry(DefaultRetryConfig.MaxAttempts, ecClient, logger)
				if err != nil {
					logger.Error(fmt.Sprintf("Failed to get oracle params with maxRetry when params update detected, price-feeder will exit, error:%v", err))
					return
				}
				feeders.UpdateOracleParams(oracleP)
			}
			if e.NSTStakersUpdate() {
				eNSTStakers := e.NSTStakers()
				feederIDs := make([]uint64, 0, len(eNSTStakers))
				for feederID, update := range eNSTStakers {
					feederIDs = append(feederIDs, feederID)
					// do the conversion on imuachain side to unify all NSTs
					if fetchertypes.NSTToken(feeders.feederMap[int(feederID)].token) == fetchertypes.NativeTokenETH {
						for _, sInfo := range update[0].SInfos() {
							for i, validator := range sInfo.Validators {
								// TODO: error handling
								validatorIdx := fetchertypes.ConvertBytesToIntStr(validator)
								sInfo.Validators[i] = validatorIdx
							}
						}
						for _, sInfo := range update[1].SInfos() {
							for i, validator := range sInfo.Validators {
								// TODO: error handling
								validatorIdx := fetchertypes.ConvertBytesToIntStr(validator)
								sInfo.Validators[i] = validatorIdx
							}
						}
					}
				}
				logger.Info("update stakers for nst", "feederIDs", feederIDs)
				failed := feeders.UpdateNSTStakers(eNSTStakers)
				for _, f := range failed {
					logger.Error("failed to update stakerInfos for nst, do resetAll", "feederID", f.feederID, "error", f.err)
					feeder := feeders.feederMap[int(f.feederID)]
					// the return error is just logged and waiting for next round to update for those nsts which failed to reset their staker infos
					err = ResetNSTStakers(ecClient, fetchertypes.GetNSTAssetID(fetchertypes.NSTToken(feeder.token)), logger, feeder, true)
					if err != nil {
						logger.Error("failed to resetAll nst stakers for fail updating", "feederID", f.feederID, "error", err)
					} else {
						sInfos, version := feeder.stakers.GetStakerInfos()
						feeder.fetcherNST.SetNSTStakers(feeder.source, sInfos, version)
					}
				}
			} else if e.NSTBalancesUpdate() {
				// balance update and stakers update will not happen in the same block
				eNSTBalances := e.NSTBalances()
				failed := feeders.UpdateNSTBalances(eNSTBalances)
				for _, f := range failed {
					logger.Error("failed to update stakerInfos for nst, do resetAll", "feederID", f.feederID, "error", f.err)
					feeder := feeders.feederMap[int(f.feederID)]
					// the return error is just logged and waiting for next round to update for those nsts which failed to reset their staker infos
					err = ResetNSTStakers(ecClient, fetchertypes.GetNSTAssetID(fetchertypes.NSTToken(feeder.token)), logger, feeder, true)
					if err != nil {
						logger.Error("failed to resetAll nst stakers for fail updating", "feederID", f.feederID, "error", err)
					} else {
						sInfos, version := feeder.stakers.GetStakerInfos()
						feeder.fetcherNST.SetNSTStakers(feeder.source, sInfos, version)
					}
				}
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
		case imuaclient.EventNSTBalances:
			feeders.UpdateNSTBalances(e)
		case imuaclient.EventNSTPieces:
			feeders.UpdateNSTPieces(e)
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

// func ResetAllStakerValidators(ec imuaclient.ImuaClientInf, feederID uint64, assetID string, logger feedertypes.LoggerInf, fs *Feeders) error {
func ResetNSTStakers(ec imuaclient.ImuaClientInf, assetID string, logger feedertypes.LoggerInf, feeder *feeder, all bool) error {
	count := 0
	for count < DefaultRetryConfig.Attempts {
		stakerInfos, version, err := ec.GetStakerInfos(assetID)
		if err != nil {
			logger.Error("failed to get stakerInfos for native-restaking-token", "feederID", feeder.feederID, "token", feeder.token, "error", err)
			count++
			continue
		}

		if fetchertypes.NSTToken(feeder.token) == fetchertypes.NativeTokenETH {
			// beaconchain use hex validators index instead of validator pubkey
			// TODO: do this conversion on imuachain side
			for _, sInfo := range stakerInfos {
				for i, validator := range sInfo.ValidatorPubkeyList {
					// TODO: error handling
					validatorIdx, _ := fetchertypes.ConvertHexToIntStr(validator)
					sInfo.ValidatorPubkeyList[i] = validatorIdx
				}
			}
		}
		if err := feeder.stakers.Reset(stakerInfos, version, all); err != nil {
			logger.Error("failed to update stakers for native-restaking-token", "feederID", feeder.feederID, "token", feeder.token, "error", err)
			count++
			continue
		}
		return nil
	}
	return fmt.Errorf("failed to ResetAllStakerValidators after maxRetry:%d, feederID:%d, assetID:%s", DefaultRetryConfig.Attempts, feeder.feederID, assetID)
}

// initComponents, initialize fetcher, imuaclient, it will panic if any initialization fialed
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

		logger.Info("Initialization for price-feeder done")
		break
	}
	return nil
}
