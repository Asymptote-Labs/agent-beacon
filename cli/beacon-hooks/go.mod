module github.com/asymptote-labs/agent-beacon/cli/beacon-hooks

go 1.24

require (
	github.com/asymptote-labs/agent-beacon/pkg/asymptotetrace v0.0.0
	github.com/spf13/cobra v1.8.1
)

replace github.com/asymptote-labs/agent-beacon/pkg/asymptotetrace => ../../pkg/asymptotetrace

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)
