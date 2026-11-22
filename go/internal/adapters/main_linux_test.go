package adapters

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if runControlFixture() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
