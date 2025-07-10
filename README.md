# Price Feeder

# Overview

The price feeder is a tool that validators use to submit oracle prices to Imua nodes.

This tool fetches token prices from off-chain sources such as `CEX` and `DEX`, then submits this price information to the Imua blockchain network as a standard Cosmos SDK transaction. Once the submitted prices from validators reach the voting power threshold, they achieve consensus on-chain and become the final price.

## Workflow

1. Subscribe to a new block.
2. Retrieve prices from off-chain sources.
3. Submit a transaction with the price data to update the oracle price on-chain.

# Installation

`make install`

Or check out the latest release.

# Quick Start
## Start
`pricefeeder --config path/to/config/config.yaml --sources_path path/to/sources/config start -m [mnemonic of validator's consensus key]`

This command starts a process that continuously fetches price information from sources. It monitors the height changes of imuachain and sends price quote transactions to the imua blockchain during quote windows, according to the oracle parameter settings.

### Sample Config
#### config.yaml (path/to/config/config.yaml)
```
tokens:
  - token: ETHUSDT
    sources: chainlink
  - token: NSTETH
    sources: beaconchain
  - token: TESTTOKEN
    sources: source1, source2, source3
sender:
  mnemonic: "wonder myself quality resource ketchup occur stadium shove vicious situate plug second soccer monkey harbor output vanish then primary feed earth story real like"
  path: /Users/xx/.tmp-imuad/config
imua:
  chainid: imuachaintestnet_233-1
  appName: imua
  grpc: 127.0.0.1:9090
  ws: !!str ws://127.0.0.1:26657/websocket
  rpc: !!str http://127.0.0.1:26657
debugger:
  grpc: !!str :50051
status:
  grpc: 50052
log:
  level: info
  maxsize: 100
  maxbackups: 10
  maxage: 30
  compress: true
```
For mnemonic used by validator to sign transactions (which should be ed25519 private-key corresponding to validator's consensus key), the priority is:
1. cli: --mnemonic flag
2. config file: sender.mnemonic
3. config file: search priv_validator_key.json from sender.path

#### config for sources:
- oracle_env_chainlink.yaml (path/to/sources/config/oracle_env_chainlink.yaml)
```
urls:
  mainnet: !!str https://eth-mainnet.g.alchemy.com/v2/Gnp7OQDBAH0hkdE7AKsSyZQ_bfF5oGQY
  sepolia: !!str https://eth-sepolia.g.alchemy.com/v2/Ru0VLnIJ9Raw_MHCIM_mn0Bl036n4Uhg
tokens:
  ETHUSDT: 0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419_mainnet
  AAVEUSDT: 0x547a514d5e3769680Ce22B2361c10Ea13619e8a9_mainnet
  AMPLUSDT: 0xe20CA8D7546932360e37E9D72c1a47334af57706_mainnet
  WSTETHUSDT: 0xaaabb530434B0EeAAc9A42E25dbC6A22D7bE218E_sepolia
  STETHUSDT: 0xCfE54B5cD566aB89272946F602D76Ea879CAb4a8_mainnet
```
- oracle_env_beaconchain.yaml (path/to/sources/config/oracle_env_beaconchain.yaml
```
urls:
  beaconchain: !!str https://rpc.ankr.com/premium-http/eth_beacon/{$ANKR_API_KEY}
  eth: !!str https://rpc.ankr.com/eth_holesky/{$ANKR_API_KEY}
nstid:
  !!str 0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee_0x65
bootstrap: !!str 0x38674073a3713dd2C46892f1d2C5Dadc5Bb14172
```
## Debug
We provide command-line tools for sending price quote transactions manually through an interactive interface
### send tx immedidately
`pricefeeder --config path/to/config/config.yaml debug send-imm --feederID [1] '{"base_block":160,"nonce":2,"price":"99907000000","det_id":"123","decimal":8,"timestamp":"2024-12-27 15:16:17"}'`
### send tx on specified height
`pricefeeder --config path/to/config/config.yaml debug`

This will start a grpc service to monitor the heighs change of imuachain, and serves the 'send create-price tx' request

`pricefeeder --config path/to/config/config.yaml debug send --feederID [1] --height [160] '{"base_block":160,"nonce":2,"price":"99907000000","det_id":"123","decimal":8,"timestamp":"2024-12-27 15:16:17"}'`

The command will send the request of 'sending a creat-price tx on specified height' to the grpc server started.
