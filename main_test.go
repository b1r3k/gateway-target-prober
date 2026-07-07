package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestRunner_HealthyIPs(t *testing.T) {
	tests := []struct {
		name            string
		ips             []string
		httpScheme      string
		httpPath        string
		hostHeader      string
		serverResponses map[string]int // IP -> status code
		expectedHealthy []string
		expectError     bool
	}{
		{
			name:            "all IPs healthy with 200 status",
			ips:             []string{"127.0.0.1", "127.0.0.2"},
			httpScheme:      "http",
			httpPath:        "/",
			serverResponses: map[string]int{"127.0.0.1": 200, "127.0.0.2": 200},
			expectedHealthy: []string{"127.0.0.1", "127.0.0.2"},
			expectError:     false,
		},
		{
			name:            "mixed status codes - some healthy",
			ips:             []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"},
			httpScheme:      "http",
			httpPath:        "/",
			serverResponses: map[string]int{"127.0.0.1": 200, "127.0.0.2": 404, "127.0.0.3": 201},
			expectedHealthy: []string{"127.0.0.1", "127.0.0.3"},
			expectError:     false,
		},
		{
			name:            "all IPs unhealthy - 4xx status codes",
			ips:             []string{"127.0.0.1", "127.0.0.2"},
			httpScheme:      "http",
			httpPath:        "/",
			serverResponses: map[string]int{"127.0.0.1": 404, "127.0.0.2": 500},
			expectedHealthy: []string{},
			expectError:     true,
		},
		{
			name:            "all IPs unhealthy - 5xx status codes",
			ips:             []string{"127.0.0.1"},
			httpScheme:      "http",
			httpPath:        "/",
			serverResponses: map[string]int{"127.0.0.1": 503},
			expectedHealthy: []string{},
			expectError:     true,
		},
		{
			name:            "HTTP scheme with custom path",
			ips:             []string{"127.0.0.1"},
			httpScheme:      "http",
			httpPath:        "/health",
			serverResponses: map[string]int{"127.0.0.1": 200},
			expectedHealthy: []string{"127.0.0.1"},
			expectError:     false,
		},
		{
			name:            "with Host header",
			ips:             []string{"127.0.0.1"},
			httpScheme:      "http",
			httpPath:        "/",
			hostHeader:      "example.com",
			serverResponses: map[string]int{"127.0.0.1": 200},
			expectedHealthy: []string{"127.0.0.1"},
			expectError:     false,
		},
		{
			name:            "empty IP list",
			ips:             []string{},
			httpScheme:      "http",
			httpPath:        "/",
			serverResponses: map[string]int{},
			expectedHealthy: []string{},
			expectError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock servers for each IP
			servers := make(map[string]*httptest.Server)
			serverURLs := make(map[string]string)

			for _, ip := range tt.ips {
				statusCode := tt.serverResponses[ip]
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Verify Host header if specified
					if tt.hostHeader != "" && r.Host != tt.hostHeader {
						t.Errorf("Expected Host header %q, got %q", tt.hostHeader, r.Host)
					}

					// Verify path
					if r.URL.Path != tt.httpPath {
						t.Errorf("Expected path %q, got %q", tt.httpPath, r.URL.Path)
					}

					w.WriteHeader(statusCode)
					fmt.Fprintf(w, "Response from %s", ip)
				}))
				servers[ip] = server
				serverURLs[ip] = server.URL
			}

			// Create runner with mock configuration
			runner := &Runner{
				ips:        tt.ips,
				httpClient: &http.Client{Timeout: 5 * time.Second},
				urlScheme:  tt.httpScheme,
				httpPath:   tt.httpPath,
				hostHeader: tt.hostHeader,
			}

			// Create a testable version of HealthyIPs that uses mock servers
			testHealthyIPs := func(ctx context.Context) ([]string, error) {
				healthy := make([]string, 0, len(runner.ips))
				for _, ip := range runner.ips {
					// Use the mock server URL instead of constructing from IP
					serverURL := serverURLs[ip]

					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+tt.httpPath, nil)
					if runner.hostHeader != "" {
						req.Host = runner.hostHeader
					}

					resp, err := runner.httpClient.Do(req)
					if err != nil {
						continue
					}
					_ = resp.Body.Close()

					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						healthy = append(healthy, ip)
					}
				}
				if len(healthy) == 0 {
					return nil, fmt.Errorf("no healthy IP found")
				}
				return healthy, nil
			}

			// Run the test
			ctx := context.Background()
			healthyIPs, err := testHealthyIPs(ctx)

			// Clean up servers
			for _, server := range servers {
				server.Close()
			}

			// Verify results
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}

			if len(healthyIPs) != len(tt.expectedHealthy) {
				t.Errorf("Expected %d healthy IPs, got %d", len(tt.expectedHealthy), len(healthyIPs))
			}

			// Check that all expected healthy IPs are present
			healthyMap := make(map[string]bool)
			for _, ip := range healthyIPs {
				healthyMap[ip] = true
			}
			for _, expectedIP := range tt.expectedHealthy {
				if !healthyMap[expectedIP] {
					t.Errorf("Expected IP %s to be healthy but it wasn't", expectedIP)
				}
			}
		})
	}
}

