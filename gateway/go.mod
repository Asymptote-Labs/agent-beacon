module github.com/asymptote-labs/agent-beacon/gateway

go 1.27.0

require (
	github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve v0.0.0
	github.com/envoyproxy/go-control-plane/envoy v1.40.0
	google.golang.org/grpc v1.85.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cel.dev/expr v0.25.3 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/google/cel-go v0.30.0 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)

replace github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve => ../pkg/asymptoteobserve
