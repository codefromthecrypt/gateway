// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package host

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	func_e "github.com/tetratelabs/func-e"
	"github.com/tetratelabs/func-e/api"
	"k8s.io/utils/ptr"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/envoyproxy/gateway/internal/crypto"
	"github.com/envoyproxy/gateway/internal/envoygateway/config"
	"github.com/envoyproxy/gateway/internal/infrastructure/common"
	"github.com/envoyproxy/gateway/internal/ir"
	"github.com/envoyproxy/gateway/internal/logging"
	"github.com/envoyproxy/gateway/internal/utils"
	"github.com/envoyproxy/gateway/internal/utils/file"
	"github.com/envoyproxy/gateway/internal/xds/bootstrap"
	testutils "github.com/envoyproxy/gateway/test/utils"
)

func newMockInfra(t *testing.T, cfg *config.Server) *Infra {
	t.Helper()
	homeDir := t.TempDir()
	// Create envoy certs under home dir.
	certs, err := crypto.GenerateCerts(cfg)
	require.NoError(t, err)
	// Write certs into proxy dir.
	proxyDir := path.Join(homeDir, "envoy")
	err = file.WriteDir(certs.CACertificate, proxyDir, "ca.crt")
	require.NoError(t, err)
	err = file.WriteDir(certs.EnvoyCertificate, proxyDir, "tls.crt")
	require.NoError(t, err)
	err = file.WriteDir(certs.EnvoyPrivateKey, proxyDir, "tls.key")
	require.NoError(t, err)
	// Write sds config as well.
	err = createSdsConfig(proxyDir)
	require.NoError(t, err)

	paths := &Paths{
		ConfigHome: homeDir,
		DataHome:   homeDir,
		StateHome:  homeDir,
		RuntimeDir: homeDir,
	}
	infra := &Infra{
		Paths:         paths,
		Logger:        logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo),
		EnvoyGateway:  cfg.EnvoyGateway,
		sdsConfigPath: proxyDir,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	}
	return infra
}

func TestInfraCreateProxy(t *testing.T) {
	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)
	infra := newMockInfra(t, cfg)

	testCases := []struct {
		name          string
		infra         *ir.Infra
		expectedError string
	}{
		{
			name:          "nil cfg",
			infra:         nil,
			expectedError: "infra ir is nil",
		},
		{
			name: "nil proxy",
			infra: &ir.Infra{
				Proxy: nil,
			},
			expectedError: "infra proxy ir is nil",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := infra.CreateOrUpdateProxyInfra(t.Context(), tc.infra)
			require.EqualError(t, actual, tc.expectedError)
		})
	}
}

func TestInfra_CreateOrUpdateProxyInfra_Success(t *testing.T) {
	tmpdir := t.TempDir()
	// Ensures that all the required binaries are available.
	err := func_e.Run(t.Context(), []string{"--version"}, api.HomeDir(tmpdir))
	require.NoError(t, err)

	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)
	infra := newMockInfra(t, cfg)

	testCases := []struct {
		name              string
		proxyName         string
		expectProxyLoaded bool
	}{
		{
			name:              "create new proxy",
			proxyName:         "test-proxy",
			expectProxyLoaded: true,
		},
		{
			name:              "idempotent - proxy already exists",
			proxyName:         "test-proxy-idempotent",
			expectProxyLoaded: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			infraIR := &ir.Infra{
				Proxy: &ir.ProxyInfra{
					Name:      tc.proxyName,
					Namespace: "default",
					Config: &egv1a1.EnvoyProxy{
						Spec: egv1a1.EnvoyProxySpec{
							Logging: egv1a1.ProxyLogging{
								Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
									egv1a1.LogComponentDefault: egv1a1.LogLevelInfo,
								},
							},
						},
					},
				},
			}

			hashedName := utils.GetHashedName(tc.proxyName, 64)
			t.Cleanup(func() { infra.stopEnvoy(hashedName) })

			// First call should create the proxy
			actual := infra.CreateOrUpdateProxyInfra(t.Context(), infraIR)
			require.NoError(t, actual)

			// Verify proxy context was stored
			_, loaded := infra.proxyContextMap.Load(hashedName)
			require.Equal(t, tc.expectProxyLoaded, loaded)

			// Second call should be idempotent (early return)
			actual = infra.CreateOrUpdateProxyInfra(t.Context(), infraIR)
			require.NoError(t, actual)

			// Verify proxy is still loaded
			_, loaded = infra.proxyContextMap.Load(hashedName)
			require.Equal(t, tc.expectProxyLoaded, loaded)
		})
	}
}

