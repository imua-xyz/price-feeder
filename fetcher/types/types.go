package types

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"slices"

	"github.com/ethereum/go-ethereum/common/hexutil"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type SourceInf interface {
	// InitTokens used for initialization, it should only be called before 'Start'
	// when the source is not 'running', this method vill overwrite the source's 'tokens' list
	InitTokens(tokens []string) bool

	// Start starts fetching prices of all tokens configured in the source. token->price
	Start() map[string]*PriceSync

	// AddTokenAndStart adds a new token to fetch price for a running source
	AddTokenAndStart(token string) *addTokenRes

	// GetName returns name of the source
	GetName() string
	// Status returns the status of all tokens configured in the source: running or not
	Status() map[string]*tokenStatus

	// ReloadConfig reload the config file for the source
	//	ReloadConfigForToken(string) error

	// Stop closes all running routine generated from the source
	Stop()

	// TODO: add some interfaces to achieve more fine-grained management
	// StopToken(token string)
	// ReloadConfigAll()
	// RestarAllToken()
}

type SourceNSTInf interface {
	SourceInf
	SetNSTStakers(sInfos StakerInfos, version uint64)
}

type SourceInitFunc func(cfgPath string, logger feedertypes.LoggerInf) (SourceInf, error)

type SourceFetchFunc func(token string) (*PriceInfo, error)

// SourceReloadConfigFunc reload source config file
// reload meant to be called during source running, the concurrency should be handled well
type SourceReloadConfigFunc func(config, token string) error

type NativeRestakingInfo struct {
	Chain   string
	TokenID string
}

type PriceInfo struct {
	Price     string
	Decimal   int32
	Timestamp string
	RoundID   string
	// TODO(leonz): add a field as DetID
}

type PriceSync struct {
	lock *sync.RWMutex
	info *PriceInfo
}

type tokenInfo struct {
	name   string
	price  *PriceSync
	active *atomic.Bool
}
type tokenStatus struct {
	name   string
	price  PriceInfo
	active bool
}

type addTokenReq struct {
	tokenName string
	result    chan *addTokenRes
}

type addTokenRes struct {
	price *PriceSync
	err   error
}

// IsZero is used to check if a PriceInfo has not been assigned, similar to a nil value in a pointer variable
func (p PriceInfo) IsZero() bool {
	return len(p.Price) == 0
}

// Equal compare two PriceInfo ignoring the timestamp, roundID fields
func (p PriceInfo) EqualPrice(price PriceInfo) bool {
	if p.Price == price.Price &&
		p.Decimal == price.Decimal {
		return true
	}
	return false
}
func (p PriceInfo) EqualToBase64Price(price PriceInfo) bool {
	if len(p.Price) < 32 {
		return false
	}
	h := sha256.New()
	h.Write([]byte(p.Price))
	p.Price = base64.StdEncoding.EncodeToString(h.Sum(nil))

	if p.Price == price.Price &&
		p.Decimal == price.Decimal {
		return true
	}
	return false
}

func NewPriceSync() *PriceSync {
	return &PriceSync{
		lock: new(sync.RWMutex),
		info: &PriceInfo{},
	}
}

func (p *PriceSync) Get() PriceInfo {
	p.lock.RLock()
	price := *p.info
	p.lock.RUnlock()
	return price
}

func (p *PriceSync) Update(price PriceInfo) (updated bool) {
	p.lock.Lock()
	if !price.EqualPrice(*p.info) {
		*p.info = price
		updated = true
	}
	p.lock.Unlock()
	return
}

func (p *PriceSync) Set(price PriceInfo) {
	p.lock.Lock()
	*p.info = price
	p.lock.Unlock()
}

func NewTokenInfo(name string, price *PriceSync) *tokenInfo {
	return &tokenInfo{
		name:   name,
		price:  price,
		active: new(atomic.Bool),
	}
}

// GetPriceSync returns the price structure which has lock to make sure concurrency safe
func (t *tokenInfo) GetPriceSync() *PriceSync {
	return t.price
}

// GetPrice returns the price info
func (t *tokenInfo) GetPrice() PriceInfo {
	return t.price.Get()
}

// GetActive returns the active status
func (t *tokenInfo) GetActive() bool {
	return t.active.Load()
}

