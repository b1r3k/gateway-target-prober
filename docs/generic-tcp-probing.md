# Feature request: generic TCP probing for multi-protocol Gateway targets

## Summary

Extend `gateway-target-prober` so a Gateway target IP is advertised only when
all required public entrypoints for that IP are healthy, not just the current
HTTP(S) health URL.

The immediate production use case is a Gateway that serves web and mail traffic
from the same public edge IPs. `external-dns.alpha.kubernetes.io/target` is
written onto the parent Gateway, and ExternalDNS uses that target list for
Gateway API routes. If the prober only checks `https://<ip>:443/healthz`, it can
advertise an IP that is healthy for HTTPS but blackholes IMAPS or Submission.

## Problem

Today the prober evaluates one HTTP(S) request per candidate IP:

```text
GET https://<ip>:443/healthz
Host: health.example.com
```

That works for HTTPRoute availability, but it is not enough when the same
Gateway publishes TCPRoute listeners such as:

- `443/tcp` for HTTPS web traffic
- `993/tcp` for IMAPS
- `587/tcp` for SMTP Submission
- `25/tcp` for inbound SMTP

For home-router or firewall backed edge nodes, it is common for `443` DNAT to be
configured correctly while `993` or `587` is missing. In that state the prober
marks the IP healthy, ExternalDNS publishes it, and mail clients randomly fail
depending on which A record they select.

The desired behavior is:

```text
advertise(ip) = https_443_ok(ip) AND tcp_993_ok(ip) AND tcp_587_ok(ip) [AND tcp_25_ok(ip)]
```

If any required check fails, the IP must be omitted from the patched Gateway
target annotation.

## Proposed MVP

Add optional generic TCP connect probes that are evaluated in addition to the
existing HTTP(S) probe.

Suggested flags and environment variables:

```text
--tcp-ports=993,587,25
TCP_PORTS=993,587,25
```

Semantics:

- If `--tcp-ports` is empty, behavior remains exactly as it is today.
- For each candidate IP, run the existing HTTP(S) check first.
- Then attempt a TCP connection to each configured port on the same IP.
- The IP is healthy only if the HTTP(S) check returns a 2xx response and every
  required TCP port accepts a connection before the existing per-probe timeout.
- If all IPs fail, keep the current fail-safe behavior: refuse to patch the
  annotation rather than writing an empty target list.
- Log each per-port result with enough context to identify the failing IP and
  port.

Example deployment args:

```yaml
args:
  - --gateway-name=public-edge
  - --gateway-namespace=public-ingress-nginx
  - --annotation-key=external-dns.alpha.kubernetes.io/target
  - --ips=171.22.160.79,80.48.175.12,161.97.157.205
  - --host-header=health.example.com
  - --http-scheme=https
  - --http-path=/healthz
  - --timeout=5s
  - --tcp-ports=993,587,25
```

For the mail use case, `443`, `993`, and `587` should be required. `25` is
desirable as well, but deployments that protect inbound SMTP with source
allowlists may need to decide whether the prober's source is allowed to complete
that check.

## Design notes

The first implementation should be TCP-connect only. It should not try to speak
IMAP, SMTP, STARTTLS, or validate TLS certificates for the TCP checks. A
successful connect proves that the external routing, firewall, DNAT, hostPort,
and Gateway listener path are present for that port when the firewall performs
transparent DNAT or port-forwarding. If a firewall, load balancer, SYN proxy, or
TCP proxy accepts connections on behalf of an unavailable backend, a TCP connect
probe can succeed even while the edge node behind it is down. Protocol-aware
checks can be added later if needed.

The TCP checks should share the existing timeout unless a separate
`--tcp-timeout` is added. Keeping one timeout is simpler for the initial feature
and matches current operator expectations.

IPv6 should continue to work by using `net.JoinHostPort(ip, port)` when building
connect targets.

## Acceptance criteria

- With no TCP ports configured, the generated healthy target list is unchanged
  from current behavior.
- Given IP `A` with HTTPS healthy and TCP `993`, `587`, and `25` reachable,
  `A` remains in the patched target annotation.
- Given IP `B` with HTTPS healthy but TCP `993` unreachable, `B` is omitted.
- Given IP `C` with HTTPS unhealthy but TCP ports reachable, `C` is omitted.
- Given all IPs unhealthy by the combined criteria, the prober keeps the
  existing annotation unchanged and logs that it refused to patch an empty
  target list.
- Logs show per-IP, per-port TCP failures, for example:

```text
tcp probe failed ip=80.48.175.12 port=993 error="i/o timeout"
ip marked unhealthy ip=80.48.175.12 reason="required TCP port failed" port=993
```

## Possible future extension

If the maintainer prefers a more general API, `--tcp-ports` could later evolve
into a repeated `--probe` flag:

```text
--probe=https://:443/healthz?host=health.example.com
--probe=tcp://:993
--probe=tcp://:587
--probe=tcp://:25
```

The key requirement is still the same: a candidate IP should be advertised only
when every configured required probe succeeds.
