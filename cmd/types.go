package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cosmos/gogoproto/proto"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"

	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
)

const (
	loggerTagPrefix = "feed_%s_%d"
	statusOk        = 0
)

type PriceFetcher interface {
	GetLatestPrice(source, token string) (fetchertypes.PriceInfo, error)
	AddTokenForSource(source, token string) bool
}

type PriceFetcherNST interface {
	PriceFetcher
	SetNSTStakerInfos(sourceName string, sInfos fetchertypes.StakerInfos, version uint64)
}

type priceSubmitter interface {
	SendTx(feederID, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*sdktx.BroadcastTxResponse, error)
	SendTx2Phases(feederID, baseBlock uint64, prices []*fetchertypes.PriceInfo, phase oracletypes.AggregationPhase, nonce int32) (*sdktx.BroadcastTxResponse, error)
}

type signInfo struct {
	maxNonce int32
	roundID  int64
	nonce    int32
}

func (s *signInfo) getNextNonceAndUpdate(roundID int64) int32 {
	if roundID < s.roundID {
		return -1
	} else if roundID > s.roundID {
		s.roundID = roundID
		s.nonce = 1
		return 1
	}
	if s.nonce = s.nonce + 1; s.nonce > s.maxNonce {
		s.nonce = s.maxNonce
		return -1
	}
	return s.nonce
}

func (s *signInfo) revertNonce(roundID int64) {
	if s.roundID == roundID && s.nonce > 0 {
		s.nonce--
	}
}

type triggerHeights struct {
	commitHeight int64
	priceHeight  int64
}

type updatePrice struct {
	txHeight int64
	price    *fetchertypes.PriceInfo
}

type updateParamsReq struct {
	params *oracletypes.Params
	result chan *updateParamsRes
}

type updateParamsRes struct {
}

type localPrice struct {
	price  fetchertypes.PriceInfo
	height int64
}

type twoPhasesInfo struct {
	roundID       uint64
	cachedTree    []*oracletypes.MerkleTree
	finalizedTree *oracletypes.MerkleTree
	// this is the local index to record latest index of piece that have sent successfully
	// it starts from -1 to represent no successfully submitted piece
	sentLatestPieceIndex int64
	// this is synced with onchain information from events
	nextPieceIndex uint32
}

type PieceWithProof struct {
	Piece      string
	PieceIndex uint32
	IndexesStr string
	HashesStr  string
}

func (t *twoPhasesInfo) addMT(roundID uint64, mt *oracletypes.MerkleTree) bool {
	if roundID < t.roundID {
		return false
	}
	if roundID > t.roundID {
		t.roundID = roundID
		t.cachedTree = []*oracletypes.MerkleTree{
			mt,
		}
		t.finalizedTree = nil
		return true
	}
	if t.finalizedTree != nil {
		return false
	}

	for _, data := range t.cachedTree {

		// skip duplicated raw data
		if bytes.Equal(data.RootHash(), mt.RootHash()) {
			return false
		}
	}
	t.cachedTree = append(t.cachedTree, mt)
	return true
}

func (t *twoPhasesInfo) finalizeRawData(roundID uint64, rootHash []byte) bool {
	if roundID != t.roundID {
		return false
	}
	if t.finalizedTree != nil {
		return false
	}
	for _, mt := range t.cachedTree {
		if bytes.Equal(mt.RootHash(), rootHash) {
			t.finalizedTree = mt
			t.cachedTree = nil
			t.sentLatestPieceIndex = -1
			t.nextPieceIndex = 0
			return true
		}
	}
	// make sure finalizedRawdata is nil if no match found, it's better to be count as miss than 'malicious' for a validator
	t.finalizedTree = nil
	return false
}

