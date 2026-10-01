# FlowPilot

FlowPilot is a small Go command-line runner with a fixed **prepare → execute →
postmortem** lifecycle. It is not a general workflow engine. Trusted
configuration selects connector and workload executables; request JSON supplies
business inputs and cannot select commands or arguments.

## Local use

```sh
go test ./...
go run ./cmd/flowpilot -config config.json -request request.json
```

The command writes the machine-readable run record to stdout and returns a
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