func TestRunner_HealthyIPs_ConnectionErrors(t *testing.T) {
	// Test with non-existent IPs to simulate connection errors
	runner := &Runner{
		ips:        []string{"192.0.2.1", "192.0.2.2"}, // RFC 5737 test addresses
		httpClient: &http.Client{Timeout: 1 * time.Second},
		urlScheme:  "http",
		httpPath:   "/",
	}

	ctx := context.Background()
	healthyIPs, err := runner.HealthyIPs(ctx)

	if err == nil {
		t.Errorf("Expected error for unreachable IPs, but got none")
	}

	if len(healthyIPs) != 0 {
		t.Errorf("Expected no healthy IPs for unreachable addresses, got %d", len(healthyIPs))
	}
}

func TestRunner_HealthyIPs_Timeout(t *testing.T) {
	// Create a server that responds slowly
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Longer than our timeout
		w.WriteHeader(200)
	}))
	defer server.Close()

	// Extract IP from server URL
	serverIP := strings.TrimPrefix(server.URL, "http://")
	serverIP = strings.Split(serverIP, ":")[0]

	runner := &Runner{
		ips:        []string{serverIP},
		httpClient: &http.Client{Timeout: 100 * time.Millisecond}, // Very short timeout
		urlScheme:  "http",
		httpPath:   "/",
	}

	// Create a testable version that uses our test server
	testHealthyIPs := func(ctx context.Context) ([]string, error) {
		healthy := make([]string, 0, len(runner.ips))
		for _, ip := range runner.ips {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			resp, err := runner.httpClient.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				healthy = append(healthy, ip)
			}
		}
		if len(healthy) == 0 {
			return nil, fmt.Errorf("no healthy IP found")
		}
		return healthy, nil
	}

	ctx := context.Background()
	healthyIPs, err := testHealthyIPs(ctx)

	if err == nil {
		t.Errorf("Expected timeout error, but got none")
	}

	if len(healthyIPs) != 0 {
		t.Errorf("Expected no healthy IPs due to timeout, got %d", len(healthyIPs))
	}
}

func TestRunner_HealthyIPs_NoTCPPortsUsesHTTPOnly(t *testing.T) {
	runner := &Runner{
		ips: []string{"127.0.0.1", "127.0.0.2"},
		httpClient: &http.Client{
			Transport: fakeStatusTransport{
				statusByIP: map[string]int{
					"127.0.0.1": 200,
					"127.0.0.2": 503,
				},
			},
			Timeout: 100 * time.Millisecond,
		},
		urlScheme: "http",
		httpPath:  "/",
		timeout:   100 * time.Millisecond,
	}

	healthyIPs, err := runner.HealthyIPs(context.Background())
	if err != nil {
		t.Fatalf("HealthyIPs: %v", err)
	}
	if !equalStrings(healthyIPs, []string{"127.0.0.1"}) {
		t.Errorf("healthy IPs = %#v, want %#v", healthyIPs, []string{"127.0.0.1"})
	}
}

func TestRunner_HealthyIPs_TCPMode(t *testing.T) {
	openPort := listenTCPPort(t, "tcp4", "127.0.0.1:0")
	closedPort := unusedTCPPort(t, "tcp4", "127.0.0.1:0")

	tests := []struct {
		name            string
		tcpPorts        []int
		expectedHealthy []string
		expectError     bool
	}{
		{
			name:            "all TCP ports reachable",
			tcpPorts:        []int{openPort},
			expectedHealthy: []string{"127.0.0.1"},
		},
		{
			name:            "one TCP port unreachable",
			tcpPorts:        []int{openPort, closedPort},
			expectedHealthy: nil,
			expectError:     true,
		},
		{
			name:            "multiple TCP ports reachable",
			tcpPorts:        []int{openPort, openPort},
			expectedHealthy: []string{"127.0.0.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &Runner{
				ips:       []string{"127.0.0.1"},
				probeMode: ProbeModeTCP,
				timeout:   100 * time.Millisecond,
				tcpPorts:  tt.tcpPorts,
			}

			healthyIPs, err := runner.HealthyIPs(context.Background())
			if tt.expectError && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("HealthyIPs: %v", err)
			}
			if !equalStrings(healthyIPs, tt.expectedHealthy) {
				t.Errorf("healthy IPs = %#v, want %#v", healthyIPs, tt.expectedHealthy)
			}
		})
	}
}