// GetRawDataPiece return the raw data piece for 2nd phase price submission
// returns (rootHash, proof, error)
// rootHash: the root hash of the merkle tree as string
// proof: the proof of the raw data piece, it's a string of joined indexes and joined hashes in format of base64, separated by |
// error: error if any
func (t *twoPhasesInfo) getRawDataPieceAndProof(roundID uint64, index uint32) (*PieceWithProof, error) {
	if roundID != t.roundID {
		return nil, errors.New("no finalized raw data found for this round, roundID")
	}
	piece, ok := t.finalizedTree.PieceByIndex(index)
	if !ok {
		return nil, errors.New("failed to get raw data piece by index")
	}
	proof := t.finalizedTree.MinimalProofByIndex(index)
	idxStr, hashStr := proof.FlattenString()
	return &PieceWithProof{Piece: string(piece), PieceIndex: index, IndexesStr: idxStr, HashesStr: hashStr}, nil
}

func (t *twoPhasesInfo) getLatestRootHash() ([]byte, uint32) {
	if len(t.cachedTree) == 0 {
		return nil, 0
	}
	return t.cachedTree[len(t.cachedTree)-1].RootHash(), t.cachedTree[len(t.cachedTree)-1].LeafCount()
}

func (t *twoPhasesInfo) setRoundID(roundID uint64) bool {
	if roundID > t.roundID {
		t.roundID = roundID
		t.cachedTree = nil
		t.finalizedTree = nil
		t.sentLatestPieceIndex = -1
		t.nextPieceIndex = 0
		return true
	}
	return false
}

// TODO: stop channel to close
type feeder struct {
	logger feedertypes.LoggerInf
	// TODO: currently only 1 source for each token, so we can just set it as a field here
	source   string
	token    string
	tokenID  uint64
	feederID int
	// TODO: add check for rouleID, v1 can be skipped
	// ruleID
	startRoundID   int64
	startBaseBlock int64
	interval       int64
	endBlock       int64

	roundID uint64

	//	maxNonce int32

	fetcher    PriceFetcher
	fetcherNST PriceFetcherNST
	submitter  priceSubmitter
	lastPrice  *localPrice
	lastSent   *signInfo

	priceCh            chan *updatePrice
	heightsCh          chan *triggerHeights
	paramsCh           chan *updateParamsReq
	updateNSTStakersCh chan *updateNSTStaker

	twoPhasesInfo      *twoPhasesInfo
	twoPhasesPieceSize uint32

	stakers *fetchertypes.Stakers
}

type FeederInfo struct {
	Source         string
	Token          string
	TokenID        uint64
	FeederID       int
	StartRoundID   int64
	StartBaseBlock int64
	Interval       int64
	EndBlock       int64
	LastPrice      localPrice
	LastSent       signInfo
}

// AddRawData add rawData for 2nd phase price submission, this method will/should not be called concurrently with FinalizeRawData or GetRawDataPiece
func (f *feeder) AddRawData(roundID uint64, rd []byte, pieceSize uint32) (bool, []byte) {
	if f.twoPhasesInfo == nil {
		return false, nil
	}
	mt, err := oracletypes.DeriveMT(pieceSize, rd)
	if err != nil {
		return false, nil
	}
	fmt.Println("debug(leonz)--->AddRawData.1. size, LCount", pieceSize, mt.LeafCount())
	added := f.twoPhasesInfo.addMT(roundID, mt)
	return added, mt.RootHash()
}

// FinalizeRawData finalize the raw data for 2nd phase price submission, this method will/should not be called concurrently with AddRawData or GetRawDataPiece
func (f *feeder) FinalizeRawData(roundID uint64, rootHash []byte) bool {
	if f.twoPhasesInfo == nil {
		return false
	}
	return f.twoPhasesInfo.finalizeRawData(roundID, rootHash)
}

// GetRawDataPiece return the raw data piece for 2nd phase price submission, this method will/should not be called concurrently with AddRawData or FinalizeRawData
func (f *feeder) GetRawDataPieceAndProof(roundID uint64, index uint32) (*PieceWithProof, error) {
	if f.twoPhasesInfo == nil {
		return nil, errors.New("two phases not enabled for this feeder")
	}
	return f.twoPhasesInfo.getRawDataPieceAndProof(roundID, index)
}

func (f *feeder) GetLatestRootHash() ([]byte, uint32) {
	return f.twoPhasesInfo.getLatestRootHash()
}

