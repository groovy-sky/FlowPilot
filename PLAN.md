# FlowPilot Product Plan

## Purpose

FlowPilot is a command-line runner that uses connectors to gather the inputs for an automation run, execute a configured workload, and collect and deliver the result.

Its lifecycle is:

1. **Prepare:** Obtain and validate the data needed for the run.
2. **Execute:** Run the configured workload with that data.
3. **Postmortem:** Collect the outcome and perform configured reporting, storage, notification, or cleanup.

FlowPilot sequences these steps and passes data between them. Connectors interact with external systems; existing tools such as Ansible perform the automation work. FlowPilot does not replace Ansible, Docker, cloud deployment engines, or CI platforms.

FlowPilot is the working product name. Public name availability and trademarks must be checked before release. Connect Hub may be used as organizational branding without becoming a runtime dependency.

## Product Scope

The initial use case is processing automation requests represented by Azure deployment outputs and executing existing Ansible workloads. User and shared flows use the same runner, with differences expressed through trusted configuration.

The architecture should allow connectors for AWS and execution through GitHub Actions, GitLab CI, Azure DevOps, and local environments. Provider-specific details belong in connectors, not in the runner.

The initial product is a command-line runner, not a hosted scheduling service or general-purpose workflow engine. A graphical interface and arbitrary workflow graphs are outside the initial scope.

## How a Run Works

### Prepare

A source connector obtains the selected request and its relevant data. The runner validates that data and combines it with trusted configuration to produce the inputs for execution.

Preparation should identify the selected request and report missing or invalid inputs before execution begins. Request data may supply business inputs, but it must not choose arbitrary executables or bypass workload policy.

Selection rules are explicit for each source. The connector must not assume that the first item returned by an API is the newest or otherwise eligible request.

### Execute

The runner invokes the configured workload with the prepared inputs. Initially, this may mean running an approved Ansible playbook through an execution connector.

The runner records the workload's outcome, including its exit status and available diagnostic references. It does not reinterpret request text as shell instructions. Workloads, executable paths, arguments, and execution targets are controlled by trusted configuration.

A failed or timed-out run must not be treated as though no work occurred. Retries are limited to workloads considered safe to retry or protected by an effective duplicate-prevention mechanism.

### Postmortem

The runner collects the available outcome and performs configured actions such as creating a report, storing results, sending notifications, cleaning up temporary files, or acknowledging a completed request.

Postmortem is attempted after preparation or execution failures when the runner remains available. It must work with partial information—for example, when request details could not be obtained.

Reporting, notification, storage, cleanup, and acknowledgement have separate results. A failure in one action must not erase the execution result or hide the original failure.

A request may be acknowledged as successfully processed only after execution succeeds and any explicitly required finalization actions pass. Deleting an Azure deployment record is one possible acknowledgement mechanism; it does not delete resources created by that deployment.

Postmortem cannot be guaranteed if the runner or host is forcibly terminated. The initial version should clearly report incomplete runs; automatic recovery can be added when required.

## Configuration and Inputs

Trusted configuration defines source locations, workload and executable paths, allowed arguments, execution targets, timeouts, notification or storage destinations, and operational policies.

Request data supplies business inputs within those restrictions. It cannot select arbitrary executables, replace connector binaries, or bypass workload policy.

Each connector documents the phases it supports, the configuration it requires, and the inputs and outputs it uses. The runner validates required fields and bindings before invoking the affected connector or workload. It also reports runtime problems such as missing credentials, permissions, executables, or accessible artifacts.

The initial lifecycle has fixed dependencies: execution requires successful preparation; postmortem can consume partial outcomes. Arbitrary user-defined dependency graphs are deferred.

Optional actions create conditional requirements. For example, notification settings are required when notifications are enabled, but notification handling must account for failures that occur before request details are available.

## Run Record

Each run has a unique run ID and a small, versioned record containing:

- request identity, if available;
- configuration identity;
- phase outcomes and timestamps;
- workload exit status;
- connector and workload identifiers or versions;
- non-secret outputs and diagnostic references;
- structured errors.

Run identity and request identity are separate: the same request can be involved in more than one run. Credentials, tokens, and sensitive raw outputs must not be stored in the run record.

For a run contained in one CI job or local invocation, a local run directory is sufficient. If phases run in separate jobs, the CI adapter must explicitly transfer the run record and required artifacts. The initial version does not require a distributed state service or cross-run request claims.

