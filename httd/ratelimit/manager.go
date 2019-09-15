package ratelimit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/andersfylling/disgord/depalias"
)

type Snowflake = depalias.Snowflake

func NewManager() *Manager {
	global := newBucket(nil)
	global.active = false

	return &Manager{
		channels: newBucketGroup(),
		guilds:   newBucketGroup(),
		webhooks: newBucketGroup(),
		others:   newBucketGroup(),
		global:   global,
	}
}

type Manager struct {
	channels bucketGroup
	guilds   bucketGroup
	webhooks bucketGroup
	others   bucketGroup

	global *bucket
}

func (r *Manager) group(id GroupID) (g bucketGroup) {
	switch id {
	case GroupChannels:
		g = r.channels
	case GroupGuilds:
		g = r.guilds
	case GroupWebhooks:
		g = r.webhooks
	case GroupOthers:
		g = r.others
	default:
		panic("unknown GroupID")
	}

	return g
}

func (r *Manager) Bucket(groupID GroupID, majorID Snowflake, localBucketKey LocalKey) (b *bucket, populated bool) {
	group := r.group(groupID)
	if b, exists := group.bucket(majorID, localBucketKey); exists {
		return b, true
	}

	b = newBucket(r.global)
	b.localKeys = []LocalKey{localBucketKey}
	group.add(majorID, b)
	return b, false
}

func (r *Manager) Consolidate(groupID GroupID, majorID Snowflake, b *bucket) {
	group := r.group(groupID)
	group.consolidate(majorID, b)
}

func (r *Manager) UpdateBucket(groupID GroupID, majorID Snowflake, localBucketKey LocalKey, header http.Header) {
	b, _ := r.Bucket(groupID, majorID, localBucketKey)

	// to synchronize the timestamp between the bot and the discord server
	// we assume the current time is equal the header date
	discordTime, err := HeaderToTime(header)
	if err != nil {
		discordTime = time.Now()
	}

	localTime := time.Now()
	diff := localTime.Sub(discordTime)

	var bu *bucket
	if global := header.Get(XRateLimitGlobal); global == "true" {
		bu = b.global
	} else {
		bu = b
	}

	bu.Lock()
	defer bu.Unlock()

	if resetStr := header.Get(XRateLimitReset); resetStr != "" {
		epoch, err := strconv.ParseInt(resetStr, 10, 64)
		if err != nil {
			return
		}

		old := b.reset
		bu.reset = time.Unix(0, epoch+diff.Nanoseconds())

		oldNewDiffMs := uint(bu.reset.Sub(old).Nanoseconds() / int64(time.Millisecond))
		if !bu.reset.Equal(old) && bu.longestTimeout < oldNewDiffMs {
			bu.longestTimeout = oldNewDiffMs
		}
	}

	if remainingStr := header.Get(XRateLimitRemaining); remainingStr != "" {
		remaining, err := strconv.ParseInt(remainingStr, 10, 64)
		if err != nil {
			return
		}

		bu.remaining = uint(remaining)
	}

	if limitStr := header.Get(XRateLimitLimit); limitStr != "" {
		limit, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil {
			return
		}

		bu.limit = uint(limit)
	}

	if key := header.Get(XRateLimitBucket); key != "" {
		bu.key = key
	}
}