func (f *feeder) SetRoundID(roundID uint64) bool {
	if f.twoPhasesInfo == nil {
		return false
	}
	return f.twoPhasesInfo.setRoundID(roundID)
}

func (f *feeder) IsTwoPhases() bool {
	return f.twoPhasesInfo != nil
}

func (f *feeder) priceChanged(p *fetchertypes.PriceInfo) bool {
	return f.lastPrice.price.Price != p.Price
}

func (f *feeder) NextSendablePieceWithProofs(roundID uint64) ([]*PieceWithProof, error) {
	fmt.Println("debug(leonz)--NextSendablePieceWithProofs.1")
	if f.twoPhasesInfo == nil {
		fmt.Println("debug(leonz)--NextSendablePieceWithProofs.2")
		return nil, errors.New("two phases not enabled for this feeder")
	}
	if f.twoPhasesInfo.roundID != roundID {
		return nil, fmt.Errorf("roundID not match, feeder roundID:%d, input roundID:%d", f.twoPhasesInfo.roundID, roundID)

	}
	if f.twoPhasesInfo.finalizedTree == nil {
		return nil, errors.New("no finalized raw data found for this round")
	}

	ret := make([]*PieceWithProof, 0, 1)

	if int64(f.twoPhasesInfo.nextPieceIndex) > f.twoPhasesInfo.sentLatestPieceIndex {
		pwf, err := f.twoPhasesInfo.getRawDataPieceAndProof(roundID, f.twoPhasesInfo.nextPieceIndex)
		if err != nil {
			fmt.Println("debug(leonz)--NextSendablePieceWithProofs.5", err)
			return nil, err
		}
		ret = append(ret, pwf)
	}

	if f.twoPhasesInfo.sentLatestPieceIndex == -1 && f.twoPhasesInfo.nextPieceIndex == 0 {
		// try to get one more pwf if exists
		pwf, err := f.twoPhasesInfo.getRawDataPieceAndProof(roundID, 1)
		if err == nil {
			ret = append(ret, pwf)
		}
	}

	fmt.Println("debug(leonz)--NextSendablePieceWithProofs.6")
	return ret, nil
}

func (f *feeder) UpdateSentLatestPieceIndex(roundID uint64, index int64) int64 {
	if f.twoPhasesInfo.roundID != roundID {
		return -1
	}
	old := f.twoPhasesInfo.sentLatestPieceIndex
	f.twoPhasesInfo.sentLatestPieceIndex = index
	return old
}

func (f *feeder) updateNSTStakers(sInfo *updateNSTStaker) {
	select {
	case f.updateNSTStakersCh <- sInfo:
		// don't block
	default:
	}
}

func (f *feeder) Info() FeederInfo {
	var lastPrice localPrice
	var lastSent signInfo
	if f.lastPrice != nil {
		lastPrice = *f.lastPrice
	}
	if f.lastSent != nil {
		lastSent = *f.lastSent
	}
	return FeederInfo{
		Source:         f.source,
		Token:          f.token,
		TokenID:        f.tokenID,
		FeederID:       f.feederID,
		StartRoundID:   f.startRoundID,
		StartBaseBlock: f.startBaseBlock,
		Interval:       f.interval,
		EndBlock:       f.endBlock,
		LastPrice:      lastPrice,
		LastSent:       lastSent,
	}
}

func newFeeder(tf *oracletypes.TokenFeeder, feederID int, fetcher PriceFetcher, submitter priceSubmitter, source string, token string, maxNonce int32, pieceSize uint32, isTwoPhases bool, logger feedertypes.LoggerInf) (*feeder, error) {
	ret := feeder{
		logger:   logger,
		source:   source,
		token:    token,
		tokenID:  tf.TokenID,
		feederID: feederID,
		// these conversion a safe since the block height defined in cosmossdk is int64
		startRoundID:   int64(tf.StartRoundID),
		startBaseBlock: int64(tf.StartBaseBlock),
		interval:       int64(tf.Interval),
		endBlock:       int64(tf.EndBlock),
		fetcher:        fetcher,
		submitter:      submitter,
		lastPrice:      &localPrice{},
		lastSent: &signInfo{
			maxNonce: maxNonce,
		},

		priceCh:   make(chan *updatePrice, 1),
		heightsCh: make(chan *triggerHeights, 1),
		paramsCh:  make(chan *updateParamsReq, 1),
	}

	if isTwoPhases {
		fNST, ok := fetcher.(PriceFetcherNST)
		if !ok {
			return nil, errors.New("failed to cast fetcher to PriceFetcherNST")
		}
		ret.twoPhasesInfo = &twoPhasesInfo{}
		ret.twoPhasesPieceSize = pieceSize
		ret.updateNSTStakersCh = make(chan *updateNSTStaker, 1)
		ret.stakers = fetchertypes.NewStakers()
		ret.fetcherNST = fNST
	}

	return &ret, nil
}

