package types

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// SourceInf defines the interface for a price source, including initialization, starting, adding tokens, and stopping.
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

	// ReloadConfig reload the config file for the source
	//	ReloadConfigForToken(string) error

	// Stop closes all running routine generated from the source
	Stop()

	PriceUpdate() bool

	ResetPriceUpdate()

	// TODO: add some interfaces to achieve more fine-grained management
	// StopToken(token string)
	// ReloadConfigAll()
	// RestarAllToken()
}

// SourceNSTInf extends SourceInf with a method to set NST stakers and version.
type SourceNSTInf interface {
	SourceInf
	SetNSTStakers(sInfos StakerInfos, version, withdrawVersion uint64)
}

// SourceInitFunc is a function type for initializing a SourceInf from a config path and logger.
type SourceInitFunc func(cfgPath string, logger feedertypes.LoggerInf) (SourceInf, error)

// SourceFetchFunc is a function type for fetching a price for a given token.
type SourceFetchFunc func(token string) (*PriceInfo, error)

// SourceReloadConfigFunc is a function type for reloading a source's config file for a specific token.
// reload meant to be called during source running, the concurrency should be handled well
type SourceReloadConfigFunc func(config, token string) error

type NativeRestakingInfo struct {
	Chain   string
	TokenID string
}

// PriceInfo holds price data for a token, including value, decimals, timestamp, and round ID.
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

type TokenStatus struct {
	Name   string
	Price  PriceInfo
	Active bool
}

type addTokenReq struct {
	tokenName string
	result    chan *addTokenRes
}

type addTokenRes struct {
	price *PriceSync
	err   error
}

// IsZero checks if a PriceInfo has not been assigned a value.
func (p PriceInfo) IsZero() bool {
	return len(p.Price) == 0
}

// EqualPrice compares two PriceInfo values, ignoring timestamp and roundID fields.
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

// NewPriceSync creates a new PriceSync instance with a mutex and empty PriceInfo.
func NewPriceSync() *PriceSync {
	return &PriceSync{
		lock: new(sync.RWMutex),
		info: &PriceInfo{},
	}
}

// Get returns a copy of the current PriceInfo in a concurrency-safe way.
func (p *PriceSync) Get() PriceInfo {
	p.lock.RLock()
	price := *p.info
	p.lock.RUnlock()
	return price
}

// Update sets the PriceInfo if it differs from the current value, returning true if updated.
func (p *PriceSync) Update(price PriceInfo) (updated bool) {
	p.lock.Lock()
	if !price.EqualPrice(*p.info) {
		*p.info = price
		updated = true
	}
	p.lock.Unlock()
	return
}

// Set sets the PriceInfo value in a concurrency-safe way.
func (p *PriceSync) Set(price PriceInfo) {
	p.lock.Lock()
	*p.info = price
	p.lock.Unlock()
}

// NewTokenInfo creates a new tokenInfo with the given name and PriceSync.
func NewTokenInfo(name string, price *PriceSync) *tokenInfo {
	return &tokenInfo{
		name:   name,
		price:  price,
		active: new(atomic.Bool),
	}
}

// GetPriceSync returns the PriceSync for the token.
func (t *tokenInfo) GetPriceSync() *PriceSync {
	return t.price
}

// GetPrice returns the current PriceInfo for the token.
func (t *tokenInfo) GetPrice() PriceInfo {
	return t.price.Get()
}

// GetActive returns whether the token is active.
func (t *tokenInfo) GetActive() bool {
	return t.active.Load()
}

