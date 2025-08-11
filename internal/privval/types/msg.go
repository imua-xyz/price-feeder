// package: github.com/okex/exchain/libs/tendermint/privval/types
package types

func MustWrapMsg(msg any) (re OracleStreamMessage) {
	switch m := msg.(type) {
	case *PriceFeedRequest:
		re = OracleStreamMessage{
			Sum: &OracleStreamMessage_PriceFeedRequest{
				PriceFeedRequest: m,
			},
		}
	case *PriceFeedResponse:
		re = OracleStreamMessage{
			Sum: &OracleStreamMessage_PriceFeedResponse{
				PriceFeedResponse: m,
			},
		}
	case *Ping:
		re = OracleStreamMessage{
			Sum: &OracleStreamMessage_Ping{
				Ping: m,
			},
		}
	case *Pong:
		re = OracleStreamMessage{
			Sum: &OracleStreamMessage_Pong{
				Pong: m,
			},
		}
	default:
		// log unknown message type
	}
	return
}