func TestInfra_DeleteProxyInfra(t *testing.T) {
	tmpdir := t.TempDir()
	// Ensures that all the required binaries are available.
	err := func_e.Run(t.Context(), []string{"--version"}, api.HomeDir(tmpdir))
	require.NoError(t, err)

	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)
	infra := newMockInfra(t, cfg)

	testCases := []struct {
		name          string
		setupProxy    bool
		proxyName     string
		infraIR       *ir.Infra
		expectedError string
		expectRemoved bool
	}{
		{
			name:       "delete existing proxy",
			setupProxy: true,
			proxyName:  "test-proxy-delete",
			infraIR: &ir.Infra{
				Proxy: &ir.ProxyInfra{
					Name:      "test-proxy-delete",
					Namespace: "default",
					Config: &egv1a1.EnvoyProxy{
						Spec: egv1a1.EnvoyProxySpec{
							Logging: egv1a1.ProxyLogging{
								Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
									egv1a1.LogComponentDefault: egv1a1.LogLevelInfo,
								},
							},
						},
					},
				},
			},
			expectedError: "",
			expectRemoved: true,
		},
		{
			name:       "delete non-existent proxy",
			setupProxy: false,
			proxyName:  "non-existent-proxy",
			infraIR: &ir.Infra{
				Proxy: &ir.ProxyInfra{
					Name:      "non-existent-proxy",
					Namespace: "default",
					Config:    &egv1a1.EnvoyProxy{},
				},
			},
			expectedError: "",
			expectRemoved: false,
		},
		{
			name:          "nil infra",
			setupProxy:    false,
			infraIR:       nil,
			expectedError: "infra ir is nil",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var hashedName string
			if tc.setupProxy {
				// Create a proxy first
				actual := infra.CreateOrUpdateProxyInfra(t.Context(), tc.infraIR)
				require.NoError(t, actual)

				hashedName = utils.GetHashedName(tc.proxyName, 64)
				t.Cleanup(func() { infra.stopEnvoy(hashedName) })

				_, loaded := infra.proxyContextMap.Load(hashedName)
				require.True(t, loaded, "proxy should be loaded before deletion")
			}

			// Delete the proxy
			actual := infra.DeleteProxyInfra(t.Context(), tc.infraIR)
			if tc.expectedError != "" {
				require.EqualError(t, actual, tc.expectedError)
			} else {
				require.NoError(t, actual)
			}

			// Verify deletion
			if tc.expectRemoved {
				_, loaded := infra.proxyContextMap.Load(hashedName)
				require.False(t, loaded, "proxy should be removed after deletion")
			}
		})
	}
}