// SetActive set the active status
func (t *tokenInfo) SetActive(v bool) {
	t.active.Store(v)
}

func newAddTokenReq(tokenName string) (*addTokenReq, chan *addTokenRes) {
	resCh := make(chan *addTokenRes, 1)
	req := &addTokenReq{
		tokenName: tokenName,
		result:    resCh,
	}
	return req, resCh
}

func (r *addTokenRes) Error() error {
	return r.err
}
func (r *addTokenRes) Price() *PriceSync {
	return r.price
}

var _ SourceInf = &Source{}

// Source is a common implementation of SourceInf
type Source struct {
	logger    feedertypes.LoggerInf
	cfgPath   string
	running   bool
	priceList map[string]*PriceSync
	name      string
	locker    *sync.Mutex
	stop      chan struct{}
	// 'fetch' interacts directly with data source
	fetch            SourceFetchFunc
	reload           SourceReloadConfigFunc
	tokens           map[string]*tokenInfo
	activeTokenCount *atomic.Int32
	interval         time.Duration
	addToken         chan *addTokenReq
	// used to trigger reloading source config
	tokenNotConfigured chan string
}

// NewSource returns a implementaion of sourceInf
// for sources they could utilitize this function to provide that 'SourceInf' by taking care of only the 'fetch' function
func NewSource(logger feedertypes.LoggerInf, name string, fetch SourceFetchFunc, cfgPath string, reload SourceReloadConfigFunc) *Source {
	return &Source{
		logger:             logger,
		cfgPath:            cfgPath,
		name:               name,
		locker:             new(sync.Mutex),
		stop:               make(chan struct{}),
		tokens:             make(map[string]*tokenInfo),
		activeTokenCount:   new(atomic.Int32),
		priceList:          make(map[string]*PriceSync),
		interval:           defaultInterval,
		addToken:           make(chan *addTokenReq, defaultPendingTokensLimit),
		tokenNotConfigured: make(chan string, 1),
		fetch:              fetch,
		reload:             reload,
	}
}

// InitTokenNames adds the token names in the source's token list
// NOTE: call before start
func (s *Source) InitTokens(tokens []string) bool {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.running {
		s.logger.Info("failed to add a token with 'InitTokens' for the running source, use 'addTokenAndStart' instead")
		return false
	}
	// reset all tokens
	s.tokens = make(map[string]*tokenInfo)
	for _, token := range tokens {
		// we standardize all token names to lowercase
		token = strings.ToLower(token)
		s.tokens[token] = NewTokenInfo(token, NewPriceSync())
	}
	return true
}

// Start starts background routines to fetch all registered token for the source frequently
// and watch for 1. add token, 2.stop events
// TODO(leon): return error and existing map when running already
func (s *Source) Start() map[string]*PriceSync {
	s.locker.Lock()
	if s.running {
		s.logger.Error("failed to start the source which is already running", "source", s.name)
		s.locker.Unlock()
		return nil
	}
	if len(s.tokens) == 0 {
		s.logger.Error("failed to start the source which has no tokens set", "source", s.name)
		s.locker.Unlock()
		return nil
	}
	s.running = true
	s.locker.Unlock()
	ret := make(map[string]*PriceSync)
	for tName, token := range s.tokens {
		ret[tName] = token.GetPriceSync()
		s.logger.Info("start fetching prices", "source", s.name, "token", token, "tokenName", token.name)
		s.startFetchToken(token)
	}
	// main routine of source, listen to:
	// addToken to add a new token for the source and start fetching that token's price
	// tokenNotConfigured to reload the source's config file for required token
	// stop closes the source routines and set runnign status to false
	go func() {
		for {
			select {
			case req := <-s.addToken:
				price := NewPriceSync()
				// check token existence and then add to token list & start if not exists
				if token, ok := s.tokens[req.tokenName]; !ok {
					token = NewTokenInfo(req.tokenName, price)
					s.tokens[req.tokenName] = token
					s.logger.Info("add a new token and start fetching price", "source", s.name, "token", req.tokenName)
					s.startFetchToken(token)
				} else {
					s.logger.Info("didn't add duplicated token, return existing priceSync", "source", s.name, "token", req.tokenName)
					price = token.GetPriceSync()
				}
				req.result <- &addTokenRes{
					price: price,
					err:   nil,
				}
			case tName := <-s.tokenNotConfigured:
				if err := s.reloadConfigForToken(tName); err != nil {
					s.logger.Error("failed to reload config for adding token", "source", s.name, "token", tName)
				}
			case <-s.stop:
				s.logger.Info("exit listening rountine for addToken", "source", s.name)
				// waiting for all token routines to exist
				for s.activeTokenCount.Load() > 0 {
					time.Sleep(1 * time.Second)
				}
				s.locker.Lock()
				s.running = false
				s.locker.Unlock()
				return
			}
		}
	}()
	return ret
}