func (f *feeder) start() {
	go func() {
		for {
			select {
			case h := <-f.heightsCh:
				if h.priceHeight > f.lastPrice.height {
					// the block event arrived early, wait for the price update events to update local price
					continue
				}
				baseBlock, roundID, delta, active := f.calculateRound(h.commitHeight)
				f.roundID = uint64(roundID)
				if !active {
					continue
				}
				// there's a finilzedTree and pending pieces to submit
				if pwfs, err := f.NextSendablePieceWithProofs(uint64(roundID)); err == nil {
					fmt.Printf("debug(leonz)--->len(pwfs)=%d, roundID=%d, block:%d\r\n", len(pwfs), roundID, h.commitHeight)
					for _, pwf := range pwfs {
						fmt.Println("debug(leonz)--->constructing priceInfo:.....")
						pInfos := make([]*fetchertypes.PriceInfo, 0, 2)
						pInfos = append(pInfos, &fetchertypes.PriceInfo{
							Price:   pwf.Piece,
							RoundID: fmt.Sprintf("%d", pwf.PieceIndex),
						})
						fmt.Printf("debug(leonz)--->constructing.piece:....., piece:%s, pieceIndex:%d\r\n", pwf.Piece, pwf.PieceIndex)
						if len(pwf.IndexesStr) > 0 && len(pwf.HashesStr) > 0 {
							pInfos = append(pInfos, &fetchertypes.PriceInfo{
								Price:   pwf.HashesStr,
								RoundID: pwf.IndexesStr,
							})
							fmt.Printf("debug(leonz)--->constructing.proof:....., indexesStr:%s, indexesStr:%s\r\n", pwf.HashesStr, pwf.IndexesStr)
						}
						oldIndex := f.UpdateSentLatestPieceIndex(uint64(roundID), int64(pwf.PieceIndex))
						_, err := f.submitter.SendTx2Phases(uint64(f.feederID), uint64(baseBlock), pInfos, oracletypes.AggregationPhaseTwo, 1)
						if err != nil {
							f.logger.Error("failed to send tx for 2nd-phase price submission", "roundID", roundID, "delta", delta, "feeder", f.Info(), "height_commit", h.commitHeight, "height_price", h.priceHeight, "error", err)
							// revert local index if failed to submit
							f.UpdateSentLatestPieceIndex(uint64(roundID), oldIndex)
						} else {
							f.logger.Info("sent tx to submit price with rawData piece for 2nd phase", "pieceIndex", pwf.PieceIndex, "baseBlock", baseBlock, "delta", delta, "roundID", roundID)
						}
					}
					// the feeder is in phase two submitting rawData piece, so skip checking for phase one
					continue
				} else if f.IsTwoPhases() {
					f.logger.Info("didn't able to get next sendable piece with proofs for 2nd-phase price submission", "roundID", roundID, "error", err)
				}

				fmt.Printf("debug(leonz))--no 2ndPhase tx, feederID:%d, roundID:%d, block:%d\r\n", f.feederID, roundID, h.commitHeight)

				// TODO: replace 3 with MaxNonce
				if delta < 3 {
					f.logger.Info("trigger feeder", "height_commit", h.commitHeight, "height_price", h.priceHeight)
					f.SetRoundID(uint64(roundID))

					if price, err := f.fetcher.GetLatestPrice(f.source, f.token); err != nil {
						f.logger.Error("failed to get latest price", "roundID", roundID, "delta", delta, "feeder", f.Info(), "error", err)
						if errors.Is(err, feedertypes.ErrSourceTokenNotConfigured) {
							f.logger.Error("add token from configure of source", "token", f.token, "source", f.source)
							// blocked this feeder since no available fetcher_source_price working
							if added := f.fetcher.AddTokenForSource(f.source, f.token); !added {
								f.logger.Error("failed to complete adding token from configure, pleas check and update the config file of source if necessary", "token", f.token, "source", f.source)
							}
						}
					} else {
						if price.IsZero() {
							f.logger.Info("got nil latest price, skip submitting price", "roundID", roundID, "delta", delta)
							f.logger.Debug("got latsetprice equal to local cache", "feeder", f.Info())
							continue
						}

						if f.IsTwoPhases() {
							debugRet, rootHash := f.AddRawData(uint64(roundID), []byte(price.Price), f.twoPhasesPieceSize)
							fmt.Printf("debug(leonz)--->AddRawData.result:%t,feederID:%d,roundID:%d,block:%d,fetchedLatestRoot:%s,syncedLatestRoot:%s\r\n", debugRet, f.feederID, roundID, h.commitHeight, base64.StdEncoding.EncodeToString(rootHash), base64.StdEncoding.EncodeToString([]byte(f.lastPrice.price.Price)))
							changes := oracletypes.RawDataNST{}
							err := proto.Unmarshal([]byte(price.Price), &changes)
							fmt.Println("debug(leonz)--->AddRawData.changes:", changes, err)
							if bytes.Equal(rootHash, []byte(f.lastPrice.price.Price)) {
								f.logger.Info("didn't submit price for 1st-phase of 2phases due to price not changed", "roundID", roundID, "delta", delta, "price", price)
								f.logger.Debug("got latsetprice(rootHash) equal to local cache", "feeder", f.Info())
								continue
							}
						} else if !f.priceChanged(&price) {
							f.logger.Info("didn't submit price due to price not changed", "roundID", roundID, "delta", delta, "price", price)
							f.logger.Debug("got latsetprice equal to local cache", "feeder", f.Info())
							continue
						}
						if nonce := f.lastSent.getNextNonceAndUpdate(roundID); nonce < 0 {
							f.logger.Error("failed to submit due to no available nonce", "roundID", roundID, "delta", delta, "feeder", f.Info())
						} else {
							var res *sdktx.BroadcastTxResponse
							var err error
							if f.IsTwoPhases() {
								fmt.Printf("debug(leonz)--->phase 1 of 2phases feederID:%d, roundID:%d, block:%d\r\n", f.feederID, roundID, h.commitHeight)
								if root, count := f.GetLatestRootHash(); count > 0 {
									price.Price = string(root)
									price.RoundID = fmt.Sprintf("%d", count)
									f.logger.Info("phase 1 message of 2phases feeder", "rootHash", hex.EncodeToString(root), "piece_count", count)
									res, err = f.submitter.SendTx2Phases(uint64(f.feederID), uint64(baseBlock), []*fetchertypes.PriceInfo{&price}, oracletypes.AggregationPhaseOne, nonce)
								} else {
									f.logger.Error("failed to submit 1st-phase price due to no available rootHash for 2-phases aggregation submission", "roundID", roundID, "delta", delta, "feeder", f.Info())
									continue
								}
							} else {
								res, err = f.submitter.SendTx(uint64(f.feederID), uint64(baseBlock), price, nonce)
							}
							if err != nil {
								f.lastSent.revertNonce(roundID)
								f.logger.Error("failed to send tx submitting price", "price", price, "nonce", nonce, "baseBlock", baseBlock, "delta", delta, "feeder", f.Info(), "error_feeder", err)
							}
							if txResponse := res.GetTxResponse(); txResponse.Code == statusOk {
								f.logger.Info("sent tx to submit price", "price", price, "nonce", nonce, "baseBlock", baseBlock, "delta", delta)
							} else {
								f.lastSent.revertNonce(roundID)
								f.logger.Error("failed to send tx submitting price", "price", price, "nonce", nonce, "baseBlock", baseBlock, "delta", delta, "feeder", f.Info(), "response_rawlog", txResponse.RawLog)
							}
						}
					}
				}
			case price := <-f.priceCh:
				f.lastPrice.price = *(price.price)
				// update latest height that price had been updated
				if f.IsTwoPhases() {
					rootHash, err := base64.StdEncoding.DecodeString(price.price.Price)
					if err != nil {
						f.logger.Error("failed to update local price due to failed to parse rootHash from base64 price-string", "price", price.price, "error", err)
						continue
					}
					f.lastPrice.price = *(price.price)
					f.lastPrice.price.Price = string(rootHash)
					f.logger.Info("finalize rootHash", "root", price.price.Price)
					f.FinalizeRawData(f.roundID, rootHash)
				} else {
					f.lastPrice.price = *(price.price)
				}
				// update latest height that price had been updated
				f.lastPrice.height = price.txHeight
				f.logger.Info("synced local price with latest price from imuachain", "price", price.price, "txHeight", price.txHeight)
			case req := <-f.paramsCh:
				if err := f.updateFeederParams(req.params); err != nil {
					// This should not happen under this case.
					f.logger.Error("failed to update params", "new params", req.params)
				}
				req.result <- &updateParamsRes{}
			case req := <-f.updateNSTStakersCh:
				if err := f.stakers.Update(req.add, req.remove, req.nextVersion, req.latestVersion); err != nil {
					// TODO(leonz): return error for resetAll
				}
				sInfos, version := f.stakers.GetStakerInfos()
				f.fetcherNST.SetNSTStakerInfos(f.source, sInfos, version)
			}
		}
	}()
}

