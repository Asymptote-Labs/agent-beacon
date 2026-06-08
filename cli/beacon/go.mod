module github.com/asymptote-labs/agent-beacon/cli/beacon

go 1.24

require (
	github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve v0.0.0
	github.com/pelletier/go-toml/v2 v2.3.1
	github.com/spf13/cobra v1.8.1
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve => ../../pkg/asymptoteobserve

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.4 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)
