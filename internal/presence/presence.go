package presence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

type Viewer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Anonymous bool   `json:"anonymous"`
	LastSeen  int64  `json:"last_seen"`
}

type Event struct {
	Type   string  `json:"type"`
	Viewer Viewer  `json:"viewer,omitempty"`
	Now    int64   `json:"now"`
	Count  int     `json:"count,omitempty"`
}

type Presence struct {
	client *redis.Client
}

func New(redisURL string) *Presence {
	opt, _ := redis.ParseURL(redisURL)
	client := redis.NewClient(opt)
	return &Presence{client: client}
}

func keyFor(videoID string) string {
	return "presence:" + videoID
}

func channelFor(videoID string) string {
	return "presence_events:" + videoID
}

func (p *Presence) sweepExpiredViewers(videoID string, ttlSeconds int) error {
	if ttlSeconds < 30 {
		ttlSeconds = 30
	}

	k := keyFor(videoID)
	res, err := p.client.HGetAll(ctx, k).Result()
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-time.Duration(ttlSeconds) * time.Second).Unix()
	removed := make([]Viewer, 0)
	for viewerID, raw := range res {
		var vv Viewer
		if err := json.Unmarshal([]byte(raw), &vv); err != nil {
			continue
		}
		if vv.LastSeen != 0 && vv.LastSeen < cutoff {
			removed = append(removed, Viewer{ID: viewerID, Name: vv.Name, AvatarURL: vv.AvatarURL, Anonymous: vv.Anonymous, LastSeen: vv.LastSeen})
		}
	}

	if len(removed) == 0 {
		return nil
	}

	pipe := p.client.TxPipeline()
	for _, viewer := range removed {
		pipe.HDel(ctx, k, viewer.ID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	for _, viewer := range removed {
		p.client.Publish(ctx, channelFor(videoID), marshalOrEmpty(Event{Type: "leave", Viewer: viewer, Now: time.Now().Unix()}))
	}

	return nil
}

// AddViewer creates/updates a viewer entry and sets TTL on the presence key.
func (p *Presence) AddViewer(videoID string, v Viewer, ttlSeconds int) error {
	v.LastSeen = time.Now().Unix()
	b, _ := json.Marshal(v)
	k := keyFor(videoID)
	if ttlSeconds < 30 {
		ttlSeconds = 30
	}

	// Keep joins idempotent for heartbeats from the same viewer.
	if err := p.sweepExpiredViewers(videoID, ttlSeconds); err != nil {
		return err
	}
	prev, _ := p.client.HGet(ctx, k, v.ID).Result()
	if err := p.client.HSet(ctx, k, v.ID, b).Err(); err != nil {
		return err
	}
	// set TTL to allow automatic expiry of empty viewers
	p.client.Expire(ctx, k, time.Duration(ttlSeconds)*time.Second)
	// Publish join only when this is a new viewer or identity changed.
	if prev == "" || prev != string(b) {
		evt := Event{Type: "join", Viewer: v, Now: time.Now().Unix()}
		p.client.Publish(ctx, channelFor(videoID), marshalOrEmpty(evt))
	}
	return nil
}

func (p *Presence) RemoveViewer(videoID, viewerID string) error {
	k := keyFor(videoID)
	_, err := p.client.HDel(ctx, k, viewerID).Result()
	if err != nil {
		return err
	}
	// publish leave event
	evt := Event{Type: "leave", Viewer: Viewer{ID: viewerID}, Now: time.Now().Unix()}
	p.client.Publish(ctx, channelFor(videoID), marshalOrEmpty(evt))
	return nil
}

func (p *Presence) GetViewers(videoID string) ([]Viewer, int, error) {
	if err := p.sweepExpiredViewers(videoID, 75); err != nil {
		return nil, 0, err
	}
	k := keyFor(videoID)
	res, err := p.client.HGetAll(ctx, k).Result()
	if err != nil {
		return nil, 0, err
	}
	viewers := make([]Viewer, 0, len(res))
	anonymousCount := 0
	for _, v := range res {
		var vv Viewer
		if err := json.Unmarshal([]byte(v), &vv); err != nil {
			continue
		}
		if vv.Anonymous {
			anonymousCount++
		} else {
			viewers = append(viewers, vv)
		}
	}
	return viewers, anonymousCount, nil
}

func (p *Presence) SubscribeEvents(videoID string) *redis.PubSub {
	return p.client.Subscribe(ctx, channelFor(videoID))
}

func marshalOrEmpty(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