// UpdateParams updates the feeder's params from oracle params, this method will block if the channel is full
// which means the update for params will must be delivered to the feeder's routine when this method is called
// blocked
func (f *feeder) updateParams(params *oracletypes.Params) chan *updateParamsRes {
	// TODO update oracle parms
	res := make(chan *updateParamsRes)
	req := &updateParamsReq{params: params, result: res}
	f.paramsCh <- req
	return res
}

// UpdatePrice will upate local price for feeder
// non-blocked
func (f *feeder) updatePrice(txHeight int64, price *fetchertypes.PriceInfo) {
	// we dont't block this process when the channelis full, if this updating is skipped
	// it will be update at next time when event arrived
	select {
	case f.priceCh <- &updatePrice{price: price, txHeight: txHeight}:
	default:
	}
}

// Trigger notify the feeder that a new block height is committed
// non-blocked
func (f *feeder) trigger(commitHeight, priceHeight int64) {
	// the channel got 1 buffer, so it should always been sent successfully
	// and if not(the channel if full), we just skip this height and don't block
	select {
	case f.heightsCh <- &triggerHeights{commitHeight: commitHeight, priceHeight: priceHeight}:
	default:
	}
}

func (f *feeder) updateFeederParams(p *oracletypes.Params) error {
	if p == nil || len(p.TokenFeeders) < f.feederID+1 {
		return errors.New("invalid oracle parmas")
	}
	// TODO: update feeder's params
	tokenFeeder := p.TokenFeeders[f.feederID]
	if f.endBlock != int64(tokenFeeder.EndBlock) {
		f.endBlock = int64(tokenFeeder.EndBlock)
	}
	if f.startBaseBlock != int64(tokenFeeder.StartBaseBlock) {
		f.startBaseBlock = int64(tokenFeeder.StartBaseBlock)
	}
	if f.interval != int64(tokenFeeder.Interval) {
		f.interval = int64(tokenFeeder.Interval)
	}
	if p.MaxNonce > 0 {
		f.lastSent.maxNonce = p.MaxNonce
	}
	return nil
}

