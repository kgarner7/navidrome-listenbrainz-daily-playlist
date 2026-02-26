//go:build !wasip1

package subsonic

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSubsonic(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Subsonic Test Suite")
}
