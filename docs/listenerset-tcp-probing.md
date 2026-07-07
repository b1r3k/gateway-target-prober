# Gateway and ListenerSet TCP probing

`gateway-target-prober` supports one probe mode and one target object per process:

- `http`: the existing HTTP/S health check mode.
- `tcp`: generic TCP-connect checks for non-HTTP services.

Use the existing HTTP/S prober on a shared Gateway when web records should intentionally share one target list. Use a separate target object for mail or other non-HTTP records when they need independent DNS withdrawal. The separate target can be a dedicated Gateway, or a standard Gateway API `ListenerSet` when your Gateway API, Envoy Gateway, and ExternalDNS versions support ListenerSets.

## Existing HTTP/S Gateway-wide prober

This deployment shape continues to work unchanged and patches the `public-edge` Gateway target annotation. ExternalDNS applies that target list to DNS names whose targets come from this Gateway:

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

Keep this setup for HTTP/S records that should share Gateway-wide health. Do not also use this same `public-edge` Gateway target annotation for a service-specific TCP prober unless those TCP records are intended to publish exactly the same target list as the HTTP/S records.

## TCP mode rules

TCP mode does not run an HTTP/S probe:

```yaml
args:
  - --probe-mode=tcp
  - --tcp-ports=25
```

`--tcp-ports` is valid only with `--probe-mode=tcp`. TCP mode marks an IP healthy only when every configured port accepts a connection before the timeout. If every candidate IP is unhealthy, the prober keeps the existing annotation instead of writing an empty target list.

Set `--annotation-key` to the target annotation key your ExternalDNS version watches. The examples use the current cluster's `external-dns.alpha.kubernetes.io/target`; newer ExternalDNS versions also document `external-dns.kubernetes.io/target`.

## Why the current mixed Gateway is not isolated

With a mixed `public-edge` Gateway, a TCPRoute can declare a hostname such as `mail.jachym.dev` through a Route annotation while still using `public-edge` as its parent:

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: mail-pilot-smtp-inbound
  namespace: mail-pilot
  annotations:
    external-dns.alpha.kubernetes.io/hostname: "mail.jachym.dev"
spec:
  parentRefs:
    - name: public-edge
      namespace: public-ingress-nginx
      sectionName: smtp-25
  rules:
    - backendRefs:
        - name: mail-smtp
          port: 25
```

That route is not independently target-scoped. ExternalDNS gets the hostname from the TCPRoute annotation, but it still gets the target IP list from the parent `public-edge` Gateway annotation. A second prober must not patch `public-edge` for `mail.jachym.dev`, because it would overwrite the same object-wide target list used by HTTP/S records.

To make `mail.jachym.dev` independent, move the SMTP listener and TCPRoute parent reference to one of these target sources:

- a dedicated mail Gateway
- a ListenerSet attached to `public-edge`

Route-level `external-dns.alpha.kubernetes.io/target` annotations are not the target scoping mechanism for Gateway API records.

## Option A: dedicated mail Gateway

The baseline scoped option is a dedicated Gateway for mail records. The TCP prober patches this Gateway, not `public-edge`:

```yaml
args:
  - --gateway-name=mail-edge
  - --gateway-namespace=public-ingress-nginx
  - --annotation-key=external-dns.alpha.kubernetes.io/target
  - --ips=171.22.160.79,80.48.175.12,161.97.157.205
  - --probe-mode=tcp
  - --tcp-ports=25
  - --interval=30s
  - --timeout=5s
```

Example Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mail-edge
  namespace: public-ingress-nginx
  annotations:
    external-dns.alpha.kubernetes.io/target: "171.22.160.79,80.48.175.12,161.97.157.205"
spec:
  gatewayClassName: envoy-public
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

Move the SMTP TCPRoute to the dedicated Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: mail-pilot-smtp-inbound
  namespace: mail-pilot
  labels:
    mail-routing: "true"
  annotations:
    external-dns.alpha.kubernetes.io/hostname: "mail.jachym.dev"
spec:
  parentRefs:
    - name: mail-edge
      namespace: public-ingress-nginx
      sectionName: smtp-25
  rules:
    - backendRefs:
        - name: mail-smtp
          port: 25
```

Use this option when ListenerSet support is not available yet, or when a separate mail Gateway is operationally simpler in your cluster.

## Option B: ListenerSet target attached to public-edge

When standard ListenerSet support is available, keep the shared `public-edge` data plane and put the mail listener in a ListenerSet with its own target annotation. The TCP prober patches the ListenerSet:

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

The parent Gateway must allow ListenerSets. Add `allowedListeners` beside the listeners you already use; do not replace the current HTTP/S listeners with this minimal example:

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

Move the SMTP TCPRoute parent reference to the ListenerSet:

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: mail-pilot-smtp-inbound
  namespace: mail-pilot
  labels:
    mail-routing: "true"
  annotations:
    external-dns.alpha.kubernetes.io/hostname: "mail.jachym.dev"
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

When this route references `smtp-edge`, ExternalDNS can use the ListenerSet target annotation instead of the parent Gateway target annotation, so the SMTP prober can remove an unhealthy IP from `mail.jachym.dev` without changing the HTTP/S target list on `public-edge`.

## Other TCP service examples

Run a separate prober and target object for every DNS record that needs independent withdrawal:

```yaml
# SMTP inbound for mail.jachym.dev
- --target-name=smtp-edge
- --probe-mode=tcp
- --tcp-ports=25

# Submission for submission.jachym.dev
- --target-name=submission-edge
- --probe-mode=tcp
- --tcp-ports=587

# IMAPS for imaps.jachym.dev
- --target-name=imaps-edge
- --probe-mode=tcp
- --tcp-ports=993
```

JMAP over HTTPS should use `http` mode against a JMAP or application health endpoint, not `tcp` mode, when HTTP status is the desired health signal.

## ExternalDNS requirements

ExternalDNS needs the Gateway API sources for the route kinds you publish. For the examples above, HTTP/S records and SMTP TCPRoute records normally require:

```yaml
args:
  - --source=gateway-httproute
  - --source=gateway-tcproute
```

For ListenerSet target annotations, ExternalDNS must also watch ListenerSets:

```yaml
args:
  - --gateway-listener-sets
```

ExternalDNS RBAC for the Gateway API source should include Gateways, the route kinds, and ListenerSets when ListenerSet support is enabled:

```yaml
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["gateways", "listenersets", "httproutes", "tcproutes"]
  verbs: ["get", "list", "watch"]
```

Route annotations such as `external-dns.alpha.kubernetes.io/hostname` can provide hostnames for TCPRoute resources. Route-level `external-dns.alpha.kubernetes.io/target` annotations do not isolate Gateway API targets; use a dedicated Gateway or ListenerSet target annotation instead.

## TCP-connect limitations

TCP mode proves that the configured address and port accepted a connection from the prober. It does not prove SMTP correctness, STARTTLS support, IMAP greeting behavior, JMAP application health, authentication, or mail delivery.

TCP-connect can be misleading when a firewall, SYN proxy, load balancer, or TCP proxy accepts connections while the backend service is unavailable. For that topology, use TCP mode only as an edge-path signal and keep protocol correctness in separate observability.

For protected ports such as inbound SMTP with source allowlists, the prober source must be allowed to connect or the check will correctly mark that IP unhealthy from the prober's perspective.

## References

- Gateway API ListenerSet guide: https://gateway-api.sigs.k8s.io/guides/user-guides/listener-set/
- ExternalDNS Gateway API source notes: https://github.com/kubernetes-sigs/external-dns/blob/master/docs/sources/gateway-api.md