// SetActive sets the active status for the token.
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
	fetch  SourceFetchFunc
	reload SourceReloadConfigFunc
	tokens map[string]*tokenInfo
	// tokensSnapshot   atomic.Value
	priceUpdate      *atomic.Bool
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
		priceUpdate:        new(atomic.Bool),
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
					}
				} else {
					// update price
					updated := token.price.Update(*price)
					if updated {
						s.priceUpdate.Store(true)
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

func (s *Source) PriceUpdate() bool {
	return s.priceUpdate.Load()
}

func (s *Source) ResetPriceUpdate() {
	s.priceUpdate.Store(false)
}

type NSTToken string

const (
	defaultPendingTokensLimit = 5
	defaultInterval           = 30 * time.Second
	// interval set for debug
	// defaultInterval = 5 * time.Second
	Chainlink    = "chainlink"
	BaseCurrency = "usdt"
	BeaconChain  = "beaconchain"
	Solana       = "solana"

	NativeTokenETH NSTToken = "nsteth"
	NativeTokenSOL NSTToken = "nstsol"

	DefaultSlotsPerEpoch = uint64(32)
)

var (
	NSTZeroChanges              = make([]byte, 0)
	EmptyRawDataChangesRootHash = sha256.Sum256([]byte{})
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

// SetNativeAssetID sets the asset ID for a given NSTToken.
func SetNativeAssetID(nstToken NSTToken, assetID string) {
	NSTAssetIDMap[nstToken] = assetID
}

// GetNSTSource returns the source name for a given NSTToken.
func GetNSTSource(nstToken NSTToken) string {
	return NSTSourceMap[nstToken]
}

// GetNSTAssetID returns the asset ID for a given NSTToken.
func GetNSTAssetID(nstToken NSTToken) string {
	return NSTAssetIDMap[nstToken]
}

// IsNSTToken checks if a token name is an NSTToken.
func IsNSTToken(tokenName string) bool {
	if _, ok := NSTTokens[NSTToken(tokenName)]; ok {
		return true
	}
	return false
}

type StakerInfo struct {
	Address         string
	Validators      []string
	Balance         uint64
	WithdrawVersion uint64
}

type WithdrawInfo struct {
	StakerIndex     uint32
	WithdrawVersion uint64
}

type DepositInfo struct {
	StakerIndex uint32
	StakerAddr  string
	Validator   string
	Amount      uint64
}

type StakerInfos map[uint32]*StakerInfo

// NewStakerInfo creates a new StakerInfo instance.
func NewStakerInfo() *StakerInfo {
	return &StakerInfo{
		Validators: make([]string, 0),
		Balance:    0,
	}
}

func (s *StakerInfo) RemoveValidator(validator string, balance uint64) bool {
	if s == nil {
		return false
	}
	if !slices.Contains(s.Validators, validator) {
		return false
	}
	if s.Balance < balance {
		return false
	}
	s.Balance -= balance
	s.Validators = slices.Delete(s.Validators, slices.Index(s.Validators, validator), slices.Index(s.Validators, validator)+1)
	return true
}

// AddValidator adds a validator and its balance to the StakerInfo if not already present.
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
	Locker          *sync.RWMutex
	Version         uint64
	WithdrawVersion uint64
	SInfos          StakerInfos
	// version -> {stakerAddr, validator, amount}
	SInfosAdd         map[uint64]*DepositInfo // StakerInfosAdd
	WithdrawInfos     []*WithdrawInfo         // WithdrawInfos
	prevWithdrawInfos map[uint32]uint64
}

// GetCopy returns a deep copy of the StakerInfos map.
func (sis StakerInfos) GetCopy() StakerInfos {
	ret := make(StakerInfos)
	for idx, si := range sis {
		ret[idx] = &StakerInfo{
			Address:         si.Address,
			Validators:      si.Validators,
			Balance:         si.Balance,
			WithdrawVersion: si.WithdrawVersion,
		}
	}
	return ret
}

// GetSVList returns a map of staker indices to their validator lists.
func (sis *StakerInfos) GetSVList() map[uint32][]string {
	ret := make(map[uint32][]string)
	for idx, si := range *sis {
		ret[idx] = si.Validators
	}
	return ret
}

// Add adds a StakerInfo to the StakerInfos for a given staker index.
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

// Remove removes a StakerInfo from the StakerInfos for a given staker index.
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

// AddAllOrNone adds all StakerInfos from sAdd, or none if any would conflict.
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

// RemoveAllOrNone removes all StakerInfos in sRemove, or none if any would fail.
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

// Update adds and removes StakerInfos in a single operation.
func (sis *StakerInfos) Update(sAdd, sRemove StakerInfos) error {
	if err := sis.AddAllOrNone(sAdd); err != nil {
		return err
	}
	if err := sis.RemoveAllOrNone(sRemove); err != nil {
		return err
	}
	return nil
}

// NewStakers creates a new Stakers instance.
func NewStakers() *Stakers {
	return &Stakers{
		Locker:        new(sync.RWMutex),
		SInfos:        make(StakerInfos),
		SInfosAdd:     make(map[uint64]*DepositInfo),
		WithdrawInfos: make([]*WithdrawInfo, 0),
	}
}

// Length returns the number of stakers.
func (s *Stakers) Length() int {
	if s == nil {
		return 0
	}
	s.Locker.RLock()
	l := len(s.SInfos)
	s.Locker.RUnlock()
	return l
}

// GetStakerInfos returns a copy of the staker infos and the current version.
func (s *Stakers) GetStakerInfos() (StakerInfos, uint64, uint64) {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.SInfos.GetCopy(), s.Version, s.WithdrawVersion
}

// GetStakersNoCopy returns the staker infos and version without copying (should not be modified).
func (s *Stakers) GetStakersNoCopy() (StakerInfos, uint64, uint64) {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.SInfos, s.Version, s.WithdrawVersion
}

// SetStakerInfos sets the staker infos and version, replacing the current data.
func (s *Stakers) SetStakerInfos(sInfos StakerInfos, version uint64) {
	s.Locker.Lock()
	s.SInfos = sInfos
	s.Version = version
	s.Locker.Unlock()
}

// GetStakerBalances returns a map of staker indices to their balances.
func (s *Stakers) GetStakerBalances() map[uint32]uint64 {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	ret := make(map[uint32]uint64)
	for idx, si := range s.SInfos {
		ret[idx] = si.Balance
	}
	return ret
}

// func (s *Stakers) CacheDeposits(deposits map[uint64]*DepositInfo, nextVersion uint64) error {
// CacheDeposits caches deposits for the next version, ensuring the version is continuous and not already existing.
// NOTE: we don't make sure all-or-nothing, dirty data caused by error should be handled by caller (currently caller will eventually do reset all)
func (s *Stakers) CacheDepositsWithdraws(deposits map[uint64]*DepositInfo, nextVersion uint64, withdraws []*WithdrawInfo) error {
	if len(deposits) > 0 && nextVersion == 0 {
		return fmt.Errorf("failed to cache deposits, nextVersion is 0")
	}
	if len(s.SInfosAdd) > 0 && (s.SInfosAdd[nextVersion-1] == nil || s.SInfosAdd[nextVersion] != nil) {
		return fmt.Errorf("failed to cache deposits, version not continuos or already exists, nextVersion:%d, no nextVersion-1:%t", nextVersion, s.SInfosAdd[nextVersion-1] == nil)
	}
	if len(s.SInfosAdd) > 0 && s.Version+1 != nextVersion {
		return fmt.Errorf("failed to cache deposits, version mismatch, current:%d, next:%d", s.Version, nextVersion)
	}

	for i := 0; i < len(deposits); i++ {
		v := nextVersion + uint64(i)
		deposit, exists := deposits[v]
		if !exists {
			return fmt.Errorf("failed to cache deposits, version not continuos, missed version:%d", nextVersion+uint64(i))
		}
		if s.SInfosAdd == nil {
			s.SInfosAdd = make(map[uint64]*DepositInfo)
		}
		if s.SInfosAdd[v] != nil {
			return fmt.Errorf("failed to cache deposits, version already exists, version:%d", v)
		}
		s.SInfosAdd[v] = &DepositInfo{
			StakerIndex: deposit.StakerIndex,
			StakerAddr:  deposit.StakerAddr,
			Validator:   deposit.Validator,
			Amount:      deposit.Amount,
		}
	}
	for _, withdraw := range withdraws {
		if withdraw.WithdrawVersion == 0 {
			return fmt.Errorf("failed to cache withdraws, withdraw version is 0")
		}
		if s.WithdrawVersion >= withdraw.WithdrawVersion {
			return fmt.Errorf("failed to cache withdraws, withdraw version already exists, current:%d, next:%d", s.WithdrawVersion, withdraw.WithdrawVersion)
		}
		if len(s.WithdrawInfos) > 0 && s.WithdrawInfos[len(s.WithdrawInfos)-1].WithdrawVersion+1 != withdraw.WithdrawVersion {
			return fmt.Errorf("failed to cache withdraws, withdraw version not continuos, current:%d, next:%d", s.WithdrawInfos[len(s.WithdrawInfos)-1].WithdrawVersion, withdraw.WithdrawVersion)
		}
		s.WithdrawInfos = append(s.WithdrawInfos, &WithdrawInfo{
			StakerIndex:     withdraw.StakerIndex,
			WithdrawVersion: withdraw.WithdrawVersion,
		})
	}
	return nil
}

// Update updates the staker infos by adding and removing, and sets the new version.
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

// tryGrowVersionsFromCache tries to grow the staker infos from the cache to a specific version: apply appending deposits to input_version
func (s *Stakers) tryGrowVersionsFromCache(version, withdrawVersion uint64, update bool) error {
	if version < s.Version || withdrawVersion < s.WithdrawVersion {
		return fmt.Errorf("grow to a history version, current:%d, next:%d", s.Version, version)
	}

	if version == s.Version && withdrawVersion == s.WithdrawVersion {
		if update {
			return fmt.Errorf("grow to a history version by deposit, current f:%d, fw:%d next f%d, fw:%d", s.Version, s.WithdrawVersion, version, withdrawVersion)
		} else {
			return nil
		}
	}

	if len(s.SInfosAdd) == 0 && version > s.Version {
		// TODO: do withdrawVersion
		return fmt.Errorf("failed to grow version, stakerInfosAdd is nil")
	}

	if len(s.WithdrawInfos) == 0 && withdrawVersion > s.WithdrawVersion {
		return fmt.Errorf("failed to grow version, withdrawInfos is empty, current:%d, next:%d", s.WithdrawVersion, withdrawVersion)
	}

	// feedVersion will at least grow by 2 or not grow at all
	// grow the version to the next version
	var err error
	i := s.Version + 1
	latestWithdrawVersion := s.WithdrawVersion
	defer func() {
		if err != nil {
			if i > 1 || latestWithdrawVersion > s.WithdrawVersion {
				s.downgradeVersion(i-1, latestWithdrawVersion)
			}
		}
	}()
	// handle deposits applying
	if version > s.Version {
		for ; i <= version; i++ {
			// grow the version
			deposit := s.SInfosAdd[i]
			if deposit == nil {
				err = fmt.Errorf("failed to grow version, stakerInfosAdd for version %d is nil", i)
				return err
			}
			sInfo, exists := s.SInfos[uint32(deposit.StakerIndex)]
			if !exists {
				if int(deposit.StakerIndex) != len(s.SInfos) {
					err = fmt.Errorf("failed to grow version, stakerIndex is not continuous, new staker-index:%d, current max-index:%d", deposit.StakerIndex, len(s.SInfos))
					return err
				}
				s.SInfos[uint32(deposit.StakerIndex)] = &StakerInfo{
					Address:    deposit.StakerAddr,
					Validators: []string{deposit.Validator},
					Balance:    deposit.Amount,
				}
			} else if !sInfo.AddValidator(deposit.Validator, deposit.Amount) {
				err = fmt.Errorf("failed to grow version, validatorIndex already exists, staker-index:%d, validator:%s", deposit.StakerIndex, deposit.Validator)
				return err
			}
		}
	}
	// handle withdraws applying
	if withdrawVersion > s.WithdrawVersion {
		if s.WithdrawInfos[len(s.WithdrawInfos)-1].WithdrawVersion < withdrawVersion {
			return fmt.Errorf("failed to grow version, withdrawInfos is not continuous, current:%d, next:%d", s.WithdrawVersion, withdrawVersion)
		}
		s.prevWithdrawInfos = make(map[uint32]uint64)
		for _, withdraw := range s.WithdrawInfos {
			if withdraw.WithdrawVersion > withdrawVersion {
				// we have reached the target withdraw version
				break
			}
			sInfo, exists := s.SInfos[withdraw.StakerIndex]
			if !exists {
				err = fmt.Errorf("failed to grow version, stakerIndex not found in stakerInfos, staker-index:%d", withdraw.StakerIndex)
				return err
			}
			latestWithdrawVersion = withdraw.WithdrawVersion
			// for same staker index, it could be changed multiple times, we only need to keep the original withdraw version
			if _, exist := s.prevWithdrawInfos[withdraw.StakerIndex]; !exist {
				s.prevWithdrawInfos[withdraw.StakerIndex] = sInfo.WithdrawVersion
			}
			sInfo.WithdrawVersion = withdraw.WithdrawVersion
		}
	}
	return nil
}

// GrowVersionsFromCacheByDeposit grows the staker infos from the cache to a specific version.
func (s *Stakers) GrowVersionsFromCacheByDepositWithdraw(version, withdrawVersion uint64) error {
	if version < 1 {
		return fmt.Errorf("version is less than 1, version:%d", version)
	}
	s.Locker.Lock()
	defer s.Locker.Unlock()

	if err := s.tryGrowVersionsFromCache(version, withdrawVersion, true); err != nil {
		return err
	}
	s.complete(version, withdrawVersion)
	s.Version = version
	s.WithdrawVersion = withdrawVersion
	return nil
}

func (s *Stakers) complete(version, withdrawVersion uint64) {
	for v := range s.SInfosAdd {
		if v <= version {
			delete(s.SInfosAdd, v)
		}
	}
	idx := 0
	for ; idx < len(s.WithdrawInfos); idx++ {
		if s.WithdrawInfos[idx].WithdrawVersion > withdrawVersion {
			break
		}
	}
	s.WithdrawInfos = s.WithdrawInfos[idx:]
	s.prevWithdrawInfos = make(map[uint32]uint64)
}

func (s *Stakers) downgradeVersion(version, _ uint64) {
	for i := version; i > s.Version; i-- {
		deposit := s.SInfosAdd[i]
		sInfo := s.SInfos[uint32(deposit.StakerIndex)]
		sInfo.RemoveValidator(deposit.Validator, deposit.Amount)
		s.Version--
	}
	for sIndex, sVersion := range s.prevWithdrawInfos {
		sInfo, exists := s.SInfos[sIndex]
		if !exists {
			continue
		}
		sInfo.WithdrawVersion = sVersion
	}
	s.prevWithdrawInfos = make(map[uint32]uint64)
}

// ApplyBalanceChanges applies balance changes from RawDataNST to the staker infos.
func (s *Stakers) ApplyBalanceChanges(changes *oracletypes.RawDataNST, version uint64, withdrawVersion uint64) (err error) {
	s.Locker.Lock()
	defer s.Locker.Unlock()
	// Check version match before applying changes
	if s.Version != changes.Version {
		return fmt.Errorf("version mismatch, current:%d, next:%d", s.Version, changes.Version)
	}
	// append cached validators from deposit
	if err = s.tryGrowVersionsFromCache(version, withdrawVersion, false); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if version > 1 || withdrawVersion > s.WithdrawVersion {
				s.downgradeVersion(version, withdrawVersion)
			}
		} else {
			s.complete(version, withdrawVersion)
		}
	}()
	// Prepare to track stakers to remove and those to update
	updated := make(map[uint32]*StakerInfo)
	for _, change := range changes.NstBalanceChanges {
		sInfo, exists := s.SInfos[uint32(change.StakerIndex)]
		if !exists {
			err = fmt.Errorf("stakerIndex not found, staker-index:%d", change.StakerIndex)
			return err
		}
		// staker info has been grown to the latest withdraw version, so we can campare here to decide whether to update
		if sInfo.WithdrawVersion > s.WithdrawVersion {
			// skip update
			continue
		}
		// Prepare an updated copy of the staker info with the new balance
		tmp := *sInfo // shallow copy is safe since Validators is not changed
		tmp.Balance = change.Balance
		updated[uint32(change.StakerIndex)] = &tmp
	}

	// Apply all balance updates
	for sIdx, sInfo := range updated {
		s.SInfos[sIdx] = sInfo
	}
	// Increment the version after successful application
	s.Version = version
	s.WithdrawVersion = withdrawVersion
	return nil
}

