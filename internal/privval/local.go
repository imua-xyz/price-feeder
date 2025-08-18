package privval

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"

	cryptoed25519 "crypto/ed25519"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/go-bip39"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

type PrivValidatorImplLocal struct {
	privKey cryptotypes.PrivKey
}

var _ PrivValidator = (*PrivValidatorImplLocal)(nil)

const defaultPrivFile = "priv_validator_key.json"

func (pv *PrivValidatorImplLocal) SignRawDataSync(rawMessage []byte) (singature []byte, err error) {
	return pv.privKey.Sign(rawMessage)
}

// NewPrivValidatorImplLocal creates a new instance of PrivValidatorImplLocal.
func NewPrivValidatorImplLocal(conf *feedertypes.Config, logger feedertypes.LoggerInf) (pv *PrivValidatorImplLocal, err error) {
	confSender := conf.Sender
	privBase64 := ""

	mnemonic := confSender.Mnemonic
	privFile := confSender.PrivFile
	if len(privFile) == 0 {
		privFile = defaultPrivFile
	}
	if len(mnemonic) == 0 {
		// load privatekey from local path
		var file *os.File
		file, err = os.Open(path.Join(confSender.Path, privFile))
		if err != nil {
			// return feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to open consensuskey file, %v", err))
			err = feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to open consensuskey file, path:%s, privFile:%s, error:%v", confSender.Path, privFile, err))
			return
		}
		defer file.Close()
		var privKey feedertypes.PrivValidatorKey
		if err = json.NewDecoder(file).Decode(&privKey); err != nil {
			err = feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse consensuskey from json file, file path:%s,  error:%v", privFile, err))
			return
		}
		logger.Info("load privatekey from local file", "path", privFile)
		privBase64 = privKey.PrivKey.Value
	} else if !bip39.IsMnemonicValid(mnemonic) {
		err = feedertypes.ErrInitFail.Wrap(fmt.Sprintf("invalid mnemonic:%s", mnemonic))
		return
	}
	var privKey cryptotypes.PrivKey
	if len(mnemonic) > 0 {
		privKey = ed25519.GenPrivKeyFromSecret([]byte(mnemonic))
	} else {
		var privBytes []byte
		privBytes, err = base64.StdEncoding.DecodeString(privBase64)
		if err != nil {
			err = feedertypes.ErrInitFail.Wrap(fmt.Sprintf("failed to parse privatekey from base64_string:%s, error:%v", privBase64, err))
			return
		}
		//nolint:all
		privKey = &ed25519.PrivKey{
			Key: cryptoed25519.PrivateKey(privBytes),
		}
	}
	pv = &PrivValidatorImplLocal{
		privKey: privKey,
	}
	return pv, nil
}

func (pv *PrivValidatorImplLocal) Init() {}

func (pv *PrivValidatorImplLocal) GetPubKey() (cryptotypes.PubKey, error) {
	if pv.privKey == nil {
		return nil, errors.New("privKey is nil")
	}
	return pv.privKey.PubKey(), nil
}
