package queue

import (
	"context"
	"encoding/json"
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
