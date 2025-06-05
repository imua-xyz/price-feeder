package feeder

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/cosmos/gogoproto/proto"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	imuaclienttypes "github.com/imua-xyz/price-feeder/imuaclient"
	"github.com/imua-xyz/price-feeder/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"

	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
)

const (
	loggerTagPrefix = "feed_%s_%d"
	statusOk        = 0
)

var (
	ErrNoAvailableNextIndex *types.Err = types.NewErr("there's no available next index")
)

type PriceFetcher interface {
	GetLatestPrice(source, token string) (fetchertypes.PriceInfo, error)
	AddTokenForSource(source, token string) bool
}

type PriceFetcherNST interface {
	PriceFetcher
	SetNSTStakers(sourceName string, sInfos fetchertypes.StakerInfos, version uint64)
}

type priceSubmitter interface {
	SendTx(feederID, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*sdktx.BroadcastTxResponse, error)
	SendTx2Phases(feederID, baseBlock uint64, prices []*fetchertypes.PriceInfo, phase oracletypes.AggregationPhase, nonce int32) (*sdktx.BroadcastTxResponse, error)
}

type signInfo struct {
	maxNonce int32
	roundID  uint64
	nonce    int32
}

func (s *signInfo) getNextNonceAndUpdate(roundID uint64) int32 {
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

func (s *signInfo) revertNonce(roundID uint64) {
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

// updatePrice will transafer the decimal for LST
func (lp *localPrice) updatePrice(p *updatePrice, twoPhases bool) (string, error) {
	// TODO: convert decimal
	updatedPrice := *(p.price)
	if twoPhases {
		rootHash, err := base64.StdEncoding.DecodeString(p.price.Price)
		rootHash = rootHash[:32]
		if err != nil {
			return "", fmt.Errorf("failed to parse rootHash from base64 price-string, price:%v, error:%w", p.price, err)
		}
		updatedPrice.Price = string(rootHash)
	}
	lp.price = updatedPrice
	lp.height = p.txHeight
	return lp.price.Price, nil
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
	seenIndex      map[uint32]struct{}
}

type PieceWithProof struct {
	Piece      string
	PieceIndex uint32
	IndexesStr string
	HashesStr  string
}

func newTwoPhasesInfo() *twoPhasesInfo {
	return &twoPhasesInfo{
		sentLatestPieceIndex: -1,
		seenIndex:            make(map[uint32]struct{}),
	}
}

func (t *twoPhasesInfo) nextIndexIsLastOrMore() (bool, bool) {
	if t.finalizedTree == nil {
		return false, false
	}
	return t.nextPieceIndex >= t.finalizedTree.LeafCount()-1, true
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

func (t *twoPhasesInfo) finalizedRawDataByRootHash(rootHash []byte) []byte {
	if t.finalizedTree != nil && bytes.Equal(t.finalizedTree.RootHash(), rootHash) {
		rawData, _ := t.finalizedTree.CompleteRawData()
		return rawData
	}
	return nil
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
			t.resetFinalize()
			t.finalizedTree = mt
			t.cachedTree = nil
			return true
		}
	}
	// make sure finalizedRawdata is nil if no match found, it's better to be count as miss than 'malicious' for a validator
	t.resetFinalize()
	return false
}

// resetFinalize reset the finalizedTree and related fields
func (t *twoPhasesInfo) resetFinalize() {
	t.finalizedTree = nil
	t.sentLatestPieceIndex = -1
	t.nextPieceIndex = 0
	t.seenIndex = make(map[uint32]struct{})
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
		t.resetFinalize()
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
	decimal  int32
	tokenID  uint64
	feederID int
	// TODO: add check for rouleID, v1 can be skipped
	// ruleID
	startRoundID   int64
	startBaseBlock int64
	interval       int64
	endBlock       int64

	roundID   uint64
	baseBlock uint64

	fetcher    PriceFetcher
	fetcherNST PriceFetcherNST
	submitter  priceSubmitter
	lastPrice  *localPrice
	lastSent   *signInfo

	priceCh      chan *updatePrice
	heightsCh    chan *triggerHeights
	paramsCh     chan *updateParamsReq
	nstStakersCh chan *updateNSTStakerReq
	nstPieceCh   chan *updateNSTPieceReq
	nstBalanceCh chan *updateNSTBalanceReq

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

func (f *feeder) GetLatestRootHash() ([]byte, uint32) {
	return f.twoPhasesInfo.getLatestRootHash()
}

func (f *feeder) SetRoundID(roundID uint64) bool {
	if f.twoPhasesInfo == nil {
		return false
	}
	return f.twoPhasesInfo.setRoundID(roundID)
}

func (f *feeder) TwoPhaseFinalizedRoundID() (roundID uint64, isTwoPhase, finalizeed bool) {
	if f.twoPhasesInfo == nil {
		return 0, false, false
	}
	if f.twoPhasesInfo.finalizedTree == nil {
		return 0, true, false
	}
	return f.twoPhasesInfo.roundID, true, true
}

func (f *feeder) IsTwoPhases() bool {
	return f.twoPhasesInfo != nil
}

func (f *feeder) priceChanged(p *fetchertypes.PriceInfo) bool {
	return f.lastPrice.price.Price != p.Price
}

func (f *feeder) NextSendablePieceWithProofs(roundID uint64) ([]*PieceWithProof, error) {
	if f.twoPhasesInfo == nil {
		return nil, errors.New("two phases not enabled for this feeder")
	}
	if f.twoPhasesInfo.roundID != roundID {
		return nil, fmt.Errorf("roundID not match, feeder roundID:%d, input roundID:%d", f.twoPhasesInfo.roundID, roundID)
	}
	if f.twoPhasesInfo.finalizedTree == nil {
		return nil, errors.New("no finalized raw data found for this round")
	}

	ret := make([]*PieceWithProof, 0, 1)

	nextIdx := int64(f.twoPhasesInfo.nextPieceIndex)
	if nextIdx < f.twoPhasesInfo.sentLatestPieceIndex {
		return nil, ErrNoAvailableNextIndex.Wrap(fmt.Sprintf("latestSent:%d, nextPieceIndex:%d, leafCount:%d", f.twoPhasesInfo.sentLatestPieceIndex, nextIdx, f.twoPhasesInfo.finalizedTree.LeafCount()))
	}
	last, _ := f.twoPhasesInfo.nextIndexIsLastOrMore()
	if nextIdx > f.twoPhasesInfo.sentLatestPieceIndex {
		pwf, err := f.twoPhasesInfo.getRawDataPieceAndProof(roundID, f.twoPhasesInfo.nextPieceIndex)
		if err != nil {
			return nil, err
		}
		ret = append(ret, pwf)
		if last {
			return ret, nil
		}
	}
	if last {
		return nil, ErrNoAvailableNextIndex.Wrap(fmt.Sprintf("latestSent:%d, nextPieceIndex:%d, leafCount:%d", f.twoPhasesInfo.sentLatestPieceIndex, nextIdx, f.twoPhasesInfo.finalizedTree.LeafCount()))
	}
	pwf, err := f.twoPhasesInfo.getRawDataPieceAndProof(roundID, f.twoPhasesInfo.nextPieceIndex+1)
	if err == nil {
		ret = append(ret, pwf)
	}

	return ret, nil
}

// updateNSTPieceIndex update the next piece index to be sent for 2nd phase price submission
func (f *feeder) updateNSTPieceIndex(index uint32) error {
	if f.twoPhasesInfo == nil {
		return errors.New("two phases not enabled for this feeder")
	}

	if f.twoPhasesInfo.finalizedTree == nil {
		return errors.New("no finalized raw data found for this round")
	}

	if last, _ := f.twoPhasesInfo.nextIndexIsLastOrMore(); last {
		return fmt.Errorf("nextIndexPiece had reached the last piece, skip update with synced-index:%d, local-nextIndex:%d", index, f.twoPhasesInfo.nextPieceIndex)
	}

	if _, ok := f.twoPhasesInfo.seenIndex[index]; ok {
		return fmt.Errorf("index already seen, index:%d", index)
	}

	if index <= f.twoPhasesInfo.nextPieceIndex {
		f.twoPhasesInfo.nextPieceIndex++
		f.twoPhasesInfo.seenIndex[index] = struct{}{}
		return nil
	}

	if index < f.twoPhasesInfo.finalizedTree.LeafCount()-1 {
		f.twoPhasesInfo.nextPieceIndex = index + 1
		f.twoPhasesInfo.seenIndex[index] = struct{}{}
		return nil
	}

	return fmt.Errorf("failed to update nextPieceIndex:%d with input index:%d", f.twoPhasesInfo.nextPieceIndex, index)
}

// applyNSTBalanceUpdate apply the balance changes for 2nd phase price submission
// it will update the stakers and related source information by invoking fetcherNST
// it finds the finalized raw data by rootHash from finalizedTree and apply the balance changes, and will return error if no finalized raw data found
// this is used to sync the balance changes from imuachain to the feeder
func (f *feeder) applyNSTBalanceUpdate(rootHash []byte, version uint64) error {
	if f.twoPhasesInfo == nil {
		return errors.New("two phases not enabled for this feeder")
	}

	if bytes.Equal(rootHash, fetchertypes.EmptyRawDataChangesRootHash[:]) {
		// this is a special case for empty raw data changes which is triggered by the staker's deposit, so we need to use version+1
		if err := f.stakers.GrowVersionsFromCacheByDeposit(version); err != nil {
			f.logger.Error("failed to grow versions from cache", "error", err, "next feed-version", version)
			return err
		}
		f.logger.Info("successfully grow versions from cache", "updated feed-version", version)
	} else {
		rawData := f.twoPhasesInfo.finalizedRawDataByRootHash(rootHash)
		if rawData == nil {
			return errors.New("no finalized raw data found for this rootHash")
		}

		changes := &oracletypes.RawDataNST{}
		if err := proto.Unmarshal(rawData, changes); err != nil {
			return err
		}
		if err := f.stakers.ApplyBalanceChanges(changes, version); err != nil {
			return err
		}
		f.logger.Info("successfully applied balance changes", "updated feed-version", version)
	}
	sInfos, _ := f.stakers.GetStakerInfos()
	f.fetcherNST.SetNSTStakers(f.source, sInfos, version)
	return nil
}

// UpdateSentLatestPieceIndex update the sentLatestPieceIndex for 2nd phase price submission
// it will return the old index if the roundID matches, otherwise return -1
func (f *feeder) UpdateSentLatestPieceIndex(roundID uint64, index int64) int64 {
	if f.twoPhasesInfo.roundID != roundID {
		return -1
	}
	old := f.twoPhasesInfo.sentLatestPieceIndex
	f.twoPhasesInfo.sentLatestPieceIndex = index
	return old
}

// updateNSTStakers update the stakers for 2nd phase price submission
// it is used to sync the stakers information from imuachain to the feeder when depoisit/withdrwal nst happens
func (f *feeder) updateNSTStakers(sInfo *imuaclienttypes.EventNSTStaker) error {
	errCh := make(chan error)
	select {
	case f.nstStakersCh <- &updateNSTStakerReq{
		info: sInfo,
		res:  errCh,
	}:
		return <-errCh
		// don't block
	default:
	}
	return nil
}

// updateNSTBalances update the balances for 2nd phase price submission
// it is used to sync the balance changes from imuachain to the feeder when any balance of stakers changed
func (f *feeder) updateNSTBalance(balanceInfo *imuaclienttypes.EventNSTBalance) error {
	errCh := make(chan error)
	select {
	case f.nstBalanceCh <- &updateNSTBalanceReq{
		info: balanceInfo,
		res:  errCh,
	}:
		return <-errCh
		// don't block
	default:
	}
	return nil
}

// updateNSTPiece update the piece index for 2nd phase price submission
// it is used to sync the piece index from imuachain to the feeder when the piece index is updated which means the piece is finalized/processed by imuachain
func (f *feeder) updateNSTPiece(pieceIndex uint32) error {
	errCh := make(chan error)
	select {
	case f.nstPieceCh <- &updateNSTPieceReq{
		pieceIndex: pieceIndex,
		res:        errCh,
	}:
		return <-errCh
		// don't block
	default:
	}
	return nil
}

// Info return the feeder's information, it's used for logging by export all the information of the feeder
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

// neFeeder create a new feeder with the given parameters
func newFeeder(tf *oracletypes.TokenFeeder, feederID int, fetcher PriceFetcher, submitter priceSubmitter, source string, token string, maxNonce, decimal int32, pieceSize uint32, isTwoPhases bool, logger feedertypes.LoggerInf) (*feeder, error) {
	ret := feeder{
		logger:   logger,
		source:   source,
		token:    token,
		decimal:  decimal,
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

		ret.twoPhasesInfo = newTwoPhasesInfo()
		ret.twoPhasesPieceSize = pieceSize
		ret.nstStakersCh = make(chan *updateNSTStakerReq, 1)
		ret.nstPieceCh = make(chan *updateNSTPieceReq, 1)
		ret.nstBalanceCh = make(chan *updateNSTBalanceReq, 1)
		ret.stakers = fetchertypes.NewStakers()
		ret.fetcherNST = fNST
	}

	return &ret, nil
}

// return true when 1st phase for input roundID had been finalized
func (f *feeder) send2ndPhaseTx() ([]uint32, bool) {
	if f.twoPhasesInfo == nil {
		return nil, false
	}
	pwfs, err := f.NextSendablePieceWithProofs(f.roundID)
	if err == nil {
		ret := make([]uint32, 0)
		for _, pwf := range pwfs {
			pInfos := make([]*fetchertypes.PriceInfo, 0, 2)
			pInfos = append(pInfos, &fetchertypes.PriceInfo{
				Price:   pwf.Piece,
				RoundID: fmt.Sprintf("%d", pwf.PieceIndex),
			})
			if len(pwf.IndexesStr) > 0 && len(pwf.HashesStr) > 0 {
				pInfos = append(pInfos, &fetchertypes.PriceInfo{
					Price:   pwf.HashesStr,
					RoundID: pwf.IndexesStr,
				})
			}
			oldIndex := f.UpdateSentLatestPieceIndex(f.roundID, int64(pwf.PieceIndex))
			res, err := f.submitter.SendTx2Phases(uint64(f.feederID), f.baseBlock, pInfos, oracletypes.AggregationPhaseTwo, 1)
			if err != nil || res.GetTxResponse().Code != statusOk {
				f.logger.Error("failed to send tx for 2nd-phase price submission", "roundID", f.roundID, "feeder", f.Info(), "error", err, "res.RawLog", res.GetTxResponse().RawLog)
				// revert local index if failed to submit
				f.UpdateSentLatestPieceIndex(f.roundID, oldIndex)
				// piece index is not updated, so break the loop
				// TODO: ? should we break the loop here? or just try over following pieces
				break
			}
			ret = append(ret, pwf.PieceIndex)
			f.logger.Info("sent tx to submit price with rawData piece for 2nd phase", "pieceIndex", pwf.PieceIndex, "baseBlock", f.baseBlock, "roundID", f.roundID)
		}
		// the feeder is in phase two submitting rawData piece, so skip checking for phase one
		return ret, true
	}

	f.logger.Info("didn't able to get next sendable piece with proofs for 2nd-phase price submission", "roundID", f.roundID, "error", err)
	if errors.Is(err, ErrNoAvailableNextIndex) {
		return nil, true
	}

	return nil, false
}

func (f *feeder) start() {
	go func() {
		for {
			select {
			case h := <-f.heightsCh:
				if h.priceHeight > f.lastPrice.height {
					// the block event arrived early, wait for the price update events to update local price
					f.logger.Info("skip trigger for price update on current commitHeight", "height-commit", h.commitHeight, "height-price", h.priceHeight)
					continue
				}
				baseBlock, roundID, delta, active := f.calculateRound(h.commitHeight)
				f.roundID = roundID
				f.baseBlock = baseBlock
				if !active {
					f.logger.Debug("feeder not active", "feederID", f.feederID, "startBaseBlock", f.startBaseBlock, "current height", h.commitHeight)
					continue
				}

				if sentIndexes, is2nd := f.send2ndPhaseTx(); is2nd {
					if len(sentIndexes) > 0 {
						f.logger.Info("triggered 2nd-phase rawData piece message transaction sending", "current height", h.commitHeight, "price height", h.priceHeight, "sent_count", len(sentIndexes), "indexes", sentIndexes)
					}
					continue
				}

				// TODO: replace 3 with MaxNonce
				if delta < 3 {
					f.logger.Info("trigger feeder", "height_commit", h.commitHeight, "height_price", h.priceHeight)
					f.SetRoundID(roundID)

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
							f.logger.Debug("got latestprice equal to local cache", "feeder", f.Info())
							continue
						}

						if f.IsTwoPhases() {
							_, rootHash := f.AddRawData(roundID, []byte(price.Price), f.twoPhasesPieceSize)
							if bytes.Equal(rootHash, []byte(f.lastPrice.price.Price)) {
								f.logger.Info("didn't submit price for 1st-phase of 2phases due to price not changed", "roundID", roundID, "delta", delta, "price", price)
								f.logger.Debug("got latesttprice(rootHash) equal to local cache", "feeder", f.Info())
								continue
							}
						} else {
							// convert the decimal of LST to match definition in oracle module config
							if price.Decimal != f.decimal {
								price.Price = convertDecimal(price.Price, price.Decimal, f.decimal)
								price.Decimal = f.decimal
							}
							if !f.priceChanged(&price) {
								f.logger.Info("didn't submit price due to price not changed", "roundID", roundID, "delta", delta, "price", price)
								f.logger.Debug("got latestprice equal to local cache", "feeder", f.Info())
								continue
							}
						}
						if nonce := f.lastSent.getNextNonceAndUpdate(roundID); nonce < 0 {
							f.logger.Error("failed to submit due to no available nonce", "roundID", roundID, "delta", delta, "feeder", f.Info())
						} else {
							var res *sdktx.BroadcastTxResponse
							var err error
							if f.IsTwoPhases() {
								if root, count := f.GetLatestRootHash(); count > 0 {
									countBytes := strconv.FormatUint(uint64(count), 10)
									tmp := make([]byte, 0, len(root)+len(countBytes))
									tmp = append(tmp, root...)
									tmp = append(tmp, countBytes...)
									price.Price = string(tmp)
									// for imuachain validation on oracle price-feed messages
									price.Decimal = f.decimal
									f.logger.Info("phase 1 message of 2phases feeder", "rootHash", hex.EncodeToString(root), "piece_count", count, "roundID", price.RoundID)
									res, err = f.submitter.SendTx2Phases(uint64(f.feederID), baseBlock, []*fetchertypes.PriceInfo{&price}, oracletypes.AggregationPhaseOne, nonce)
								} else {
									f.logger.Error("failed to submit 1st-phase price due to no available rootHash for 2-phases aggregation submission", "roundID", roundID, "delta", delta, "feeder", f.Info())
									continue
								}
							} else {
								res, err = f.submitter.SendTx(uint64(f.feederID), baseBlock, price, nonce)
							}
							if err != nil {
								f.lastSent.revertNonce(roundID)
								f.logger.Error("failed to send tx submitting price", "price", price, "nonce", nonce, "baseBlock", baseBlock, "delta", delta, "feeder", f.Info(), "error_feeder", err)
								continue
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
				if f.IsTwoPhases() {
					rootHash, err := f.lastPrice.updatePrice(price, true)
					if err != nil {
						f.logger.Error("failed to update local price", "error", err)
						continue
					}
					f.logger.Info("finalize rootHash", "root", rootHash)
					f.FinalizeRawData(f.roundID, []byte(rootHash))
					if sentIndexes, is2nd := f.send2ndPhaseTx(); is2nd {
						f.logger.Info("sent 2nd phase rawdata piece transaction after 1st phase price updated", "sent_count", len(sentIndexes), "indexes", sentIndexes)
					}
				} else if _, err := f.lastPrice.updatePrice(price, false); err != nil {
					f.logger.Error("failed to update local price", "error", err)
					continue
				}
				f.logger.Info("synced local price with latest price from imuachain", "price", price.price, "txHeight", price.txHeight)
			case req := <-f.paramsCh:
				if err := f.updateFeederParams(req.params); err != nil {
					// This should not happen under this case.
					f.logger.Error("failed to update params", "new params", req.params)
				}
				req.result <- &updateParamsRes{}
				// TODO: ? no need to take care of concurrency for these requests of nstStakers change with channel
			case req := <-f.nstStakersCh:
				v, _ := req.info.Versions()
				if err := f.stakers.CacheDeposits(req.info.Deposits(), v); err != nil {
					req.res <- err
				} else {
					f.logger.Info("successfully cached deposits", "updated cached-version", v, "feederID", f.feederID)
					if v == 1 {
						if err := f.stakers.GrowVersionsFromCacheByDeposit(v); err != nil {
							f.logger.Error("failed to grow versions from cache on first deposit", "error", err, "feederID", f.feederID)
							req.res <- err
						} else {
							f.logger.Info("successfully grow versions from cache on first deposit, then reset stakerlist for fetcher", "feederID", f.feederID)
							sInfos, version := f.stakers.GetStakerInfos()
							f.fetcherNST.SetNSTStakers(f.source, sInfos, version)
						}
					}
					req.res <- nil
				}
			case req := <-f.nstBalanceCh:
				req.res <- f.applyNSTBalanceUpdate(req.info.RootHash(), req.info.Version())
			case req := <-f.nstPieceCh:
				req.res <- f.updateNSTPieceIndex(req.pieceIndex)
				if sentIndexes, is2nd := f.send2ndPhaseTx(); is2nd {
					f.logger.Info("sent 2nd phase rawdata piece transaction after 2nd rawdata piece index updated", "sent_count", len(sentIndexes), "indexes", sentIndexes)
				}
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

func (f *feeder) calculateRound(h int64) (baseBlock, roundID uint64, delta int64, active bool) {
	// endBlock itself is considered as active
	if f.startBaseBlock > h || (f.endBlock > 0 && h > f.endBlock) {
		return
	}
	active = true
	delta = (h - f.startBaseBlock) % f.interval
	roundID = uint64((h-f.startBaseBlock)/f.interval + f.startRoundID)
	baseBlock = uint64(h - delta)
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
	infos imuaclienttypes.EventNSTStakers
	res   chan []*failedFeedersWithError
}

type updateNSTStakerReq struct {
	info *imuaclienttypes.EventNSTStaker
	res  chan error
}

type updateNSTPiecesReq struct {
	infos imuaclienttypes.EventNSTPieces
	res   chan []*failedFeedersWithError
}

type updateNSTPieceReq struct {
	pieceIndex uint32
	res        chan error
}

type updateNSTBalancesReq struct {
	infos imuaclienttypes.EventNSTBalances
	res   chan []*failedFeedersWithError
}

type updateNSTBalanceReq struct {
	info *imuaclienttypes.EventNSTBalance
	res  chan error
}

type Feeders struct {
	locker    *sync.Mutex
	running   bool
	fetcher   PriceFetcher
	submitter priceSubmitter
	logger    feedertypes.LoggerInf
	feederMap map[int]*feeder
	// TODO: feeder has sync management, so feeders could remove these channel
	trigger             chan *triggerReq
	updatePriceCh       chan *updatePricesReq
	updateParamsCh      chan *oracletypes.Params
	updateNSTStakersCh  chan *updateNSTStakersReq
	updateNSTBalancesCh chan *updateNSTBalancesReq
	updateNSTPiecesCh   chan *updateNSTPiecesReq
}

func NewFeeders(logger feedertypes.LoggerInf, fetcher PriceFetcher, submitter priceSubmitter) *Feeders {
	return &Feeders{
		locker:    new(sync.Mutex),
		logger:    logger,
		fetcher:   fetcher,
		submitter: submitter,
		feederMap: make(map[int]*feeder),
		// don't block on height increasing
		trigger:       make(chan *triggerReq, 1),
		updatePriceCh: make(chan *updatePricesReq, 1),
		// it's safe to have a buffer to not block running feeders,
		// since for running feeders, only endBlock is possible to be modified
		updateParamsCh: make(chan *oracletypes.Params, 1),

		// this request will be recieved at most one per block, so the buffer 1 is enough
		updateNSTStakersCh:  make(chan *updateNSTStakersReq, 1),
		updateNSTBalancesCh: make(chan *updateNSTBalancesReq, 1),
		updateNSTPiecesCh:   make(chan *updateNSTPiecesReq, 1),
	}

}

func (fs *Feeders) SetupFeeder(tf *oracletypes.TokenFeeder, feederID int, source string, token string, maxNonce, decimal int32, pieceSize uint32, isTwoPhases bool) {
	fs.locker.Lock()
	defer fs.locker.Unlock()
	if fs.running {
		fs.logger.Error("failed to setup feeder for a running feeders, this should be called before feeders is started", "feederID", feederID)
		return
	}
	f, err := newFeeder(tf, feederID, fs.fetcher, fs.submitter, source, token, maxNonce, decimal, pieceSize, isTwoPhases, fs.logger.With("feeder", fmt.Sprintf(loggerTagPrefix, token, feederID)))
	if err != nil {
		fs.logger.Error("failed to create feeder", "feederID", feederID, "error", err)
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
					if tfID == 0 {
						continue
					}
					if _, ok := existingFeederIDs[int64(tfID)]; !ok {
						// create and start a new feeder
						tokenName := strings.ToLower(params.Tokens[tf.TokenID].Name)
						decimal := params.Tokens[tf.TokenID].Decimal
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

						feeder, err := newFeeder(tf, tfID, fs.fetcher, fs.submitter, source, tokenName, params.MaxNonce, decimal, params.PieceSizeByte, params.IsRule2PhasesByFeederID(uint64(tfID)), fs.logger)
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
				res := make([]*failedFeedersWithError, 0)
				for feederID, stakerInfo := range req.infos {
					feeder, ok := fs.feederMap[int(feederID)]
					if !ok {
						fs.logger.Error("failed to get feeder by feederID when update price for feeders", "updatePriceReq", req)
					}
					if err := feeder.updateNSTStakers(stakerInfo); err != nil {
						res = append(res, &failedFeedersWithError{
							feederID: feederID,
							err:      err,
						})
					}
				}
				req.res <- res
			case req := <-fs.updateNSTBalancesCh:
				res := make([]*failedFeedersWithError, 0)
				for feederID, balanceInfo := range req.infos {
					feeder, ok := fs.feederMap[int(feederID)]
					if !ok {
						fs.logger.Error("failed to get feeder by feederID when update price for feeders", "updatePriceReq", req)
						continue
					}
					if err := feeder.updateNSTBalance(balanceInfo); err != nil {
						res = append(res, &failedFeedersWithError{
							feederID: feederID,
							err:      err,
						})
					} else {
						fs.logger.Info("successfully updated NST balance", "feederID", feederID, "balanceInfo", balanceInfo)
					}
				}
				req.res <- res
			case req := <-fs.updateNSTPiecesCh:
				res := make([]*failedFeedersWithError, 0)
				for feederID, pieceInfo := range req.infos {
					feeder, ok := fs.feederMap[int(feederID)]
					if !ok {
						fs.logger.Error("failed to get feeder by feederID when update price for feeders", "updatePriceReq", req)
					}
					if err := feeder.updateNSTPiece(pieceInfo); err != nil {
						res = append(res, &failedFeedersWithError{
							feederID: feederID,
							err:      err,
						})
					}
				}
				req.res <- res
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

func (fs *Feeders) UpdateNSTStakers(updates imuaclienttypes.EventNSTStakers) []*failedFeedersWithError {
	res := make(chan []*failedFeedersWithError)
	req := &updateNSTStakersReq{infos: updates, res: res}
	select {
	case fs.updateNSTStakersCh <- req:
		// if the request of one block is missed, feeder will find out the version mismatch and then update activity
		return <-res
	default:
		ret := make([]*failedFeedersWithError, 0, len(req.infos))
		for feederID := range updates {
			ret = append(ret, &failedFeedersWithError{feederID: feederID, err: errors.New("failed to update NST staker infos due to channel full")})
		}
		return ret
	}
}

func (fs *Feeders) UpdateNSTBalances(updates imuaclienttypes.EventNSTBalances) []*failedFeedersWithError {
	res := make(chan []*failedFeedersWithError)
	req := &updateNSTBalancesReq{infos: updates, res: res}
	select {
	case fs.updateNSTBalancesCh <- req:
		// if the request of one block is missed, feeder will find out the version mismatch and then update activity
		return <-res
	default:
		ret := make([]*failedFeedersWithError, 0, len(req.infos))
		for feederID := range updates {
			ret = append(ret, &failedFeedersWithError{feederID: feederID, err: errors.New("failed to update NST balance due to channel full")})
		}
		return ret
	}

}

func (fs *Feeders) UpdateNSTPieces(updates imuaclienttypes.EventNSTPieces) []*failedFeedersWithError {
	res := make(chan []*failedFeedersWithError)
	req := &updateNSTPiecesReq{infos: updates, res: res}
	select {
	case fs.updateNSTPiecesCh <- req:
		return <-res
	default:
		ret := make([]*failedFeedersWithError, 0, len(req.infos))
		for feederID := range updates {
			ret = append(ret, &failedFeedersWithError{feederID: feederID, err: errors.New("failed to update NST balance due to channel full")})
		}
		return ret
	}
}

func convertDecimal(price string, decimalFrom, decimalTo int32) string {
	if decimalTo > decimalFrom {
		return price + strings.Repeat("0", int(decimalTo-decimalFrom))
	}
	if decimalTo < decimalFrom {
		delta := int(decimalFrom - decimalTo)
		if len(price) <= delta {
			return ""
		}
		return price[:len(price)-delta]
	}
	return price
}
