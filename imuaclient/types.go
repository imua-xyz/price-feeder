package imuaclient

import (
	cryptoed25519 "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/go-bip39"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/evmos/evmos/v16/encoding"
	"github.com/imua-xyz/imuachain/app"
	cmdcfg "github.com/imua-xyz/imuachain/cmd/config"
	oracleTypes "github.com/imua-xyz/imuachain/x/oracle/types"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type ImuaClientInf interface {
	// Query
	GetParams() (*oracletypes.Params, error)
	GetLatestPrice(tokenID uint64) (oracletypes.PriceTimeRound, error)
	GetStakerInfos(assetID string) ([]*oracleTypes.StakerInfo, *oracleTypes.NSTVersion, error)
	// GetStakerInfo(assetID, stakerAddr string) ([]*oracleTypes.StakerInfo, int64, error)

	// Tx
	SendTx(feederID uint64, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*sdktx.BroadcastTxResponse, error)

	// Ws subscriber
	Subscribe()
}

type EventInf interface {
	Type() EventType
}

type FeedVersion struct {
	Version         uint64
	WithdrawVersion uint64
}

type EventNewBlock struct {
	height       int64
	gas          string
	paramsUpdate bool
	nstStakers   EventNSTStakers
	nstBalances  EventNSTBalances
	feederIDs    map[int64]struct{}
	// nstFeedVersions map[uint64]uint64
	nstFeedVersions map[uint64]*FeedVersion
}

func (s *SubscribeResult) GetEventNewBlock() (*EventNewBlock, error) {
	height, ok := s.BlockHeight()
	if !ok {
		return nil, errors.New("failed to get height from event_newBlock response")
	}
	fee, ok := s.Fee()
	if !ok {
		return nil, errors.New("failed to get gas from event_newBlock response")
	}
	feederIDs, ok := s.FeederIDs()
	if !ok {
		return nil, errors.New("failed to get feederIDs from event_newBlock response")
	}

	ret := &EventNewBlock{
		height:       height,
		gas:          fee,
		paramsUpdate: s.ParamsUpdate(),
		feederIDs:    feederIDs,
	}

	if len(s.Result.Events.NSTStakersChange) > 0 {
		eNSTStakers, err := s.getEventNSTStakers()
		if err != nil {
			return nil, err
		}
		ret.nstStakers = eNSTStakers
	}

	if len(s.Result.Events.NSTFeedVersion) > 0 {
		feedVersions, err := s.GetFeedVersions()
		if err != nil {
			return nil, fmt.Errorf("failed to parse feedVersion from event_newBlock response, error:%w", err)
		}
		ret.nstFeedVersions = feedVersions
	}

	if len(s.Result.Events.NSTBalanceChange) > 0 {
		nstBalances, err := s.getEventNSTBalances()
		if err != nil {
			return nil, fmt.Errorf("failed to parse nstBalanceChange from event_newBlock response, error:%w", err)
		}
		ret.nstBalances = nstBalances
	}
	return ret, nil
}

func (e *EventNewBlock) Height() int64 {
	return e.height
}

func (e *EventNewBlock) Gas() string {
	return e.gas
}

func (e *EventNewBlock) ParamsUpdate() bool {
	return e.paramsUpdate
}

func (e *EventNewBlock) NSTStakersUpdate() bool {
	return len(e.nstStakers) > 0
}

func (e *EventNewBlock) NSTBalancesUpdate() bool {
	return len(e.nstBalances) > 0
}

func (e *EventNewBlock) NSTFeedVersionsUpdate() bool {
	return len(e.nstFeedVersions) > 0
}

func (e *EventNewBlock) NSTFeedVersions() map[uint64]*FeedVersion {
	return e.nstFeedVersions
}

func (e *EventNewBlock) ConvertNSTBalanceChangesFromFeedVersions() EventNSTBalances {
	if len(e.nstFeedVersions) == 0 {
		return nil
	}
	ret := make(EventNSTBalances)
	for feederID, version := range e.nstFeedVersions {
		ret[feederID] = &EventNSTBalance{
			rootHash:        fetchertypes.EmptyRawDataChangesRootHash[:],
			version:         version.Version,
			withdrawVersion: version.WithdrawVersion,
		}
	}
	return ret
}

func (e *EventNewBlock) NSTStakers() EventNSTStakers {
	return e.nstStakers
}

func (e *EventNewBlock) NSTBalances() EventNSTBalances {
	return e.nstBalances
}

func (e *EventNewBlock) FeederIDs() map[int64]struct{} {
	return e.feederIDs
}

func (e *EventNewBlock) Type() EventType {
	return ENewBlock
}

type FinalPrice struct {
	tokenID int64
	roundID string
	price   string
	decimal int32
}

func (f *FinalPrice) TokenID() int64 {
	return f.tokenID
}
func (f *FinalPrice) RoundID() string {
	return f.roundID
}
func (f *FinalPrice) Price() string {
	return f.price
}
func (f *FinalPrice) Decimal() int32 {
	return f.decimal
}

type EventUpdatePrice struct {
	prices   []*FinalPrice
	txHeight int64
}

func (s *SubscribeResult) GetEventUpdatePrice() (*EventUpdatePrice, error) {
	prices, ok := s.FinalPrice()
	if !ok {
		return nil, errors.New("failed to get finalPrice from event_txUpdatePrice response")
	}
	txHeight, ok := s.TxHeight()
	if !ok {
		return nil, errors.New("failed to get txHeight from event_txUpdatePrice response")
	}
	return &EventUpdatePrice{
		prices:   prices,
		txHeight: txHeight,
	}, nil
}
func (e *EventUpdatePrice) Prices() []*FinalPrice {
	return e.prices
}

func (e *EventUpdatePrice) TxHeight() int64 {
	return e.txHeight
}

func (e *EventUpdatePrice) Type() EventType {
	return EUpdatePrice
}

type EventNSTBalances map[uint64]*EventNSTBalance

type EventNSTBalance struct {
	rootHash        []byte
	version         uint64
	withdrawVersion uint64
}

func (e *EventNSTBalance) RootHash() []byte {
	return e.rootHash
}

func (e *EventNSTBalance) Versions() (uint64, uint64) {
	return e.version, e.withdrawVersion
}

func (e EventNSTBalances) Type() EventType {
	return ENSTBalances
}

func (s *SubscribeResult) getEventNSTBalances() (EventNSTBalances, error) {
	if len(s.Result.Events.NSTBalanceChange) < 1 {
		return nil, errors.New("failed to get nstBalanceChange from event_txUpdaetNSTBalance response")
	}
	ret := make(EventNSTBalances)

	for _, nstBC := range s.Result.Events.NSTBalanceChange {
		tmp := strings.Split(nstBC, "|")
		if len(tmp) != 4 {
			return nil, errors.New("failed to parse nstBalanceChange from event_txUpdateNSTBalance response, expected 3 parts")
		}
		feederID, err := strconv.ParseUint(tmp[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse feederID from nstBalanceChange in event_txUpdateNSTBalance response, error:%w", err)
		}
		version, err := strconv.ParseUint(tmp[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse version from nstBalanceChange in event_txUpdateNSTBalance response, feederID:%d, error:%w", feederID, err)
		}
		withdrawVersion, err := strconv.ParseUint(tmp[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse withdrawVersion from nstBalanceChange in event_txUpdateNSTBalance response, feederID:%d, error:%w", feederID, err)
		}

		rootHash, err := base64.StdEncoding.DecodeString(tmp[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse rootHash from nstBalanceChange in event_txUpdateNSTBalance response, feederID:%d, error:%w", feederID, err)
		}
		ret[feederID] = &EventNSTBalance{
			rootHash:        rootHash,
			version:         version,
			withdrawVersion: withdrawVersion,
		}
	}
	return ret, nil
}

type EventNSTPieces map[uint64]uint32

func (e EventNSTPieces) Type() EventType {
	return ENSTPiece
}

func (s *SubscribeResult) GetEventNSTPiece() (EventNSTPieces, error) {
	if len(s.Result.Events.NSTPieceChange) < 1 {
		return nil, errors.New("failed to get RawDataPieceChange from event_txRawDataPiece response")
	}
	ret := make(EventNSTPieces)
	for _, rawDataPiece := range s.Result.Events.NSTPieceChange {
		tmp := strings.Split(rawDataPiece, "_")

		pieceIndex, err := strconv.ParseUint(tmp[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse pieceIndex from event_txRawDataPiece response, error:%w", err)
		}
		feederID, err := strconv.ParseUint(tmp[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse feederID from event_txRawDataPiece response, error:%w", err)
		}
		ret[feederID] = uint32(pieceIndex)
	}
	return ret, nil
}

type EventNSTStakers map[uint64]*EventNSTStaker

type EventNSTStaker struct {
	withdraws                  []*fetchertypes.WithdrawInfo
	deposits                   map[uint64]*fetchertypes.DepositInfo
	nextVersion, latestVersion uint64
}

func (e *EventNSTStaker) Deposits() map[uint64]*fetchertypes.DepositInfo {
	if e == nil {
		return nil
	}
	return e.deposits
}

func (e *EventNSTStaker) Withdraws() []*fetchertypes.WithdrawInfo {
	if e == nil {
		return nil
	}
	return e.withdraws
}

func (e *EventNSTStaker) Versions() (uint64, uint64) {
	if e == nil {
		return 0, 0
	}
	return e.nextVersion, e.latestVersion
}

// func (s *SubscribeResult) GetFeedVersions() (map[uint64]uint64, error) {
func (s *SubscribeResult) GetFeedVersions() (map[uint64]*FeedVersion, error) {
	if len(s.Result.Events.NSTFeedVersion) < 1 {
		return nil, errors.New("failed to get feedVersion from event_newBlock response")
	}
	ret := make(map[uint64]*FeedVersion)
	for _, feedVersion := range s.Result.Events.NSTFeedVersion {
		tmp := strings.Split(feedVersion, "_")
		if len(tmp) != 3 {
			return nil, errors.New("failed to parse feedVersion from event_newBlock response, expected 2 parts")
		}
		feederID, err := strconv.ParseUint(tmp[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse feederID from feedVersion in event_newBlock response, error:%w", err)
		}
		version, err := strconv.ParseUint(tmp[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse version from feedVersion in event_newBlock response, error:%w", err)
		}
		withdrawVersion, err := strconv.ParseUint(tmp[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse version from feedVersion in event_newBlock response, error:%w", err)
		}

		ret[feederID] = &FeedVersion{
			Version:         version,
			WithdrawVersion: withdrawVersion,
		}
	}
	return ret, nil
}

func (s *SubscribeResult) getEventNSTStakers() (EventNSTStakers, error) {
	if len(s.Result.Events.NSTStakersChange) == 0 {
		return nil, errors.New("failed to get NativeTokenChange from event_txUpdateNST response")
	}

	tmp := s.Result.Events.NSTStakersChange[0]

	nstChanges := strings.Split(tmp, "|")

	ret := make(EventNSTStakers)

	for _, nstChange := range nstChanges {
		parsed := strings.Split(nstChange, "_")
		if len(parsed) != 7 {
			return nil, fmt.Errorf("failed to parse nstChange: expected 7 parts but got %d, nstChange: %s", len(parsed), nstChange)
		}
		if parsed[0] != "deposit" {
			return nil, fmt.Errorf("failed to parse nstChange: expected 'deposit' but got %s", parsed[0])
		}
		tmpIndex, err := strconv.ParseInt(parsed[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse stakerID in nstChange from evetn_txUpdateNST response, error:%w", err)
		}
		stakerIndex := uint32(tmpIndex)

		feederID, err := strconv.ParseUint(parsed[6], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse feederID in nstChange from event_txUpdateNST response, error:%w", err)
		}

		var eInfo *EventNSTStaker

		if eInfo = ret[feederID]; eInfo == nil {
			eInfo = &EventNSTStaker{
				deposits:  make(map[uint64]*fetchertypes.DepositInfo),
				withdraws: make([]*fetchertypes.WithdrawInfo, 0),
			}
			ret[feederID] = eInfo
		}
		version, err := strconv.ParseUint(parsed[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse beaconchain_sync_index in nstChange from event_txUpdateNST response, error:%w", err)
		}
		if parsed[3] == withdrawValidator {
			// withdraw
			eInfo.withdraws = append(eInfo.withdraws, &fetchertypes.WithdrawInfo{
				StakerIndex:     stakerIndex,
				WithdrawVersion: version,
			})
			continue
		}
		if _, exists := eInfo.deposits[version]; exists {
			return nil, fmt.Errorf("failed to parse nstChange: duplicate version %d in nstChange", version)
		}
		if version > eInfo.latestVersion {
			eInfo.latestVersion = version
		}
		if eInfo.nextVersion == 0 || version < eInfo.nextVersion {
			eInfo.nextVersion = version
		}
		amount, err := strconv.ParseUint(parsed[5], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse amount in nstChange from event_txUpdateNST response, error:%w", err)
		}
		stakerAddr := parsed[2]
		validatorDecoded, _ := hexutil.Decode(parsed[3])
		eInfo.deposits[version] = &fetchertypes.DepositInfo{
			StakerIndex: stakerIndex,
			StakerAddr:  stakerAddr,
			Validator:   string(validatorDecoded),
			Amount:      amount,
		}
	}
	return ret, nil
}

type EventType int

type EventRes struct {
	Height       string
	Gas          string
	ParamsUpdate bool
	Price        []string
	FeederIDs    string
	TxHeight     string
	NativeETH    string
	Type         EventType
}

type SubscribeResult struct {
	Result struct {
		Query string `json:"query"`
		Data  struct {
			Value struct {
				TxResult struct {
					Height string `json:"height"`
				} `json:"TxResult"`
				Block struct {
					Header struct {
						Height string `json:"height"`
					} `json:"header"`
				} `json:"block"`
			} `json:"value"`
		} `json:"data"`
		Events struct {
			Fee              []string `json:"fee_market.base_fee"`
			ParamsUpdate     []string `json:"create_price.params_update"`
			FinalPrice       []string `json:"create_price.final_price"`
			PriceUpdate      []string `json:"create_price.price_update"`
			FeederID         []string `json:"create_price.feeder_id"`
			FeederIDs        []string `json:"create_price.feeder_ids"`
			NSTStakersChange []string `json:"create_price.nst_stakers_change"`
			NSTPieceUpdate   []string `json:"create_price.nst_piece_update"`
			NSTPieceChange   []string `json:"create_price.nst_piece_change"`
			NSTBalanceUpdate []string `json:"create_price.nst_balance_update"`
			NSTBalanceChange []string `json:"create_price.nst_balance_change"`
			NSTFeedVersion   []string `json:"create_price.nst_feed_version"`
		} `json:"events"`
	} `json:"result"`
}

func (s *SubscribeResult) BlockHeight() (int64, bool) {
	if h := s.Result.Data.Value.Block.Header.Height; len(h) > 0 {
		height, err := strconv.ParseInt(h, 10, 64)
		if err != nil {
			logger.Error("failed to parse int64 from height in SubscribeResult", "error", err, "height_str", h)
		}
		return height, true
	}
	return 0, false
}

func (s *SubscribeResult) TxHeight() (int64, bool) {
	if h := s.Result.Data.Value.TxResult.Height; len(h) > 0 {
		height, err := strconv.ParseInt(h, 10, 64)
		if err != nil {
			logger.Error("failed to parse int64 from txheight in SubscribeResult", "error", err, "height_str", h)
		}
		return height, true
	}
	return 0, false
}

// FeederIDs will return (nil, true) when there's no feederIDs
func (s *SubscribeResult) FeederIDs() (feederIDs map[int64]struct{}, valid bool) {
	events := s.Result.Events
	if len(events.PriceUpdate) > 0 && events.PriceUpdate[0] == updated {
		if feederIDsStr := strings.Split(events.FeederIDs[0], "_"); len(feederIDsStr) > 0 {
			feederIDs = make(map[int64]struct{})
			for _, feederIDStr := range feederIDsStr {
				id, err := strconv.ParseInt(feederIDStr, 10, 64)
				if err != nil {
					logger.Error("failed to parse int64 from feederIDs in subscribeResult", "feederIDs", feederIDs)
					feederIDs = nil
					return
				}
				feederIDs[id] = struct{}{}
			}
			valid = true
		}

	}
	// we don't take it as a 'false' case when there's no feederIDs
	valid = true
	return
}

func (s *SubscribeResult) FinalPrice() (prices []*FinalPrice, valid bool) {
	if fps := s.Result.Events.FinalPrice; len(fps) > 0 {
		prices = make([]*FinalPrice, 0, len(fps))
		for _, price := range fps {
			parsed := strings.Split(price, "_")
			if l := len(parsed); l > 4 {
				// nsteth
				parsed[2] = strings.Join(parsed[2:l-1], "_")
				parsed[3] = parsed[l-1]
				parsed = parsed[:4]
			}
			if len(parsed[2]) == 32 {
				// make sure this base64 string is valid
				if _, err := base64.StdEncoding.DecodeString(parsed[2]); err != nil {
					logger.Error("failed to parse base64 encoded string when parse finalprice.price from SbuscribeResult", "parsed.price", parsed[2])
					return
				}
			}
			tokenID, err := strconv.ParseInt(parsed[0], 10, 64)
			if err != nil {
				logger.Error("failed to parse finalprice.tokenID from SubscribeResult", "parsed.tokenID", parsed[0])
				prices = nil
				return
			}
			decimal, err := strconv.ParseInt(parsed[3], 10, 32)
			if err != nil {
				logger.Error("failed to parse finalprice.decimal from SubscribeResult", "parsed.decimal", parsed[3])
				prices = nil
				return
			}
			prices = append(prices, &FinalPrice{
				tokenID: tokenID,
				roundID: parsed[1],
				price:   parsed[2],
				// conversion is safe
				decimal: int32(decimal),
			})
		}
		valid = true
	}
	return
}

func (s *SubscribeResult) ParamsUpdate() bool {
	return len(s.Result.Events.ParamsUpdate) > 0
}

func (s *SubscribeResult) Fee() (string, bool) {
	if len(s.Result.Events.Fee) == 0 {
		return "", false
	}
	return s.Result.Events.Fee[0], true
}

const (
	// current version of 'Oracle' only support id=1(chainlink) as valid source
	Chainlink         uint64 = 1
	denom                    = "hua"
	withdrawValidator        = "0xFFFFFFFFFFFFFFFF"
)

const (
	ENewBlock EventType = iota + 1
	EUpdatePrice
	// EUpdateNST
	ENSTStakers
	ENSTPiece
	ENSTBalances
)

var (
	logger feedertypes.LoggerInf

	blockMaxGas uint64

	defaultImuaClient *imuaClient
)

// Init intialize the imuaclient with configuration including consensuskey info, chainID
func Init(conf *feedertypes.Config, mnemonic, privFile string, txOnly bool, standalone bool) error {
	if logger = feedertypes.GetLogger("imuaclient"); logger == nil {
		panic("logger is not initialized")
	}

	// set prefixs to imua when start as standlone mode
	if standalone {
		config := sdk.GetConfig()
		cmdcfg.SetBech32Prefixes(config)
	}

	confImua := conf.Imua
	confSender := conf.Sender
	privBase64 := ""

	// if mnemonic is not set from flag, then check config file to find if there is mnemonic configured
	if len(mnemonic) == 0 && len(confSender.Mnemonic) > 0 {
		logger.Info("set mnemonic from config", "mnemonic", confSender.Mnemonic)
		mnemonic = confSender.Mnemonic
	}

	if len(mnemonic) == 0 {
		// load privatekey from local path
		file, err := os.Open(path.Join(confSender.Path, privFile))
		if err != nil {
			// return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to open consensuskey file, %v", err))
			return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to open consensuskey file, path:%s, privFile:%s, error:%v", confSender.Path, privFile, err))
		}
		defer file.Close()
		var privKey feedertypes.PrivValidatorKey
		if err := json.NewDecoder(file).Decode(&privKey); err != nil {
			return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse consensuskey from json file, file path:%s,  error:%v", privFile, err))
		}
		logger.Info("load privatekey from local file", "path", privFile)
		privBase64 = privKey.PrivKey.Value
	} else if !bip39.IsMnemonicValid(mnemonic) {
		return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("invalid mnemonic:%s", mnemonic))
	}
	var privKey cryptotypes.PrivKey
	if len(mnemonic) > 0 {
		privKey = ed25519.GenPrivKeyFromSecret([]byte(mnemonic))
	} else {
		privBytes, err := base64.StdEncoding.DecodeString(privBase64)
		if err != nil {
			return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse privatekey from base64_string:%s, error:%v", privBase64, err))
		}
		//nolint:all
		privKey = &ed25519.PrivKey{
			Key: cryptoed25519.PrivateKey(privBytes),
		}
	}

	encCfg := encoding.MakeConfig(app.ModuleBasics)

	if len(confImua.ChainID) == 0 {
		return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("ChainID must be specified in config"))
	}

	var err error
	if defaultImuaClient, err = NewImuaClient(logger, confImua.Grpc, confImua.Ws, conf.Imua.Rpc, privKey, encCfg, confImua.ChainID, txOnly); err != nil {
		if errors.Is(err, feedertypes.ErrInitConnectionFail) {
			return err
		}
		return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to NewImuaClient, privKey:%v, chainID:%s, error:%v", privKey, confImua.ChainID, err))
	}

	return nil
}
