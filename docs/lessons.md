# Lessons

## 2026-06-02: TCP connect probes behind firewalls

When designing network health checks for edge nodes behind firewalls, distinguish transparent DNAT or port-forwarding from TCP termination, SYN proxying, load balancing, or firewall-side accept behavior. A TCP connect probe is a backend reachability signal only when the public path is transparently forwarded to the node; otherwise the probe can succeed while the backend is unavailable.

Rule: before treating a successful TCP connection as proof of backend liveness, capture the forwarding model as an explicit requirement or assumption.

## 2026-06-19: Preserve existing Gateway-wide probe semantics

When refactoring probe modes or DNS target scoping, first capture the exact operator deployment that must remain compatible. A Gateway target annotation can intentionally apply to every DNS name ExternalDNS derives from that Gateway, so service-specific checks need a separately scoped target instead of changing the meaning of the existing Gateway-wide deployment.

Rule: before proposing per-service probing on Gateway API resources, identify which object ExternalDNS reads the target annotation from and state whether the patch affects one DNS name or all records attached to that target.

Correction: do not expand compatibility requirements beyond the deployment the user explicitly needs preserved. Existing HTTP/S Gateway-wide behavior remains compatible, and TCP should be a separate mode. Do not carry forward combined HTTP/S+TCP behavior unless the user explicitly asks for it.

## 2026-06-19: Mixed Gateway listeners share one target annotation

When HTTP/S and TCP listeners live on the same Gateway, ExternalDNS still reads the target override from the Gateway object, not from individual listeners. TCPRoute hostname annotations can name the mail record, but they do not create an independent target list while the route remains parented to the same Gateway.

Rule: for independent HTTP and SMTP DNS health, plan a target-source split such as a dedicated mail Gateway or ListenerSet. Do not imply that a second prober can isolate one TCPRoute by patching the shared Gateway annotation.

## 2026-07-03: Keep non-HTTP DNS health at the requested probe depth

When turning non-HTTP reachability into DNS target withdrawal, distinguish target scoping from probe depth. Standard ListenerSet target objects solve per-service DNS isolation, while the health check can still be generic TCP-connect if that is the requested product scope. Do not upgrade the plan to protocol-aware SMTP, IMAP, STARTTLS, or JMAP handshakes unless the user asks for that depth.

Rule: use standard ListenerSet target objects for independent non-HTTP DNS targets, and document TCP-connect limitations clearly. Keep protocol hostnames configurable, avoid shared `mail.*` when independent withdrawal is required, and defer protocol-aware checks to a separate scope.
