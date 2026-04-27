# Notifications Smoke Test

Use this to verify the Notifications v1 flow end to end.

## Prerequisites

- Backend running on `http://localhost:8080`
- DB migrations applied through `015_fix_notifications_dedupe_index.sql`
- Two users/channels available:
  - Recipient owner user/channel (owns the video/comment)
  - Actor channel (performs comment/like actions)

## 1) Identify Test IDs

Run in psql (`giltube` DB):

```sql
-- Pick one ready video and its owner user/channel
SELECT v.id AS video_id, v.title, v.channel_id AS owner_channel_id, ch.user_id AS owner_user_id
FROM videos v
JOIN channels ch ON ch.id = v.channel_id
WHERE v.status = 'ready'
LIMIT 1;

-- Pick another channel as actor (must be different from owner channel)
SELECT id AS actor_channel_id, user_id AS actor_user_id, name
FROM channels
WHERE id <> '<owner_channel_id>'
LIMIT 1;
```

Save:

- `VIDEO_ID`
- `OWNER_USER_ID`
- `OWNER_CHANNEL_ID`
- `ACTOR_CHANNEL_ID`

## 2) Clear Existing Notifications (Optional)

```sql
DELETE FROM notifications WHERE recipient_user_id = '<OWNER_USER_ID>';
```

## 3) Trigger Each Event

### A. Comment on video -> `comment_video`

```bash
curl -X POST "http://localhost:8080/api/v1/videos/VIDEO_ID/comments" \
  -F "channel_id=ACTOR_CHANNEL_ID" \
  -F "text=Smoke test comment"
```

### B. Reply to comment -> `reply_comment`

First get a target comment for the owner on the same video:

```sql
SELECT c.id
FROM comments c
WHERE c.video_id = 'VIDEO_ID' AND c.channel_id = 'OWNER_CHANNEL_ID'
LIMIT 1;
```

Then reply:

```bash
curl -X POST "http://localhost:8080/api/v1/videos/VIDEO_ID/comments" \
  -F "channel_id=ACTOR_CHANNEL_ID" \
  -F "text=Smoke test reply" \
  -F "parent_comment_id=PARENT_COMMENT_ID"
```

### C. Like video -> `like_video`

```bash
curl -X POST "http://localhost:8080/api/v1/videos/VIDEO_ID/like?channel_id=ACTOR_CHANNEL_ID"
```

### D. Like comment -> `like_comment`

Use an owner comment ID from step B query:

```bash
curl -X POST "http://localhost:8080/api/v1/comments/PARENT_COMMENT_ID/like?channel_id=ACTOR_CHANNEL_ID"
```

## 4) Verify Notification Rows

```sql
SELECT id, type, recipient_user_id, actor_channel_id, related_video_id, related_comment_id, is_read, minute_bucket, created_at
FROM notifications
WHERE recipient_user_id = '<OWNER_USER_ID>'
ORDER BY created_at DESC;
```

Expected:

- Rows for `comment_video`, `reply_comment`, `like_video`, `like_comment`
- `recipient_user_id` should be owner user
- `actor_channel_id` should be actor channel

## 5) Verify API Endpoints

### List

```bash
curl -H "X-User-ID: OWNER_USER_ID" "http://localhost:8080/api/v1/notifications?limit=20&offset=0"
```

### Unread count

```bash
curl -H "X-User-ID: OWNER_USER_ID" "http://localhost:8080/api/v1/notifications/unread-count"
```

### Mark one read

```bash
curl -X PATCH "http://localhost:8080/api/v1/notifications/NOTIFICATION_ID/read" \
  -H "Content-Type: application/json" \
  -H "X-User-ID: OWNER_USER_ID" \
  -d '{"is_read": true}'
```

### Mark all read

```bash
curl -X POST "http://localhost:8080/api/v1/notifications/read-all" \
  -H "X-User-ID: OWNER_USER_ID"
```

## 6) Validate Deduping

Run the same action multiple times within one minute (for example repeated like attempts after unlike/like cycle) and verify dedupe key behavior:

```sql
SELECT type, actor_channel_id, related_video_id, related_comment_id, minute_bucket, COUNT(*)
FROM notifications
WHERE recipient_user_id = '<OWNER_USER_ID>'
GROUP BY type, actor_channel_id, related_video_id, related_comment_id, minute_bucket
HAVING COUNT(*) > 1;
```

Expected: no duplicates for same dedupe key and minute bucket.

## 7) Push Subscription Sanity

```bash
curl -H "X-User-ID: OWNER_USER_ID" "http://localhost:8080/api/v1/notifications/push/config"
```

Expected:

- `enabled` reflects backend config
- `vapid_public_key` present when push is configured
