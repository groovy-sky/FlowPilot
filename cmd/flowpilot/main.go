package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	flowpilot "github.com/groovy-sky/FlowPilot/flowpilot"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	configPath  string
	requestPath string
	runDir      string
	name        string
	quiet       bool
	noConfig    bool
}

func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	var opts options
	flags := flag.NewFlagSet("flowpilot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "JSON configuration file (required without a command)")
	flags.StringVar(&opts.requestPath, "request", "", "JSON request file (defaults to stdin; configured workflow only)")
	flags.StringVar(&opts.runDir, "run-dir", "", "directory for run records and logs (command mode)")
	flags.StringVar(&opts.name, "name", "", "human-readable run name (command mode)")
	flags.BoolVar(&opts.quiet, "quiet", false, "store command output without mirroring it (command mode)")
	flags.BoolVar(&opts.noConfig, "no-config", false, "skip optional configuration discovery (command mode)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  flowpilot [options] -- <executable> [arguments...]")
		fmt.Fprintln(stderr, "  flowpilot -config config.json [-request request.json]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		return runCommand(opts, flags.Args(), getenv, stdin, stdout, stderr)
	}
	return runConfigured(opts, stdin, stdout, stderr)
}

func runCommand(opts options, command []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if opts.requestPath != "" {
		return failCommand(stderr, errors.New("-request cannot be used with a command"))
	}
	configPath := opts.configPath
	if configPath == "" && !opts.noConfig {
		workingDir, err := os.Getwd()
		if err != nil {
			return failCommand(stderr, err)
		}
		if configPath, err = flowpilot.DiscoverConfig(getenv, workingDir); err != nil {
			return failCommand(stderr, err)
		}
	}
	config, err := flowpilot.LoadCommandConfig(configPath)
	if err != nil {
		return failCommand(stderr, err)
	}
	config.RunDirectory = flowpilot.ResolveRunDirectory(opts.runDir, getenv, config.RunDirectory)

	// Ctrl-C reaches the command through the terminal's process group; keep
	// FlowPilot alive so it can record the outcome. SIGTERM and SIGHUP are
	// forwarded to the command as SIGTERM.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	defer signal.Reset(os.Interrupt)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	cmd := flowpilot.Command{Path: command[0], Args: command[1:], Name: opts.name, Stdin: stdin}
	if !opts.quiet {
		cmd.Stdout, cmd.Stderr = stdout, stderr
	}
	runner := flowpilot.Runner{Config: config, Connect: flowpilot.ConnectorRunner{MaxOutputBytes: config.Policy.MaxOutputBytes}}
	record, runErr := runner.Exec(ctx, cmd)
	if record == nil {
		return failCommand(stderr, runErr)
	}
	code := flowpilot.CommandExitCode(record, runErr)
	if runErr != nil && (record.WorkloadExitCode == nil || *record.WorkloadExitCode == 0) {
		fmt.Fprintln(stderr, "flowpilot:", runErr)
	}
	if !opts.quiet {
		fmt.Fprintf(stderr, "flowpilot: run %s %s (exit %d); record: %s\n", record.RunID, record.Status, code,
			filepath.Join(filepath.Dir(record.Command.StdoutLog), record.RunID+".json"))
	}
	return code
}

func failCommand(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "flowpilot:", err)
	return flowpilot.ExitInternal
}

func runConfigured(opts options, stdin io.Reader, stdout, stderr io.Writer) int {
	if opts.configPath == "" {
		return fail(stderr, "-config is required without a command")
	}
	configData, err := os.ReadFile(opts.configPath)
	if err != nil {
		return fail(stderr, err.Error())
	}
	var config flowpilot.Config
	if err := json.Unmarshal(configData, &config); err != nil {
		return fail(stderr, "invalid config: "+err.Error())
	}
	var request any
	var requestData []byte
	if opts.requestPath != "" {
		requestData, err = os.ReadFile(opts.requestPath)
	} else {
		requestData, err = io.ReadAll(stdin)
	}
	if err != nil {
		return fail(stderr, err.Error())
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		return fail(stderr, "invalid request: "+err.Error())
	}
	record, runErr := (flowpilot.Runner{Config: config, Connect: flowpilot.ConnectorRunner{MaxOutputBytes: config.Policy.MaxOutputBytes}}).Run(context.Background(), request)
	if record != nil {
		encoded, _ := json.MarshalIndent(record, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if runErr != nil {
		return 1
	}
	return 0
}

func fail(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "flowpilot:", message)
	return 2
}
