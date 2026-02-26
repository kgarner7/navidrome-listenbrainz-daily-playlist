package dispatcher

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Dispatcher", func() {
	BeforeEach(func() {
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
		host.SchedulerMock.ExpectedCalls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	Describe("parseRatings", func() {
		It("should gracefully handle nil", func() {
			ratings := parseRatings(nil)
			Expect(ratings).To(Equal(map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should parse all rating levels", func() {
			ratings := parseRatings([]string{"0", "1", "2", "3", "4", "5"})
			Expect(ratings).To(Equal(map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should exclude ratings when asked", func() {
			ratings := parseRatings([]string{"0", "2", "3", "4", "5"})
			Expect(ratings).To(Equal(map[int32]bool{0: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should exclude other ratings when asked", func() {
			ratings := parseRatings([]string{"1"})
			Expect(ratings).To(Equal(map[int32]bool{1: true}))
		})

		It("should allow duplicate ratings", func() {
			ratings := parseRatings([]string{"1", "1", "5"})
			Expect(ratings).To(Equal(map[int32]bool{1: true, 5: true}))
		})

		It("should ignore invalid rating values", func() {
			ratings := parseRatings([]string{"1", "1", "5", "6", "-1", "bad"})
			Expect(ratings).To(Equal(map[int32]bool{1: true, 5: true}))
		})
	})

	Describe("GetConfig", func() {
		It("should reject a config missing key users", func() {
			pdk.PDKMock.On("GetConfig", "users").Return("", false)

			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(BeZero())
			Expect(err).To(MatchError("missing required 'users' configuration"))
		})
	})
})