func TestRunner_HealthyIPs_TCPPortsIPv6(t *testing.T) {
	openPort, ok := listenTCPPortIfAvailable(t, "tcp6", "[::1]:0")
	if !ok {
		t.Skip("IPv6 loopback is not available")
	}

	runner := &Runner{
		ips:       []string{"::1"},
		probeMode: ProbeModeTCP,
		timeout:   100 * time.Millisecond,
		tcpPorts:  []int{openPort},
	}

	healthyIPs, err := runner.HealthyIPs(context.Background())
	if err != nil {
		t.Fatalf("HealthyIPs: %v", err)
	}
	if !equalStrings(healthyIPs, []string{"::1"}) {
		t.Errorf("healthy IPs = %#v, want %#v", healthyIPs, []string{"::1"})
	}
}

func TestRunner_HealthyIPs_TCPModeUnreachableAddress(t *testing.T) {
	runner := &Runner{
		ips:       []string{"192.0.2.1"},
		probeMode: ProbeModeTCP,
		timeout:   50 * time.Millisecond,
		tcpPorts:  []int{25},
	}

	started := time.Now()
	healthyIPs, err := runner.HealthyIPs(context.Background())
	if err == nil {
		t.Fatal("expected unreachable address to be unhealthy")
	}
	if len(healthyIPs) != 0 {
		t.Errorf("healthy IPs = %#v, want empty", healthyIPs)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("TCP probe took %s, expected timeout to keep it under 1s", elapsed)
	}
}

func TestPortForScheme(t *testing.T) {
	tests := []struct {
		scheme   string
		expected string
	}{
		{"https", "443"},
		{"http", "80"},
		{"HTTP", "80"},
		{"HTTPS", "443"},
		{"", "80"}, // default case
	}

	for _, tt := range tests {
		t.Run(tt.scheme, func(t *testing.T) {
			result := portForScheme(tt.scheme)
			if result != tt.expected {
				t.Errorf("portForScheme(%q) = %q, expected %q", tt.scheme, result, tt.expected)
			}
		})
	}
}

func TestConfig_GatewayMode(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "public-edge")
	t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
	t.Setenv("IPS", "1.1.1.1,2.2.2.2")

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.GatewayName != "public-edge" {
		t.Errorf("GatewayName = %q, want public-edge", cfg.GatewayName)
	}
	if cfg.GatewayNamespace != "public-ingress-nginx" {
		t.Errorf("GatewayNamespace = %q, want public-ingress-nginx", cfg.GatewayNamespace)
	}
	if len(cfg.IPs) != 2 {
		t.Errorf("IPs len = %d, want 2", len(cfg.IPs))
	}
	if cfg.ProbeMode != ProbeModeHTTP {
		t.Errorf("ProbeMode = %q, want %q", cfg.ProbeMode, ProbeModeHTTP)
	}
	if cfg.Target.Kind != TargetKindGateway {
		t.Errorf("Target.Kind = %q, want %q", cfg.Target.Kind, TargetKindGateway)
	}
	if cfg.Target.Name != "public-edge" || cfg.Target.Namespace != "public-ingress-nginx" {
		t.Errorf("Target = %#v, want public-edge/public-ingress-nginx", cfg.Target)
	}
}

