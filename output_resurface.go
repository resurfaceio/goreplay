package main

import (
	"bufio"
	"bytes"
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

	messagesChan     chan *Message
	fullMessagesChan chan *HTTPMessage
	messageCounter   [3]int
}

// Initializes Resurface logger
func NewResurfaceOutput(address string, rules string) PluginWriter {
	o := new(ResurfaceOutput)
	var err error

	// Set capture URL and logging rules
	o.config = &ResurfaceConfig{
		bufferSize: 10000,
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
	//o.messages = make(map[string]*HTTPMessage, o.config.bufferSize)

	// Manually remove orphaned requests/responses
	//go o.collectStrays()

	o.messagesChan = make(chan *Message, o.config.bufferSize*2)
	o.fullMessagesChan = make(chan *HTTPMessage, o.config.bufferSize)
	o.messageCounter = [3]int{0, 0, 0}

	go o.sendRequest()
	go o.worker()

	return o
}

func (o *ResurfaceOutput) worker() {
	straysTicker := time.NewTicker(time.Second * 10)
	messages := make(map[string]*HTTPMessage, o.config.bufferSize)
	for {
		select {
		case msg := <-o.messagesChan:
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

			if reqFound && respFound {
				o.fullMessagesChan <- message
				delete(messages, messageID)
			}

		case <-straysTicker.C:
			if n := len(messages); n > 0 {
				Debug(3, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "Number of messages in queue:", n)
				for id, message := range messages {
					Debug(4, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "Checking message:", id)
					hasRequest := message.request != nil
					hasResponse := message.response != nil
					if ((hasRequest && !hasResponse) || (!hasRequest && hasResponse)) && time.Since(message.initTime) >= time.Second*10 {
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

						delete(messages, id)
						o.messageCounter[2]++
						Debug(3, "[OUTPUT][RESURFACE][STRAY-COLLECTOR]", "MESSAGE", id, "DELETED")
					}
				}
			} else {
				if o.messageCounter[0] != 0 {
					Debug(1, "[OUTPUT][RESURFACE]", "messages received:", o.messageCounter[0], ", full messages sent:", o.messageCounter[1], ", deleted messages:", o.messageCounter[2])
					if o.messageCounter[0]-o.messageCounter[1]*2-o.messageCounter[2] == 0 {
						Debug(1, "[OUTPUT][RESURFACE]", "all good")
					}
					o.messageCounter = [3]int{0, 0, 0}
				}
			}
		}
	}
}

func (o *ResurfaceOutput) PluginWrite(msg *Message) (n int, err error) {
	n = len(msg.Data) + len(msg.Meta)
	if isOriginPayload(msg.Meta) {
		o.messagesChan <- msg
	} else {
		Debug(2, "[OUTPUT][RESURFACE]", "Message is not request or response")
	}
	return
}

// Submits HTTP message to Resurface using logger-go
func (o *ResurfaceOutput) sendRequest() {
	for {
		select {
		case message := <-o.fullMessagesChan:
			o.messageCounter[1]++
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
		}
	}
}

func (o *ResurfaceOutput) String() string {
	return "Resurface output: " + o.config.options.Url
}

// Closes the data channel
func (o *ResurfaceOutput) Close() error {
	Debug(1, "[OUTPUT][RESURFACE]", "messages received:", o.messageCounter[0], ", full messages sent:", o.messageCounter[1], ", deleted messages:", o.messageCounter[2])
	return nil
}
