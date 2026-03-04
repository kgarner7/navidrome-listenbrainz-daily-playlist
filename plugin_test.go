//go:build !wasip1

package main

import (
	"errors"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Plugin", func() {
	var b *brainzPlaylistPlugin

	BeforeEach(func() {
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
		host.SchedulerMock.Calls = nil
		host.SchedulerMock.ExpectedCalls = nil
		host.TaskMock.Calls = nil
		host.TaskMock.ExpectedCalls = nil
		b = &brainzPlaylistPlugin{}
	})

	Describe("OnInit", func() {
		queueConfig := host.QueueConfig{Concurrency: 1, MaxRetries: 5, RetentionMs: 60_000, BackoffMs: 150_000, DelayMs: 30_000}

		It("should error error if schedule is not a number", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("a", true)
			err := b.OnInit()
			Expect(err).To(MatchError("Invalid schedule a: strconv.Atoi: parsing \"a\": invalid syntax"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
		})

		It("should error if value is negative", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("-1", true)
			err := b.OnInit()
			Expect(err).To(MatchError("Schedule is not a valid hour (between [0, 23], inclusive): -1"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
		})

		It("should error if value is beyond 23", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("24", true)
			err := b.OnInit()
			Expect(err).To(MatchError("Schedule is not a valid hour (between [0, 23], inclusive): 24"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
		})

		It("should error if unable to create a queue", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("7", true)
			host.TaskMock.On("CreateQueue", "job-queue", queueConfig).Return(errors.New("error"))
			err := b.OnInit()
			Expect(err).To(MatchError("Unable to create task queue: error"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
			host.TaskMock.AssertCalled(GinkgoT(), "CreateQueue", "job-queue", queueConfig)
		})

		It("should error if config is invalid", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("7", true)
			host.TaskMock.On("CreateQueue", "job-queue", queueConfig).Return(nil)
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(0), errors.New("Error"))
			pdk.PDKMock.On("GetConfig", "users").Return("", false)
			err := b.OnInit()
			Expect(err).To(MatchError("missing required 'users' configuration"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
			host.TaskMock.AssertCalled(GinkgoT(), "CreateQueue", "job-queue", queueConfig)
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "users")
		})

		It("should error if config if unable to schedule repeated task", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("7", true)
			host.TaskMock.On("CreateQueue", "job-queue", queueConfig).Return(nil)
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(0), errors.New("Error"))
			pdk.PDKMock.On("GetConfig", "users").Return("[]", true)
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)
			host.SchedulerMock.On("ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron").Return("", errors.New("error"))
			err := b.OnInit()
			Expect(err).To(MatchError("Failed to schedule playlist sync. Is your schedule a valid cron expression? error"))
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
			host.TaskMock.AssertCalled(GinkgoT(), "CreateQueue", "job-queue", queueConfig)
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "users")
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "fallbackCount")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron")
		})

		It("should succeed if check on startup is false", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("7", true)
			host.TaskMock.On("CreateQueue", "job-queue", queueConfig).Return(nil)
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(0), errors.New("Error"))
			pdk.PDKMock.On("GetConfig", "users").Return("[]", true)
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)
			host.SchedulerMock.On("ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron").Return("1234", nil)
			pdk.PDKMock.On("GetConfig", "checkOnStartup").Return("false", true)
			err := b.OnInit()
			Expect(err).To(BeNil())
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
			host.TaskMock.AssertCalled(GinkgoT(), "CreateQueue", "job-queue", queueConfig)
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "users")
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "fallbackCount")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron")
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "checkOnStartup")
			host.SchedulerMock.AssertNotCalled(GinkgoT(), "ScheduleOneTime", int32(1), "fetch", "fetch")
		})

		It("should succeed if check on startup is true", func() {
			pdk.PDKMock.On("GetConfig", "schedule").Return("7", true)
			host.TaskMock.On("CreateQueue", "job-queue", queueConfig).Return(nil)
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(0), errors.New("Error"))
			pdk.PDKMock.On("GetConfig", "users").Return("[]", true)
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)
			host.SchedulerMock.On("ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron").Return("1234", nil)
			pdk.PDKMock.On("GetConfig", "checkOnStartup").Return("true", true)
			host.SchedulerMock.On("ScheduleOneTime", int32(1), "fetch", "fetch").Return("5678", nil)
			err := b.OnInit()
			Expect(err).To(BeNil())
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "schedule")
			host.TaskMock.AssertCalled(GinkgoT(), "CreateQueue", "job-queue", queueConfig)
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "users")
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "fallbackCount")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleRecurring", "0 7 * * *", "daily-cron", "daily-cron")
			pdk.PDKMock.AssertCalled(GinkgoT(), "GetConfig", "checkOnStartup")
			host.SchedulerMock.AssertCalled(GinkgoT(), "ScheduleOneTime", int32(1), "fetch", "fetch")
		})
	})
})