// AddTokenAndStart adds token into a running source and start fetching that token
// return (nil, false) and skip adding this token when previously adding request is not handled
// if the token is already exist, it will that correspondin *priceSync
func (s *Source) AddTokenAndStart(token string) *addTokenRes {
	s.locker.Lock()
	defer s.locker.Unlock()
	if !s.running {
		return &addTokenRes{
			price: nil,
			err:   fmt.Errorf("didn't add token due to source:%s not running", s.name),
		}
	}
	// we don't block the process when the channel is not available
	// caller should handle the returned bool value properly
	addReq, addResCh := newAddTokenReq(token)
	select {
	case s.addToken <- addReq:
		return <-addResCh
	default:
	}
	// TODO(leon): define an res-skipErr variable
	return &addTokenRes{
		price: nil,
		err:   fmt.Errorf("didn't add token, too many pendings, limit:%d", defaultPendingTokensLimit),
	}
}

func (s *Source) Stop() {
	s.logger.Info("stop source and close all running routines", "source", s.name)
	s.locker.Lock()
	// make it safe when closed more than one time
	select {
	case _, ok := <-s.stop:
		if ok {
			close(s.stop)
		}
	default:
		close(s.stop)
	}
	s.locker.Unlock()
}

func (s *Source) startFetchToken(token *tokenInfo) {
	s.activeTokenCount.Add(1)
	token.SetActive(true)
	go func() {
		defer func() {
			token.SetActive(false)
			s.activeTokenCount.Add(-1)
		}()
		tic := time.NewTicker(s.interval)
		for {
			select {
			case <-s.stop:
				s.logger.Info("exist fetching routine", "source", s.name, "token", token)
				return
			case <-tic.C:
				if price, err := s.fetch(token.name); err != nil {
					if errors.Is(err, feedertypes.ErrSourceTokenNotConfigured) {
						s.logger.Info("token not config for source", "token", token.name)
						s.tokenNotConfigured <- token.name
					} else {
						s.logger.Error("failed to fetch price", "source", s.name, "token", token.name, "error", err)
						// TODO(leon): exist this routine after maximum fails ?
						// s.tokens[token.name].active = false
						// return
					}
				} else {
					// update price
					updated := token.price.Update(*price)
					if updated {
						s.logger.Info("updated price", "source", s.name, "token", token.name, "price", *price)
					}
				}
			}
		}
	}()
}

func (s *Source) reloadConfigForToken(token string) error {
	if err := s.reload(s.cfgPath, token); err != nil {
		return fmt.Errorf("failed to reload config file to from path:%s when adding token", s.cfgPath)
	}
	return nil
}

func (s *Source) GetName() string {
	return s.name
}

func (s *Source) Status() map[string]*tokenStatus {
	s.locker.Lock()
	ret := make(map[string]*tokenStatus)
	for tName, token := range s.tokens {
		ret[tName] = &tokenStatus{
			name:   tName,
			price:  token.price.Get(),
			active: token.GetActive(),
		}
	}
	s.locker.Unlock()
	return ret
}

type NSTToken string

const (
	defaultPendingTokensLimit = 5
	// defaultInterval           = 30 * time.Second
	// interval set for debug
	defaultInterval = 5 * time.Second
	Chainlink       = "chainlink"
	BaseCurrency    = "usdt"
	BeaconChain     = "beaconchain"
	Solana          = "solana"

	NativeTokenETH NSTToken = "nsteth"
	NativeTokenSOL NSTToken = "nstsol"

	DefaultSlotsPerEpoch = uint64(32)
)

