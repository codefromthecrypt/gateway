// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package host

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/gateway/internal/envoygateway/config"
)

func TestMaybeGenerateCertificates_DirectoryExists(t *testing.T) {
	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "envoy")

	// Create directory to simulate existing certs
	require.NoError(t, os.MkdirAll(certPath, 0o750))

	err = maybeGenerateCertificates(cfg, certPath)
	require.NoError(t, err)

	// Directory should exist but no cert files should be created
	require.DirExists(t, certPath)
	require.NoFileExists(t, filepath.Join(certPath, "ca.crt"))
	require.NoFileExists(t, filepath.Join(certPath, "tls.crt"))
	require.NoFileExists(t, filepath.Join(certPath, "tls.key"))
}

func TestMaybeGenerateCertificates_DirectoryDoesNotExist(t *testing.T) {
	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "envoy")

	// Directory does not exist, should generate certificates
	err = maybeGenerateCertificates(cfg, certPath)
	require.NoError(t, err)

	// Verify cert files were created
	require.FileExists(t, filepath.Join(certPath, "ca.crt"))
	require.FileExists(t, filepath.Join(certPath, "tls.crt"))
	require.FileExists(t, filepath.Join(certPath, "tls.key"))
}

func TestMaybeGenerateCertificates_StatError(t *testing.T) {
	cfg, err := config.New(io.Discard, io.Discard)
	require.NoError(t, err)

	// Create an unreadable directory to trigger a stat error
	tmpDir := t.TempDir()
	unreadableDir := filepath.Join(tmpDir, "unreadable")
	require.NoError(t, os.MkdirAll(unreadableDir, 0o000))
	t.Cleanup(func() { os.Chmod(unreadableDir, 0o755) })

	certPath := filepath.Join(unreadableDir, "envoy")

	err = maybeGenerateCertificates(cfg, certPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to stat cert dir")
}