The runner owns the run record. Connectors receive inputs and return results; they do not edit the record directly. State versions and retention rules should be documented before the record is shared between phases or retained as an operational artifact.

## Connectors

Connectors are small components that interact with external systems—for example, obtaining a request, invoking an approved workload, or storing a report. A connector may support more than one phase when that is useful, such as reading a request during preparation and acknowledging it after successful execution.

The initial packaging model is separate executables. Connectors accept structured inputs and return structured results; diagnostic logs are kept separate from those results. The runner distinguishes a connector error or crash from a workload failure.

Connector paths and versions come from trusted configuration. Connector execution has time and output limits. Connectors are trusted components with access to the credentials assigned to them; running a connector as a separate process does not make it a security sandbox.

The connector interface should be versioned and documented, but the initial implementation should support only the capabilities needed by the first workflows.

## Cloud and Platform Support

Azure connectors may read ARM deployment outputs, discover related environment information, and acknowledge processed deployment records. AWS connectors may use CloudFormation, queues, or other request sources according to their own semantics.

Each source connector translates provider-specific data into the inputs needed by the workload while preserving necessary provider metadata. Azure and AWS request lifecycles are not assumed to be interchangeable.

Execution connectors may invoke Docker, Ansible, or approved local tools. Running FlowPilot inside a container does not automatically provide access to a container engine; execution requirements must be configured explicitly.

CI adapters translate platform facilities into runner inputs and outputs. They handle authentication setup, artifact transfer, platform-specific status reporting, and arranging postmortem after an earlier phase fails. Azure DevOps variables, GitHub outputs, and GitLab artifacts are integration mechanisms, not the canonical run record.

CI adapters should support both a combined invocation and independent phase invocation where a real workflow requires it.

## Authentication and Security

Authentication uses cloud SDK mechanisms appropriate to the host. Prefer CI workload identity federation or managed identities over long-lived credentials.

Each phase or connector receives only the credentials and permissions it needs. Different service connections may remain separate.

Credentials, tokens, secret-bearing arguments, and sensitive raw outputs must not be persisted in run records or ordinary reports. Logs and artifacts may also contain sensitive information, so apply appropriate redaction and access controls.

Validate resource identifiers, workload names, paths, variables, and notification destinations. Avoid unrestricted shell command construction and use allowlists where request data can influence execution.

## Reporting and Outcomes

Every run produces a machine-readable outcome and, where configured, a human-readable report. The outcome includes the run and request identifiers, phase results, timings, workload status, errors, and references to available diagnostics.

Logs distinguish runner diagnostics from connector and workload output and carry a run identifier for correlation. Notifications summarize relevant results and link to protected artifacts instead of including unrestricted logs.

The final run status reflects the workload outcome and any configured required finalization actions. It retains the original failure when later reporting, notification, storage, or cleanup actions also fail.

## Delivery Stages

### Foundation

Define the three-phase lifecycle, minimal run record, connector input and output conventions, configuration boundaries, and failure reporting. Implement local persistence and validation.

### Azure Migration

Implement the Azure request connector, required environment discovery, existing Ansible workload execution, and postmortem actions for current user and shared flows. Preserve intentional differences in environment overrides and recipient policies.

Define request selection and acknowledgement behavior. Do not enable overlapping consumers unless the source workflow has an appropriate duplicate-prevention approach.

### CI Support

Add thin adapters for Azure DevOps, GitHub Actions, and GitLab CI as needed. Validate credential boundaries, state and artifact transfer, failure reporting, and postmortem behavior.

### AWS Support

Add AWS connectors when a concrete workflow requires them. Verify that the runner's inputs and lifecycle can support AWS without provider-specific changes to the runner.

### Operational Hardening

Add automated recovery, durable shared state, request claims, connector distribution controls, or additional retention features only when operational needs justify them.

## Acceptance Criteria

- User and shared Azure workflows use the same runner with trusted configuration differences.
- Preparation obtains and validates the inputs needed by execution.
- Execution runs only the configured, approved workload.
- Postmortem collects and reports the available outcome after preparation or execution failure when the runner remains available.
- Failed execution is not acknowledged as successfully processed.
- Connector failures are distinguishable from workload failures.
- Run records and reports do not persist credentials.
- Missing required inputs or dependencies are reported before the affected action begins.
- The initial workflow can be run locally and through its chosen CI adapter.
