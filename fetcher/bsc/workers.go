package bsc

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type jobKind int

const (
	jobKindBatchCapsules jobKind = iota

// maxConcurrency = 256
// // max concurrency set from config
// concurrency = 0
)

type workerLevel struct {
	workers int
	base    int // base threshold for initial assignment
	up      int // threshold to scale up from this level
	down    int // threshold to scale down to this level
}

var workerLevels = []workerLevel{
	{10, 2000, 2100, 1900},
	{30, 6000, 6100, 5900},
	{50, 10000, 10100, 9900},
	{70, 0, 0, 0}, // no upper limit
}

type scannerInf interface {
	batchQueryCapsules(ctx context.Context, capsules []common.Address, blockNumber *big.Int) (map[common.Address]*big.Int, error)
}

type job struct {
	kind       jobKind
	block      uint64
	capsules   []common.Address
	resultCh   chan result
}

type result struct {
	amounts map[common.Address]*big.Int
	// amounts     map[common.Address]uint64
	err error
	// needRefresh []common.Address
}

// TODO: Since updating delegator balances is mostly IO-bound, a fixed number of workers is used. A semaphore limits the concurrency level, avoiding dynamic worker count changes.
type workerPool struct {
	resizing atomic.Bool
	//	lock    sync.Mutex
	scanner scannerInf
	ctx     context.Context
	cancel  context.CancelFunc
	//	wg            sync.WaitGroup
	jobs chan job
	// results     chan result
	active      atomic.Int32
	stopWorkers chan chan struct{}
	// confirmedStop chan struct{}
}

type workerPoolConfig struct {
	numWorkers int
	jobBuffer  int
}

// startNewWorkerPool starts a new worker pool with the given number of workers
func startNewWorkerPool(scanner scannerInf, cfg workerPoolConfig) *workerPool {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &workerPool{
		jobs:        make(chan job, cfg.jobBuffer),
		scanner:     scanner,
		ctx:         ctx,
		cancel:      cancel,
		stopWorkers: make(chan chan struct{}, cfg.numWorkers),
	}
	//	for i := 0; i < cfg.numWorkers; i++ {
	//		pool.spawnWorker()
	//	}
	if !pool.start(cfg.numWorkers) {
		// TODO: handle error (safe for now since the wokerpool is set up on beginning of the program)
		panic("failed to start worker pool")
	}
	return pool
}

func (wp *workerPool) start(num int) bool {
	// avoid resizing during start/spawning
	if !wp.resizing.CompareAndSwap(false, true) {
		return false
	}
	for i := 0; i < num; i++ {
		wp.spawnWorker()
	}
	wp.resizing.Store(false)
	return true
}

func (wp *workerPool) stop() {
	wp.resize(0)
}

func (wp *workerPool) spawnWorker() {
	wp.active.Add(1)
	go func() {
		defer wp.active.Add(-1)
		for {
			select {
			case j := <-wp.jobs:
				ctx, cancel := context.WithTimeout(wp.ctx, 5*time.Second)
				res := wp.handleJob(ctx, j)
				cancel()
				j.resultCh <- res
			case ack := <-wp.stopWorkers:
				ack <- struct{}{}
				return
			}
		}
	}()
}

// handleJob processes a job in the worker goroutine
func (wp *workerPool) handleJob(ctx context.Context, j job) result {
	switch j.kind {
	case jobKindBatchCapsules:
		blockNumber := new(big.Int).SetUint64(j.block)
		amounts, err := wp.scanner.batchQueryCapsules(ctx, j.capsules, blockNumber)
		return result{
			amounts: amounts,
			err:     err,
		}
	default:
		return result{
			err: errInvalidJob,
		}
	}
}

// resize adjusts the number of workers based on job count with hysteresis buffer
// If numJobs is 0, it will stop all workers
func (wp *workerPool) resize(numJobs int) bool {
	if !wp.resizing.CompareAndSwap(false, true) {
		return false
	}
	defer wp.resizing.Store(false)

	var targetWorkers int
	if numJobs == 0 {
		targetWorkers = 0
	} else {
		targetWorkers = wp.calculateTargetWorkers(numJobs)
	}

	currentWorkers := int(wp.active.Load())
	delta := targetWorkers - currentWorkers
	switch {
	case delta == 0:
		// return true
		// spaw new active workers
	case delta > 0:
		for delta > 0 {
			wp.spawnWorker()
			delta--
		}
		// stop some active workers to reduce the count
	case delta < 0:
		delta *= -1
		ack := make(chan struct{}, delta)
		for i := 0; i < delta; i++ {
			wp.stopWorkers <- ack
		}
		for range ack {
			delta--
			if delta <= 0 {
				close(ack)
				// return true
				break
			}
		}
	}
	return true
}

// calculateTargetWorkers determines the target worker count based on current state and job count
// Uses hysteresis buffer to prevent frequent resizing
func (wp *workerPool) calculateTargetWorkers(numJobs int) int {
	currentWorkers := int(wp.active.Load())

	// Find current level index
	currentIdx := -1
	for i, l := range workerLevels {
		if l.workers == currentWorkers {
			currentIdx = i
			break
		}
	}

	// Unknown state: find appropriate level from job count (small to large)
	if currentIdx == -1 {
		for i := 0; i < len(workerLevels)-1; i++ {
			if numJobs <= workerLevels[i].base {
				return workerLevels[i].workers
			}
		}
		return workerLevels[len(workerLevels)-1].workers
	}

	// Scale up: from level i to i+1, need numJobs > workerLevels[i].up
	targetIdx := currentIdx
	for i := currentIdx; i < len(workerLevels)-1 && numJobs > workerLevels[i].up; i++ {
		targetIdx = i + 1
	}
	if targetIdx > currentIdx {
		return workerLevels[targetIdx].workers
	}

	// Scale down: from level i to i-1, need numJobs < workerLevels[i-1].down
	for i := currentIdx - 1; i >= 0 && numJobs < workerLevels[i].down; i-- {
		targetIdx = i
	}
	if targetIdx < currentIdx {
		return workerLevels[targetIdx].workers
	}

	// Stay at current level
	return currentWorkers
}

func (wp *workerPool) runBatch(ctx context.Context, jobs []job) (map[common.Address]uint64, error) {
	if len(jobs) == 0 {
		return map[common.Address]uint64{}, nil
	}

	// Dynamically adjust worker count based on job count
	wp.resize(len(jobs))

	var cancel context.CancelFunc
	wp.ctx, cancel = context.WithTimeout(wp.ctx, 20*time.Second)
	defer cancel()
	batchResultCh := make(chan result, len(jobs))
	go func() {
		for _, j := range jobs {
			j.resultCh = batchResultCh
			select {
			case <-ctx.Done():
				// return nil, ctx.Err()
				return
			case wp.jobs <- j:
			}
		}
	}()
	var firstErr error
	res := make(map[common.Address]uint64)
	for i := 0; i < len(jobs); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-batchResultCh:
			// Check for errors and ensure we continue processing other results for successful jobs
			if r.err != nil {
				if firstErr == nil {
					firstErr = r.err
				}
				// If there was an error for this item, do not add its amounts and proceed to next result
				continue
			}
			for d, amt := range r.amounts {
				if amt == nil || amt.Sign() <= 0 {
					res[d] = 0
					continue
				}
				amt = amt.Div(amt, divisor)
				if !amt.IsUint64() {
					return nil, fmt.Errorf("total balance exceeds uint64 max for delegator %s: %s", d.Hex(), amt.String())
				}
				res[d] = amt.Uint64()
			}
		}
	}
	close(batchResultCh)
	return res, firstErr
}