// TODO: stop feeder routine
// func (f *feeder) Stop()

func (f *feeder) calculateRound(h int64) (baseBlock, roundID, delta int64, active bool) {
	// endBlock itself is considered as active
	if f.startBaseBlock > h || (f.endBlock > 0 && h > f.endBlock) {
		return
	}
	active = true
	delta = (h - f.startBaseBlock) % f.interval
	roundID = (h-f.startBaseBlock)/f.interval + f.startRoundID
	baseBlock = h - delta
	return
}

type triggerReq struct {
	height    int64
	feederIDs map[int64]struct{}
}

type finalPrice struct {
	feederID int64
	price    string
	decimal  int32
	roundID  string
}
type updatePricesReq struct {
	txHeight int64
	prices   []*finalPrice
}

type failedFeedersWithError struct {
	feederID uint64
	err      error
}
type updateNSTStakersReq struct {
	infos []*updateNSTStaker
	res   chan []*failedFeedersWithError
}

type updateNSTStaker struct {
	feederID                   uint64
	add                        fetchertypes.StakerInfos
	remove                     fetchertypes.StakerInfos
	nextVersion, latestVersion uint64
}

type updateNSTPieceIndexReq struct{}

type Feeders struct {
	locker    *sync.Mutex
	running   bool
	fetcher   PriceFetcher
	submitter priceSubmitter
	logger    feedertypes.LoggerInf
	feederMap map[int]*feeder
	// TODO: feeder has sync management, so feeders could remove these channel
	trigger            chan *triggerReq
	updatePriceCh      chan *updatePricesReq
	updateParamsCh     chan *oracletypes.Params
	updateNSTStakersCh chan *updateNSTStakersReq
	updateNSTPieces    chan *updateNSTPieceIndexReq
}