func TestExtractSemver(t *testing.T) {
	tests := []struct {
		image   string
		want    string
		wantErr bool
	}{
		{"docker.io/envoyproxy/envoy:distroless-v1.35.0", "1.35.0", false},
		{"envoyproxy/envoy:v1.28.1", "1.28.1", false},
		{"envoyproxy/envoy:latest", "", true},
		{"envoyproxy/envoy", "", true},
		{"envoyproxy/envoy:distroless-v1.35", "", true},
		{"envoyproxy/envoy:distroless-v1.35.0-extra", "1.35.0", false},
		{"envoyproxy/envoy:1.2.3", "1.2.3", false},
		{"envoyproxy/envoy:foo-2.3.4-bar", "2.3.4", false},
	}
	for _, tc := range tests {
		t.Run(tc.image, func(t *testing.T) {
			got, err := extractSemver(tc.image)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

// TestInfra_runEnvoy verifies Envoy process lifecycle, output redirection, and XDG directory usage.
func TestInfra_runEnvoy(t *testing.T) {
	// Create separate XDG directories
	baseDir := t.TempDir()
	configHome := path.Join(baseDir, "config")
	dataHome := path.Join(baseDir, "data")
	stateHome := path.Join(baseDir, "state")
	runtimeDir := path.Join(baseDir, "runtime")

	// Create separate buffers for stdout and stderr
	buffers := testutils.DumpLogsOnFail(t, "stdout", "stderr")
	stdout := buffers[0]
	stderr := buffers[1]

	paths := &Paths{
		ConfigHome: configHome,
		DataHome:   dataHome,
		StateHome:  stateHome,
		RuntimeDir: runtimeDir,
	}
	i := &Infra{
		Paths:  paths,
		Logger: logging.DefaultLogger(stdout, egv1a1.LogLevelInfo),
		Stdout: stdout,
		Stderr: stderr,
	}

	// Run envoy once to let func-e set up all XDG directories
	args := []string{
		"--config-yaml",
		"admin: {address: {socket_address: {address: '127.0.0.1', port_value: 9901}}}",
	}
	i.runEnvoy(t.Context(), "", "test", args)
	_, ok := i.proxyContextMap.Load("test")
	require.True(t, ok, "expected proxy context to be stored")

	// Wait for func-e to create all XDG directories
	require.Eventually(t, func() bool {
		_, err := os.Stat(path.Join(configHome, "envoy-version"))
		return err == nil
	}, 5*time.Second, 100*time.Millisecond, "envoy-version file should be created in configHome")

	i.stopEnvoy("test")
	_, ok = i.proxyContextMap.Load("test")
	require.False(t, ok, "expected proxy context to be removed")

	t.Run("xdg_directory_state", func(t *testing.T) {
		// Verify XDG directories were created at configured paths by func-e
		// This proves the Paths configuration was properly propagated to func-e API

		// ConfigHome must exist with envoy-version file
		require.DirExists(t, configHome, "configHome should exist at configured path")
		require.FileExists(t, path.Join(configHome, "envoy-version"), "envoy-version file should exist in configHome")

		// DataHome must exist with envoy-versions subdirectory for downloaded binaries
		require.DirExists(t, dataHome, "dataHome should exist at configured path")
		require.DirExists(t, path.Join(dataHome, "envoy-versions"), "envoy-versions dir should exist under dataHome")

		// StateHome must exist with envoy-runs subdirectory for per-run logs
		require.DirExists(t, stateHome, "stateHome should exist at configured path")
		require.DirExists(t, path.Join(stateHome, "envoy-runs"), "envoy-runs dir should exist under stateHome")

		// RuntimeDir must exist - func-e creates runID subdirectories with admin-address.txt
		require.DirExists(t, runtimeDir, "runtimeDir should exist at configured path")

		// Verify each XDG directory is separate (not the same path)
		require.NotEqual(t, configHome, dataHome, "configHome and dataHome must be different")
		require.NotEqual(t, dataHome, stateHome, "dataHome and stateHome must be different")
		require.NotEqual(t, stateHome, runtimeDir, "stateHome and runtimeDir must be different")
	})

	t.Run("output_redirection", func(t *testing.T) {
		// Verify output was captured in buffers (not os.Stdout/Stderr)
		totalOutput := stdout.Len() + stderr.Len()
		require.Positive(t, totalOutput, "expected some output to be captured in stdout or stderr buffers")
	})

	t.Run("stop_start_cycle", func(t *testing.T) {
		// Ensures that run -> stop cycle works multiple times without issues
		for range 5 {
			args := []string{
				"--config-yaml",
				"admin: {address: {socket_address: {address: '127.0.0.1', port_value: 9901}}}",
			}
			i.runEnvoy(t.Context(), "", "test", args)
			require.Len(t, i.proxyContextMap, 1)
			i.stopEnvoy("test")
			require.Empty(t, i.proxyContextMap)
			// If the cleanup didn't work, the error due to "address already in use" will be
			// tried to be written to the nil logger, which will panic.
		}
	})
}

func TestGetEnvoyVersion(t *testing.T) {
	tests := []struct {
		name         string
		defaultImage string
		provider     *egv1a1.EnvoyProxyProvider
		want         string
	}{
		{
			name:         "k8s provider default release version",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-v1.35.0",
			provider:     egv1a1.DefaultEnvoyProxyProvider(),
			want:         "1.35.0",
		},
		{
			name:         "k8s provider dev version",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-dev",
			provider:     egv1a1.DefaultEnvoyProxyProvider(),
			want:         "",
		},
		{
			name:         "host provider envoy version unset",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-v1.35.0",
			provider: &egv1a1.EnvoyProxyProvider{
				Type: egv1a1.EnvoyProxyProviderTypeHost,
				Host: &egv1a1.EnvoyProxyHostProvider{},
			},
			want: "1.35.0",
		},
		{
			name:         "host provider envoy version empty",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-v1.35.0",
			provider: &egv1a1.EnvoyProxyProvider{
				Type: egv1a1.EnvoyProxyProviderTypeHost,
				Host: &egv1a1.EnvoyProxyHostProvider{EnvoyVersion: ptr.To("")},
			},
			want: "1.35.0",
		},
		{
			name:         "host provider envoy version unset dev version",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-dev",
			provider: &egv1a1.EnvoyProxyProvider{
				Type: egv1a1.EnvoyProxyProviderTypeHost,
				Host: &egv1a1.EnvoyProxyHostProvider{},
			},
			want: "",
		},
		{
			name:         "host provider envoy version empty dev version",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-dev",
			provider: &egv1a1.EnvoyProxyProvider{
				Type: egv1a1.EnvoyProxyProviderTypeHost,
				Host: &egv1a1.EnvoyProxyHostProvider{EnvoyVersion: ptr.To("")},
			},
			want: "",
		},
		{
			name:         "host provider envoy version custom",
			defaultImage: "docker.io/envoyproxy/envoy:distroless-v1.35.0",
			provider: &egv1a1.EnvoyProxyProvider{
				Type: egv1a1.EnvoyProxyProviderTypeHost,
				Host: &egv1a1.EnvoyProxyHostProvider{EnvoyVersion: ptr.To("1.2.3")},
			},
			want: "1.2.3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			infra := &Infra{defaultEnvoyImage: tc.defaultImage}
			proxyConfig := &egv1a1.EnvoyProxy{
				Spec: egv1a1.EnvoyProxySpec{
					Provider: tc.provider,
				},
			}
			require.Equal(t, tc.want, infra.getEnvoyVersion(proxyConfig))
		})
	}
}

// TestTopologyInjectorDisabledInHostMode verifies we don't cause a 15+ second
// startup delay in standalone mode as Envoy waits for endpoint discovery.
// See: https://github.com/envoyproxy/gateway/issues/7080
func TestNewInfra(t *testing.T) {
	// This test verifies successful creation of Infra using a temp directory.
	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)

	// Create a temp directory with certificates for testing
	tmpdir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpdir)
	t.Setenv("XDG_DATA_HOME", tmpdir)

	// Generate and write test certificates
	certs, err := crypto.GenerateCerts(cfg)
	require.NoError(t, err)
	certPath := filepath.Join(tmpdir, "envoy-gateway", "envoy")
	require.NoError(t, os.MkdirAll(certPath, 0o750))
	require.NoError(t, file.Write(string(certs.CACertificate), filepath.Join(certPath, "ca.crt")))
	require.NoError(t, file.Write(string(certs.EnvoyCertificate), filepath.Join(certPath, "tls.crt")))
	require.NoError(t, file.Write(string(certs.EnvoyPrivateKey), filepath.Join(certPath, "tls.key")))

	actual, err := NewInfra(t.Context(), cfg, logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo))
	require.NoError(t, err)
	require.NotNil(t, actual)
	require.NotNil(t, actual.Paths)
	require.Equal(t, certPath, actual.sdsConfigPath)
	require.NotNil(t, actual.Logger)
	require.NotNil(t, actual.EnvoyGateway)
	require.Equal(t, egv1a1.DefaultEnvoyProxyImage, actual.defaultEnvoyImage)
	require.NotNil(t, actual.Stdout)
	require.NotNil(t, actual.Stderr)
}

