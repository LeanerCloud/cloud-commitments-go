package memorystore

import (
	"os"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/billingguard"
)

func TestMain(m *testing.M) { os.Exit(billingguard.Run(m)) }