// Reset resets the staker infos to the provided list and version.
func (s *Stakers) Reset(sInfos []*oracletypes.StakerInfo, version *oracletypes.NSTVersion, all bool) error {
	s.Locker.Lock()
	defer s.Locker.Unlock()
	if !all && s.Version+1 != version.FeedVersion.Version {
		return fmt.Errorf("version mismatch, current:%d, next:%d", s.Version, version.FeedVersion.Version)
	}
	tmp := make(StakerInfos)
	deposits := make(map[uint64]*DepositInfo)
	withdraws := make([]*WithdrawInfo, 0, len(sInfos))
	seenStakerIdx := make(map[uint64]struct{})
	for _, sInfo := range sInfos {
		if _, ok := seenStakerIdx[uint64(sInfo.StakerIndex)]; ok {
			return fmt.Errorf("duplicated stakerIndex, staker-index:%d", sInfo.StakerIndex)
		}
		seenStakerIdx[uint64(sInfo.StakerIndex)] = struct{}{}

		// we don't limit the staker size here, it's guaranteed by imuachain
		validators := make([]string, 0, len(sInfo.ValidatorList))
		seenValidatorIdx := make(map[string]struct{})
		pendingDepositAmount := uint64(0)
		for _, validator := range sInfo.ValidatorList {
			if _, ok := seenValidatorIdx[validator.ValidatorPubkey]; ok {
				return fmt.Errorf("duplicated validatorIndex, validator-index-hex:%s", validator)
			}
			if validator.Version <= version.FeedVersion.Version {
				validators = append(validators, validator.ValidatorPubkey)
			} else {
				deposits[validator.Version] = &DepositInfo{
					StakerIndex: sInfo.StakerIndex,
					StakerAddr:  sInfo.StakerAddr,
					Validator:   validator.ValidatorPubkey,
					Amount:      validator.DepositAmount,
				}
				pendingDepositAmount += validator.DepositAmount
			}
			seenValidatorIdx[validator.ValidatorPubkey] = struct{}{}
		}
		balance := uint64(0)
		l := len(sInfo.BalanceList)
		if l > 0 && sInfo.BalanceList[l-1] != nil && sInfo.BalanceList[l-1].Balance > 0 {
			balance = uint64(sInfo.BalanceList[l-1].Balance)
		}
		if balance < pendingDepositAmount {
			return fmt.Errorf("balance is less than pending deposit amount, staker-index:%d, balance:%d, pending-deposit-amount:%d", sInfo.StakerIndex, balance, pendingDepositAmount)
		}
		if len(validators) > 0 {
			// so we actully might include withdrawVersion > feedWithdrawVersion as 'current snapshot' info, but it's fine for withdraw
			tmp[uint32(sInfo.StakerIndex)] = &StakerInfo{
				Address:         sInfo.StakerAddr,
				Validators:      validators,
				Balance:         balance - pendingDepositAmount,
				WithdrawVersion: sInfo.WithdrawVersion,
			}
		}
		// we might duplicated withdrawVersion, but it's fine to apply again with the same withdrawVersion when we do growVersion later
		if sInfo.WithdrawVersion > version.FeedWithdrawVersion {
			withdraws = append(withdraws, &WithdrawInfo{
				StakerIndex:     sInfo.StakerIndex,
				WithdrawVersion: sInfo.WithdrawVersion,
			})
		}
	}
	// sort withdraws by WithdrawVersion
	sort.Slice(withdraws, func(i, j int) bool {
		return withdraws[i].WithdrawVersion < withdraws[j].WithdrawVersion
	})
	if all {
		s.SInfos = tmp
		s.SInfosAdd = deposits
		s.WithdrawInfos = withdraws
	} else {
		for k, v := range tmp {
			s.SInfos[k] = v
		}
		for k, v := range deposits {
			s.SInfosAdd[k] = v
		}
		// append withdraws
		s.WithdrawInfos = append(s.WithdrawInfos, s.WithdrawInfos...)
	}
	s.Version = version.FeedVersion.Version
	s.WithdrawVersion = version.FeedWithdrawVersion
	return nil
}

// ConvertHexToIntStr converts a hex string to its integer string representation.
func ConvertHexToIntStr(hexStr string) (string, error) {
	vBytes, err := hexutil.Decode(hexStr)
	if err != nil {
		return "", err
	}
	return new(big.Int).SetBytes(vBytes).String(), nil
}

// ConvertBytesToIntStr converts a byte string to its integer string representation.
func ConvertBytesToIntStr(bytesStr string) string {
	return new(big.Int).SetBytes([]byte(bytesStr)).String()
}

func IsContractAddress(addr string, client *ethclient.Client, logger feedertypes.LoggerInf) bool {
	if len(addr) == 0 {
		logger.Error("contract address is empty")
		return false
	}

	// Ensure it is an Ethereum address: 0x followed by 40 hexadecimal characters.
	re := regexp.MustCompile("^0x[0-9a-fA-F]{40}$")
	if !re.MatchString(addr) {
		logger.Error(" contract address is not valid", "address", addr)
		return false
	}

	// Ensure it is a contract address.
	address := common.HexToAddress(addr)
	bytecode, err := client.CodeAt(context.Background(), address, nil)
	if err != nil {
		logger.Error("failed to get code at contract address", "address", address, "error", err)
		return false
	}
	return len(bytecode) > 0
}
