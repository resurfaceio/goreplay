package goreplay

import (
	"bufio"
	"bytes"
	"time"

	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/buger/goreplay/internal/byteutils"

	resurfaceLogger "github.com/resurfaceio/logger-go/v3"
)

type HTTPMessage struct {
	request  *Message
	response *Message
	initTime time.Time
}

type ResurfaceConfig struct {
	options    resurfaceLogger.Options
	bufferSize int
}

type ResurfaceOutput struct {
	config  *ResurfaceConfig
	rLogger *resurfaceLogger.HttpLogger

	messages       chan *Message
	httpMessages   chan *HTTPMessage
	messageCounter [3]int
}

const logPrefix = "[OUTPUT][RESURFACE]"

// NewResurfaceOutput Initializes Resurface logger
func NewResurfaceOutput(address string, rules string) PluginWriter {
	o := new(ResurfaceOutput)
	var err error

	u, _ := resurfaceLogger.GetUsageLoggers()
	// Set capture URL and logging rules
	o.config = &ResurfaceConfig{
		bufferSize: u.ConfigByDefault()["MESSAGE_QUEUE_SIZE"],
	}
	o.config.options = resurfaceLogger.Options{
		Url:   address,
		Rules: rules,
	}

	// Initialize logger
	o.rLogger, err = resurfaceLogger.NewHttpLogger(o.config.options)
	if err != nil {
		log.Printf("%s Resurface options error[%q]\n", logPrefix, err)
		return nil
	}
	if o.rLogger.Enabled() {
		Debug(1, logPrefix, "Resurface logger enabled")
	} else {
		Debug(1, logPrefix, "Resurface logger disabled")
		if _, err = url.ParseRequestURI(o.config.options.Url); err != nil {
			log.Printf("%s Error parsing HTTP(S) output URL[%q]\n", logPrefix, err)
			return nil
		}
	}

	// Build and send captured messages concurrently
	o.messages = make(chan *Message, o.config.bufferSize*2)
	o.httpMessages = make(chan *HTTPMessage, o.config.bufferSize)

	go o.sendMessages()
	go o.buildMessages()

	o.messageCounter = [3]int{0, 0, 0}
	return o
}

// PluginWrite Writes captured message to output
func (o *ResurfaceOutput) PluginWrite(msg *Message) (n int, err error) {
	n = len(msg.Data) + len(msg.Meta)
	if isOriginPayload(msg.Meta) {
		o.messages <- msg
	} else {
		Debug(2, logPrefix, "Message is not request or response")
	}
	return
}

// buildMessages Matches captured HTTP requests with responses
func (o *ResurfaceOutput) buildMessages() {
	// Keep track of both requests and responses for matching
	messages := make(map[string]*HTTPMessage, o.config.bufferSize)
	// Manually check and remove orphaned requests/responses every 10 s
	straysTicker := time.NewTicker(time.Second * 10)
	for {
		select {
		case msg := <-o.messages:
			o.messageCounter[0]++
			metaSlice := payloadMeta(msg.Meta)
			// UUID shared by a given request and its corresponding response
			messageID := byteutils.SliceToString(metaSlice[1])

			message, messageFound := messages[messageID]
			if !messageFound {
				// Message timestamp
				messageTimestamp := byteutils.SliceToString(metaSlice[2])

				message = &HTTPMessage{}
				messages[messageID] = message

				messageTime, err := strconv.ParseInt(messageTimestamp, 10, 64)
				if err == nil {
					message.initTime = time.Unix(0, messageTime)
				} else {
					message.initTime = time.Now()
					Debug(2, logPrefix, "Error parsing message timestamp", err.Error())
				}
			}

			reqFound := message.request != nil
			if !reqFound && isRequestPayload(msg.Meta) {
				message.request = msg
				reqFound = true
			}

			respFound := message.response != nil
			if !respFound && !isRequestPayload(msg.Meta) {
				message.response = msg
				respFound = true
			}

			if reqFound && respFound {
				o.httpMessages <- message
				o.messageCounter[1]++
				delete(messages, messageID)
			}

		case <-straysTicker.C:
			if n := len(messages); n > 0 {
				Debug(3, logPrefix, "Number of messages in queue:", n)
				for id, message := range messages {
					Debug(4, logPrefix, "Checking message:", id)
					hasRequest := message.request != nil
					hasResponse := message.response != nil
					if ((hasRequest && !hasResponse) || (!hasRequest && hasResponse)) &&
						time.Since(message.initTime) >= time.Minute*2 {

						Debug(3, logPrefix, "STRAY MESSAGE:", id)
						if Settings.Verbose > 3 {
							if hasRequest {
								Debug(4, logPrefix, "REQUEST:", byteutils.SliceToString(message.request.Meta))
								Debug(5, logPrefix, "REQUEST:\n", byteutils.SliceToString(message.request.Data))
							}
							if hasResponse {
								Debug(4, logPrefix, "RESPONSE:", byteutils.SliceToString(message.response.Meta))
								Debug(5, logPrefix, "RESPONSE:\n", byteutils.SliceToString(message.response.Data))
							}
						}

						delete(messages, id)
						Debug(3, logPrefix, "MESSAGE", id, "DELETED")
						o.messageCounter[2]++
					}
				}
			} else if o.messageCounter[0]+o.messageCounter[1]+o.messageCounter[2] != 0 {
				Debug(1, logPrefix, "messages received:", o.messageCounter[0],
					", full messages sent:", o.messageCounter[1], ", orphans deleted:", o.messageCounter[2])
				o.messageCounter = [3]int{0, 0, 0}
			}
		}
	}
}

// sendMessages Submits HTTP message to Resurface using logger-go
func (o *ResurfaceOutput) sendMessages() {
	for {
		select {
		case message := <-o.httpMessages:
			req, reqErr := http.ReadRequest(bufio.NewReader(bytes.NewReader(message.request.Data)))
			if reqErr != nil {
				continue
			}

			resp, respErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(message.response.Data)), req)
			if respErr != nil {
				continue
			}

			reqMeta := payloadMeta(message.request.Meta)
			respMeta := payloadMeta(message.response.Meta)

			//Debug(4, "[OUTPUT][RESURFACE]", "Processing Message:", id)
			if Settings.Verbose > 4 {
				Debug(5, logPrefix, "Processing Request:", byteutils.SliceToString(reqMeta[1]))
				Debug(6, logPrefix, byteutils.SliceToString(message.request.Data))
				Debug(5, logPrefix, "Processing Response:", byteutils.SliceToString(respMeta[1]))
				Debug(6, logPrefix, byteutils.SliceToString(message.response.Data))
			}

			reqTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(reqMeta[2]), 10, 64)
			respTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(respMeta[2]), 10, 64)

			interval := (respTimestamp - reqTimestamp) / 1000000
			if interval < 0 {
				interval = 0
			}

			resurfaceLogger.SendHttpMessage(o.rLogger, resp, req, respTimestamp/1000000, interval, nil)
		}
	}
}

// String Returns the configured capture URL
func (o *ResurfaceOutput) String() string {
	return "Resurface output: " + o.config.options.Url
}

// Close Closes the data channel
func (o *ResurfaceOutput) Close() error {

	Debug(1, logPrefix,
		"messages received:", o.messageCounter[0], ", full messages sent:", o.messageCounter[1],
		", orphans deleted:", o.messageCounter[2])
	return nil
}
