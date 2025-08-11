// Package privval is used to define the interface for a private validator.
// Which is responsible for signing oracle feeding messages
package privval

import (
	"time"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/imua-xyz/price-feeder/internal/privval/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"github.com/rs/zerolog/log"
)

const (
	maxWaiters       = 1000
	defaultRWTimeout = 5 * time.Second
	pingTimeout      = 10 * time.Second
	signatureTimeout = 3 * time.Second
)

var logger = log.Logger

type PrivValidator interface {
	SignRawDataSync(rawMessage []byte) (singature []byte, err error)
	Init()
	GetPubKey() cryptotypes.PubKey
}

type sendResp struct {
	id      uint64
	written int
	result  <-chan []byte
	err     error
}

type sendObj struct {
	payload []byte
	res     chan sendResp
}

type sendStreamObj struct {
	payload *types.OracleStreamMessage
	res     chan sendResp
}

func GetPrivValidator(conf *feedertypes.Config, logger feedertypes.LoggerInf) (pv PrivValidator, err error) {
	senderConf := conf.Sender
	if len(senderConf.PrivListenAddr) > 0 {
		pv, err = NewPrivValidatorImplRemote(senderConf.PrivListenAddr)
	} else {
		pv, err = NewPrivValidatorImplLocal(conf, logger)
	}
	if err == nil {
		pv.Init()
	}
	return pv, err
}