var (
	// NSTETHZeroChanges = make([]byte, 32)
	NSTETHZeroChanges = make([]byte, 0)
	// source -> initializers of source
	SourceInitializers   = make(map[string]SourceInitFunc)
	ChainToSlotsPerEpoch = map[uint64]uint64{
		101:   DefaultSlotsPerEpoch,
		40161: DefaultSlotsPerEpoch,
		40217: DefaultSlotsPerEpoch,
	}

	NSTTokens = map[NSTToken]struct{}{
		NativeTokenETH: {},
		NativeTokenSOL: {},
	}
	NSTAssetIDMap = make(map[NSTToken]string)
	NSTSourceMap  = map[NSTToken]string{
		NativeTokenETH: BeaconChain,
		NativeTokenSOL: Solana,
	}

	Logger feedertypes.LoggerInf
)

func SetNativeAssetID(nstToken NSTToken, assetID string) {
	NSTAssetIDMap[nstToken] = assetID
}

// GetNSTSource returns source name as string
func GetNSTSource(nstToken NSTToken) string {
	return NSTSourceMap[nstToken]
}

// GetNSTAssetID returns nst assetID as string
func GetNSTAssetID(nstToken NSTToken) string {
	return NSTAssetIDMap[nstToken]
}

func IsNSTToken(tokenName string) bool {
	if _, ok := NSTTokens[NSTToken(tokenName)]; ok {
		return true
	}
	return false
}

type StakerInfo struct {
	Validators []string
	Balance    uint64
}

type StakerInfos map[uint32]*StakerInfo

func NewStakerInfo() *StakerInfo {
	return &StakerInfo{
		Validators: make([]string, 0),
		Balance:    0,
	}
}

func (s *StakerInfo) AddValidator(validator string, balance uint64) bool {
	if s == nil {
		return false
	}
	if slices.Contains(s.Validators, validator) {
		return false
	}
	s.Validators = append(s.Validators, validator)
	s.Balance += balance
	return true
}

type Stakers struct {
	Locker  *sync.RWMutex
	Version uint64
	SInfos  StakerInfos
}

func (sis StakerInfos) GetCopy() StakerInfos {
	//	ret := make(map[uint64][]string)
	ret := make(StakerInfos)
	for idx, si := range sis {
		ret[idx] = &StakerInfo{
			Validators: si.Validators,
			Balance:    si.Balance,
		}
	}
	return ret
}

func (sis *StakerInfos) GetSVList() map[uint32][]string {
	ret := make(map[uint32][]string)
	for idx, si := range *sis {
		ret[idx] = si.Validators
	}
	return ret
}

// Add will add the given stakerInfo of stakerIndex to the stakerInfos, if the process failed, the original StakerInfos might be changed, however this is faster than AddAllOrNone, so it suits the case when the 'allOrNone' is not important
func (sis *StakerInfos) Add(stakerIndex uint32, sAdd *StakerInfo) error {
	if sis == nil {
		return fmt.Errorf("failed to do add, stakerInfos is nil")
	}
	sInfo, exists := (*sis)[stakerIndex]
	if !exists {
		sInfo = NewStakerInfo()
		(*sis)[stakerIndex] = sInfo
	}
	seen := make(map[string]struct{})
	for _, v := range sInfo.Validators {
		seen[v] = struct{}{}
	}
	for _, v := range sAdd.Validators {
		if _, ok := seen[v]; ok {
			return fmt.Errorf("failed to do add, validatorIndex already exists, staker-index:%d, validator:%s", stakerIndex, v)
		}
		sInfo.Validators = append(sInfo.Validators, v)
		seen[v] = struct{}{}
	}
	sInfo.Balance += sAdd.Balance
	return nil
}

