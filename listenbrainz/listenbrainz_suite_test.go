//go:build !wasip1

package listenbrainz

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestListenBrainz(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ListenBrainz Test Suite")
}
