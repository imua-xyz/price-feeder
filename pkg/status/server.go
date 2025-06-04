package status

import (
    "context"
    "sync"
    "time"

    pb "github.com/imua-xyz/price-feeder/proto/query/types/feeder"
)

type PriceInfo struct {
    FeederID  string
    Price     string
    UpdatedAt time.Time
}

type StatusServer struct {
    pb.UnimplementedFeederStatusServer

    mu     sync.RWMutex
    alive  bool
    prices map[string]PriceInfo
}

func NewStatusServer() *StatusServer {
    return &StatusServer{
        alive:  true,
        prices: make(map[string]PriceInfo),
    }
}

func (s *StatusServer) SetAlive(v bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.alive = v
}

func (s *StatusServer) UpdatePrice(info PriceInfo) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.prices[info.FeederID] = info
}

// === gRPC Methods ===

func (s *StatusServer) HealthCheck(ctx context.Context, _ *pb.Empty) (*pb.HealthResponse, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return &pb.HealthResponse{Alive: s.alive}, nil
}

func (s *StatusServer) GetAllPrices(ctx context.Context, _ *pb.Empty) (*pb.PriceList, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    var list []*pb.PriceInfo
    for _, p := range s.prices {
        list = append(list, &pb.PriceInfo{
            FeederId:  p.FeederID,
            Price:     p.Price,
            UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
        })
    }
    return &pb.PriceList{Prices: list}, nil
}

func (s *StatusServer) GetPrice(ctx context.Context, req *pb.FeederIDRequest) (*pb.PriceInfo, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    p, ok := s.prices[req.FeederId]
    if !ok {
        return nil, fmt.Errorf("feeder %s not found", req.FeederId)
    }
    return &pb.PriceInfo{
        FeederId:  p.FeederID,
        Price:     p.Price,
        UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
    }, nil
}}
