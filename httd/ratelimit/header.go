package ratelimit

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// http rate limit identifiers
const (
	XRateLimitPrecision  = "X-RateLimit-Precision"
	XRateLimitBucket     = "X-RateLimit-Bucket"
	XRateLimitLimit      = "X-RateLimit-Limit"
	XRateLimitRemaining  = "X-RateLimit-Remaining"
	XRateLimitReset      = "X-RateLimit-Reset"
	XRateLimitResetAfter = "X-RateLimit-Reset-After"
	XRateLimitGlobal     = "X-RateLimit-Global"
	RateLimitRetryAfter  = "Retry-After"
)

// HeaderToTime takes the response header from Discord and extracts the
// timestamp. Useful for detecting time desync between discord and client
func HeaderToTime(header http.Header) (t time.Time, err error) {
	// date: Fri, 14 Sep 2018 19:04:24 GMT
	dateStr := header.Get("date")
	if dateStr == "" {
		err = errors.New("missing header field 'date'")
		return
	}

	t, err = time.Parse(time.RFC1123, dateStr)
	return
}

type RateLimitResponseStructure struct {
	Message    string `json:"message"`     // A message saying you are being rate limited.
	RetryAfter int64  `json:"retry_after"` // The number of milliseconds to wait before submitting another request.
	Global     bool   `json:"global"`      // A value indicating if you are being globally rate limited or not
}

// CorrectDiscordHeader overrides header fields with body content and make sure every header field
// uses milliseconds and not seconds. Regards rate limits only.
func CorrectDiscordHeader(statusCode int, header http.Header, body []byte) (h http.Header, err error) {
	timestamp, err := HeaderToTime(header)
	if err != nil {
		timestamp = time.Now()
		// does add an delay, but there is no reason
		// to go insane if timestamp could not be handled
		// TODO: log failure
	}

	if retry := header.Get(XRateLimitResetAfter); retry != "" {
		delay, err := strconv.ParseFloat(retry, 64)
		if err != nil {
			return nil, err
		}
		header.Set(XRateLimitResetAfter, strconv.FormatInt(int64(delay*1000), 10))
	}

	if statusCode == http.StatusTooManyRequests {
		var rateLimitBodyInfo *RateLimitResponseStructure
		if err = json.Unmarshal(body, &rateLimitBodyInfo); err != nil {
			return nil, err
		}
		if rateLimitBodyInfo.Global {
			header.Set(XRateLimitGlobal, "true")
		}
		if rateLimitBodyInfo.RetryAfter > 0 {
			header.Set(XRateLimitResetAfter, strconv.FormatInt(rateLimitBodyInfo.RetryAfter, 10))
		}
	}

	if retryAfter := header.Get(RateLimitRetryAfter); retryAfter != "" {
		header.Set(XRateLimitResetAfter, retryAfter)
	}

	if reset := header.Get(XRateLimitReset); reset != "" {
		epoch, err := strconv.ParseFloat(reset, 64)
		if err != nil {
			return nil, err
		}
		header.Set(XRateLimitReset, strconv.FormatInt(int64(epoch*1000), 10))
	}

	if resetStr := header.Get(XRateLimitReset); resetStr == "" {
		if retry := header.Get(XRateLimitResetAfter); retry != "" {
			delay, err := strconv.ParseInt(retry, 10, 64)
			if err != nil {
				return nil, err
			}

			reset := timestamp.Add(time.Duration(delay) * time.Millisecond)
			ms := reset.UnixNano() / int64(time.Millisecond)
			header.Set(XRateLimitReset, strconv.FormatInt(ms, 10))
		}
	}

	return header, nil
}
