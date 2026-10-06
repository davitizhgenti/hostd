package core

import (
	"testing"

	"go.uber.org/goleak"
)

// Every test in core must leave no goroutines behind.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
