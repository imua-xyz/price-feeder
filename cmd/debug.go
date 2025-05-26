package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/imua-xyz/price-feeder/debugger"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	flagFeederID = "feederID"
	flagHeight   = "height"
)

func init() {
	debugStartCmd.PersistentFlags().Uint64(flagFeederID, 0, "feederID of the token")
	debugStartCmd.PersistentFlags().Int64(flagHeight, 0, "committed block height after which the tx will be sent")

	debugStartCmd.AddCommand(
		debugSendCmd,
		debugSendImmCmd,
	)
}

var debugStartCmd = &cobra.Command{
	Use:   "debug",
	Short: "start listening to new blocks",
	Long:  "start listening to new blocks",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := feedertypes.NewLogger(feedertypes.LogConf{Level: "debug"})
		DebugPriceFeeder(feederConfig, logger, mnemonic, sourcesPath)
		return nil
	},
}

var debugSendCmd = &cobra.Command{
	Use:   `send --feederID [feederID] --height [height] [{"baseblock":1,"nonce":1,"price":"999","det_id":"123","decimal":8,"timestamp":"2006-01-02 15:16:17"}]`,
	Short: "send a create-price message to imuad",
	Long:  "Send a create-price message to imuad, the flag -h is optional. The tx will be sent immediately if that value is not set.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		feederID, err := cmd.Parent().PersistentFlags().GetUint64(flagFeederID)
		if err != nil {
			return err
		}
		height, err := cmd.Parent().PersistentFlags().GetInt64(flagHeight)
		if err != nil {
			return err
		}
		msgStr := args[0]
		msgPrice := &debugger.PriceMsg{}
		if err := json.Unmarshal([]byte(msgStr), msgPrice); err != nil {
			return err
		}
		res, err := sendTx(feederID, height, msgPrice, feederConfig.Debugger.Grpc)
		if err != nil {
			return err
		}
		if len(res.Err) > 0 {
			fmt.Println("")
		}
		printProto(res)
		return nil
	},
}

var debugSendImmCmd = &cobra.Command{
	Use:   `send-imm --feederID [feederID] [{"baseblock":1,"nonce":1,"price":"999","det_id":"123","decimal":8,"timestamp":"2006-01-02 15:16:17"}]`,
	Short: "send a create-price message to imuad",
	Long:  "Send a create-price message to imuad, the flag -h is optional. The tx will be sent immediately if that value is not set.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		feederID, err := cmd.Parent().PersistentFlags().GetUint64(flagFeederID)
		if err != nil {
			return err
		}
		msgStr := args[0]
		msgPrice := &PriceJSON{}
		if err := json.Unmarshal([]byte(msgStr), msgPrice); err != nil {
			return err
		}
		if err := msgPrice.validate(); err != nil {
			return err
		}
		res, err := sendTxImmediately(feederID, msgPrice)
		if err != nil {
			return err
		}
		printProto(res)
		return nil
	},
}

func printProto(m proto.Message) {
	marshaled, err := protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}.Marshal(m)
	if err != nil {
		fmt.Printf("failed to print proto message, error:%v", err)
	}
	fmt.Println(string(marshaled))
}
