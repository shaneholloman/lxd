package shared

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// connectErrorPrefix used as prefix to error returned from RFC3493Dialer.
const connectErrorPrefix = "Unable to connect to"

// RFC3493Dialer connects to the specified server and returns the connection.
// If the connection cannot be established then an error with the connectErrorPrefix is returned.
func RFC3493Dialer(context context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}

	var errs []error
	for _, a := range addrs {
		c, err := net.DialTimeout(network, net.JoinHostPort(a, port), 10*time.Second)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		tc, ok := c.(*net.TCPConn)
		if ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(3 * time.Second)
		}

		return c, nil
	}

	return nil, fmt.Errorf("%s: %s (%v)", connectErrorPrefix, address, errs)
}

// IsConnectionError returns true if the given error is due to the dialer not being able to connect to the target
// LXD server.
func IsConnectionError(err error) bool {
	// FIXME: unfortunately the LXD client currently does not provide a way to differentiate between errors.
	return strings.Contains(err.Error(), connectErrorPrefix)
}

// InitTLSConfig returns a tls.Config populated with default encryption
// parameters. This is used as baseline config for both client and server
// certificates used by LXD.
func InitTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
}

func finalizeTLSConfig(tlsConfig *tls.Config, tlsRemoteCert *x509.Certificate) {
	// Setup RootCA
	if tlsConfig.RootCAs == nil {
		tlsConfig.RootCAs, _ = systemCertPool()
	}

	// Trusted certificates
	if tlsRemoteCert != nil {
		if tlsConfig.RootCAs == nil {
			tlsConfig.RootCAs = x509.NewCertPool()
		}

		// Make it a valid RootCA
		tlsRemoteCert.IsCA = true
		tlsRemoteCert.KeyUsage = x509.KeyUsageCertSign

		// Setup the pool
		tlsConfig.RootCAs.AddCert(tlsRemoteCert)

		// Set the ServerName
		if tlsRemoteCert.DNSNames != nil {
			tlsConfig.ServerName = tlsRemoteCert.DNSNames[0]
		}
	}
}

// GetTLSConfig returns a client TLS configuration suitable for requests to LXD.
func GetTLSConfig(tlsRemoteCert *x509.Certificate) (*tls.Config, error) {
	tlsConfig := InitTLSConfig()

	finalizeTLSConfig(tlsConfig, tlsRemoteCert)

	return tlsConfig, nil
}

// GetTLSConfigMem returns a client TLS configuration suitable for requests to LXD, including client certificates for mTLS.
func GetTLSConfigMem(tlsClientCert string, tlsClientKey string, tlsClientCA string, tlsRemoteCertPEM string, insecureSkipVerify bool) (*tls.Config, error) {
	tlsConfig := InitTLSConfig()

	// Client authentication
	if tlsClientCert != "" && tlsClientKey != "" {
		cert, err := tls.X509KeyPair([]byte(tlsClientCert), []byte(tlsClientKey))
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
		tlsConfig.GetClientCertificate = func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			// GetClientCertificate is called if not nil instead of performing the default selection of an appropriate
			// certificate from the `Certificates` list. We only have one-key pair to send, and we always want to send it
			// because this is what uniquely identifies the caller to the server.
			return &cert, nil
		}
	}

	var tlsRemoteCert *x509.Certificate
	if tlsRemoteCertPEM != "" {
		// Ignore any content outside of the PEM bytes we care about
		certBlock, _ := pem.Decode([]byte(tlsRemoteCertPEM))
		if certBlock == nil {
			return nil, errors.New("Invalid remote certificate")
		}

		var err error
		tlsRemoteCert, err = x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, err
		}
	}

	if tlsClientCA != "" {
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM([]byte(tlsClientCA))

		tlsConfig.RootCAs = caPool
	}

	finalizeTLSConfig(tlsConfig, tlsRemoteCert)

	// Only skip TLS verification if no remote certificate is available.
	if tlsRemoteCert == nil {
		tlsConfig.InsecureSkipVerify = insecureSkipVerify
	}

	return tlsConfig, nil
}

// IsLoopback returns true if the given interface is a loopback interface.
func IsLoopback(iface *net.Interface) bool {
	return int(iface.Flags&net.FlagLoopback) > 0
}

// AllocatePort asks the kernel for a free open port that is ready to use.
func AllocatePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return -1, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return -1, err
	}

	return l.Addr().(*net.TCPAddr).Port, l.Close()
}