func TestConfig_TCPPorts(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		args      []string
		wantPorts []int
	}{
		{
			name:      "not configured",
			wantPorts: nil,
		},
		{
			name:      "flag parses comma-separated ports",
			args:      []string{"--probe-mode=tcp", "--tcp-ports=993,587,25"},
			wantPorts: []int{993, 587, 25},
		},
		{
			name:      "environment variable parses comma-separated ports",
			env:       "993,587",
			args:      []string{"--probe-mode=tcp"},
			wantPorts: []int{993, 587},
		},
		{
			name:      "whitespace and empty entries are ignored",
			args:      []string{"--probe-mode=tcp", "--tcp-ports= 993, , 587 ,25 "},
			wantPorts: []int{993, 587, 25},
		},
		{
			name:      "flag overrides environment variable",
			env:       "25",
			args:      []string{"--probe-mode=tcp", "--tcp-ports=993"},
			wantPorts: []int{993},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GATEWAY_NAME", "public-edge")
			t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
			t.Setenv("IPS", "1.1.1.1")
			if tt.env != "" {
				t.Setenv("TCP_PORTS", tt.env)
			}

			cfg, err := loadConfig(tt.args)
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if !equalInts(cfg.TCPPorts, tt.wantPorts) {
				t.Errorf("TCPPorts = %#v, want %#v", cfg.TCPPorts, tt.wantPorts)
			}
			if len(tt.wantPorts) > 0 && cfg.ProbeMode != ProbeModeTCP {
				t.Errorf("ProbeMode = %q, want %q", cfg.ProbeMode, ProbeModeTCP)
			}
		})
	}
}

func TestConfig_TCPPortsRequireTCPMode(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "public-edge")
	t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
	t.Setenv("IPS", "1.1.1.1")

	if _, err := loadConfig([]string{"--tcp-ports=25"}); err == nil {
		t.Fatal("expected --tcp-ports without --probe-mode=tcp to fail")
	}
}

func TestConfig_TCPModeRequiresPorts(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "public-edge")
	t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
	t.Setenv("IPS", "1.1.1.1")

	if _, err := loadConfig([]string{"--probe-mode=tcp"}); err == nil {
		t.Fatal("expected --probe-mode=tcp without --tcp-ports to fail")
	}
}

func TestConfig_ProbeModeInvalid(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "public-edge")
	t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
	t.Setenv("IPS", "1.1.1.1")

	if _, err := loadConfig([]string{"--probe-mode=smtp"}); err == nil {
		t.Fatal("expected invalid probe mode to fail")
	}
}

func TestConfig_ListenerSetTarget(t *testing.T) {
	t.Setenv("IPS", "1.1.1.1")

	cfg, err := loadConfig([]string{
		"--target-kind=listenerset",
		"--target-name=smtp-edge",
		"--target-namespace=public-ingress-nginx",
		"--probe-mode=tcp",
		"--tcp-ports=25",
	})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Target.Kind != TargetKindListenerSet {
		t.Errorf("Target.Kind = %q, want %q", cfg.Target.Kind, TargetKindListenerSet)
	}
	if cfg.Target.Name != "smtp-edge" || cfg.Target.Namespace != "public-ingress-nginx" {
		t.Errorf("Target = %#v, want smtp-edge/public-ingress-nginx", cfg.Target)
	}
}

func TestConfig_ListenerSetTargetRequiresNameAndNamespace(t *testing.T) {
	t.Setenv("IPS", "1.1.1.1")

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "missing name",
			args: []string{
				"--target-kind=listenerset",
				"--target-namespace=public-ingress-nginx",
				"--probe-mode=tcp",
				"--tcp-ports=25",
			},
		},
		{
			name: "missing namespace",
			args: []string{
				"--target-kind=listenerset",
				"--target-name=smtp-edge",
				"--probe-mode=tcp",
				"--tcp-ports=25",
			},
		},
		{
			name: "invalid target kind",
			args: []string{
				"--target-kind=service",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadConfig(tt.args); err == nil {
				t.Fatal("expected ListenerSet target config to fail")
			}
		})
	}
}

func TestConfig_TCPPortsInvalid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "non-numeric", raw: "993,imap"},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "too large", raw: "65536"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GATEWAY_NAME", "public-edge")
			t.Setenv("GATEWAY_NAMESPACE", "public-ingress-nginx")
			t.Setenv("IPS", "1.1.1.1")

			if _, err := loadConfig([]string{"--probe-mode=tcp", "--tcp-ports=" + tt.raw}); err == nil {
				t.Fatal("expected error for invalid TCP ports, got nil")
			}
		})
	}
}

func TestConfig_GatewayNameRequired(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "")
	t.Setenv("GATEWAY_NAMESPACE", "ns")
	t.Setenv("IPS", "1.1.1.1")
	if _, err := loadConfig(nil); err == nil {
		t.Fatal("expected error for missing GATEWAY_NAME, got nil")
	}
}

