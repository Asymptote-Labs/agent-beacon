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
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.4 // indirect
	github.com/google/cel-go v0.28.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)
