// Package billingguard keeps GCP service tests off the real Cloud Billing
// Catalog. Production code logs and swallows a failed billing-service
// construction, so a test that forgot to inject a fake would otherwise pass
// silently on a CI runner and hit the network on a developer machine.
package billingguard

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const marker = "failed to create billing service"

type watcher struct {
	next io.Writer
	mu   sync.Mutex
	hits []string
}

func (w *watcher) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(marker)) {
		w.mu.Lock()
		w.hits = append(w.hits, string(bytes.TrimSpace(p)))
		w.mu.Unlock()
	}
	return w.next.Write(p)
}

// Run is for a package's TestMain. It points Application Default Credentials
// at a missing file and the metadata server at a closed port, so the real
// billing client cannot be built, then fails the package if any test tried.
func Run(m *testing.M) int {
	for k, v := range map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": "/nonexistent/billingguard-no-adc.json",
		"GCE_METADATA_HOST":              "127.0.0.1:1",
		"CLOUDSDK_CONFIG":                filepath.Join(os.TempDir(), "billingguard-no-gcloud"),
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "billingguard: cannot isolate %s: %v\n", k, err)
			return 1
		}
	}
	if err := os.Unsetenv("GOOGLE_API_KEY"); err != nil {
		fmt.Fprintf(os.Stderr, "billingguard: cannot unset GOOGLE_API_KEY: %v\n", err)
		return 1
	}

	w := &watcher{next: os.Stderr}
	log.SetOutput(w)
	code := m.Run()
	if code == 0 && len(w.hits) > 0 {
		fmt.Fprintf(os.Stderr, "billingguard: %d test(s) reached the real billing service; inject a BillingService fake:\n", len(w.hits))
		for _, h := range w.hits {
			fmt.Fprintln(os.Stderr, "  "+h)
		}
		return 1
	}
	return code
}
