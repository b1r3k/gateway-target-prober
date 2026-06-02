# Lessons

## 2026-06-02: TCP connect probes behind firewalls

When designing network health checks for edge nodes behind firewalls, distinguish transparent DNAT or port-forwarding from TCP termination, SYN proxying, load balancing, or firewall-side accept behavior. A TCP connect probe is a backend reachability signal only when the public path is transparently forwarded to the node; otherwise the probe can succeed while the backend is unavailable.

Rule: before treating a successful TCP connection as proof of backend liveness, capture the forwarding model as an explicit requirement or assumption.
