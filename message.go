package disgord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/andersfylling/disgord/constant"
	"github.com/andersfylling/disgord/endpoint"
	"github.com/andersfylling/disgord/httd"
)

// different message acticity types
const (
	_ = iota
	MessageActivityTypeJoin
	MessageActivityTypeSpectate
	MessageActivityTypeListen
	MessageActivityTypeJoinRequest
)

// The different message types usually generated by Discord. eg. "a new user joined"
const (
	MessageTypeDefault = iota
	MessageTypeRecipientAdd
	MessageTypeRecipientRemove
	MessageTypeCall
	MessageTypeChannelNameChange
	MessageTypeChannelIconChange
	MessageTypeChannelPinnedMessage
	MessageTypeGuildMemberJoin
)

const (
	AttachmentSpoilerPrefix = "SPOILER_"
)

// NewMessage ...
func NewMessage() *Message {
	return &Message{}
}

//func NewDeletedMessage() *DeletedMessage {
//	return &DeletedMessage{}
//}

//type DeletedMessage struct {
//	ID        Snowflake `json:"id"`
//	ChannelID Snowflake `json:"channel_id"`
//}

// MessageActivity https://discordapp.com/developers/docs/resources/channel#message-object-message-activity-structure
type MessageActivity struct {
	Type    int    `json:"type"`
	PartyID string `json:"party_id"`
}