func NewFeeders(logger feedertypes.LoggerInf, fetcher PriceFetcher, submitter priceSubmitter) *Feeders {
	return &Feeders{
		locker:    new(sync.Mutex),
		logger:    logger,
		fetcher:   fetcher,
		submitter: submitter,
		feederMap: make(map[int]*feeder),
		//		feederMap: fm,
		// don't block on height increasing
		trigger:       make(chan *triggerReq, 1),
		updatePriceCh: make(chan *updatePricesReq, 1),
		// it's safe to have a buffer to not block running feeders,
		// since for running feeders, only endBlock is possible to be modified
		updateParamsCh: make(chan *oracletypes.Params, 1),

		// this request will be recieved at most one per block, so the buffer 1 is enough
		updateNSTStakersCh: make(chan *updateNSTStakersReq, 1),
	}

}

func (fs *Feeders) SetupFeeder(tf *oracletypes.TokenFeeder, feederID int, source string, token string, maxNonce int32, pieceSize uint32, isTwoPhases bool) {
	fs.locker.Lock()
	defer fs.locker.Unlock()
	if fs.running {
		fs.logger.Error("failed to setup feeder for a running feeders, this should be called before feeders is started")
		return
	}
	f, err := newFeeder(tf, feederID, fs.fetcher, fs.submitter, source, token, maxNonce, pieceSize, isTwoPhases, fs.logger.With("feeder", fmt.Sprintf(loggerTagPrefix, token, feederID)))
	if err != nil {
		fs.logger.Error("failed to create feeder", "error", err)
		return
	}

	fs.feederMap[feederID] = f
}

