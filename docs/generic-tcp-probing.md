# Generic TCP probing

This document is a historical feature note. The current implementation target is the ListenerSet-scoped TCP model in `docs/listenerset-tcp-probing.md`.

## Current contract

`gateway-target-prober` supports generic TCP-connect checks as a separate probe mode:

```yaml
args:
  - --target-kind=listenerset
  - --target-name=smtp-edge
  - --target-namespace=public-ingress-nginx
  - --annotation-key=external-dns.alpha.kubernetes.io/target
  - --ips=171.22.160.79,80.48.175.12,161.97.157.205
  - --probe-mode=tcp
  - --tcp-ports=25
  - --interval=30s
  - --timeout=5s
```

The old combined model, where HTTP/S had to pass first and then TCP ports had to pass on the same Gateway target, is no longer the intended behavior. HTTP/S and TCP checks are separate modes, and independent DNS withdrawal requires independent target objects.

## Why ListenerSet scoping matters

A Gateway target annotation is object-wide. Every DNS record whose target comes from that Gateway shares the same target list.

That is useful for HTTP/S records that intentionally share Gateway-wide health. It is unsafe for service-specific records such as SMTP or IMAPS when those records need independent target withdrawal.

Use a standard Gateway API ListenerSet per non-HTTP target. ExternalDNS can read target annotations from ListenerSets when ListenerSet support is enabled, and `gateway-target-prober` can patch that ListenerSet without changing the Gateway-wide HTTP/S target list.

## TCP-connect semantics

In `tcp` mode, an IP is healthy only if every configured TCP port accepts a connection before the timeout. If every candidate IP is unhealthy, the prober keeps the existing annotation instead of writing an empty target list.

TCP-connect does not speak SMTP, IMAP, STARTTLS, or JMAP. It proves edge-path reachability from the prober to the configured public port. Protocol correctness belongs in separate observability unless a future feature adds protocol-aware probes.