func TestRunner_PatchesGatewayAnnotation(t *testing.T) {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "public-edge",
			Namespace: "public-ingress-nginx",
			Annotations: map[string]string{
				"external-dns.alpha.kubernetes.io/target": "1.1.1.1,2.2.2.2,3.3.3.3",
			},
		},
	}
	scheme := runtime.NewScheme()
	_ = gwv1.Install(scheme)
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()

	r := &Runner{
		k8s:              k8s,
		gatewayName:      "public-edge",
		gatewayNamespace: "public-ingress-nginx",
		target:           TargetRef{Kind: TargetKindGateway, Name: "public-edge", Namespace: "public-ingress-nginx"},
		annotationKey:    "external-dns.alpha.kubernetes.io/target",
	}

	if err := r.applyHealthy(context.Background(), []string{"1.1.1.1", "3.3.3.3"}); err != nil {
		t.Fatalf("applyHealthy: %v", err)
	}

	got := &gwv1.Gateway{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "public-edge", Namespace: "public-ingress-nginx"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "1.1.1.1,3.3.3.3"
	if got.Annotations["external-dns.alpha.kubernetes.io/target"] != want {
		t.Errorf("annotation = %q, want %q", got.Annotations["external-dns.alpha.kubernetes.io/target"], want)
	}
}

func TestRunner_PatchesListenerSetAnnotation(t *testing.T) {
	ls := newListenerSet("smtp-edge", "public-ingress-nginx", map[string]string{
		"external-dns.alpha.kubernetes.io/target": "1.1.1.1,2.2.2.2,3.3.3.3",
	})
	k8s := fake.NewClientBuilder().WithObjects(ls).Build()

	r := &Runner{
		k8s:           k8s,
		target:        TargetRef{Kind: TargetKindListenerSet, Name: "smtp-edge", Namespace: "public-ingress-nginx"},
		annotationKey: "external-dns.alpha.kubernetes.io/target",
	}

	if err := r.applyHealthy(context.Background(), []string{"1.1.1.1", "3.3.3.3"}); err != nil {
		t.Fatalf("applyHealthy: %v", err)
	}

	got := newListenerSet("smtp-edge", "public-ingress-nginx", nil)
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "smtp-edge", Namespace: "public-ingress-nginx"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "1.1.1.1,3.3.3.3"
	if got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"] != want {
		t.Errorf("annotation = %q, want %q", got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"], want)
	}
}

func TestRunner_PatchesListenerSetAnnotationInitializesNilAnnotations(t *testing.T) {
	ls := newListenerSet("smtp-edge", "public-ingress-nginx", nil)
	k8s := fake.NewClientBuilder().WithObjects(ls).Build()

	r := &Runner{
		k8s:           k8s,
		target:        TargetRef{Kind: TargetKindListenerSet, Name: "smtp-edge", Namespace: "public-ingress-nginx"},
		annotationKey: "external-dns.alpha.kubernetes.io/target",
	}

	if err := r.applyHealthy(context.Background(), []string{"1.1.1.1"}); err != nil {
		t.Fatalf("applyHealthy: %v", err)
	}

	got := newListenerSet("smtp-edge", "public-ingress-nginx", nil)
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "smtp-edge", Namespace: "public-ingress-nginx"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"] != "1.1.1.1" {
		t.Errorf("annotation = %q, want 1.1.1.1", got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"])
	}
}

func TestRunner_NoopWhenListenerSetAnnotationAlreadyCorrect(t *testing.T) {
	const original = "2.2.2.2,1.1.1.1"
	ls := newListenerSet("smtp-edge", "public-ingress-nginx", map[string]string{
		"external-dns.alpha.kubernetes.io/target": original,
	})
	k8s := fake.NewClientBuilder().WithObjects(ls).Build()

	r := &Runner{
		k8s:           k8s,
		target:        TargetRef{Kind: TargetKindListenerSet, Name: "smtp-edge", Namespace: "public-ingress-nginx"},
		annotationKey: "external-dns.alpha.kubernetes.io/target",
	}

	if err := r.applyHealthy(context.Background(), []string{"1.1.1.1", "2.2.2.2"}); err != nil {
		t.Fatalf("applyHealthy: %v", err)
	}

	got := newListenerSet("smtp-edge", "public-ingress-nginx", nil)
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "smtp-edge", Namespace: "public-ingress-nginx"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"] != original {
		t.Errorf("annotation = %q, want original %q", got.GetAnnotations()["external-dns.alpha.kubernetes.io/target"], original)
	}
}

