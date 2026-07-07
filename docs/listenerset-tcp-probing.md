# ListenerSet TCP probing

`gateway-target-prober` supports two probe modes:

- `http`: the existing HTTP/S health check mode.
- `tcp`: generic TCP-connect checks for non-HTTP services.

Each prober instance patches one target object. Keep the existing HTTP/S prober on the shared Gateway when web records should share one target list. Use a standard Gateway API `ListenerSet` target for each independently controlled non-HTTP DNS record.

## Existing HTTP/S Gateway-wide prober

This deployment shape continues to work unchanged and patches the Gateway target annotation:

```yaml
args:
  - --gateway-name=public-edge
  - --gateway-namespace=public-ingress-nginx
  - --annotation-key=external-dns.alpha.kubernetes.io/target
  - --ips=171.22.160.79,80.48.175.12,161.97.157.205
  - --host-header=health.cx-lab.com
  - --interval=30s
  - --timeout=5s
  - --http-path=/healthz
  - --http-scheme=https
```

This should only be used for records that intentionally share the Gateway target list.

## TCP mode for non-HTTP services

Run a separate prober for each ListenerSet target:

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

`--tcp-ports` is valid only with `--probe-mode=tcp`. TCP mode marks an IP healthy only when every configured port accepts a connection before the timeout.

Set `--annotation-key` to the target annotation key your ExternalDNS version watches. The examples use the current cluster's `external-dns.alpha.kubernetes.io/target`; newer ExternalDNS versions also document `external-dns.kubernetes.io/target`.

Examples:

```yaml
# SMTP inbound
- --target-name=smtp-edge
- --probe-mode=tcp
- --tcp-ports=25

# Submission
- --target-name=submission-edge
- --probe-mode=tcp
- --tcp-ports=587

# IMAPS
- --target-name=imaps-edge
- --probe-mode=tcp
- --tcp-ports=993
```

JMAP over HTTPS should use `http` mode against a JMAP or application health endpoint, not `tcp` mode, when HTTP status is the desired health signal.

## ListenerSet target shape

A Gateway must allow ListenerSets before they can attach:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public-edge
  namespace: public-ingress-nginx
  annotations:
    external-dns.alpha.kubernetes.io/target: "171.22.160.79,80.48.175.12,161.97.157.205"
spec:
  gatewayClassName: envoy-public
  allowedListeners:
    namespaces:
      from: Same
  listeners:
    - name: http-80
      port: 80
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: Same
```

For an existing Gateway, add `allowedListeners` beside the listeners you already use. Do not replace the current HTTP/S listeners with this minimal example.

Create one ListenerSet per independently controlled DNS target:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: ListenerSet
metadata:
  name: smtp-edge
  namespace: public-ingress-nginx
  annotations:
    external-dns.alpha.kubernetes.io/target: "171.22.160.79,80.48.175.12,161.97.157.205"
spec:
  parentRef:
    namespace: public-ingress-nginx
    name: public-edge
    kind: Gateway
    group: gateway.networking.k8s.io
  listeners:
    - name: smtp-25
      port: 25
      protocol: TCP
      allowedRoutes:
        kinds:
          - kind: TCPRoute
        namespaces:
          from: Selector
          selector:
            matchLabels:
              mail-routing: "true"
```

Attach the route to the ListenerSet instead of the shared Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: smtp-inbound
  namespace: mail
  annotations:
    external-dns.alpha.kubernetes.io/hostname: "smtp.example.com"
spec:
  parentRefs:
    - name: smtp-edge
      namespace: public-ingress-nginx
      kind: ListenerSet
      group: gateway.networking.k8s.io
      sectionName: smtp-25
  rules:
    - backendRefs:
        - name: mail-smtp
          port: 25
```

If the TCPRoute remains parented directly to `public-edge`, ExternalDNS still uses the Gateway target annotation. A second prober cannot isolate that DNS record by patching the same Gateway target.

## ExternalDNS requirements

ExternalDNS must watch ListenerSets for ListenerSet target annotations:

```yaml
args:
  - --source=gateway-tcproute
  - --gateway-listener-sets
```

Its RBAC must include `listenersets`:

```yaml
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["gateways", "listenersets", "tcproutes"]
  verbs: ["get", "list", "watch"]
```

Route annotations such as `external-dns.alpha.kubernetes.io/hostname` can still provide hostnames for TCPRoute resources. Route-level `external-dns.alpha.kubernetes.io/target` annotations are not the target scoping mechanism for Gateway API records.

## TCP-connect limitations

TCP mode proves that the configured address and port accepted a connection from the prober. It does not prove SMTP correctness, STARTTLS support, IMAP greeting behavior, JMAP application health, authentication, or mail delivery.

TCP-connect can be misleading when a firewall, SYN proxy, load balancer, or TCP proxy accepts connections while the backend service is unavailable. For that topology, use TCP mode only as an edge-path signal and keep protocol correctness in separate observability.

For protected ports such as inbound SMTP with source allowlists, the prober source must be allowed to connect or the check will correctly mark that IP unhealthy from the prober's perspective.

## References

- Gateway API ListenerSet guide: https://gateway-api.sigs.k8s.io/guides/user-guides/listener-set/
- ExternalDNS Gateway API source notes: https://github.com/kubernetes-sigs/external-dns/blob/master/docs/sources/gateway-api.md
