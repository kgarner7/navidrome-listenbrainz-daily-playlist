package main

import (
	"encoding/json"
	"fmt"
	"listenbrainz-daily-playlist/dispatcher"
	"strconv"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

const (
	fetch     = "fetch"
	dailyCron = "daily-cron"
)

type brainzPlaylistPlugin struct{}

func (b *brainzPlaylistPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	return dispatcher.InitialFetch()
}

func (b *brainzPlaylistPlugin) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	var job dispatcher.Job
	err := json.Unmarshal(req.Payload, &job)

	if err != nil {
		msg := fmt.Sprintf("unable to deserialize callback to a valid job: %v\n%s", err, req.Payload)
		pdk.Log(pdk.LogError, msg)
		return msg, nil
	}

	pdk.Log(pdk.LogTrace, "Dispatching job: "+string(req.Payload))
	result := job.Dispatch()

	if result != nil {
		if result.Retryable {
			return "", result.Error
		}

		msg := result.Error.Error()
		pdk.Log(pdk.LogError, "An unrecoverable error occurred: "+msg)

		return result.Error.Error(), nil
	}

	return "", nil
}

func (b *brainzPlaylistPlugin) OnInit() error {
	schedule, ok := pdk.GetConfig("schedule")
	if !ok {
		schedule = "8"
	}

	schedInt, err := strconv.Atoi(schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %s: %v", schedule, err)
	}

	if schedInt < 0 || schedInt > 23 {
		return fmt.Errorf("schedule is not a valid hour (between [0, 23], inclusive): %d", schedInt)
	}

	err = dispatcher.CreateQueue()
	if err != nil {
		return fmt.Errorf("unable to create task queue: %v", err)
	}

	dispatcher.ClearQueue()

	_, err = dispatcher.GetConfig()
	if err != nil {
		return err
	}

	_, err = host.SchedulerScheduleRecurring(fmt.Sprintf("0~59 %d * * *", schedInt), dailyCron, dailyCron)
	if err != nil {
		return fmt.Errorf("failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
	}

	checkOnStartup, ok := pdk.GetConfig("checkOnStartup")

	if !ok || checkOnStartup != "false" {
		_, err := host.SchedulerScheduleOneTime(1, fetch, fetch)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to do initial sync. Proceeding anyway %v", err))
		}
	}

	return nil
}

func main() {}

func init() {
	lifecycle.Register(&brainzPlaylistPlugin{})
	scheduler.Register(&brainzPlaylistPlugin{})
	taskworker.Register(&brainzPlaylistPlugin{})
}
