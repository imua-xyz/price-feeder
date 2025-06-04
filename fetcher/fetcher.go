package fetcher

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const (
	loggerTag       = "fetcher"
	loggerTagPrefix = "fetcher_%s"

	snapshotInterval = 30 * time.Second
)

var (
	logger feedertypes.LoggerInf

	defaultFetcher *Fetcher
)

type Fetcher struct {
	logger  feedertypes.LoggerInf
	locker  *sync.Mutex
	running bool
	sources map[string]types.SourceInf
	// source->map{token->price}
	priceReadList   map[string]map[string]*types.PriceSync
	tokensStatus    atomic.Value
	addSourceToken  chan *addTokenForSourceReq
	getLatestPrice  chan *getLatestPriceReq
	setNSTStakersCh chan *setNSTStakersReq
	stop            chan struct{}
}

type addTokenForSourceReq struct {
	source string
	token  string
	result chan bool
}

type getLatestPriceReq struct {
	source string
	token  string
	result chan *getLatestPriceRes
}

type getLatestPriceRes struct {
	price types.PriceInfo
	err   error
}

type setNSTStakersReq struct {
	sourceName string
	sInfos     types.StakerInfos
	version    uint64
}

func newGetLatestPriceReq(source, token string) (*getLatestPriceReq, chan *getLatestPriceRes) {
	res := make(chan *getLatestPriceRes, 1)
	return &getLatestPriceReq{source: source, token: token, result: res}, res
}

func NewFetcher(logger feedertypes.LoggerInf, sources map[string]types.SourceInf) *Fetcher {
	return &Fetcher{
		logger:         logger,
		locker:         new(sync.Mutex),
		sources:        sources,
		priceReadList:  make(map[string]map[string]*types.PriceSync),
		addSourceToken: make(chan *addTokenForSourceReq, 5),
		// getLatestPrice: make(chan *getLatestPriceReq),
		getLatestPrice:  make(chan *getLatestPriceReq, 5),
		setNSTStakersCh: make(chan *setNSTStakersReq, 10),
		stop:            make(chan struct{}),
	}
}

// AddTokenForSource adds token for existing source
// blocked waiting for the result to return
func (f *Fetcher) AddTokenForSource(source, token string) bool {
	res := make(chan bool, 1)
	f.addSourceToken <- &addTokenForSourceReq{
		source: source,
		token:  token,
		result: res,
	}
	return <-res
}

// TODO::
func (f *Fetcher) AddTokenForSourceUnBlocked(source, token string) {
	res := make(chan bool)
	f.addSourceToken <- &addTokenForSourceReq{
		source: source,
		token:  token,
		result: res,
	}
}

func (f *Fetcher) SetNSTStakers(sourceName string, sInfos types.StakerInfos, version uint64) {
	f.setNSTStakersCh <- &setNSTStakersReq{sourceName: sourceName, sInfos: sInfos, version: version}
}

// GetLatestPrice return the queried price for the token from specified source
// blocked waiting for the result to return
func (f *Fetcher) GetLatestPrice(source, token string) (types.PriceInfo, error) {
	req, res := newGetLatestPriceReq(source, token)
	f.getLatestPrice <- req
	result := <-res
	return result.price, result.err
}