func (sis *StakerInfos) Remove(stakerIndex uint32, sRemove StakerInfo) error {
	if sis == nil {
		return fmt.Errorf("failed to do add, stakerInfos is nil")
	}
	sInfo, exists := (*sis)[stakerIndex]
	if !exists {
		return fmt.Errorf("failed to do remove, stakerIndex not found, staker-index:%d", stakerIndex)
	}
	if sInfo.Balance < sRemove.Balance {
		return fmt.Errorf("failed to remove, balance not enough, staker-index:%d, balance:%d, remove-balance:%d", stakerIndex, sInfo.Balance, sRemove.Balance)
	}

	seen := make(map[string]struct{})
	for _, v := range sRemove.Validators {
		seen[v] = struct{}{}
	}
	result := make([]string, 0, len(sInfo.Validators))
	for _, v := range sInfo.Validators {
		if _, ok := seen[v]; !ok {
			result = append(result, v)
		}
	}
	if len(sInfo.Validators)-len(result) != len(sRemove.Validators) {
		return fmt.Errorf("failed to remove validators, including non-existing validatorIndex to remove, staker-index:%d", stakerIndex)
	}
	if len(result) == 0 {
		delete(*sis, stakerIndex)
	} else {
		sInfo.Validators = result
	}
	sInfo.Balance -= sRemove.Balance
	return nil
}

func (sis *StakerInfos) AddAllOrNone(sAdd StakerInfos) error {
	if len(sAdd) == 0 {
		return nil
	}
	tmp := sis.GetSVList()
	for idx, siAdd := range sAdd {
		if si, exists := (*sis)[idx]; exists {
			seen := make(map[string]struct{})
			for _, v := range si.Validators {
				seen[v] = struct{}{}
			}
			for _, v := range siAdd.Validators {
				if _, ok := seen[v]; ok {
					return fmt.Errorf("failed to do add, validatorIndex already exists, staker-index:%d, validator:%s", idx, v)
				}
				tmp[idx] = append(tmp[idx], v)
				seen[v] = struct{}{}
			}
		} else {
			tmp[idx] = siAdd.Validators
		}
	}
	for idx, siAdd := range sAdd {
		if _, ok := (*sis)[idx]; !ok {
			(*sis)[idx] = &StakerInfo{
				Validators: tmp[idx],
				Balance:    siAdd.Balance,
			}
		} else {
			(*sis)[idx].Validators = tmp[idx]
			(*sis)[idx].Balance += siAdd.Balance
		}
	}
	return nil
}

func (sis *StakerInfos) RemoveAllOrNone(sRemove StakerInfos) error {
	if len(sRemove) == 0 {
		return nil
	}
	tmp := sis.GetSVList()
	for idx, siRemove := range sRemove {
		if si, exists := (*sis)[idx]; exists {
			if si.Balance < siRemove.Balance {
				return fmt.Errorf("failed to remove, balance not enough, staker-index:%d, balance:%d, remove-balance:%d", idx, si.Balance, siRemove.Balance)
			}
			seen := make(map[string]struct{})
			for _, v := range siRemove.Validators {
				seen[v] = struct{}{}
			}
			result := make([]string, 0, len(si.Validators))
			for _, v := range si.Validators {
				if _, ok := seen[v]; !ok {
					result = append(result, v)
				}
			}
			if len(si.Validators)-len(result) != len(siRemove.Validators) {
				return fmt.Errorf("failed to remove validators, including non-existing validatorIndex to remove, staker-index:%d", idx)
			}
			if len(result) == 0 {
				delete(*sis, idx)
			} else {
				tmp[idx] = result
			}
		} else {
			return fmt.Errorf("failed to do remove, stakerIndex not found, staker-index:%d", idx)
		}
	}
	for idx, siRemove := range sRemove {
		if _, ok := tmp[idx]; !ok {
			delete((*sis), idx)
		} else {
			(*sis)[idx].Validators = tmp[idx]
			(*sis)[idx].Balance -= siRemove.Balance

		}
	}
	return nil
}

func (sis *StakerInfos) Update(sAdd, sRemove StakerInfos) error {
	if err := sis.AddAllOrNone(sAdd); err != nil {
		return err
	}
	if err := sis.RemoveAllOrNone(sRemove); err != nil {
		return err
	}
	return nil
}

func NewStakers() *Stakers {
	return &Stakers{
		Locker: new(sync.RWMutex),
		SInfos: make(StakerInfos),
	}
}

func (s *Stakers) Length() int {
	if s == nil {
		return 0
	}
	s.Locker.RLock()
	l := len(s.SInfos)
	s.Locker.RUnlock()
	return l
}

// GetStakerInfos returns the stakerInfos and version, the stakerInfos is a copy, so it can be modified
func (s *Stakers) GetStakerInfos() (StakerInfos, uint64) {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.SInfos.GetCopy(), s.Version
}

