package chain

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/timeflareio/guardian/internal/config"
)

// Dial opens the gRPC connection to the configured endpoint.
//
// Every gRPC connection this repository makes comes through here, daemon and
// operator tool alike. Two places deciding transport security is the duplication
// where one of them gets missed, and the one that gets missed is the one nobody
// is looking at.
//
// The dial is lazy: grpc.NewClient returns before any connection attempt, so a
// wrong endpoint or a failed handshake surfaces at the first call rather than
// here.
func Dial(cfg *config.Config, logger *zap.Logger) (*grpc.ClientConn, error) {
	creds, err := transportCredentials(cfg, logger)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(cfg.GRPCEndpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client for %s: %w", cfg.GRPCEndpoint, err)
	}
	return conn, nil
}

// transportCredentials builds the credentials for the configured endpoint.
//
// Plaintext is the default, and it is the right one for the colocated node most
// guardians run: a loopback connection has nothing to protect, and defaulting to
// TLS would break every local deployment for no gain. Selecting a non-local
// network from the chain's registry turns grpc_tls on, so the setting arrives
// with the deployment that needs it.
//
// What crosses this connection is worth stating, because it is not only queries:
// the gas figure the daemon signs against, the guardian record the startup
// self-check compares to, the key-epoch history that decides which private key
// decrypts a share, and every outbound signed transaction.
func transportCredentials(cfg *config.Config, logger *zap.Logger) (credentials.TransportCredentials, error) {
	if !cfg.GRPCTLS {
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.GRPCTLSInsecureSkipVerify, //nolint:gosec // operator-set, warned about below, and the alternative operators reach for is full plaintext
	}

	if cfg.GRPCTLSCAFile != "" {
		pem, err := os.ReadFile(cfg.GRPCTLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read grpc_tls_ca_file %s: %w", cfg.GRPCTLSCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("grpc_tls_ca_file %s contains no certificates", cfg.GRPCTLSCAFile)
		}
		tlsCfg.RootCAs = pool
	}

	// Loudly, and every time. A skipped verification looks identical to a working
	// one from the outside, which is what makes it worth saying out loud rather
	// than leaving in the configuration for someone to notice.
	if cfg.GRPCTLSInsecureSkipVerify && logger != nil {
		logger.Warn("gRPC certificate verification is DISABLED — the connection is encrypted but the node is unauthenticated, so anyone able to intercept it can impersonate the chain",
			zap.String("grpc_endpoint", cfg.GRPCEndpoint),
			zap.String("remedy", "unset grpc_tls_insecure_skip_verify and set grpc_tls_ca_file to the node's CA"))
	}

	return credentials.NewTLS(tlsCfg), nil
}