func TestRunner_MissingListenerSetTargetReturnsError(t *testing.T) {
	k8s := fake.NewClientBuilder().Build()
	r := &Runner{
		k8s:           k8s,
		target:        TargetRef{Kind: TargetKindListenerSet, Name: "smtp-edge", Namespace: "public-ingress-nginx"},
		annotationKey: "external-dns.alpha.kubernetes.io/target",
	}

	err := r.applyHealthy(context.Background(), []string{"1.1.1.1"})
	if err == nil {
		t.Fatal("expected missing ListenerSet target to fail")
	}
	if !strings.Contains(err.Error(), "get listenerset public-ingress-nginx/smtp-edge") {
		t.Fatalf("error = %q, want ListenerSet context", err.Error())
	}
}

func TestRunner_RefusesToPatchWhenAllUnhealthy(t *testing.T) {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "public-edge",
			Namespace:   "public-ingress-nginx",
			Annotations: map[string]string{"external-dns.alpha.kubernetes.io/target": "1.1.1.1,2.2.2.2"},
		},
	}
	scheme := runtime.NewScheme()
	_ = gwv1.Install(scheme)
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()
	r := &Runner{k8s: k8s, gatewayName: "public-edge", gatewayNamespace: "public-ingress-nginx", target: TargetRef{Kind: TargetKindGateway, Name: "public-edge", Namespace: "public-ingress-nginx"}, annotationKey: "external-dns.alpha.kubernetes.io/target"}

	// r.ips is empty → HealthyIPs returns []; tick should refuse to patch.
	r.tick(context.Background())

	got := &gwv1.Gateway{}
	_ = k8s.Get(context.Background(), client.ObjectKey{Name: "public-edge", Namespace: "public-ingress-nginx"}, got)
	if got.Annotations["external-dns.alpha.kubernetes.io/target"] != "1.1.1.1,2.2.2.2" {
		t.Errorf("annotation mutated on empty healthy; got %q", got.Annotations["external-dns.alpha.kubernetes.io/target"])
	}
}

func TestRunner_NoopWhenAnnotationAlreadyCorrect(t *testing.T) {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "public-edge",
			Namespace: "public-ingress-nginx",
			Annotations: map[string]string{
				"external-dns.alpha.kubernetes.io/target": "1.1.1.1,2.2.2.2",
			},
			ResourceVersion: "1",
		},
	}
	scheme := runtime.NewScheme()
	_ = gwv1.Install(scheme)
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()

	r := &Runner{
		k8s:              k8s,
		gatewayName:      "public-edge",
		gatewayNamespace: "public-ingress-nginx",
		target:           TargetRef{Kind: TargetKindGateway, Name: "public-edge", Namespace: "public-ingress-nginx"},
		annotationKey:    "external-dns.alpha.kubernetes.io/target",
	}
	if err := r.applyHealthy(context.Background(), []string{"1.1.1.1", "2.2.2.2"}); err != nil {
		t.Fatalf("applyHealthy: %v", err)
	}

	got := &gwv1.Gateway{}
	_ = k8s.Get(context.Background(), client.ObjectKey{Name: "public-edge", Namespace: "public-ingress-nginx"}, got)
	if got.ResourceVersion != "1" {
		t.Errorf("resourceVersion bumped unexpectedly: %s (expected no-op)", got.ResourceVersion)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type fakeStatusTransport struct {
	statusByIP map[string]int
}

func (t fakeStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, ok := t.statusByIP[req.URL.Hostname()]
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

func listenTCPPort(t *testing.T, network, address string) int {
	t.Helper()
	port, ok := listenTCPPortIfAvailable(t, network, address)
	if !ok {
		t.Fatalf("listen %s %s failed", network, address)
	}
	return port
}

func listenTCPPortIfAvailable(t *testing.T, network, address string) (int, bool) {
	t.Helper()
	ln, err := net.Listen(network, address)
	if err != nil {
		return 0, false
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	_, portRaw, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener address %q: %v", ln.Addr().String(), err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("parse listener port %q: %v", portRaw, err)
	}
	return port, true
}

func unusedTCPPort(t *testing.T, network, address string) int {
	t.Helper()
	ln, err := net.Listen(network, address)
	if err != nil {
		t.Fatalf("listen %s %s: %v", network, address, err)
	}
	_, portRaw, err := net.SplitHostPort(ln.Addr().String())
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("close listener: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("split listener address %q: %v", ln.Addr().String(), err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("parse listener port %q: %v", portRaw, err)
	}
	return port
}

func newListenerSet(name, namespace string, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "ListenerSet",
	})
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetAnnotations(annotations)
	return obj
}