func (f *Fetcher) Start() error {
	f.locker.Lock()
	if f.running {
		f.locker.Unlock()
		return errors.New("failed to start fetcher which is already running")
	}
	if len(f.sources) == 0 {
		f.locker.Unlock()
		return errors.New("failed to start fetcher with no sources set")
	}
	priceList := make(map[string]map[string]*types.PriceSync)
	for sName, source := range f.sources {
		f.logger.Info("start source", "source", sName)
		prices := source.Start()
		priceList[sName] = prices
	}
	f.priceReadList = priceList
	f.running = true
	f.locker.Unlock()

	go func() {
		const timeout = 10 * time.Second
		tic := time.NewTicker(snapshotInterval)
		for {
			select {
			case <-tic.C:
				cpy := make(map[string]map[string]*types.TokenStatus)
				for sName, source := range f.sources {
					status := source.Status()
					cpy[sName] = status
				}
				f.tokensStatus.Store(cpy)

				// for sName, source := range f.priceReadList {
				// 	if !f.sources[sName].PriceUpdate() {
				// 		continue
				// 	}
				// 	for tName, price := range source {
				// 		if price == nil {
				// 			f.logger.Error("price is nil", "source", sName, "token", tName)
				// 			continue
				// 		}
				// 		p := price.Get()
				// 		cpy[sName][tName] = &types.TokenStatus{
				// 			Name:   tName,
				// 			Price:  p,
				// 			Active: true,
				// 		}
				// 	}
				// }
				f.tokensStatus.Store(cpy)
			case req := <-f.addSourceToken:
				timer := time.NewTimer(timeout)
				select {
				case <-timer.C:
					req.result <- false
					f.logger.Error("timeout while adding token", "source", req.source, "token", req.token)
				default:
					// it's safe to add one token multiple times
					if source, ok := f.sources[req.source]; ok {
						if res := source.AddTokenAndStart(req.token); res.Error() != nil {
							// TODO: clean logs
							f.logger.Error("failed to AddTokenAndStart", "source", source.GetName(), "token", req.token, "error", res.Error())
							req.result <- false
						} else {
							f.priceReadList[req.source][req.token] = res.Price()
							req.result <- true
						}
					} else {
						// we don't support adding source dynamically
						f.logger.Error("failed to add token for a nonexistent soruce", "source", req.source, "token", req.token)
						req.result <- false
					}
				}
			case req := <-f.getLatestPrice:
				timer := time.NewTimer(timeout)
				select {
				case <-timer.C:
					req.result <- &getLatestPriceRes{
						price: types.PriceInfo{},
						err:   fmt.Errorf("timeout while getting price for token %s from source %s", req.token, req.source),
					}
				default:
					if s := f.priceReadList[req.source]; s == nil {
						req.result <- &getLatestPriceRes{
							price: types.PriceInfo{},
							err:   fmt.Errorf("failed to get price of token:%s from a nonexistent source:%s", req.token, req.source),
						}
					} else if price := s[req.token]; price == nil {
						req.result <- &getLatestPriceRes{
							price: types.PriceInfo{},
							err:   feedertypes.ErrSourceTokenNotConfigured.Wrap(fmt.Sprintf("failed to get price of token:%s from a nonexistent token from an existing source:%s", req.token, req.source)),
						}
					} else {
						req.result <- &getLatestPriceRes{
							price: price.Get(),
							err:   nil,
						}
					}
				}
			case req := <-f.setNSTStakersCh:
				timer := time.NewTimer(timeout)
				select {
				case <-timer.C:
					f.logger.Error("timeout while setting NST stakers", "version", req.version)
				default:
					s, ok := f.sources[req.sourceName]
					if !ok {
						f.logger.Error("failed to set NST stakers for a nonexistent source", "source", req.sourceName)
					}
					sNST, ok := s.(types.SourceNSTInf)
					if !ok {
						f.logger.Error("failed to set NST stakers for a source which doesn't support NST stakers", "source", req.sourceName)
					}
					sNST.SetNSTStakers(req.sInfos, req.version)
				}
			case <-f.stop:
				f.locker.Lock()
				for _, source := range f.sources {
					source.Stop()
				}
				f.running = false
				f.locker.Unlock()
				return
			}
		}
	}()
	return nil
}

func (f Fetcher) Stop() {
	f.locker.Lock()
	select {
	case _, ok := <-f.stop:
		if ok {
			close(f.stop)
		}
	default:
		close(f.stop)
	}
	f.locker.Unlock()
}

// Init initializes the fetcher with sources and tokens
func Init(tokenSources []feedertypes.TokenSources, sourcesPath string) error {
	if logger = feedertypes.GetLogger(loggerTag); logger == nil {
		panic("logger is not initialized")
	}

	sources := make(map[string]types.SourceInf)
	sourceTokens := make(map[string][]string)
	for _, ts := range tokenSources {
		sNames := strings.Split(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, ts.Sources), ",")

		var err error
		// add sources with names
		for _, sName := range sNames {
			source := sources[sName]
			// new a source if not exists
			if source == nil {
				l := feedertypes.GetLogger(fmt.Sprintf(loggerTagPrefix, sName))
				source, err = types.SourceInitializers[sName](sourcesPath, l)
				if err != nil {
					return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to init source:%s, sources_config_path:%s, error:%v", sName, sourcesPath, err))
				}
				sources[sName] = source
			}
			sourceTokens[sName] = append(sourceTokens[sName], ts.Token)
		}
	}
	// setup tokens for sources
	for sName, tokens := range sourceTokens {
		sources[sName].InitTokens(tokens)
	}

	defaultFetcher = NewFetcher(logger, sources)
	return nil
}

func GetFetcher() (*Fetcher, bool) {
	if defaultFetcher == nil {
		return nil, false
	}
	return defaultFetcher, true
}