// Start will start to listen the trigger(newHeight) and updatePrice events
// usd channels to avoid race condition on map
func (fs *Feeders) Start() {
	fs.locker.Lock()
	if fs.running {
		fs.logger.Error("failed to start feeders since it's already running")
		fs.locker.Unlock()
		return
	}
	fs.running = true
	fs.locker.Unlock()
	for _, f := range fs.feederMap {
		f.start()
	}
	go func() {
		for {
			select {
			case params := <-fs.updateParamsCh:
				results := []chan *updateParamsRes{}
				existingFeederIDs := make(map[int64]struct{})
				for _, f := range fs.feederMap {
					res := f.updateParams(params)
					results = append(results, res)
					existingFeederIDs[int64(f.feederID)] = struct{}{}
				}
				// wait for all feeders to complete updateing params
				for _, res := range results {
					<-res
				}
				for tfID, tf := range params.TokenFeeders {
					if _, ok := existingFeederIDs[int64(tfID)]; !ok {
						// create and start a new feeder
						tokenName := strings.ToLower(params.Tokens[tf.TokenID].Name)
						source := fetchertypes.Chainlink
						if fetchertypes.IsNSTToken(tokenName) {
							nstToken := fetchertypes.NSTToken(tokenName)
							if source = fetchertypes.GetNSTSource(nstToken); len(source) == 0 {
								fs.logger.Error("failed to add new feeder, source of nst token is not set", "token", tokenName)
							}
						} else if !strings.HasSuffix(tokenName, fetchertypes.BaseCurrency) {
							// NOTE: this is for V1 only
							tokenName += fetchertypes.BaseCurrency
						}

						feeder, err := newFeeder(tf, tfID, fs.fetcher, fs.submitter, source, tokenName, params.MaxNonce, params.PieceSizeByte, params.IsRule2PhasesByFeederID(uint64(tfID)), fs.logger)
						if err != nil {
							fs.logger.Error("failed to create feeder", "error", err)
							continue
						}
						fs.feederMap[tfID] = feeder
						feeder.start()
					}
				}
			case t := <-fs.trigger:
				// the order does not matter
				for _, f := range fs.feederMap {
					priceHeight := int64(0)
					if _, ok := t.feederIDs[int64(f.feederID)]; ok {
						priceHeight = t.height
					}
					f.trigger(t.height, priceHeight)
				}
			case req := <-fs.updatePriceCh:
				for _, price := range req.prices {
					// int conversion is safe
					if feeder, ok := fs.feederMap[int(price.feederID)]; !ok {
						fs.logger.Error("failed to get feeder by feederID when update price for feeders", "updatePriceReq", req)
						continue
					} else {
						fs.logger.Info("update price for feeder", "feeder", feeder.Info(), "price", price.price, "roundID", price.roundID, "txHeight", req.txHeight)
						feeder.updatePrice(req.txHeight, &fetchertypes.PriceInfo{
							Price:   price.price,
							Decimal: price.decimal,
							RoundID: price.roundID,
						})
					}
				}
			case req := <-fs.updateNSTStakersCh:
				for _, stakerInfo := range req.infos {
					feeder, ok := fs.feederMap[int(stakerInfo.feederID)]
					if !ok {
						fs.logger.Error("failed to get feeder by feederID when update price for feeders", "updatePriceReq", req)
					}
					feeder.updateNSTStakers(stakerInfo)
				}
			}
		}
	}()
}

// Trigger notify all feeders that a new block height is committed
// non-blocked
func (fs *Feeders) Trigger(height int64, feederIDs map[int64]struct{}) {
	select {
	case fs.trigger <- &triggerReq{height: height, feederIDs: feederIDs}:
	default:
	}
}

// UpdatePrice will upate local price for all feeders
// non-blocked
func (fs *Feeders) UpdatePrice(txHeight int64, prices []*finalPrice) {
	select {
	case fs.updatePriceCh <- &updatePricesReq{txHeight: txHeight, prices: prices}:
	default:
	}
}

// UpdateOracleParams updates all feeders' params from oracle params
// if the receiving channel is full, blocking until all updateParams are received by the channel
func (fs *Feeders) UpdateOracleParams(p *oracletypes.Params) {
	if p == nil {
		fs.logger.Error("received nil oracle params")
		return
	}
	if len(p.TokenFeeders) == 0 {
		fs.logger.Error("received empty token feeders")
		return
	}
	fs.updateParamsCh <- p
}

func (fs *Feeders) UpdateNSTStakers(updates []*updateNSTStaker) []*failedFeedersWithError {
	res := make(chan []*failedFeedersWithError)
	req := &updateNSTStakersReq{infos: updates, res: res}
	select {
	case fs.updateNSTStakersCh <- req:
		// if the request of one block is missed, feeder will find out the version mismatch and then update activity
		return <-res
	default:
		ret := make([]*failedFeedersWithError, 0, len(req.infos))
		for _, sInfo := range req.infos {
			ret = append(ret, &failedFeedersWithError{feederID: sInfo.feederID, err: errors.New("failed to update NST staker infos due to channel full")})
		}
		return ret
	}
}