// MessageApplication https://discordapp.com/developers/docs/resources/channel#message-object-message-application-structure
type MessageApplication struct {
	ID          Snowflake `json:"id"`
	CoverImage  string    `json:"cover_image"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Name        string    `json:"name"`
}

// Message https://discordapp.com/developers/docs/resources/channel#message-object-message-structure
type Message struct {
	Lockable        `json:"-"`
	ID              Snowflake          `json:"id"`
	ChannelID       Snowflake          `json:"channel_id"`
	Author          *User              `json:"author"`
	Content         string             `json:"content"`
	Timestamp       Time               `json:"timestamp"`
	EditedTimestamp Time               `json:"edited_timestamp"` // ?
	Tts             bool               `json:"tts"`
	MentionEveryone bool               `json:"mention_everyone"`
	Mentions        []*User            `json:"mentions"`
	MentionRoles    []Snowflake        `json:"mention_roles"`
	Attachments     []*Attachment      `json:"attachments"`
	Embeds          []*Embed           `json:"embeds"`
	Reactions       []*Reaction        `json:"reactions"`       // ?
	Nonce           Snowflake          `json:"nonce,omitempty"` // ?, used for validating a message was sent
	Pinned          bool               `json:"pinned"`
	WebhookID       Snowflake          `json:"webhook_id"` // ?
	Type            uint               `json:"type"`
	Activity        MessageActivity    `json:"activity"`
	Application     MessageApplication `json:"application"`

	// GuildID is not set when using a REST request. Only socket events.
	GuildID Snowflake `json:"guild_id"`

	// SpoilerTagContent is only true if the entire message text is tagged as a spoiler (aka completely wrapped in ||)
	SpoilerTagContent        bool `json:"-"`
	SpoilerTagAllAttachments bool `json:"-"`
	HasSpoilerImage          bool `json:"-"`
}

var _ Reseter = (*Message)(nil)
var _ fmt.Stringer = (*Message)(nil)
var _ internalUpdater = (*Message)(nil)
var _ discordDeleter = (*Message)(nil)

func (m *Message) String() string {
	return "message{" + m.ID.String() + "}"
}

func (m *Message) updateInternals() {
	if len(m.Content) >= len("||||") {
		prefix := m.Content[0:2]
		suffix := m.Content[len(m.Content)-2 : len(m.Content)]
		m.SpoilerTagContent = prefix+suffix == "||||"
	}

	m.SpoilerTagAllAttachments = len(m.Attachments) > 0
	for i := range m.Attachments {
		m.Attachments[i].updateInternals()
		if !m.Attachments[i].SpoilerTag {
			m.SpoilerTagAllAttachments = false
			break
		} else {
			m.HasSpoilerImage = true
		}
	}
}

// TODO: why is this method needed?
//func (m *Message) MarshalJSON() ([]byte, error) {
//	if m.ID.Empty() {
//		return []byte("{}"), nil
//	}
//
//	//TODO: remove copying of mutex
//	return json.Marshal(Message(*m))
//}

// TODO: await for caching
//func (m *Message) DirectMessage(session Session) bool {
//	return m.Type == ChannelTypeDM
//}

type messageDeleter interface {
	DeleteMessage(channelID, msgID Snowflake) (err error)
}

// DeepCopy see interface at struct.go#DeepCopier
func (m *Message) DeepCopy() (copy interface{}) {
	copy = NewMessage()
	m.CopyOverTo(copy)

	return
}

// CopyOverTo see interface at struct.go#Copier
func (m *Message) CopyOverTo(other interface{}) (err error) {
	var message *Message
	var valid bool
	if message, valid = other.(*Message); !valid {
		err = newErrorUnsupportedType("argument given is not a *Message type")
		return
	}

	if constant.LockedMethods {
		m.RLock()
		message.Lock()
	}

	message.ID = m.ID
	message.ChannelID = m.ChannelID
	message.Content = m.Content
	message.Timestamp = m.Timestamp
	message.EditedTimestamp = m.EditedTimestamp
	message.Tts = m.Tts
	message.MentionEveryone = m.MentionEveryone
	message.MentionRoles = m.MentionRoles
	message.Pinned = m.Pinned
	message.WebhookID = m.WebhookID
	message.Type = m.Type
	message.Activity = m.Activity
	message.Application = m.Application

	if m.Author != nil {
		message.Author = m.Author.DeepCopy().(*User)
	}

	if !m.Nonce.Empty() {
		message.Nonce = m.Nonce
	}

	for _, mention := range m.Mentions {
		message.Mentions = append(message.Mentions, mention.DeepCopy().(*User))
	}

	for _, attachment := range m.Attachments {
		message.Attachments = append(message.Attachments, attachment.DeepCopy().(*Attachment))
	}

	for _, embed := range m.Embeds {
		message.Embeds = append(message.Embeds, embed.DeepCopy().(*Embed))
	}

	for _, reaction := range m.Reactions {
		message.Reactions = append(message.Reactions, reaction.DeepCopy().(*Reaction))
	}

	if constant.LockedMethods {
		m.RUnlock()
		message.Unlock()
	}

	return
}

func (m *Message) deleteFromDiscord(s Session, flags ...Flag) (err error) {
	if m.ID.Empty() {
		err = newErrorMissingSnowflake("message is missing snowflake")
		return
	}

	err = s.DeleteMessage(m.ChannelID, m.ID, flags...)
	return
}

// MessageUpdater is a interface which only holds the message update method
type MessageUpdater interface {
	UpdateMessage(chanID, msgID Snowflake, flags ...Flag) *updateMessageBuilder
}

// Update after changing the message object, call update to notify Discord about any changes made
func (m *Message) update(client MessageUpdater, flags ...Flag) (msg *Message, err error) {
	if constant.LockedMethods {
		m.RLock()
	}
	builder := client.UpdateMessage(m.ChannelID, m.ID, flags...).SetContent(m.Content)
	if len(m.Embeds) > 0 {
		builder.SetEmbed(m.Embeds[0])
	}
	if constant.LockedMethods {
		m.RUnlock()
	}

	return builder.Execute()
}

// MessageSender is an interface which only holds the method needed for creating a channel message
type MessageSender interface {
	CreateMessage(channelID Snowflake, params *CreateMessageParams, flags ...Flag) (ret *Message, err error)
}

// Send sends this message to discord.
func (m *Message) Send(client MessageSender, flags ...Flag) (msg *Message, err error) {
	if constant.LockedMethods {
		m.RLock()
	}
	// TODO: attachments
	params := &CreateMessageParams{
		Content: m.Content,
		Tts:     m.Tts,
		Nonce:   m.Nonce,
		// File: ...
		// Embed: ...
	}
	if len(m.Embeds) > 0 {
		params.Embed = &Embed{}
		_ = m.Embeds[0].CopyOverTo(params.Embed)
	}
	channelID := m.ChannelID

	if constant.LockedMethods {
		m.RUnlock()
	}

	msg, err = client.CreateMessage(channelID, params, flags...)
	return
}

type msgSender interface {
	SendMsg(channelID Snowflake, data ...interface{}) (msg *Message, err error)
}

// Reply input any type as an reply. int, string, an object, etc.
func (m *Message) Reply(client msgSender, data ...interface{}) (*Message, error) {
	return client.SendMsg(m.ChannelID, data...)
}

// Respond responds to a message using a Message object.
// Deprecated: use Reply
func (m *Message) Respond(client MessageSender, message *Message) (msg *Message, err error) {
	if constant.LockedMethods {
		m.RLock()
	}
	id := m.ChannelID
	if constant.LockedMethods {
		m.RUnlock()
	}

	if constant.LockedMethods {
		message.Lock()
	}
	message.ChannelID = id
	if constant.LockedMethods {
		message.Unlock()
	}
	msg, err = message.Send(client)
	return
}

// RespondString sends a reply to a message in the form of a string
// Deprecated: use Reply
func (m *Message) RespondString(client MessageSender, content string) (msg *Message, err error) {
	params := &CreateMessageParams{
		Content: content,
	}

	if constant.LockedMethods {
		m.RLock()
	}
	msg, err = client.CreateMessage(m.ChannelID, params)
	if constant.LockedMethods {
		m.RUnlock()
	}
	return
}

// AddReaction adds a reaction to the message
//func (m *Message) AddReaction(reaction *Reaction) {}

// RemoveReaction removes a reaction from the message
//func (m *Message) RemoveReaction(id Snowflake)    {}

//////////////////////////////////////////////////////
//
// REST Methods
//
//////////////////////////////////////////////////////

// GetChannelMessagesParams https://discordapp.com/developers/docs/resources/channel#get-channel-messages-query-string-params
// TODO: ensure limits
type GetMessagesParams struct {
	Around Snowflake `urlparam:"around,omitempty"`
	Before Snowflake `urlparam:"before,omitempty"`
	After  Snowflake `urlparam:"after,omitempty"`
	Limit  int       `urlparam:"limit,omitempty"`
}

var _ URLQueryStringer = (*GetMessagesParams)(nil)

// GetMessages [REST] Returns the messages for a channel. If operating on a guild channel, this endpoint requires
// the 'VIEW_CHANNEL' permission to be present on the current user. If the current user is missing
// the 'READ_MESSAGE_HISTORY' permission in the channel then this will return no messages
// (since they cannot read the message history). Returns an array of message objects on success.
//  Method                  GET
//  Endpoint                /channels/{channel.id}/messages
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#get-channel-messages
//  Reviewed                2018-06-10
//  Comment                 The before, after, and around keys are mutually exclusive, only one may
//                          be passed at a time. see ReqGetChannelMessagesParams.
func (c *Client) GetMessages(channelID Snowflake, params URLQueryStringer, flags ...Flag) (ret []*Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}

	var query string
	if params != nil {
		query += params.URLQueryString()
	}

	r := c.newRESTRequest(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    endpoint.ChannelMessages(channelID) + query,
	}, flags)
	r.factory = func() interface{} {
		tmp := make([]*Message, 0)
		return &tmp
	}

	return getMessages(r.Execute)
}

// GetMessage [REST] Returns a specific message in the channel. If operating on a guild channel, this endpoints
// requires the 'READ_MESSAGE_HISTORY' permission to be present on the current user.
// Returns a message object on success.
//  Method                  GET
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#get-channel-message
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) GetMessage(channelID, messageID Snowflake, flags ...Flag) (message *Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if messageID.Empty() {
		err = errors.New("messageID must be set to get a specific message from a channel")
		return
	}

	r := c.newRESTRequest(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    endpoint.ChannelMessage(channelID, messageID),
	}, flags)
	r.pool = c.pool.message
	r.factory = func() interface{} {
		return &Message{}
	}

	return getMessage(r.Execute)
}

// NewMessageByString creates a message object from a string/content
func NewMessageByString(content string) *CreateMessageParams {
	return &CreateMessageParams{
		Content: content,
	}
}

// CreateMessageParams JSON params for CreateChannelMessage
type CreateMessageParams struct {
	Content string    `json:"content"`
	Nonce   Snowflake `json:"nonce,omitempty"`
	Tts     bool      `json:"tts,omitempty"`
	Embed   *Embed    `json:"embed,omitempty"` // embedded rich content

	Files []CreateMessageFileParams `json:"-"` // Always omit as this is included in multipart, not JSON payload

	SpoilerTagContent        bool `json:"-"`
	SpoilerTagAllAttachments bool `json:"-"`
}

func (p *CreateMessageParams) prepare() (postBody interface{}, contentType string, err error) {
	// spoiler tag
	if p.SpoilerTagContent && len(p.Content) > 0 {
		p.Content = "|| " + p.Content + " ||"
	}

	if len(p.Files) == 0 {
		postBody = p
		contentType = httd.ContentTypeJSON
		return
	}

	if p.SpoilerTagAllAttachments {
		for i := range p.Files {
			p.Files[i].SpoilerTag = true
		}
	}

	if p.Embed != nil {
		// check for spoilers
		for i := range p.Files {
			if p.Files[i].SpoilerTag && strings.Contains(p.Embed.Image.URL, p.Files[i].FileName) {
				s := strings.Split(p.Embed.Image.URL, p.Files[i].FileName)
				if len(s) > 0 {
					s[0] += AttachmentSpoilerPrefix + p.Files[i].FileName
					p.Embed.Image.URL = strings.Join(s, "")
				}
			}
		}
	}

	// Set up a new multipart writer, as we'll be using this for the POST body instead
	buf := new(bytes.Buffer)
	mp := multipart.NewWriter(buf)

	// Write the existing JSON payload
	var payload []byte
	payload, err = json.Marshal(p)
	if err != nil {
		return
	}
	if err = mp.WriteField("payload_json", string(payload)); err != nil {
		return
	}

	// Iterate through all the files and write them to the multipart blob
	for i, file := range p.Files {
		if err = file.write(i, mp); err != nil {
			return
		}
	}

	mp.Close()

	postBody = buf
	contentType = mp.FormDataContentType()

	return
}

// CreateMessageFileParams contains the information needed to upload a file to Discord, it is part of the
// CreateMessageParams struct.
type CreateMessageFileParams struct {
	Reader   io.Reader `json:"-"` // always omit as we don't want this as part of the JSON payload
	FileName string    `json:"-"`

	// SpoilerTag lets discord know that this image should be blurred out.
	// Current Discord behaviour is that whenever a message with one or more images is marked as
	// spoiler tag, all the images in that message are blurred out. (independent of msg.Content)
	SpoilerTag bool `json:"-"`
}

// write helper for file uploading in messages
func (f *CreateMessageFileParams) write(i int, mp *multipart.Writer) error {
	var filename string
	if f.SpoilerTag {
		filename = AttachmentSpoilerPrefix + f.FileName
	} else {
		filename = f.FileName
	}
	w, err := mp.CreateFormFile("file"+strconv.FormatInt(int64(i), 10), filename)
	if err != nil {
		return err
	}

	if _, err = io.Copy(w, f.Reader); err != nil {
		return err
	}

	return nil
}

// CreateMessage [REST] Post a message to a guild text or DM channel. If operating on a guild channel, this
// endpoint requires the 'SEND_MESSAGES' permission to be present on the current user. If the tts field is set to true,
// the SEND_TTS_MESSAGES permission is required for the message to be spoken. Returns a message object. Fires a
// Message Create Gateway event. See message formatting for more information on how to properly format messages.
// The maximum request size when sending a message is 8MB.
//  Method                  POST
//  Endpoint                /channels/{channel.id}/messages
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#create-message
//  Reviewed                2018-06-10
//  Comment                 Before using this endpoint, you must connect to and identify with a gateway at least once.
func (c *Client) CreateMessage(channelID Snowflake, params *CreateMessageParams, flags ...Flag) (ret *Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return nil, err
	}
	if params == nil {
		err = errors.New("message must be set")
		return nil, err
	}

	var (
		postBody    interface{}
		contentType string
	)

	if postBody, contentType, err = params.prepare(); err != nil {
		return nil, err
	}

	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    "/channels/" + channelID.String() + "/messages",
		Body:        postBody,
		ContentType: contentType,
	}, flags)
	r.pool = c.pool.message
	r.factory = func() interface{} {
		return &Message{}
	}

	return getMessage(r.Execute)
}

// UpdateMessage [REST] Edit a previously sent message. You can only edit messages that have been sent by the
// current user. Returns a message object. Fires a Message Update Gateway event.
//  Method                  PATCH
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#edit-message
//  Reviewed                2018-06-10
//  Comment                 All parameters to this endpoint are optional.
// TODO: verify embed is working
func (c *Client) UpdateMessage(chanID, msgID Snowflake, flags ...Flag) (builder *updateMessageBuilder) {
	builder = &updateMessageBuilder{}
	builder.r.itemFactory = func() interface{} {
		return &Message{}
	}
	builder.r.flags = flags
	builder.r.addPrereq(chanID.Empty(), "channelID must be set to get channel messages")
	builder.r.addPrereq(msgID.Empty(), "msgID must be set to edit the message")
	builder.r.setup(c.cache, c.req, &httd.Request{
		Method:      http.MethodPatch,
		Ratelimiter: ratelimitChannelMessages(chanID),
		Endpoint:    "/channels/" + chanID.String() + "/messages/" + msgID.String(),
		ContentType: httd.ContentTypeJSON,
	}, nil)

	return builder
}

// DeleteMessage [REST] Delete a message. If operating on a guild channel and trying to delete a message that was not
// sent by the current user, this endpoint requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response
// on success. Fires a Message Delete Gateway event.
//  Method                  DELETE
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages [DELETE]
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#delete-message
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) DeleteMessage(channelID, msgID Snowflake, flags ...Flag) (err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if msgID.Empty() {
		err = errors.New("msgID must be set to delete the message")
		return
	}

	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodDelete,
		Ratelimiter: ratelimitChannelMessagesDelete(channelID),
		Endpoint:    endpoint.ChannelMessage(channelID, msgID),
	}, flags)
	r.expectsStatusCode = http.StatusNoContent

	_, err = r.Execute()
	return err
}

// DeleteMessagesParams https://discordapp.com/developers/docs/resources/channel#bulk-delete-messages-json-params
type DeleteMessagesParams struct {
	Messages []Snowflake `json:"messages"`
	m        sync.RWMutex
}

func (p *DeleteMessagesParams) tooMany(messages int) (err error) {
	if messages > 100 {
		err = errors.New("must be 100 or less messages to delete")
	}

	return
}

func (p *DeleteMessagesParams) tooFew(messages int) (err error) {
	if messages < 2 {
		err = errors.New("must be at least two messages to delete")
	}

	return
}

// Valid validates the DeleteMessagesParams data
func (p *DeleteMessagesParams) Valid() (err error) {
	p.m.RLock()
	defer p.m.RUnlock()

	messages := len(p.Messages)
	if err = p.tooMany(messages); err != nil {
		return
	}
	err = p.tooFew(messages)
	return
}

// AddMessage Adds a message to be deleted
func (p *DeleteMessagesParams) AddMessage(msg *Message) (err error) {
	p.m.Lock()
	defer p.m.Unlock()

	if err = p.tooMany(len(p.Messages) + 1); err != nil {
		return
	}

	// TODO: check for duplicates as those are counted only once

	p.Messages = append(p.Messages, msg.ID)
	return
}

// DeleteMessages [REST] Delete multiple messages in a single request. This endpoint can only be used on guild
// channels and requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response on success. Fires multiple
// Message Delete Gateway events.Any message IDs given that do not exist or are invalid will count towards
// the minimum and maximum message count (currently 2 and 100 respectively). Additionally, duplicated IDs
// will only be counted once.
//  Method                  POST
//  Endpoint                /channels/{channel.id}/messages/bulk-delete
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages [DELETE] TODO: is this limiter key incorrect?
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#delete-message
//  Reviewed                2018-06-10
//  Comment                 This endpoint will not delete messages older than 2 weeks, and will fail if any message
//                          provided is older than that.
func (c *Client) DeleteMessages(chanID Snowflake, params *DeleteMessagesParams, flags ...Flag) (err error) {
	if chanID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return err
	}
	if err = params.Valid(); err != nil {
		return err
	}

	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ratelimiter: ratelimitChannelMessagesDelete(chanID),
		Endpoint:    endpoint.ChannelMessagesBulkDelete(chanID),
		ContentType: httd.ContentTypeJSON,
		Body:        params,
	}, flags)
	r.expectsStatusCode = http.StatusNoContent

	_, err = r.Execute()
	return err
}

// TriggerTypingIndicator [REST] Post a typing indicator for the specified channel. Generally bots should not implement
// this route. However, if a bot is responding to a command and expects the computation to take a few seconds, this
// endpoint may be called to let the user know that the bot is processing their message. Returns a 204 empty response
// on success. Fires a Typing Start Gateway event.
//  Method                  POST
//  Endpoint                /channels/{channel.id}/typing
//  Rate limiter [MAJOR]    /channels/{channel.id}/typing
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#trigger-typing-indicator
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) TriggerTypingIndicator(channelID Snowflake, flags ...Flag) (err error) {
	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ratelimiter: ratelimitChannelTyping(channelID),
		Endpoint:    endpoint.ChannelTyping(channelID),
	}, flags)
	r.expectsStatusCode = http.StatusNoContent

	_, err = r.Execute()
	return err
}

// GetPinnedMessages [REST] Returns all pinned messages in the channel as an array of message objects.
//  Method                  GET
//  Endpoint                /channels/{channel.id}/pins
//  Rate limiter [MAJOR]    /channels/{channel.id}/pins
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#get-pinned-messages
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) GetPinnedMessages(channelID Snowflake, flags ...Flag) (ret []*Message, err error) {
	r := c.newRESTRequest(&httd.Request{
		Ratelimiter: ratelimitChannelPins(channelID),
		Endpoint:    endpoint.ChannelPins(channelID),
	}, flags)
	r.factory = func() interface{} {
		tmp := make([]*Message, 0)
		return &tmp
	}

	return getMessages(r.Execute)
}

// PinMessage see Client.PinMessageID
func (c *Client) PinMessage(message *Message, flags ...Flag) error {
	return c.PinMessageID(message.ChannelID, message.ID, flags...)
}

// PinMessageID [REST] Pin a message by its ID and channel ID. Requires the 'MANAGE_MESSAGES' permission.
// Returns a 204 empty response on success.
//  Method                  PUT
//  Endpoint                /channels/{channel.id}/pins/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/pins
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#add-pinned-channel-message
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) PinMessageID(channelID, messageID Snowflake, flags ...Flag) (err error) {
	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodPut,
		Ratelimiter: ratelimitChannelPins(channelID),
		Endpoint:    endpoint.ChannelPin(channelID, messageID),
	}, flags)
	r.expectsStatusCode = http.StatusNoContent

	_, err = r.Execute()
	return err
}

// UnpinMessage see Client.UnpinMessageID
func (c *Client) UnpinMessage(message *Message, flags ...Flag) error {
	return c.UnpinMessageID(message.ChannelID, message.ID, flags...)
}

// UnpinMessageID [REST] Delete a pinned message in a channel. Requires the 'MANAGE_MESSAGES' permission.
// Returns a 204 empty response on success. Returns a 204 empty response on success.
//  Method                  DELETE
//  Endpoint                /channels/{channel.id}/pins/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/pins
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#delete-pinned-channel-message
//  Reviewed                2018-06-10
//  Comment                 -
func (c *Client) UnpinMessageID(channelID, messageID Snowflake, flags ...Flag) (err error) {
	if channelID.Empty() {
		return errors.New("channelID must be set to target the correct channel")
	}
	if messageID.Empty() {
		return errors.New("messageID must be set to target the specific channel message")
	}

	r := c.newRESTRequest(&httd.Request{
		Method:      http.MethodDelete,
		Ratelimiter: ratelimitChannelPins(channelID),
		Endpoint:    endpoint.ChannelPin(channelID, messageID),
	}, flags)
	r.expectsStatusCode = http.StatusNoContent

	_, err = r.Execute()
	return err
}

//////////////////////////////////////////////////////
//
// REST Wrappers
//
//////////////////////////////////////////////////////

func (c *Client) SetMsgContent(chanID, msgID Snowflake, content string) (*Message, error) {
	return c.UpdateMessage(chanID, msgID).SetContent(content).Execute()
}

func (c *Client) SetMsgEmbed(chanID, msgID Snowflake, embed *Embed) (*Message, error) {
	return c.UpdateMessage(chanID, msgID).SetEmbed(embed).Execute()
}

//////////////////////////////////////////////////////
//
// REST Builders
//
//////////////////////////////////////////////////////

// updateMessageBuilder, params here
//  https://discordapp.com/developers/docs/resources/channel#edit-message-json-params
//generate-rest-params: content:string, embed:*Embed,
//generate-rest-basic-execute: message:*Message,
type updateMessageBuilder struct {
	r RESTBuilder
}
