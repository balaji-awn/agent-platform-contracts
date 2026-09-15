// Command mockplatform serves the mock agent platform for local development, for example behind
// Asoar's backend while working on the designer.
//
//	go run ./cmd/mockplatform -addr 127.0.0.1:8090 -base-path /v1 -latency 2s
//
// It seeds alert-triage@3 (mock.AlertTriage) plus any versions in the -seed file, a JSON array of
// agent version contracts, each with an optional "default_output". Every execution succeeds with
// its version's default output; script other outcomes from Go tests with the mock package.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/mock"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	basePath := flag.String("base-path", "/v1", "path prefix of the API")
	token := flag.String("token", "", "bearer token to require; empty accepts any request")
	latency := flag.Duration("latency", 500*time.Millisecond, "how long each execution runs")
	seed := flag.String("seed", "", "JSON file with an array of agent versions to add")
	flag.Parse()

	srv := mock.New(mock.Options{Token: *token, BasePath: *basePath, Latency: *latency})
	if err := srv.AddAgentVersion(mock.AlertTriage()); err != nil {
		log.Fatal(err)
	}
	if *seed != "" {
		data, err := os.ReadFile(*seed)
		if err != nil {
			log.Fatal(err)
		}
		var versions []mock.AgentVersion
		if err := json.Unmarshal(data, &versions); err != nil {
			log.Fatalf("%s: %v", *seed, err)
		}
		for _, v := range versions {
			if err := srv.AddAgentVersion(v); err != nil {
				log.Fatal(err)
			}
		}
	}
	log.Printf("mock agent platform listening on http://%s%s", *addr, *basePath)
	log.Fatal(http.ListenAndServe(*addr, srv))
}
