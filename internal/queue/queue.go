package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

type Queue struct {
	client *redis.Client
}

type Job struct {
	VideoID  string `json:"video_id"`
	FilePath string `json:"file_path"`
}

type DownloadJob struct {
	VideoID string `json:"video_id"`
	Quality string `json:"quality"`
}

type LiveRecordingJob struct {
	Action       string `json:"action"`
	LiveStreamID string `json:"live_stream_id"`
	ChannelID    string `json:"channel_id"`
	StreamKey    string `json:"stream_key"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

const liveRecordingLeasePrefix = "live_recording_lease:"

func New(redisURL string) *Queue {
	opt, _ := redis.ParseURL(redisURL)
	client := redis.NewClient(opt)

	return &Queue{client: client}
}

func (q *Queue) Enqueue(job Job) error {
	data, _ := json.Marshal(job)
	return q.client.RPush(ctx, "video_jobs", data).Err()
}

func (q *Queue) EnqueueDownload(job DownloadJob) error {
	data, _ := json.Marshal(job)
	return q.client.RPush(ctx, "download_jobs", data).Err()
}

func (q *Queue) EnqueueLiveRecording(job LiveRecordingJob) error {
	data, _ := json.Marshal(job)
	return q.client.RPush(ctx, "live_recording_jobs", data).Err()
}

func (q *Queue) liveRecordingLeaseKey(streamKey string) string {
	return liveRecordingLeasePrefix + streamKey
}

func (q *Queue) AcquireLiveRecordingLease(streamKey, workerID string, ttl time.Duration) (bool, error) {
	key := q.liveRecordingLeaseKey(streamKey)
	acquired, err := q.client.SetNX(ctx, key, workerID, ttl).Result()
	if err != nil || acquired {
		return acquired, err
	}

	currentOwner, err := q.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, err
	}

	if currentOwner != workerID {
		return false, nil
	}

	return q.client.Expire(ctx, key, ttl).Result()
}

func (q *Queue) LiveRecordingLeaseOwner(streamKey string) (string, error) {
	owner, err := q.client.Get(ctx, q.liveRecordingLeaseKey(streamKey)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return owner, err
}

func (q *Queue) RefreshLiveRecordingLease(streamKey, workerID string, ttl time.Duration) (bool, error) {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`
	result, err := q.client.Eval(ctx, script, []string{q.liveRecordingLeaseKey(streamKey)}, workerID, int64(ttl/time.Millisecond)).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *Queue) ReleaseLiveRecordingLease(streamKey, workerID string) (bool, error) {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`
	result, err := q.client.Eval(ctx, script, []string{q.liveRecordingLeaseKey(streamKey)}, workerID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *Queue) Dequeue() (*Job, error) {
	result, err := q.client.BLPop(ctx, 0, "video_jobs").Result()
	if err != nil {
		return nil, err
	}

	var job Job
	json.Unmarshal([]byte(result[1]), &job)

	return &job, nil
}

func (q *Queue) DequeueDownload() (*DownloadJob, error) {
	result, err := q.client.BLPop(ctx, 0, "download_jobs").Result()
	if err != nil {
		return nil, err
	}

	var job DownloadJob
	json.Unmarshal([]byte(result[1]), &job)

	return &job, nil
}

func (q *Queue) DequeueLiveRecording() (*LiveRecordingJob, error) {
	result, err := q.client.BLPop(ctx, 0, "live_recording_jobs").Result()
	if err != nil {
		return nil, err
	}

	var job LiveRecordingJob
	json.Unmarshal([]byte(result[1]), &job)

	return &job, nil
}