func TestCreateSdsConfig(t *testing.T) {
	dir := t.TempDir()
	// Create required cert files
	require.NoError(t, file.WriteDir([]byte("test ca"), dir, XdsTLSCaFilename))
	require.NoError(t, file.WriteDir([]byte("test cert"), dir, XdsTLSCertFilename))
	require.NoError(t, file.WriteDir([]byte("test key"), dir, XdsTLSKeyFilename))

	actual := createSdsConfig(dir)
	require.NoError(t, actual)

	// Verify CA config was created
	caConfigPath := filepath.Join(dir, common.SdsCAFilename)
	actualCAConfig, err := os.ReadFile(caConfigPath)
	require.NoError(t, err)
	require.NotEmpty(t, actualCAConfig)

	// Verify cert config was created
	certConfigPath := filepath.Join(dir, common.SdsCertFilename)
	actualCertConfig, err := os.ReadFile(certConfigPath)
	require.NoError(t, err)
	require.NotEmpty(t, actualCertConfig)
}

func TestTopologyInjectorDisabledInHostMode(t *testing.T) {
	testCases := []struct {
		name                          string
		topologyInjectorDisabled      bool
		expectLocalClusterInBootstrap bool
	}{
		{
			name:                          "topology injector enabled",
			topologyInjectorDisabled:      false,
			expectLocalClusterInBootstrap: true,
		},
		{
			name:                          "topology injector disabled",
			topologyInjectorDisabled:      true,
			expectLocalClusterInBootstrap: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			proxyInfra := &ir.ProxyInfra{
				Name:      "test-proxy",
				Namespace: "default",
				Config: &egv1a1.EnvoyProxy{
					Spec: egv1a1.EnvoyProxySpec{
						Logging: egv1a1.ProxyLogging{
							Level: map[egv1a1.ProxyLogComponent]egv1a1.LogLevel{
								egv1a1.LogComponentDefault: egv1a1.LogLevelInfo,
							},
						},
					},
				},
			}

			bootstrapConfigOptions := &bootstrap.RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{
						Disable: true,
					},
				},
				XdsServerHost:            ptr.To("0.0.0.0"),
				AdminServerPort:          ptr.To(int32(0)),
				StatsServerPort:          ptr.To(int32(0)),
				TopologyInjectorDisabled: tc.topologyInjectorDisabled,
			}

			args, err := common.BuildProxyArgs(proxyInfra, nil, bootstrapConfigOptions, "test-node", false)
			require.NoError(t, err)

			// Extract the bootstrap YAML from args (it's after --config-yaml)
			var bootstrapYAML string
			for i, arg := range args {
				if arg == "--config-yaml" && i+1 < len(args) {
					bootstrapYAML = args[i+1]
					break
				}
			}
			require.NotEmpty(t, bootstrapYAML, "bootstrap YAML not found in args")

			if tc.expectLocalClusterInBootstrap {
				require.Contains(t, bootstrapYAML, "local_cluster_name:")
			} else {
				require.NotContains(t, bootstrapYAML, "local_cluster_name:")
			}
		})
	}
}
