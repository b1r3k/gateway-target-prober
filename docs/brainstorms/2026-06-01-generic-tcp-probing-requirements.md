---
date: 2026-06-01
topic: generic-tcp-probing
---

# Generic TCP Probing Requirements

## Summary

Add optional required TCP connect probes to `gateway-target-prober` so a candidate IP is advertised only when the existing HTTP(S) health check passes and every configured TCP port accepts a connection. The first version uses `--tcp-ports` / `TCP_PORTS` and preserves today's behavior when no TCP ports are configured.

---

## Problem Frame

`gateway-target-prober` currently checks one HTTP(S) health URL per candidate IP before patching the Gateway target annotation used by ExternalDNS. That is sufficient for HTTPRoute availability, but not for Gateway deployments where the same public edge IPs also serve TCPRoute listeners such as IMAPS, SMTP Submission, or inbound SMTP.

In the motivating deployment, an edge IP can be healthy for HTTPS while a home-router, firewall, DNAT rule, host port, or Gateway listener path is missing for a mail port. If that IP remains in `external-dns.alpha.kubernetes.io/target`, DNS clients may randomly select an address that blackholes mail traffic.

---

## Key Decisions

- **Ship the narrow `--tcp-ports` MVP first.** A comma-separated TCP port list covers the immediate web-plus-mail Gateway need with less operator-facing surface area than a general probe configuration model.
- **Keep the existing HTTP(S) probe as the primary gate.** TCP checks are additional required entrypoint checks, not a replacement for the current HTTP(S) health URL.
- **Use TCP connect-only checks.** The first version verifies that each required public TCP port accepts a connection; it does not speak IMAP, SMTP, STARTTLS, or validate TLS certificates.
- **Require every configured TCP port.** A candidate IP is healthy only when all required entrypoints for that IP are reachable, favoring safe DNS targets over partial availability.
- **Reuse the existing per-probe timeout.** A separate TCP timeout is deferred to keep the first version simple and aligned with current operator expectations.

---

## Actors

- A1. **Cluster operator:** Configures the prober for a Gateway whose advertised IPs serve multiple public protocols.
- A2. **`gateway-target-prober`:** Evaluates candidate IPs and patches the Gateway target annotation with only healthy targets.
- A3. **ExternalDNS:** Reads the Gateway target annotation and publishes DNS records from the patched target list.
- A4. **Protocol clients:** Web and mail clients that rely on DNS targets leading to fully reachable public entrypoints.

---

## Requirements

**Configuration and Compatibility**

- R1. The prober accepts an optional comma-separated TCP port list through `--tcp-ports` and `TCP_PORTS`.
- R2. When no TCP ports are configured, healthy target selection remains unchanged from the current HTTP(S)-only behavior.
- R3. TCP probes use the same candidate IP list as the existing HTTP(S) probe.
- R4. TCP connect targets must support IPv4 and IPv6 address formatting.

**Combined Health Evaluation**

- R5. For each candidate IP, the existing HTTP(S) check runs first.
- R6. If the HTTP(S) check does not return a 2xx response, the candidate IP is unhealthy regardless of TCP port reachability.
- R7. If TCP ports are configured and the HTTP(S) check succeeds, the prober attempts a TCP connection to each configured port on the same candidate IP.
- R8. A candidate IP is healthy only when the HTTP(S) check succeeds and every configured TCP port accepts a connection before the timeout.
- R9. A candidate IP with any required TCP port failure is omitted from the patched Gateway target annotation.

**Fail-Safe Behavior and Observability**

- R10. If every candidate IP is unhealthy under the combined criteria, the prober preserves the existing fail-safe behavior and refuses to patch an empty target list.
- R11. Logs identify per-IP, per-port TCP failures with enough context for an operator to find the missing or blocked entrypoint.
- R12. Logs identify when an IP is marked unhealthy because a required TCP port failed.

---

## Key Flow

- F1. Combined probe cycle
  - **Trigger:** The prober evaluates the configured candidate IPs during its normal interval or startup tick.
  - **Actors:** A1, A2, A3
  - **Steps:** The prober runs the current HTTP(S) check for each IP. For each HTTP(S)-healthy IP, it checks every configured TCP port. It builds the target list from IPs that satisfy all required checks.
  - **Outcome:** The Gateway target annotation contains only IPs that are healthy for the HTTP(S) entrypoint and all required TCP entrypoints, unless all candidates fail and the fail-safe prevents patching.
  - **Covered by:** R2, R5, R6, R7, R8, R9, R10

---

## Acceptance Examples

- AE1. HTTP-only compatibility
  - **Covers R2.**
  - **Given:** No TCP ports are configured.
  - **When:** The prober evaluates candidate IPs.
  - **Then:** The generated healthy target list matches current HTTP(S)-only behavior.

- AE2. Fully healthy multi-protocol IP
  - **Covers R7, R8.**
  - **Given:** Candidate IP `A` has a healthy HTTPS check and TCP ports `993`, `587`, and `25` accept connections.
  - **When:** TCP ports `993,587,25` are configured.
  - **Then:** IP `A` remains in the patched target annotation.

- AE3. HTTP healthy but required TCP port unreachable
  - **Covers R8, R9, R11, R12.**
  - **Given:** Candidate IP `B` has a healthy HTTPS check but TCP port `993` is unreachable.
  - **When:** TCP port `993` is configured as required.
  - **Then:** IP `B` is omitted, and logs identify the failed IP and port.

- AE4. HTTP unhealthy but TCP ports reachable
  - **Covers R5, R6.**
  - **Given:** Candidate IP `C` has an unhealthy HTTPS check while required TCP ports accept connections.
  - **When:** The prober evaluates IP `C`.
  - **Then:** IP `C` is omitted because HTTP(S) remains the first required gate.

- AE5. All candidates fail combined checks
  - **Covers R10.**
  - **Given:** Every candidate IP fails either the HTTP(S) check or at least one required TCP port.
  - **When:** The prober finishes the cycle with no healthy IPs.
  - **Then:** The existing Gateway annotation is left unchanged and the prober logs that it refused to patch an empty target list.

---

## Scope Boundaries

- Protocol-aware IMAP, SMTP, STARTTLS, or TLS certificate validation probes are deferred.
- A separate `--tcp-timeout` or `TCP_TIMEOUT` setting is deferred.
- A general repeated `--probe` configuration model is deferred; the first version should not require operators to rewrite the current HTTP(S) probe configuration.
- Per-port optionality or quorum logic is outside the MVP; every configured TCP port is required.

---

## Dependencies / Assumptions

- The Gateway target annotation remains the authority ExternalDNS uses for the advertised target list.
- Operators choose TCP ports whose successful connection from the prober source is a meaningful public reachability signal.
- TCP connect probes are assumed to traverse transparent firewall DNAT or port-forwarding to the edge node. Firewalls, load balancers, SYN proxies, or TCP proxies that accept connections on behalf of an unavailable backend can make a TCP connect probe succeed even when the node behind the firewall is down.
- For inbound SMTP on port `25`, deployments with source allowlists may need to omit that port or allow the prober source.
- The accepted MVP behavior is that each configured TCP port can consume up to the per-probe timeout when a candidate IP is slow or unreachable.

---

## Sources / Research

- `docs/generic-tcp-probing.md` describes the original feature request, motivating mail-over-Gateway use case, MVP behavior, and acceptance criteria.
- `main.go` currently implements HTTP(S)-only health checks, Gateway annotation patching, and the all-unhealthy fail-safe behavior that this feature must preserve.
- `main_test.go` contains the current unit-test surface for config parsing, health evaluation, annotation patching, and fail-safe behavior.