// GetStakerInfosNoCopy returns the stakerInfos and version, the stakerInfos is not a copy, so it should not be modified
func (s *Stakers) GetStakersNoCopy() (StakerInfos, uint64) {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.SInfos, s.Version
}

// SetStakerInfos set the stakerInfos and version, this method should be called when the stakerInfos is initialized or reset, it will not modify the existing stakerInfos but just replace it
func (s *Stakers) SetStakerInfos(sInfos StakerInfos, version uint64) {
	s.Locker.Lock()
	s.SInfos = sInfos
	s.Version = version
	s.Locker.Unlock()
}

func (s *Stakers) GetStakerBalances() map[uint32]uint64 {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	ret := make(map[uint32]uint64)
	for idx, si := range s.SInfos {
		ret[idx] = si.Balance
	}
	return ret
}

func (s *Stakers) Update(sInfosAdd, sInfosRemove StakerInfos, nextVersion, latestVersion uint64) error {
	s.Locker.Lock()
	defer s.Locker.Unlock()
	if s.Version+1 != nextVersion {
		return fmt.Errorf("version mismatch, current:%d, next:%d", s.Version, nextVersion)
	}
	if err := s.SInfos.Update(sInfosAdd, sInfosRemove); err != nil {
		return err
	}
	s.Version = latestVersion
	return nil
}

func (s *Stakers) ApplyBalanceChanges(changes *oracletypes.RawDataNST) error {
	s.Locker.Lock()
	defer s.Locker.Unlock()
	if s.Version != changes.Version {
		return fmt.Errorf("version mismatch, current:%d, next:%d", s.Version, changes.Version)
	}
	for _, change := range changes.NstBalanceChanges {
		sInfo, exists := s.SInfos[uint32(change.StakerIndex)]
		if !exists {
			return fmt.Errorf("stakerIndex not found, staker-index:%d", change.StakerIndex)
		}
		sInfo.Balance = change.Balance
	}
	s.Version++
	return nil
}

func (s *Stakers) Reset(sInfos []*oracletypes.StakerInfo, version uint64, all bool) error {
	s.Locker.Lock()
	defer s.Locker.Unlock()
	if !all && s.Version+1 != version {
		return fmt.Errorf("version mismatch, current:%d, next:%d", s.Version, version)
	}
	tmp := make(StakerInfos)
	seenStakerIdx := make(map[uint64]struct{})
	for _, sInfo := range sInfos {
		if _, ok := seenStakerIdx[uint64(sInfo.StakerIndex)]; ok {
			return fmt.Errorf("duplicated stakerIndex, staker-index:%d", sInfo.StakerIndex)
		}
		seenStakerIdx[uint64(sInfo.StakerIndex)] = struct{}{}

		// we don't limit the staker size here, it's guaranteed by imuachain, and the size might not be equal to the biggest index
		validators := make([]string, 0, len(sInfo.ValidatorPubkeyList))
		seenValidatorIdx := make(map[string]struct{})
		for _, validator := range sInfo.ValidatorPubkeyList {
			if _, ok := seenValidatorIdx[validator]; ok {
				return fmt.Errorf("duplicated validatorIndex, validator-index-hex:%s", validator)
			}
			validators = append(validators, validator)
			seenValidatorIdx[validator] = struct{}{}
		}
		balance := uint64(0)
		l := len(sInfo.BalanceList)
		if l > 0 && sInfo.BalanceList[l-1] != nil && sInfo.BalanceList[l-1].Balance > 0 {
			balance = uint64(sInfo.BalanceList[l-1].Balance)
		}
		tmp[uint32(sInfo.StakerIndex)] = &StakerInfo{
			Validators: validators,
			Balance:    balance,
		}
	}
	if all {
		s.SInfos = tmp
	} else {
		for k, v := range tmp {
			s.SInfos[k] = v
		}
	}
	s.Version = version
	return nil
}

func ConvertHexToIntStr(hexStr string) (string, error) {
	vBytes, err := hexutil.Decode(hexStr)
	if err != nil {
		return "", err
	}
	return new(big.Int).SetBytes(vBytes).String(), nil

}
