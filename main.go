package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	networkingv1 "k8s.io/api/networking/v1"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	zap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var (
	// Version information set at build time via ldflags
	version = "dev"
	commit  = "unknown"
	date    = "unknown"

	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(gwv1.Install(scheme))
}

type Config struct {
	GatewayName         string
	GatewayNamespace    string
	AnnotationKey       string
	IPs                 []string
	HostHeader          string
	Interval            time.Duration
	Timeout             time.Duration
	HTTPPath            string
	HTTPScheme          string
	TCPPorts            []int
	InsecureSkipVerify  bool
	PrintVersionAndExit bool
}

// loadConfig parses the given args and env vars into a Config. It uses a
// fresh flag.FlagSet so it's safe to call multiple times (e.g., from tests).
// Pass os.Args[1:] from main(); pass nil from tests.
func loadConfig(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("gateway-target-prober", flag.ContinueOnError)
	fs.StringVar(&c.GatewayName, "gateway-name", getStr("GATEWAY_NAME", ""), "Gateway resource name to patch")
	fs.StringVar(&c.GatewayNamespace, "gateway-namespace", getStr("GATEWAY_NAMESPACE", ""), "Namespace of the Gateway resource")
	fs.StringVar(&c.AnnotationKey, "annotation-key", getStr("ANNOTATION_KEY", "external-dns.alpha.kubernetes.io/target"), "Annotation key to patch")
	ipsRaw := fs.String("ips", getStr("IPS", ""), "Comma-separated IPs to probe")
	fs.StringVar(&c.HostHeader, "host-header", getStr("HOST_HEADER", ""), "HTTP Host header for probes")
	fs.DurationVar(&c.Interval, "interval", getDuration("INTERVAL", 30*time.Second), "Probe interval")
	fs.DurationVar(&c.Timeout, "timeout", getDuration("TIMEOUT", 2*time.Second), "Per-probe timeout")
	fs.StringVar(&c.HTTPPath, "http-path", getStr("HTTP_PATH", "/"), "HTTP path to probe")
	fs.StringVar(&c.HTTPScheme, "http-scheme", getStr("HTTP_SCHEME", "http"), "http or https")
	tcpPortsRaw := fs.String("tcp-ports", getStr("TCP_PORTS", ""), "Comma-separated TCP ports to require after HTTP(S) probe")
	fs.BoolVar(&c.InsecureSkipVerify, "insecure-skip-verify", getBool("INSECURE_SKIP_VERIFY", false), "skip TLS verification")
	fs.BoolVar(&c.PrintVersionAndExit, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return c, err
	}

	// If version was requested, skip further validation; caller handles exit.
	if c.PrintVersionAndExit {
		return c, nil
	}

	if c.GatewayName == "" {
		return c, errors.New("--gateway-name (or GATEWAY_NAME) is required")
	}
	if c.GatewayNamespace == "" {
		return c, errors.New("--gateway-namespace (or GATEWAY_NAMESPACE) is required")
	}
	if *ipsRaw == "" {
		return c, errors.New("--ips (or IPS) is required")
	}
	c.IPs = splitAndTrim(*ipsRaw)
	if len(c.IPs) == 0 {
		return c, errors.New("--ips (or IPS) is required")
	}
	tcpPorts, err := parseTCPPorts(*tcpPortsRaw)
	if err != nil {
		return c, err
	}
	c.TCPPorts = tcpPorts
	return c, nil
}

type Runner struct {
	k8s              client.Client
	gatewayName      string
	gatewayNamespace string
	annotationKey    string
	ips              []string
	httpClient       *http.Client
	urlScheme        string
	httpPath         string
	hostHeader       string
	tcpPorts         []int
	interval         time.Duration
	timeout          time.Duration
}

