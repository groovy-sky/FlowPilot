# FlowPilot

FlowPilot is a small Go command-line runner with a fixed **prepare → execute →
postmortem** lifecycle. It is not a general workflow engine. Any command can be
run and recorded locally without configuration; optional trusted JSON
configuration adds connectors and policy or selects the workload itself.
Request JSON supplies business inputs and cannot select commands or arguments.

## Run a command (no configuration)

Everything after `--` is executed directly, without a shell and without any
configuration file:

```sh
go run ./cmd/flowpilot -- /bin/sh -c 'echo out; echo err >&2; exit 7'
flowpilot -name build -- make build
```

The command is resolved through `PATH`, receives FlowPilot's stdin and
environment, and its stdout and stderr are mirrored to the console while being
stored. FlowPilot exits with the command's exit status (`128 + signal` when the
command is killed by a signal). Its own failures use `125`, `126` when the
command cannot be started, and `127` when it cannot be found. Ctrl-C reaches the
command through the terminal; `SIGTERM` and `SIGHUP` sent to FlowPilot are
forwarded to the command as `SIGTERM` so the run is still recorded. An
interrupt sent only to the FlowPilot process (not to the terminal's process
group) is not forwarded.

Each invocation stores three files in the run directory, all with mode 0600:

- `<run_id>.json` — run record with status, timestamps, configuration
  identity, phase outcomes, `workload_exit_code`, and a `command` section
  (requested executable, resolved path, redacted arguments, working directory,
  PID, signal, duration, and log paths). It is written before the command
  starts and rewritten when the run finishes.
- `<run_id>.stdout.log` and `<run_id>.stderr.log` — the command's raw output.

The run directory is the first of: `-run-dir`, `$FLOWPILOT_RUN_DIR`,
`run_directory` from optional configuration, `$XDG_STATE_HOME/flowpilot/runs`,
and `~/.local/state/flowpilot/runs`. A one-line summary with the record path is
written to stderr.

Command options: `-name` (stored as `name` in the record), `-run-dir`,
`-quiet` (store output without mirroring it or printing the summary),
`-result-json` (emit the final result as JSON), `-config`, and `-no-config`.
Arguments following `--token`, `--password`,
`--secret`, `--api-key`, `--authorization`, and similar flags (including
`--flag=value` forms) are redacted in the record but passed unchanged to the
command. The environment is never recorded; other credential formats may still
appear in arguments or output logs, so keep the run directory access-controlled.

### JSON command result

Use `-result-json` in command mode to emit exactly one JSON object to stdout
after the run finishes, instead of mirroring the command's stdout and stderr.
Raw log files and the stored run record are unchanged. FlowPilot's summary and
errors stay on stderr; add `-quiet` to suppress the summary without suppressing
the JSON result.

```sh
flowpilot -result-json -- /bin/sh -c 'printf "hello\n"; printf "oops\n" >&2'
```

Example stdout:

```json
{
  "run_id": "a1b2c3d4",
  "status": "succeeded",
  "exit_code": 0,
  "result": "aGVsbG8K",
  "error_logs": "b29wcwo="
}
```

The schema contains `run_id` (string), `status` (final run status:
`"succeeded"` or `"failed"`), `exit_code` (integer command exit code),
`result` (Base64-encoded stdout), and `error_logs` (Base64-encoded stderr).
Streams are encoded from their exact bytes, including binary data and newlines;
they are never decoded or interpreted as text. Empty streams are `""`.
If the command cannot run, `exit_code` is FlowPilot's failure code. Otherwise
it is the child's exit code, even if a later postmortem failure makes FlowPilot
exit with `125`. The existing process exit-status rules still apply.

JSON mode buffers command output in memory and Base64 is not encryption or
redaction: protect the JSON output as you would the raw logs. Failures before a
run record exists produce only an error on stderr, not a JSON result. JSON
encoding or output failures are reported on stderr without retrying a partial
write; they make an otherwise successful invocation exit with `125`, while
preserving an existing nonzero exit status.

### Optional JSON configuration

Configuration extends command runs but is never required. The first file found
is used:

1. `-config <file>`;
2. `$FLOWPILOT_CONFIG` (an error if the file does not exist);
3. `flowpilot.json` in the current working directory;
4. `$XDG_CONFIG_HOME/flowpilot/config.json` (default `~/.config/flowpilot/config.json`).

Use `-no-config` to skip discovery. The file uses the same schema as the
configured workflow below, but every field is optional and is applied on top of
the defaults (`version` 1, `identity` `"local"`, `policy.max_output_bytes`
1 MiB, which limits connector output; command logs are not truncated). The
command line replaces `workload.path`, `workload.args`, and
`workload.version`; `workload.timeout` limits the command (no limit by
default). Optional `prepare` and `postmortem` connectors run before and after
the command and receive the current run record as `request`, so a postmortem
connector can publish logs or send notifications:

```json
{
  "identity": "team-builds",
  "run_directory": "/var/lib/flowpilot/runs",
  "workload": {"timeout": "30m"},
  "postmortem": {"path": "/opt/flowpilot/report", "version": "1"}
}
```

A failed prepare connector prevents the command from running. Postmortem is
attempted after failures and cancellation; a postmortem failure after a
successful command makes FlowPilot exit with `125`. Discovered configuration
can run executables, so only use it in directories you trust.

## Configured workflow

```sh
go test ./...
go run ./cmd/flowpilot -config config.json -request request.json
```

Without a command, `-config` is required and the workload comes from the
configuration. The command writes the machine-readable run record to stdout and returns a
non-zero exit code for failed execution or required postmortem actions. A run
record is also stored as `<run_id>.json` in `run_directory` with mode 0600.
Configuration must use absolute executable paths and is intentionally JSON so
the local workflow has no implicit shell evaluation.

Example trusted configuration:

```json
{
  "version": 1,
  "identity": "local-ansible",
  "run_directory": "./runs",
  "prepare": {"path": "/opt/flowpilot/azure-source", "version": "1"},
  "workload": {"path": "/usr/bin/ansible-playbook", "version": "2.16", "args": ["site.yml"]},
  "postmortem": {"path": "/opt/flowpilot/report", "version": "1"},
  "policy": {"max_output_bytes": 1048576}
}
```

## Connector protocol

Connectors are separate trusted executables. FlowPilot sends one newline
terminated JSON object on stdin and reads one JSON result from stdout. stderr
is retained as diagnostics and is never mixed into the result:

```json
{"version":1,"phase":"prepare","run_id":"...","request":{}}
```

Results use `{"version":1,"ok":true,"request_identity":"...","output":{}}`.
Set `ok` to false and include an `error` object for a connector-level failure.
Connector execution has a timeout and output limit. The protocol is versioned;
unknown versions are rejected.

## Security and limitations

Configuration is trusted and should be protected like executable code.
Connectors are not sandboxes and receive only the credentials made available by
their host. FlowPilot does not implement Azure, Ansible, AWS, or CI provider
integrations; those belong in small adapters using this protocol. Run records
redact common credential fields (`token`, `secret`, `password`, and
`credential`) and never intentionally persist credentials, but diagnostic
access still needs normal artifact and log controls. Forced host termination
can prevent postmortem; automatic recovery and distributed state are deferred.
