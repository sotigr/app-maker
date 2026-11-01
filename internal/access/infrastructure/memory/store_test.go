package memory

import (
	"testing"

	"app-maker/internal/access/infrastructure/storetest"
)

func TestStoreConformance(t *testing.T) { storetest.Run(t, NewStore()) }
