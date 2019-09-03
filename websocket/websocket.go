package websocket

import (
	"context"
	"net/http"
	"time"

	"github.com/andersfylling/disgord/depalias"
)

type Snowflake = depalias.Snowflake

type Conn interface {
	Close() error
	Open(ctx context.Context, endpoint string, requestHeader http.Header) error
	WriteJSON(v interface{}) error
	Read(ctx context.Context) (packet []byte, err error)

	Disconnected() bool
	Inactive() bool
	InactiveSince() time.Time
}

type CloseErr struct {
	info string
}

func (e *CloseErr) Error() string {
	return e.info
}

// WebsocketErr is used internally when the websocket package returns an error. It does not represent a Discord error(!)
type WebsocketErr struct {
	ID      uint
	message string
}

func (e *WebsocketErr) Error() string {
	return e.message
}

const (
	encodingJSON = "json"
)
