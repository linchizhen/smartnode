module github.com/rocket-pool/smartnode

go 1.13

require (
	github.com/Azure/go-ansiterm v0.0.0-20210617225240-d185dfc1b5a1 // indirect
	github.com/Microsoft/go-winio v0.5.0 // indirect
	github.com/Nvveen/Gotty v0.0.0-20120604004816-cd527374f1e5 // indirect
	github.com/alessio/shellescape v1.4.1
	github.com/blang/semver/v4 v4.0.0
	github.com/btcsuite/btcd v0.21.0-beta
	github.com/btcsuite/btcutil v1.0.2
	github.com/cpuguy83/go-md2man/v2 v2.0.0 // indirect
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/docker v1.4.2-0.20180625184442-8e610b2b55bf
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/ethereum/go-ethereum v1.10.6
	github.com/fatih/color v1.7.0
	github.com/gogo/protobuf v1.3.1
	github.com/google/uuid v1.1.5
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/gorilla/websocket v1.4.2
	github.com/imdario/mergo v0.3.9
	github.com/mitchellh/go-homedir v1.1.0
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/prysmaticlabs/ethereumapis v0.0.0-20200729044127-8027cc96e2c0
	github.com/prysmaticlabs/go-ssz v0.0.0-20210121151755-f6208871c388
	github.com/rocket-pool/rocketpool-go v1.0.0-rc4.0.20210806062717-75ad8ecf821e
	github.com/sethvargo/go-password v0.2.0
	github.com/tyler-smith/go-bip39 v1.0.1-0.20181017060643-dbb3b84ba2ef
	github.com/urfave/cli v1.22.4
	github.com/wealdtech/go-eth2-types/v2 v2.5.0
	github.com/wealdtech/go-eth2-util v1.6.0
	github.com/wealdtech/go-eth2-wallet-encryptor-keystorev4 v1.1.1
	golang.org/x/crypto v0.0.0-20210322153248-0c34fe9e7dc2
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c
	golang.org/x/term v0.0.0-20201126162022-7de9c90e9dd1
	google.golang.org/grpc v1.29.1
	gopkg.in/yaml.v2 v2.3.0
)

replace github.com/rocket-pool/rocketpool-go => ../rocketpool-go