func (r *Runner) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("runner started")

	t := time.NewTicker(r.interval)
	defer t.Stop()

	// run immediately at startup
	r.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) HealthyIPs(ctx context.Context) ([]string, error) {
	logger := log.FromContext(ctx)
	healthy := make([]string, 0, len(r.ips))
	for _, ip := range r.ips {
		u := fmt.Sprintf("%s://%s%s", r.urlScheme, net.JoinHostPort(ip, portForScheme(r.urlScheme)), r.httpPath)
		logger.Info("probing IP", "ip", ip, "url", u)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)

		// Set Host header if specified
		if r.hostHeader != "" {
			req.Host = r.hostHeader
			logger.Info("setting Host header", "ip", ip, "host", r.hostHeader)
		}

		// Create a custom HTTP client for this request if we need to skip TLS verification for IP addresses
		client := r.httpClient
		if r.urlScheme == "https" && isIPAddress(ip) {
			// For HTTPS requests to IP addresses, we need to skip TLS verification
			// because certificates are typically issued for hostnames, not IP addresses.
			// ServerName (SNI) is set from hostHeader so SNI-based routing on the
			// server (e.g. envoy filter chains) picks the right virtual host —
			// Go's default sets SNI from the URL host, which for an IP is empty.
			tlsCfg := &tls.Config{InsecureSkipVerify: true}
			if r.hostHeader != "" {
				tlsCfg.ServerName = r.hostHeader
			}
			tr := &http.Transport{
				TLSClientConfig: tlsCfg,
			}
			client = &http.Client{
				Transport: tr,
				Timeout:   r.httpClient.Timeout,
			}
			logger.Info("skipped TLS verification for IP address", "ip", ip, "sni", tlsCfg.ServerName)
		}

		resp, err := client.Do(req)
		if err != nil {
			logger.Info("HTTP request failed", "ip", ip, "url", u, "error", err.Error())
			continue
		}
		_ = resp.Body.Close()
		logger.Info("HTTP response received", "ip", ip, "url", u, "status_code", resp.StatusCode)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if !r.tcpPortsHealthy(ctx, ip) {
				continue
			}
			healthy = append(healthy, ip)
			logger.Info("IP marked as healthy", "ip", ip)
		} else {
			logger.Info("IP marked as unhealthy due to status code", "ip", ip, "status_code", resp.StatusCode)
		}
	}
	if len(healthy) == 0 {
		return nil, fmt.Errorf("no healthy IP found")
	}
	return healthy, nil
}

func (r *Runner) tcpPortsHealthy(ctx context.Context, ip string) bool {
	logger := log.FromContext(ctx)
	for _, port := range r.tcpPorts {
		address := net.JoinHostPort(ip, strconv.Itoa(port))
		dialer := net.Dialer{Timeout: r.probeTimeout()}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			logger.Info("tcp probe failed", "ip", ip, "port", port, "error", err.Error())
			logger.Info("IP marked as unhealthy due to required TCP port failure", "ip", ip, "port", port)
			return false
		}
		_ = conn.Close()
		logger.Info("tcp probe succeeded", "ip", ip, "port", port)
	}
	return true
}

func (r *Runner) probeTimeout() time.Duration {
	if r.timeout > 0 {
		return r.timeout
	}
	if r.httpClient != nil && r.httpClient.Timeout > 0 {
		return r.httpClient.Timeout
	}
	return 0
}

func portForScheme(s string) string {
	if strings.ToLower(s) == "https" {
		return "443"
	}
	return "80"
}

func (r *Runner) tick(ctx context.Context) {
	logger := log.FromContext(ctx)
	healthy, err := r.HealthyIPs(ctx)
	if err != nil {
		logger.Info("probe error", "err", err)
	}
	if len(healthy) == 0 {
		logger.Info("no healthy IPs detected — refusing to patch (would blackhole DNS)", "candidates", r.ips)
		return
	}
	if err := r.applyHealthy(ctx, healthy); err != nil {
		logger.Error(err, "failed to patch gateway annotation")
	}
}

