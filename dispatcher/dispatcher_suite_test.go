//go:build !wasip1

package dispatcher

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDispatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Dispatcher test suite")
}
