package memory

import (
	"testing"

	"organon/internal/access/infrastructure/storetest"
)

func TestStoreConformance(t *testing.T) { storetest.Run(t, NewStore()) }
