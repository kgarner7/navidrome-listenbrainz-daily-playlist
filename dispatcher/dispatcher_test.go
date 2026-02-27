package dispatcher

import (
	"encoding/json"
	"os"

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

	mockUserConfig := func(path string) {
		f, err := os.ReadFile("testdata/" + path + ".json")
		if err != nil {
			panic(err)
		}
		pdk.PDKMock.On("GetConfig", "users").Return(string(f), true)
	}

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

		DescribeTable("errors", func(path, error string) {
			mockUserConfig(path)
			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(BeZero())
			Expect(err).To(MatchError(error))
		},
			Entry(
				"should reject a config where a user doesn't have a username",
				"userConfig.singleUser.missingNDUsername",
				"user must have a Navidrome username and ListenBrainz username",
			),
			Entry(
				"should reject a config which has duplicate names in the source patch",
				"userConfig.duplicateSourceName",
				"duplicate playlist name found: playlist name",
			),
			Entry(
				"should reject a config where a source patch and generated name clash",
				"userConfig.duplicatePatchAndGenerated",
				"duplicate playlist name found: playlist name 2",
			),
			Entry(
				"should reject a config where a source patch and playlist name clash",
				"userConfig.duplicatePatchAndImport",
				"duplicate playlist name found: weekly name",
			),
		)

		It("should reject a config missing key users", func() {
			pdk.PDKMock.On("GetConfig", "users").Return("", false)
			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(BeZero())
			Expect(err).To(MatchError("missing required 'users' configuration"))
		})

		It("return a good user config, no fallback", func() {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)

			users, fallback, err := GetConfig()
			Expect(users).To(Equal([]userConfig{
				{
					GeneratePlaylist:             true,
					GeneratedPlaylist:            "Generated Daily Jams",
					GeneratedPlaylistTrackAge:    60,
					GeneratedPlaylistArtistLimit: 15,
					NDUsername:                   "username",
					LbzUsername:                  "lbz username",
					LbzToken:                     "1234",
					Ratings:                      []string{"0", "2", "3", "4", "5"},
					Sources: []source{
						{
							SourcePatch:  "daily-jams",
							PlaylistName: "playlist name",
						},
						{
							SourcePatch:  "weekly-jams",
							PlaylistName: "weekly name",
						},
					},
					Playlists: []playlist{
						{Name: "1234", LbzId: "0", OneTime: true},
					},
				},
			}))
			Expect(fallback).To(Equal(15))
			Expect(err).To(BeNil())
		})

		DescribeTable("returns a good config, but invalid fallback values", func(count, error string) {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return(count, true)

			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(Equal(0))
			Expect(err).To(MatchError(error))
		},
			Entry("non-integer", "abcd", "fallbackCount is not a valid number"),
			Entry("zero", "0", "fallbackCount must be between 1 and 500 (inclusive)"),
			Entry("big number", "501", "fallbackCount must be between 1 and 500 (inclusive)"),
		)
	})

	Describe("GetDuration", func() {
		It("should return 30 for a generate job", func() {
			j := Job{JobType: GenerateJams}
			Expect(j.GetDuration()).To(Equal(int32(30)))
		})

		It("should return 30 for an import job", func() {
			j := Job{JobType: ImportPlaylist}
			Expect(j.GetDuration()).To(Equal(int32(30)))
		})

		It("should return 30 * (1 + len(patch)) for patch fetch job", func() {
			j := Job{JobType: FetchPatches, Patch: &patchJob{
				Sources: []source{{}, {}},
			}}
			Expect(j.GetDuration()).To(Equal(int32(90)))
		})
	})

	Describe("InitialFetch", func() {
		It("should dispatch a complete rule when no existing playlists exist", func() {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)

			f, err := os.ReadFile("../subsonic/testdata/noPlaylists.json")
			if err != nil {
				panic(err)
			}

			host.SubsonicAPIMock.On("Call", "/rest/getPlaylists?u=username&username=username").Return(string(f), nil)

			j := Job{
				JobType:     FetchPatches,
				Username:    "username",
				LbzUsername: "lbz username",
				LbzToken:    "1234",
				Ratings:     map[int32]bool{int32(0): true, int32(2): true, int32(3): true, int32(4): true, int32(5): true},
				Patch: &patchJob{
					Sources: []source{
						{SourcePatch: "daily-jams", PlaylistName: "playlist name"},
						{SourcePatch: "weekly-jams", PlaylistName: "weekly name"},
					},
				},
				Fallback: 15,
			}

			fetchPayload, err := json.Marshal(j)
			Expect(err).To(BeNil())
			host.SchedulerMock.On("ScheduleOneTime", int32(1), string(fetchPayload), "").Return("", nil)

			j.Patch = nil
			j.JobType = GenerateJams
			j.Generate = &generationJob{
				Name:        "Generated Daily Jams",
				TrackAge:    60,
				ArtistLimit: 15,
			}

			generatePayload, err := json.Marshal(j)
			Expect(err).To(BeNil())
			host.SchedulerMock.On("ScheduleOneTime", int32(91), string(generatePayload), "").Return("", nil)

			j.Generate = nil
			j.JobType = ImportPlaylist
			j.Import = &importJob{Name: "1234", LbzId: "0"}

			importPayload, err := json.Marshal(j)
			Expect(err).To(BeNil())
			host.SchedulerMock.On("ScheduleOneTime", int32(121), string(importPayload), "").Return("", nil)

			err = InitialFetch()
			Expect(err).To(BeNil())

			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleOneTime", int32(1), string(fetchPayload), "")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleOneTime", int32(91), string(generatePayload), "")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleOneTime", int32(121), string(importPayload), "")
		})
	})
})
