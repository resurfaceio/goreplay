package main

import (
	"bufio"
	"bytes"
	"fmt"
	"time"

	// "io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"

	// "strings"

	"github.com/buger/goreplay/byteutils"

	resurface_logger "github.com/resurfaceio/logger-go/v2"
)

type ResurfaceConfig struct {
	url *url.URL
}

type ResurfaceOutput struct {
	config  *ResurfaceConfig
	client  *http.Client
	rlogger *resurface_logger.HttpLogger

	responses map[string]*Message
	requests  map[string]*Message
}

func NewResurfaceOutput(address string, rules string) PluginWriter {
	o := new(ResurfaceOutput)
	var err error

	o.config = &ResurfaceConfig{}
	o.config.url, err = url.Parse(address)
	if err != nil {
		log.Fatal(fmt.Sprintf("[OUTPUT-RESURFACE] parse HTTP output URL error[%q]", err))
	}
	if o.config.url.Scheme == "" {
		o.config.url.Scheme = "http"
	}

	o.rlogger, err = resurface_logger.NewHttpLogger(resurface_logger.Options{
		Url:   address,
		Rules: rules,
	})
	if err != nil {
		log.Fatal(fmt.Sprintf("[OUTPUT-RESURFACE] Resurface options error[%q]", err))
	}

	o.client = &http.Client{}
	o.responses = make(map[string]*Message)
	o.requests = make(map[string]*Message)

	go o.StrayRequestsCollector()

	return o
}

func (o *ResurfaceOutput) StrayRequestsCollector() {
	var n int
	for {
		n = len(o.requests)
		for (n > 0) && (n != len(o.responses)) {
			for id := range o.requests {
				if _, hasResponse := o.responses[id]; !hasResponse {
					delete(o.requests, id)
				}
			}
		}
		time.Sleep(time.Minute)
	}
}

func (o *ResurfaceOutput) PluginWrite(msg *Message) (n int, err error) {
	var reqFound, respFound bool
	meta := payloadMeta(msg.Meta)

	requestID := byteutils.SliceToString(meta[1])
	n = len(msg.Data) + len(msg.Meta)

	if _, reqFound = o.requests[requestID]; !reqFound {
		if isRequestPayload(msg.Meta) {
			o.requests[requestID] = msg
			reqFound = true
		}
	}

	if _, respFound = o.responses[requestID]; !respFound {
		if !isRequestPayload(msg.Meta) {
			o.responses[requestID] = msg
			respFound = true
		}
	}

	if !(reqFound && respFound) {
		return
	}

	err = o.sendRequest(requestID)

	return
}

func (o *ResurfaceOutput) sendRequest(id string) error {
	req, reqErr := http.ReadRequest(bufio.NewReader(bytes.NewReader(o.requests[id].Data)))
	if reqErr != nil {
		return reqErr
	}

	resp, respErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(o.responses[id].Data)), req)
	if respErr != nil {
		return respErr
	}

	reqMeta := payloadMeta(o.requests[id].Meta)
	respMeta := payloadMeta(o.responses[id].Meta)

	reqTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(reqMeta[2]), 10, 64)
	respTimestamp, _ := strconv.ParseInt(byteutils.SliceToString(respMeta[2]), 10, 64)

	resurface_logger.SendHttpMessage(o.rlogger, resp, req, respTimestamp/1000000, (respTimestamp-reqTimestamp)/1000000)

	delete(o.requests, id)
	delete(o.responses, id)

	// Test message
	// tags := []string{
	// 	fmt.Sprintf(`["now", "%d"]`, reqTimestamp/1000000),
	// 	fmt.Sprintf(`["request_method", "%v"]`, req.Method),
	// 	fmt.Sprintf(`["request_url", "%v"]`, req.URL.String()),
	// 	fmt.Sprintf(`["response_code", "%d"]`, resp.StatusCode),
	// 	fmt.Sprintf(`["host", "localhost"]`),
	// 	fmt.Sprintf(`["interval", "%f"]`, float64(respTimestamp-reqTimestamp)/1000000),
	// }

	// payload := "[" + strings.Join(tags, ",") + "]"

	// resResp, err := o.client.Post(o.config.url.String(), "application/json", bufio.NewReader(strings.NewReader(payload)))
	// if err != nil {
	// 	return err
	// }

	// body, err := ioutil.ReadAll(resResp.Body)
	// bodyString := string(body)
	// fmt.Println(bodyString)
	// fmt.Println(payload)

	return nil
}

func (o *ResurfaceOutput) String() string {
	return "Resurface output: " + o.config.url.String()
}

// Close closes the data channel so that data
func (o *ResurfaceOutput) Close() error {
	return nil
}
