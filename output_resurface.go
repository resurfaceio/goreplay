package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"time"

	// "io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"

	// "strings"
	"github.com/buger/goreplay/byteutils"

	resurface_logger "github.com/resurfaceio/logger-go/v3"
)

type ResurfaceConfig struct {
	url *url.URL
}

type ResurfaceOutput struct {
	config   *ResurfaceConfig
	client   *http.Client
	rlogger  *resurface_logger.HttpLogger
	debugOut bool

	responses map[string]*Message
	requests  map[string]*Message
}

func NewResurfaceOutput(address string, rules string, debugOut bool) PluginWriter {
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

	o.debugOut = debugOut
	if o.debugOut {
		os.Stdout.WriteString("[Resurface] Resurface logger enabled: " + strconv.FormatBool(o.rlogger.Enabled()) + "\n")
	}
	o.client = &http.Client{}
	o.responses = make(map[string]*Message)
	o.requests = make(map[string]*Message)

	go o.StrayRequestsCollector()

	return o
}

func (o *ResurfaceOutput) StrayRequestsCollector() {
	for {
		// n := len(o.requests)
		// if (n > 0) && (n != len(o.responses)) {
		// 	for id, req := range o.requests {
		// 		ts, _ := strconv.ParseInt(byteutils.SliceToString(payloadMeta(req.Meta)[2]), 10, 64)
		// 		if _, hasResponse := o.responses[id]; time.Now().UnixNano() >= ts+int64(time.Minute) && !hasResponse {
		// 			if o.debugOut {
		// 				os.Stdout.WriteString(payloadSeparator + "STRAY REQUEST" + payloadSeparator)
		// 				os.Stdout.Write(o.requests[id].Meta)
		// 				os.Stdout.Write(o.requests[id].Data)
		// 			}
		// 			delete(o.requests, id)
		// 		}
		// 	}
		// }

		// if len(o.responses) > 0 {
		// 	os.Stdout.WriteString("[Resurface] NUMBER OF RESP IN QUEUE:" + strconv.Itoa(len(o.responses)) + "\n")
		// 	os.Stdout.Write(payloadSeparatorAsBytes)
		// 	for id, _ := range o.responses {
		// 		os.Stdout.WriteString("[Resurface] RESPONSE IN QUEUE" + "\n")
		// 		os.Stdout.WriteString("[Resurface] ")
		// 		os.Stdout.Write(o.requests[id].Meta)
		// 		os.Stdout.Write(o.requests[id].Data)
		// 	}
		// }
		time.Sleep(time.Minute / 6)
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

	if o.debugOut {
		os.Stdout.WriteString("[Resurface] REQUEST\n")
		os.Stdout.Write(o.requests[id].Meta)
		os.Stdout.Write(o.requests[id].Data)
		os.Stdout.WriteString("[Resurface] RESPONSE\n")
		os.Stdout.Write(o.responses[id].Meta)
		os.Stdout.Write(payloadSeparatorAsBytes)
	}

	resurface_logger.SendHttpMessage(o.rlogger, resp, req, respTimestamp/1000000, (respTimestamp-reqTimestamp)/1000000, nil)

	delete(o.requests, id)
	delete(o.responses, id)

	if n := len(o.requests); n > 0 {
		os.Stdout.WriteString("[Resurface] NUMBER OF REQ IN QUEUE:" + strconv.Itoa(n) + "\n")
		for id, _ := range o.requests {
			os.Stdout.WriteString("[Resurface] REQUEST IN QUEUE" + "\n")
			os.Stdout.WriteString("[Resurface] ")
			os.Stdout.Write(o.requests[id].Meta)
			os.Stdout.Write(o.requests[id].Data)
		}
	}

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
