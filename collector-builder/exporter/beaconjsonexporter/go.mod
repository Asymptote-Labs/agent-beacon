module github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter

go 1.24.0

require (
	github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve v0.0.0
	go.opentelemetry.io/collector/component v1.27.0
	go.opentelemetry.io/collector/consumer v1.27.0
	go.opentelemetry.io/collector/exporter v0.121.0
	go.opentelemetry.io/collector/pdata v1.27.0
	go.uber.org/zap v1.27.0
)

replace github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve => ../../../pkg/asymptoteobserve

require (
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/knadh/koanf/maps v0.1.1 // indirect
	github.com/knadh/koanf/providers/confmap v0.1.0 // indirect
	github.com/knadh/koanf/v2 v2.1.2 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	go.opentelemetry.io/collector/config/configretry v1.27.0 // indirect
	go.opentelemetry.io/collector/confmap v1.27.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.121.0 // indirect
	go.opentelemetry.io/collector/extension v1.27.0 // indirect
	go.opentelemetry.io/collector/extension/xextension v0.121.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.27.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.121.0 // indirect
	go.opentelemetry.io/collector/pipeline v0.121.0 // indirect
	go.opentelemetry.io/otel v1.41.0 // indirect
	go.opentelemetry.io/otel/metric v1.41.0 // indirect
	go.opentelemetry.io/otel/trace v1.41.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/grpc v1.79.3 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
