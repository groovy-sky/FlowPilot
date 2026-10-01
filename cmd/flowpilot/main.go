package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	flowpilot "github.com/groovy-sky/FlowPilot/flowpilot"
)

func main() {
	configPath := flag.String("config", "", "trusted JSON configuration file")
	requestPath := flag.String("request", "", "JSON request file (defaults to stdin)")
	flag.Parse()
	if *configPath == "" {
		fail("-config is required")
	}
	configData, err := os.ReadFile(*configPath)
	if err != nil {
		fail(err.Error())
	}
	var config flowpilot.Config
	if err := json.Unmarshal(configData, &config); err != nil {
		fail("invalid config: " + err.Error())
	}
	var request any
	var requestData []byte
	if *requestPath != "" {
		requestData, err = os.ReadFile(*requestPath)
	} else {
		requestData, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fail(err.Error())
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		fail("invalid request: " + err.Error())
	}
	record, runErr := (flowpilot.Runner{Config: config, Connect: flowpilot.ConnectorRunner{MaxOutputBytes: config.Policy.MaxOutputBytes}}).Run(context.Background(), request)
	if record != nil {
		encoded, _ := json.MarshalIndent(record, "", "  ")
		fmt.Println(string(encoded))
	}
	if runErr != nil {
		os.Exit(1)
	}
}

func fail(message string) { fmt.Fprintln(os.Stderr, "flowpilot:", message); os.Exit(2) }
