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

func New(redisURL string) *Queue {
	opt, _ := redis.ParseURL(redisURL)
	client := redis.NewClient(opt)

	return &Queue{client: client}
}

func (q *Queue) Enqueue(job Job) error {
	data, _ := json.Marshal(job)
	return q.client.RPush(ctx, "video_jobs", data).Err()
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
