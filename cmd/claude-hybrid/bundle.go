package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// ensureBundle writes a PEM bundle that contains the host's system roots
// followed by our MITM CA. Python (requests/pip/httpx), curl, git, AWS CLI
// and similar tools take a single file via SSL_CERT_FILE / REQUESTS_CA_BUNDLE
// and replace — not extend — their built-in trust when it is set. Pointing
// them at this combined bundle preserves system trust while adding ours.
func ensureBundle(certsDir string, caPEM []byte) (string, error) {
	bundlePath := filepath.Join(certsDir, "bundle.crt")

	var buf bytes.Buffer
	if sys := systemCertBundlePath(); sys != "" {
		if data, err := os.ReadFile(sys); err == nil && len(data) > 0 {
			buf.Write(data)
			if !bytes.HasSuffix(data, []byte("\n")) {
				buf.WriteByte('\n')
			}
		}
	}
	buf.WriteString("# claude-hybrid MITM CA\n")
	buf.Write(caPEM)
	if !bytes.HasSuffix(caPEM, []byte("\n")) {
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(bundlePath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write bundle: %w", err)
	}
	return bundlePath, nil
}
