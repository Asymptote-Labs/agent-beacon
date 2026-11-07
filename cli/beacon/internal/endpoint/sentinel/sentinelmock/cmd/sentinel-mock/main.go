// Command sentinel-mock runs sentinelmock until it is stopped, for package smoke tests that drive
// an installed forwarder. It prints the server URL on its first line.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel/sentinelmock"
)

func main() {
	var opts sentinelmock.Options
	var statuses, urlFile string
	flag.StringVar(&opts.Addr, "listen", "127.0.0.1:0", "listen address")
	flag.StringVar(&opts.CAFile, "ca-out", "", "write the CA certificate (PEM) here")
	flag.StringVar(&opts.RowsFile, "rows-out", "", "append accepted rows here, one JSON line each")
	flag.StringVar(&urlFile, "url-out", "", "also write the server URL here")
	flag.StringVar(&opts.TenantID, "tenant", "", "tenant ID")
	flag.StringVar(&opts.ClientID, "client-id", "", "app registration client ID")
	flag.StringVar(&opts.ClientSecret, "client-secret", "", "client secret; empty accepts a certificate assertion")
	flag.StringVar(&opts.DCRImmutableID, "dcr", "", "DCR immutable ID")
	flag.StringVar(&opts.Stream, "stream", "Custom-BeaconRuntimeRaw", "stream name")
	flag.IntVar(&opts.FailTokens, "fail-tokens", 0, "answer the first N token requests with 500")
	flag.StringVar(&statuses, "ingest-statuses", "", "comma-separated statuses for the first non-empty ingest requests")
	flag.Parse()
	for _, field := range strings.Split(statuses, ",") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		code, err := strconv.Atoi(field)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sentinel-mock: bad --ingest-statuses:", err)
			os.Exit(2)
		}
		opts.IngestStatuses = append(opts.IngestStatuses, code)
	}
	server, err := sentinelmock.Start(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sentinel-mock:", err)
		os.Exit(1)
	}
	defer server.Close()
	fmt.Println(server.URL)
	if urlFile != "" {
		if err := os.WriteFile(urlFile, []byte(server.URL+"\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sentinel-mock:", err)
			os.Exit(1)
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