func (r *Runner) applyHealthy(ctx context.Context, healthy []string) error {
	desired := strings.Join(healthy, ",")
	gw := &gwv1.Gateway{}
	key := client.ObjectKey{Name: r.gatewayName, Namespace: r.gatewayNamespace}
	if err := r.k8s.Get(ctx, key, gw); err != nil {
		return fmt.Errorf("get gateway: %w", err)
	}
	if normalizeIPList(gw.Annotations[r.annotationKey]) == normalizeIPList(desired) {
		return nil
	}
	patch := client.MergeFrom(gw.DeepCopy())
	if gw.Annotations == nil {
		gw.Annotations = map[string]string{}
	}
	gw.Annotations[r.annotationKey] = desired
	return r.k8s.Patch(ctx, gw, patch)
}

func parseEnvOrFlag(name string, fallback *string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return *fallback
}

// VersionInfo returns version information as a string
func VersionInfo() string {
	return fmt.Sprintf("version=%s commit=%s date=%s", version, commit, date)
}

func main() {
	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
		setupLog := ctrl.Log.WithName("gateway-target-prober")
		setupLog.Error(err, "config")
		os.Exit(1)
	}

	if cfg.PrintVersionAndExit {
		fmt.Println(VersionInfo())
		os.Exit(0)
	}

	// Initialize logger before deriving any named loggers
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	ctx := ctrl.SetupSignalHandler()
	logger := ctrl.Log.WithName("gateway-target-prober")
	ctx = log.IntoContext(ctx, logger)

	kubeCfg := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(kubeCfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		LeaderElection:         false, // set true for HA
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify},
	}
	httpClient := &http.Client{
		Transport: tr,
		Timeout:   cfg.Timeout,
	}

	r := &Runner{
		k8s:              mgr.GetClient(),
		gatewayName:      cfg.GatewayName,
		gatewayNamespace: cfg.GatewayNamespace,
		annotationKey:    cfg.AnnotationKey,
		ips:              cfg.IPs,
		httpClient:       httpClient,
		urlScheme:        cfg.HTTPScheme,
		httpPath:         cfg.HTTPPath,
		hostHeader:       cfg.HostHeader,
		tcpPorts:         cfg.TCPPorts,
		interval:         cfg.Interval,
		timeout:          cfg.Timeout,
	}

	if err := mgr.Add(r); err != nil {
		logger.Error(err, "unable to add runner")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting manager",
		"version", version,
		"commit", commit,
		"build_date", date,
		"gateway_name", cfg.GatewayName,
		"gateway_namespace", cfg.GatewayNamespace,
		"annotation", r.annotationKey,
		"ips", strings.Join(cfg.IPs, ","),
		"path", cfg.HTTPPath,
		"interval", r.interval.String(),
		"scheme", cfg.HTTPScheme,
		"host_header", cfg.HostHeader,
		"tcp_ports", intsToCSV(cfg.TCPPorts),
	)
	if err := mgr.Start(ctx); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getStr(env string, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}
func getDuration(env string, fallback time.Duration) time.Duration {
	if v := os.Getenv(env); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}
func getBool(env string, fallback bool) bool {
	if v := os.Getenv(env); v != "" {
		l := strings.ToLower(v)
		return l == "1" || l == "true" || l == "yes"
	}
	return fallback
}
func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
func intsToCSV(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
func parseTCPPorts(csv string) ([]int, error) {
	if strings.TrimSpace(csv) == "" {
		return nil, nil
	}
	parts := splitAndTrim(csv)
	ports := make([]int, 0, len(parts))
	for _, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid TCP port %q", part)
		}
		ports = append(ports, port)
	}
	return ports, nil
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isIPAddress checks if the given string is a valid IP address (IPv4 or IPv6)
func isIPAddress(s string) bool {
	return net.ParseIP(s) != nil
}

// normalizeIPList normalizes a comma-separated list of IPs by sorting them
// This ensures that IP order doesn't matter when comparing lists
func normalizeIPList(ipList string) string {
	if ipList == "" {
		return ""
	}

	ips := splitAndTrim(ipList)
	sort.Strings(ips)
	return strings.Join(ips, ",")
}
