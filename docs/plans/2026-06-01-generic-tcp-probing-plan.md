---
title: Generic TCP Probing
type: feat
status: completed
date: 2026-06-01
origin: docs/brainstorms/2026-06-01-generic-tcp-probing-requirements.md
---

# Generic TCP Probing

## Summary

Implement optional required TCP connect probes for `gateway-target-prober`, preserving current HTTP(S)-only behavior when no TCP ports are configured. Candidate IPs become healthy only when the existing HTTP(S) probe succeeds and every configured TCP port accepts a connection before the timeout.

---

## Problem Frame

The current prober can advertise an IP that is healthy for HTTPS while other Gateway-published TCP entrypoints, such as IMAPS or SMTP Submission, are unreachable. For transparent firewall DNAT or port-forwarded edge nodes, a TCP connect from the prober is the MVP signal that the public path reaches the node and listener.

---

## Requirements

**Configuration and Compatibility**

- R1. Add optional `--tcp-ports` and `TCP_PORTS` configuration for comma-separated required TCP ports.
- R2. Preserve existing healthy target selection when no TCP ports are configured.
- R3. Reject invalid TCP port configuration early with a clear config error.
- R4. Keep IPv4 and IPv6 connect target formatting correct.

**Combined Health Evaluation**

- R5. Keep the existing HTTP(S) probe as the first gate for each candidate IP.
- R6. Skip TCP probes for an IP when the HTTP(S) probe fails.
- R7. Mark an IP healthy only when HTTP(S) succeeds and every configured TCP port accepts a connection before the timeout.
- R8. Omit an IP from the patched annotation when any required TCP port fails.

**Fail-Safe and Observability**

- R9. Preserve the all-unhealthy fail-safe: do not patch an empty target list.
- R10. Log per-IP, per-port TCP failures with enough context to diagnose missing DNAT, firewall, listener, or node reachability.
- R11. Log when an IP is marked unhealthy because a required TCP port failed.

---

## Key Technical Decisions

- **Use integer TCP ports in config and runner state:** Parsing once at config load catches invalid values early and keeps the probe loop focused on behavior.
- **Run HTTP(S) before TCP:** This preserves the existing primary health gate and avoids extra TCP dials for IPs already known to be unhealthy.
- **Use TCP connect-only checks with the existing timeout:** This matches the MVP requirement and avoids protocol-specific behavior, while accepting that each slow failing port can consume the full per-probe timeout.
- **Use `net.JoinHostPort` for dial targets:** This preserves correct IPv6 formatting and matches the requirement that TCP probes use the same candidate IPs.
- **Treat transparent DNAT as an operational assumption:** TCP connect proves the public path only when the firewall forwards to the node rather than accepting connections on behalf of a dead backend.

---

## Implementation Units

### U1. TCP Port Configuration

- **Goal:** Add `--tcp-ports` / `TCP_PORTS` support with validation and no behavior change when omitted.
- **Files:** Modify `main.go`; modify `main_test.go`.
- **Patterns:** Follow existing `loadConfig`, `splitAndTrim`, `getStr`, and table-driven config tests in `main_test.go`.
- **Execution note:** Test-first. Add failing config tests for flag parsing, env parsing, empty input, whitespace handling, and invalid ports before implementation.
- **Test scenarios:**
  - No TCP ports configured produces an empty port list and does not fail config validation.
  - `--tcp-ports=993,587,25` parses to required ports in order.
  - `TCP_PORTS=993,587` is honored when the flag is omitted.
  - Empty entries and surrounding whitespace are ignored consistently with existing CSV parsing.
  - Invalid values such as non-numeric, zero, negative, and greater than `65535` return config errors.
- **Verification:** Targeted config tests in `main_test.go` pass.

### U2. Combined HTTP and TCP Health Evaluation

- **Goal:** Extend `Runner.HealthyIPs` so configured TCP ports are required after HTTP(S) succeeds.
- **Files:** Modify `main.go`; modify `main_test.go`.
- **Patterns:** Follow current `Runner.HealthyIPs` logging style, fail-safe behavior in `tick`, and helper style such as `portForScheme`.
- **Execution note:** Test-first. Add failing health-evaluation tests that exercise real TCP listeners before production code.
- **Test scenarios:**
  - With no TCP ports, existing HTTP(S)-only health behavior remains unchanged.
  - An HTTP-healthy IP with all required TCP ports reachable remains healthy.
  - An HTTP-healthy IP with one required TCP port unreachable is omitted.
  - An HTTP-unhealthy IP is omitted without requiring TCP success.
  - All candidates failing combined checks returns no healthy IPs so `tick` preserves the annotation.
  - IPv6 loopback connect targets work when a TCP listener is available.
- **Verification:** Targeted health tests and existing fail-safe tests pass.

### U3. Operator Surface and Full Verification

- **Goal:** Expose the configured TCP ports in startup behavior where useful, keep docs aligned, and run the repo verification suite.
- **Files:** Modify `main.go`; modify `docs/generic-tcp-probing.md` only if implementation changes the documented operator-facing behavior.
- **Patterns:** Follow existing startup log fields and project commands in `AGENTS.md`.
- **Execution note:** Verification-focused after U1 and U2 are green.
- **Test scenarios:**
  - Startup/config wiring passes parsed TCP ports into the runner.
  - The documented MVP remains accurate after implementation.
- **Verification:** `go test ./...`, `go fmt ./...`, and `go vet ./...` pass.

---

## Scope Boundaries

- Do not implement protocol-aware IMAP, SMTP, STARTTLS, or TLS certificate validation probes.
- Do not add a separate TCP timeout setting in the first version.
- Do not replace current HTTP(S) probe configuration with a general `--probe` model.
- Do not add optional/quorum semantics for TCP ports; every configured TCP port is required.

---

## Risks & Dependencies

- TCP connect probes only prove backend reachability when firewalls transparently DNAT or port-forward to the edge node. A SYN proxy, TCP proxy, load balancer, or firewall that accepts connections independently can hide a dead backend.
- Multiple unreachable TCP ports can increase probe-cycle duration because each port may consume the existing per-probe timeout.
- Port `25` may be unsuitable in deployments with SMTP source allowlists unless the prober source is allowed.

---

## Acceptance Examples

- AE1. No TCP ports configured: HTTP(S)-healthy IPs are selected exactly as before.
- AE2. HTTPS healthy plus TCP `993`, `587`, and `25` reachable: the IP remains advertised.
- AE3. HTTPS healthy plus TCP `993` unreachable: the IP is omitted and the failed IP/port is logged.
- AE4. HTTPS unhealthy plus TCP ports reachable: the IP is omitted because HTTP(S) is still the first gate.
- AE5. All candidates fail combined checks: the previous Gateway annotation remains unchanged.

---

## Sources / Research

- `docs/brainstorms/2026-06-01-generic-tcp-probing-requirements.md` is the origin requirements document.
- `docs/generic-tcp-probing.md` captures the original feature request and operator examples.
- `main.go` contains current config parsing, HTTP(S) probing, Gateway annotation patching, and fail-safe logic.
- `main_test.go` contains current config, health, annotation patching, and fail-safe tests to extend.
