package main

import (
	"bufio"
	"bytes"
	"sync"
	"time"

	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/buger/goreplay/byteutils"

	resurface_logger "github.com/resurfaceio/logger-go/v3"
)

type HTTPMessage struct {
	request  *Message
	response *Message
	initTime time.Time
}

type ResurfaceConfig struct {
	options    resurface_logger.Options
	bufferSize int
}

type ResurfaceOutput struct {
	config  *ResurfaceConfig
	rlogger *resurface_logger.HttpLogger

	messages     map[string]*HTTPMessage
	messageMutex sync.Mutex
}

// Initializes Resurface logger
func NewResurfaceOutput(address string, rules string) PluginWriter {
	o := new(ResurfaceOutput)
	var err error

	// Set capture URL and logging rules
	o.config = &ResurfaceConfig{
		bufferSize: 128,
	}
	o.config.options = resurface_logger.Options{
		Url:   address,
		Rules: rules,
	}

	// Initialize logger
	o.rlogger, err = resurface_logger.NewHttpLogger(o.config.options)
	if err != nil {
		log.Fatalf("[OUTPUT][RESURFACE] Resurface options error[%q]", err)
	}
	if o.rlogger.Enabled() {
		Debug(1, "[OUTPUT][RESURFACE]", "Resurface logger enabled")
	} else {
		Debug(1, "[OUTPUT][RESURFACE]", "Resurface logger disabled")
		if _, err = url.ParseRequestURI(o.config.options.Url); err != nil {
			log.Fatalf("[OUTPUT][RESURFACE] Error parsing HTTP(S) output URL[%q]", err)
		}
	}

	// Keep track of both requests and responses for matching
	o.messages = make(map[string]*HTTPMessage, o.config.bufferSize)

	// Manually remove orphaned requests/responses
	go o.collectStrays()

	return o
}

// Writes HTTP requests and responses to o.messages
func (o *ResurfaceOutput) PluginWrite(msg *Message) (n int, err error) {
	o.messageMutex.Lock()
	defer o.messageMutex.Unlock()

	n = len(msg.Data) + len(msg.Meta)

	if isOriginPayload(msg.Meta) {
		metaSlice := payloadMeta(msg.Meta)
		// UUID shared by a given request and its corresponding response
		messageID := byteutils.SliceToString(metaSlice[1])
		// Message timestamp
		messageTS := byteutils.SliceToString(metaSlice[2])

		message, messageFound := o.messages[messageID]
		if !messageFound {
			message = &HTTPMessage{}
			o.messages[messageID] = message

			messageTime, err := strconv.ParseInt(messageTS, 10, 64)
			if err == nil {
				message.initTime = time.Unix(0, messageTime)
			} else {
				message.initTime = time.Now()
				Debug(2, "[OUTPUT][RESURFACE]", "Error parsing message timestamp", err.Error())
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

		if !(reqFound && respFound) {
			return
		}

		err = o.sendRequest(messageID)
		if err != nil {
			Debug(2, "[OUTPUT][RESURFACE]", err.Error())
		}

	} else {
		Debug(2, "[OUTPUT][RESURFACE]", "Message is not request or response")
	}

	return
}

// Submits HTTP message to Resurface using logger-go
func (o *ResurfaceOutput) sendRequest(id string) error {
	message := o.messages[id]
	defer delete(o.messages, id)

	req, reqErr := http.ReadRequest(bufio.NewReader(bytes.NewReader(message.request.Data)))
	if reqErr != nil {
		return reqErr
	}

	resp, respErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(message.response.Data)), req)
	if respErr != nil {
		return respErr
	}

	reqMeta := payloadMeta(message.request.Meta)
	respMeta := payloadMeta(message.response.Meta)

	Debug(4, "[OUTPUT][RESURFACE]", "Processing Message:", id)
	if Settings.Verbose > 4 {
		Debug(5, "[OUTPUT][RESURFACE]", "Processing Request:", byteutils.SliceToString(reqMeta[1]))
		Debug(6, "[OUTPUT][RESURFACE]", byteutils.SliceToString(message.request.Data))
		Debug(5, "[OUTPUT][RESURFACE]", "Processing Response:", byteutils.SliceToString(respMeta[1]))
		Debug(6, "[OUTPUT][RESURFACE]", byteutils.SliceToString(message.response.Data))
	}

	reqTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(reqMeta[2]), 10, 64)
	respTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(respMeta[2]), 10, 64)

	interval := (respTimestamp - reqTimestamp) / 1000000
	if interval < 0 {
		interval = 0
	}

	resurface_logger.SendHttpMessage(o.rlogger, resp, req, respTimestamp/1000000, interval, nil)

	return nil
}

// Collect orphaned messages: requests without responses and viceversa
func (o *ResurfaceOutput) collectStrays() {
	for {
		o.messageMutex.Lock()
		n := len(o.messages)
		if n > 0 {
			Debug(3, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "Number of messages in queue:", n)
			for id, message := range o.messages {
				Debug(4, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "Checking message:", id)
				hasRequest := message.request != nil
				hasResponse := message.response != nil
				if ((hasRequest && !hasResponse) || (!hasRequest && hasResponse)) && time.Since(message.initTime) >= time.Minute {
					Debug(3, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "STRAY MESSAGE:", id)
					if Settings.Verbose > 3 {
						if hasRequest {
							Debug(4, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "REQUEST:", byteutils.SliceToString(message.request.Meta))
							Debug(5, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "REQUEST:\n", byteutils.SliceToString(message.request.Data))
						}
						if hasResponse {
							Debug(4, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "RESPONSE:", byteutils.SliceToString(message.response.Meta))
							Debug(5, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "RESPONSE:\n", byteutils.SliceToString(message.response.Data))
						}
					}

					delete(o.messages, id)

					Debug(3, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "MESSAGE", id, "DELETED")
				}
			}
		}
		o.messageMutex.Unlock()
		time.Sleep(time.Minute / 6)
	}
}

func (o *ResurfaceOutput) String() string {
	return "Resurface output: " + o.config.options.Url
}

// Closes the data channel
func (o *ResurfaceOutput) Close() error {
	return nil
}
