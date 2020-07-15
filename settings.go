package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"
)

var DEMO string

// MultiOption allows to specify multiple flags with same name and collects all values into array
type MultiOption []string

func (h *MultiOption) String() string {
	return fmt.Sprint(*h)
}

// Set gets called multiple times for each flag with same name
func (h *MultiOption) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// AppSettings is the struct of main configuration
type AppSettings struct {
	Verbose   bool
	Debug     bool
	Stats     bool
	ExitAfter time.Duration

	SplitOutput          bool
	RecognizeTCPSessions bool
	Pprof                string

	inputDummy   MultiOption
	outputDummy  MultiOption
	OutputStdout bool
	OutputNull   bool

	inputTCP        MultiOption
	InputTCPConfig  TCPInputConfig
	outputTCP       MultiOption
	OutputTCPConfig TCPOutputConfig
	OutputTCPStats  bool

	inputFile        MultiOption
	InputFileLoop    bool
	outputFile       MultiOption
	OutputFileConfig FileOutputConfig

	inputRAW                MultiOption
	InputRAWEngine          string
	InputRAWTrackResponse   bool
	InputRAWRealIPHeader    string
	InputRAWExpire          time.Duration
	InputRAWProtocol        string
	InputRAWBpfFilter       string
	InputRAWTimestampType   string
	CopyBufferSize          int64
	InputRAWImmediateMode   bool
	InputRAWBufferSize      int64
	InputRAWOverrideSnapLen bool

	InputRAWBufferSizeFlag string
	OutputFileSizeFlag     string
	OutputFileMaxSizeFlag  string
	CopyBufferSizeFlag     string

	Middleware string

	inputHTTP    MultiOption
	outputHTTP   MultiOption
	PrettifyHTTP bool

	OutputHTTPConfig HTTPOutputConfig

	outputBinary       MultiOption
	OutputBinaryConfig BinaryOutputConfig

	modifierConfig HTTPModifierConfig

	inputKafkaConfig  KafkaConfig
	outputKafkaConfig KafkaConfig

	ConfigFile string
	ConfigServerAddress string
	RemoteConfigHost string
}

// Settings holds Gor configuration
var Settings AppSettings

func usage() {
	fmt.Printf("Gor is a simple http traffic replication tool written in Go. Its main goal is to replay traffic from production servers to staging and dev environments.\nProject page: https://github.com/buger/gor\nAuthor: <Leonid Bugaev> leonsbox@gmail.com\nCurrent Version: v%s\n\n", VERSION)
	flag.PrintDefaults()
	os.Exit(2)
}

func readAndUpdateConfig(body io.ReadCloser) []byte {
	decoder := json.NewDecoder(body)

	var t AppSettings
	err := decoder.Decode(&t)
	if err != nil {
		return []byte("Error while updating flags via POST request.")
	}
	Settings = t
	newConfig, err := json.Marshal(Settings)
	if err != nil {
		return []byte("Error while updating flags via POST request.")
	}
	err = viper.ReadConfig(bytes.NewBuffer(newConfig))
	if err != nil {
		return []byte("Error while updating flags via POST request.")
	}
	return nil
}

func updateConfig(respBody []byte) {
	var t AppSettings
	if err := json.Unmarshal(respBody, t); err != nil {
		return
	}
	Settings = t
}

func flagz(res http.ResponseWriter, req *http.Request) {
	for k, v := range req.URL.Query() {
		if len(v) == 1 {
			viper.Set(k, v[0])
			viper.Unmarshal(&Settings)
		}
	}

	if req.Method == "POST" {
		res.Write(readAndUpdateConfig(req.Body))
	}
	data, _ := json.MarshalIndent(Settings, "", " ")
	res.Write(data)
}

func initConfigServer() {
	http.HandleFunc("/flagz", flagz)
	http.ListenAndServe(Settings.ConfigServerAddress, nil)
}

func init() {
	flag.Usage = usage

	flag.StringVar(&Settings.Pprof, "http-Pprof", "", "Enable profiling. Starts  http server on specified port, exposing special /Debug/Pprof endpoint. Example: `:8181`")
	flag.BoolVar(&Settings.Verbose, "Verbose", false, "Turn on more Verbose output")
	flag.BoolVar(&Settings.Debug, "Debug", false, "Turn on Debug output, shows all intercepted traffic. Works only when with `Verbose` flag")
	flag.BoolVar(&Settings.Stats, "Stats", false, "Turn on queue Stats output")

	if DEMO == "" {
		flag.DurationVar(&Settings.ExitAfter, "exit-after", 0, "exit after specified duration")
	} else {
		Settings.ExitAfter = 5 * time.Minute
	}

	flag.BoolVar(&Settings.SplitOutput, "split-output", false, "By default each output gets same traffic. If set to `true` it splits traffic equally among all outputs.")

	flag.BoolVar(&Settings.RecognizeTCPSessions, "recognize-tcp-sessions", false, "[PRO] If turned on http output will create separate worker for each TCP session. Splitting output will session based as well.")

	flag.Var(&Settings.inputDummy, "input-dummy", "Used for testing outputs. Emits 'Get /' request every 1s")
	flag.Var(&Settings.outputDummy, "output-dummy", "DEPRECATED: use --output-stdout instead")

	flag.BoolVar(&Settings.OutputStdout, "output-stdout", true, "Used for testing inputs. Just prints to console data coming from inputs.")

	flag.BoolVar(&Settings.OutputNull, "output-null", false, "Used for testing inputs. Drops all requests.")

	flag.Var(&Settings.inputTCP, "input-tcp", "Used for internal communication between Gor instances. Example: \n\t# Receive requests from other Gor instances on 28020 port, and redirect output to staging\n\tgor --input-tcp :28020 --output-http staging.com")
	flag.BoolVar(&Settings.InputTCPConfig.Secure, "input-tcp-Secure", false, "Turn on TLS security. Do not forget to specify certificate and key files.")
	flag.StringVar(&Settings.InputTCPConfig.CertificatePath, "input-tcp-certificate", "", "Path to PEM encoded certificate file. Used when TLS turned on.")
	flag.StringVar(&Settings.InputTCPConfig.KeyPath, "input-tcp-certificate-key", "", "Path to PEM encoded certificate key file. Used when TLS turned on.")

	flag.Var(&Settings.outputTCP, "output-tcp", "Used for internal communication between Gor instances. Example: \n\t# Listen for requests on 80 port and forward them to other Gor instance on 28020 port\n\tgor --input-raw :80 --output-tcp replay.local:28020")
	flag.BoolVar(&Settings.OutputTCPConfig.secure, "output-tcp-Secure", false, "Use TLS Secure connection. --input-file on another end should have TLS turned on as well.")
	flag.BoolVar(&Settings.OutputTCPConfig.sticky, "output-tcp-sticky", false, "Use Sticky connection. Request/Response with same ID will be sent to the same connection.")
	flag.BoolVar(&Settings.OutputTCPStats, "output-tcp-Stats", false, "Report TCP output queue Stats to console every 5 seconds.")

	flag.Var(&Settings.inputFile, "input-file", "Read requests from file: \n\tgor --input-file ./requests.gor --output-http staging.com")
	flag.BoolVar(&Settings.InputFileLoop, "input-file-loop", false, "Loop input files, useful for performance testing.")

	flag.Var(&Settings.outputFile, "output-file", "Write incoming requests to file: \n\tgor --input-raw :80 --output-file ./requests.gor")
	flag.DurationVar(&Settings.OutputFileConfig.flushInterval, "output-file-flush-interval", time.Second, "Interval for forcing buffer flush to the file, default: 1s.")
	flag.BoolVar(&Settings.OutputFileConfig.append, "output-file-append", false, "The flushed chunk is appended to existence file or not. ")
	flag.StringVar(&Settings.OutputFileSizeFlag, "output-file-size-limit", "32mb", "Size of each chunk. Default: 32mb")
	flag.Int64Var(&Settings.OutputFileConfig.queueLimit, "output-file-queue-limit", 256, "The length of the chunk queue. Default: 256")
	flag.StringVar(&Settings.OutputFileMaxSizeFlag, "output-file-max-size-limit", "1TB", "Max size of output file, Default: 1TB")

	flag.StringVar(&Settings.OutputFileConfig.bufferPath, "output-file-buffer", "/tmp", "The path for temporary storing current buffer: \n\tgor --input-raw :80 --output-file s3://mybucket/logs/%Y-%m-%d.gz --output-file-buffer /mnt/logs")

	flag.BoolVar(&Settings.PrettifyHTTP, "prettify-http", false, "If enabled, will automatically decode requests and responses with: Content-Encodning: gzip and Transfer-Encoding: chunked. Useful for debugging, in conjuction with --output-stdout")

	flag.Var(&Settings.inputRAW, "input-raw", "Capture traffic from given port (use RAW sockets and require *sudo* access):\n\t# Capture traffic from 8080 port\n\tgor --input-raw :8080 --output-http staging.com")

	flag.BoolVar(&Settings.InputRAWTrackResponse, "input-raw-track-response", false, "If turned on Gor will track responses in addition to requests, and they will be available to Middleware and file output.")

	flag.StringVar(&Settings.InputRAWEngine, "input-raw-engine", "libpcap", "Intercept traffic using `libpcap` (default), and `raw_socket`")

	flag.StringVar(&Settings.InputRAWProtocol, "input-raw-protocol", "http", "Specify application protocol of intercepted traffic. Possible values: http, binary")

	flag.StringVar(&Settings.InputRAWRealIPHeader, "input-raw-realip-header", "", "If not blank, injects header with given name and real IP value to the request payload. Usually this header should be named: X-Real-IP")

	flag.DurationVar(&Settings.InputRAWExpire, "input-raw-expire", time.Second*2, "How much it should wait for the last TCP packet, till consider that TCP message complete.")

	flag.StringVar(&Settings.InputRAWBpfFilter, "input-raw-bpf-filter", "", "BPF filter to write custom expressions. Can be useful in case of non standard network interfaces like tunneling or SPAN port. Example: --input-raw-bpf-filter 'dst port 80'")

	flag.StringVar(&Settings.InputRAWTimestampType, "input-raw-timestamp-type", "", "Possible values: PCAP_TSTAMP_HOST, PCAP_TSTAMP_HOST_LOWPREC, PCAP_TSTAMP_HOST_HIPREC, PCAP_TSTAMP_ADAPTER, PCAP_TSTAMP_ADAPTER_UNSYNCED. This values not supported on all systems, GoReplay will tell you available values of you put wrong one.")
	flag.StringVar(&Settings.CopyBufferSizeFlag, "copy-buffer-size", "5mb", "Set the buffer size for an individual request (default 5MB)")
	flag.BoolVar(&Settings.InputRAWOverrideSnapLen, "input-raw-override-snaplen", false, "Override the capture snaplen to be 64k. Required for some Virtualized environments")
	flag.BoolVar(&Settings.InputRAWImmediateMode, "input-raw-immediate-mode", false, "Set pcap interface to immediate mode.")
	flag.StringVar(&Settings.InputRAWBufferSizeFlag, "input-raw-buffer-size", "0", "Controls size of the OS buffer which holds packets until they dispatched. Default value depends by system: in Linux around 2MB. If you see big package drop, increase this value.")

	flag.StringVar(&Settings.Middleware, "Middleware", "", "Used for modifying traffic using external command")

	// flag.Var(&Settings.inputHTTP, "input-http", "Read requests from HTTP, should be explicitly sent from your application:\n\t# Listen for http on 9000\n\tgor --input-http :9000 --output-http staging.com")

	flag.Var(&Settings.outputHTTP, "output-http", "Forwards incoming requests to given http address.\n\t# Redirect all incoming requests to staging.com address \n\tgor --input-raw :80 --output-http http://staging.com")

	/* OutputHTTPConfig */
	flag.IntVar(&Settings.OutputHTTPConfig.BufferSize, "output-http-response-buffer", 0, "HTTP response buffer size, all data after this size will be discarded.")
	flag.BoolVar(&Settings.OutputHTTPConfig.CompatibilityMode, "output-http-compatibility-mode", false, "Use standard Go client, instead of built-in implementation. Can be slower, but more compatible.")

	flag.IntVar(&Settings.OutputHTTPConfig.workersMin, "output-http-workers-min", 0, "Gor uses dynamic worker scaling. Enter a number to set a minimum number of workers. default = 1.")
	flag.IntVar(&Settings.OutputHTTPConfig.workersMax, "output-http-workers", 0, "Gor uses dynamic worker scaling. Enter a number to set a maximum number of workers. default = 0 = unlimited.")
	flag.IntVar(&Settings.OutputHTTPConfig.queueLen, "output-http-queue-len", 1000, "Number of requests that can be queued for output, if all workers are busy. default = 1000")

	flag.IntVar(&Settings.OutputHTTPConfig.redirectLimit, "output-http-redirects", 0, "Enable how often redirects should be followed.")
	flag.DurationVar(&Settings.OutputHTTPConfig.Timeout, "output-http-timeout", 5*time.Second, "Specify HTTP request/response timeout. By default 5s. Example: --output-http-timeout 30s")
	flag.BoolVar(&Settings.OutputHTTPConfig.TrackResponses, "output-http-track-response", false, "If turned on, HTTP output responses will be set to all outputs like stdout, file and etc.")

	flag.BoolVar(&Settings.OutputHTTPConfig.stats, "output-http-Stats", false, "Report http output queue Stats to console every N milliseconds. See output-http-Stats-ms")
	flag.IntVar(&Settings.OutputHTTPConfig.statsMs, "output-http-Stats-ms", 5000, "Report http output queue Stats to console every N milliseconds. default: 5000")
	flag.BoolVar(&Settings.OutputHTTPConfig.OriginalHost, "http-original-host", false, "Normally gor replaces the Host http header with the host supplied with --output-http.  This option disables that behavior, preserving the original Host header.")
	flag.BoolVar(&Settings.OutputHTTPConfig.Debug, "output-http-Debug", false, "Enables http Debug output.")
	flag.StringVar(&Settings.OutputHTTPConfig.elasticSearch, "output-http-elasticsearch", "", "Send request and response Stats to ElasticSearch:\n\tgor --input-raw :8080 --output-http staging.com --output-http-elasticsearch 'es_host:api_port/index_name'")
	/* OutputHTTPConfig */

	flag.Var(&Settings.outputBinary, "output-binary", "Forwards incoming binary payloads to given address.\n\t# Redirect all incoming requests to staging.com address \n\tgor --input-raw :80 --input-raw-protocol binary --output-binary staging.com:80")
	/* OutputBinaryConfig */
	flag.IntVar(&Settings.OutputBinaryConfig.BufferSize, "output-tcp-response-buffer", 0, "TCP response buffer size, all data after this size will be discarded.")
	flag.IntVar(&Settings.OutputBinaryConfig.workers, "output-binary-workers", 0, "Gor uses dynamic worker scaling by default.  Enter a number to run a set number of workers.")
	flag.DurationVar(&Settings.OutputBinaryConfig.Timeout, "output-binary-timeout", 0, "Specify HTTP request/response timeout. By default 5s. Example: --output-binary-timeout 30s")
	flag.BoolVar(&Settings.OutputBinaryConfig.TrackResponses, "output-binary-track-response", false, "If turned on, Binary output responses will be set to all outputs like stdout, file and etc.")

	flag.BoolVar(&Settings.OutputBinaryConfig.Debug, "output-binary-Debug", false, "Enables binary Debug output.")
	/* OutputBinaryConfig */

	flag.StringVar(&Settings.outputKafkaConfig.host, "output-kafka-host", "", "Read request and response Stats from Kafka:\n\tgor --input-raw :8080 --output-kafka-host '192.168.0.1:9092,192.168.0.2:9092'")
	flag.StringVar(&Settings.outputKafkaConfig.topic, "output-kafka-topic", "", "Read request and response Stats from Kafka:\n\tgor --input-raw :8080 --output-kafka-topic 'kafka-log'")
	flag.BoolVar(&Settings.outputKafkaConfig.useJSON, "output-kafka-json-format", false, "If turned on, it will serialize messages from GoReplay text format to JSON.")

	flag.StringVar(&Settings.inputKafkaConfig.host, "input-kafka-host", "", "Send request and response Stats to Kafka:\n\tgor --output-stdout --input-kafka-host '192.168.0.1:9092,192.168.0.2:9092'")
	flag.StringVar(&Settings.inputKafkaConfig.topic, "input-kafka-topic", "", "Send request and response Stats to Kafka:\n\tgor --output-stdout --input-kafka-topic 'kafka-log'")
	flag.BoolVar(&Settings.inputKafkaConfig.useJSON, "input-kafka-json-format", false, "If turned on, it will assume that messages coming in JSON format rather than  GoReplay text format.")

	flag.Var(&Settings.modifierConfig.headers, "http-set-header", "Inject additional headers to http reqest:\n\tgor --input-raw :8080 --output-http staging.com --http-set-header 'User-Agent: Gor'")
	flag.Var(&Settings.modifierConfig.headers, "output-http-header", "WARNING: `--output-http-header` DEPRECATED, use `--http-set-header` instead")

	flag.Var(&Settings.modifierConfig.headerRewrite, "http-rewrite-header", "Rewrite the request header based on a mapping:\n\tgor --input-raw :8080 --output-http staging.com --http-rewrite-header Host: (.*).example.com,$1.beta.example.com")

	flag.Var(&Settings.modifierConfig.params, "http-set-param", "Set request url param, if param already exists it will be overwritten:\n\tgor --input-raw :8080 --output-http staging.com --http-set-param api_key=1")

	flag.Var(&Settings.modifierConfig.methods, "http-allow-method", "Whitelist of HTTP methods to replay. Anything else will be dropped:\n\tgor --input-raw :8080 --output-http staging.com --http-allow-method GET --http-allow-method OPTIONS")
	flag.Var(&Settings.modifierConfig.methods, "output-http-method", "WARNING: `--output-http-method` DEPRECATED, use `--http-allow-method` instead")

	flag.Var(&Settings.modifierConfig.urlRegexp, "http-allow-url", "A regexp to match requests against. Filter get matched against full url with domain. Anything else will be dropped:\n\t gor --input-raw :8080 --output-http staging.com --http-allow-url ^www.")
	flag.Var(&Settings.modifierConfig.urlRegexp, "output-http-url-regexp", "WARNING: `--output-http-url-regexp` DEPRECATED, use `--http-allow-url` instead")

	flag.Var(&Settings.modifierConfig.urlNegativeRegexp, "http-disallow-url", "A regexp to match requests against. Filter get matched against full url with domain. Anything else will be forwarded:\n\t gor --input-raw :8080 --output-http staging.com --http-disallow-url ^www.")

	flag.Var(&Settings.modifierConfig.urlRewrite, "http-rewrite-url", "Rewrite the request url based on a mapping:\n\tgor --input-raw :8080 --output-http staging.com --http-rewrite-url /v1/user/([^\\/]+)/ping:/v2/user/$1/ping")
	flag.Var(&Settings.modifierConfig.urlRewrite, "output-http-rewrite-url", "WARNING: `--output-http-rewrite-url` DEPRECATED, use `--http-rewrite-url` instead")

	flag.Var(&Settings.modifierConfig.headerFilters, "http-allow-header", "A regexp to match a specific header against. Requests with non-matching headers will be dropped:\n\t gor --input-raw :8080 --output-http staging.com --http-allow-header api-version:^v1")
	flag.Var(&Settings.modifierConfig.headerFilters, "output-http-header-filter", "WARNING: `--output-http-header-filter` DEPRECATED, use `--http-allow-header` instead")

	flag.Var(&Settings.modifierConfig.headerNegativeFilters, "http-disallow-header", "A regexp to match a specific header against. Requests with matching headers will be dropped:\n\t gor --input-raw :8080 --output-http staging.com --http-disallow-header \"User-Agent: Replayed by Gor\"")

	flag.Var(&Settings.modifierConfig.headerBasicAuthFilters, "http-basic-auth-filter", "A regexp to match the decoded basic auth string against. Requests with non-matching headers will be dropped:\n\t gor --input-raw :8080 --output-http staging.com --http-basic-auth-filter \"^customer[0-9].*\"")

	flag.Var(&Settings.modifierConfig.headerHashFilters, "http-header-limiter", "Takes a fraction of requests, consistently taking or rejecting a request based on the FNV32-1A hash of a specific header:\n\t gor --input-raw :8080 --output-http staging.com --http-header-limiter user-id:25%")

	flag.Var(&Settings.modifierConfig.headerHashFilters, "output-http-header-hash-filter", "WARNING: `output-http-header-hash-filter` DEPRECATED, use `--http-header-hash-limiter` instead")

	flag.Var(&Settings.modifierConfig.paramHashFilters, "http-param-limiter", "Takes a fraction of requests, consistently taking or rejecting a request based on the FNV32-1A hash of a specific GET param:\n\t gor --input-raw :8080 --output-http staging.com --http-param-limiter user_id:25%")

	// default values, using for tests
	Settings.OutputFileConfig.sizeLimit = 33554432
	Settings.OutputFileConfig.outputFileMaxSize = 1099511627776
	Settings.CopyBufferSize = 5242880
	Settings.InputRAWBufferSize = 0

	currrentDir, _ := os.Getwd()
	log.Printf("Locating config in folder: %s", currrentDir)

	flag.StringVar(&Settings.ConfigFile, "config-file", "config.json", "The path to Goreplay config file.")
	viper.SetConfigFile(currrentDir + "/" +Settings.ConfigFile)

	flag.StringVar(&Settings.ConfigServerAddress, "config-addr", ":9999", "The host address for config API.")
	viper.SetConfigFile(Settings.ConfigFile)

	flag.StringVar(&Settings.RemoteConfigHost, "config-remote-host", "", "The host address for config API.")

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

	// Searches for config file in given paths and read it
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Error reading config file, %s", err)
		return
	}

	if viper.IsSet("RemoteConfigHost") {
		go pollRemoteConfig()
	}

	fmt.Printf("Using config: %s\n", viper.ConfigFileUsed())

	initConfigServer()
}

func pollRemoteConfig() {
	for {
		req, err := http.NewRequest("GET", viper.GetString("RemoteConfigHost") + "/config.json", nil)
		if err != nil {
			log.Printf("Error while getting config from remote server., %s", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error while getting config from remote server., %s", err)
			continue
		}
		defer resp.Body.Close()

		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error while getting config from remote server., %s", err)
			continue
		}

		updateConfig(respBody)
		resp.Body.Close()
		time.Sleep(time.Second)
	}
}

func checkSettings() {
	outputFileSize, err := bufferParser(Settings.OutputFileSizeFlag, "32MB")
	if err != nil {
		log.Fatalf("output-file-size-limit error: %v\n", err)
	}
	Settings.OutputFileConfig.sizeLimit = outputFileSize

	outputFileMaxSize, err := bufferParser(Settings.OutputFileMaxSizeFlag, "1TB")
	if err != nil {
		log.Fatalf("output-file-max-size-limit error: %v\n", err)
	}
	Settings.OutputFileConfig.outputFileMaxSize = outputFileMaxSize

	copyBufferSize, err := bufferParser(Settings.CopyBufferSizeFlag, "5mb")
	if err != nil {
		log.Fatalf("copy-buffer-size error: %v\n", err)
	}
	Settings.CopyBufferSize = copyBufferSize

	inputRAWBufferSize, err := bufferParser(Settings.InputRAWBufferSizeFlag, "0")
	if err != nil {
		log.Fatalf("input-raw-buffer-size error: %v\n", err)
	}
	Settings.InputRAWBufferSize = inputRAWBufferSize

	// libpcap has bug in mac os x. More info: https://github.com/buger/goreplay/issues/730
	if Settings.InputRAWExpire == time.Second*2 && runtime.GOOS == "darwin" {
		Settings.InputRAWExpire = time.Second
	}
}

var previousDebugTime = time.Now()
var debugMutex sync.Mutex
var pID = os.Getpid()

// Debug take an effect only if --Verbose flag specified
func Debug(args ...interface{}) {
	if Settings.Verbose {
		debugMutex.Lock()
		defer debugMutex.Unlock()
		now := time.Now()
		diff := now.Sub(previousDebugTime).String()
		previousDebugTime = now
		fmt.Printf("[DEBUG][PID %d][%s][elapsed %s] ", pID, now.Format(time.StampNano), diff)
		fmt.Println(args...)
	}
}

// the following regexes follow Go semantics https://golang.org/ref/spec#Letters_and_digits
var (
	rB   = regexp.MustCompile(`(?i)^(?:0b|0x|0o)?[\da-f_]+$`)
	rKB  = regexp.MustCompile(`(?i)^(?:0b|0x|0o)?[\da-f_]+kb$`)
	rMB  = regexp.MustCompile(`(?i)^(?:0b|0x|0o)?[\da-f_]+mb$`)
	rGB  = regexp.MustCompile(`(?i)^(?:0b|0x|0o)?[\da-f_]+gb$`)
	rTB  = regexp.MustCompile(`(?i)^(?:0b|0x|0o)?[\da-f_]+tb$`)
	empt = regexp.MustCompile(`^[\n\t\r 0.\f\a]*$`)
)

// bufferParser parses buffer to bytes from different bases and data units
// size is the buffer in string, rpl act as a replacement for empty buffer.
// e.g: (--output-file-size-limit "") may override default 32mb with empty buffer,
// which can be solved by setting rpl by bufferParser(buffer, "32mb")
func bufferParser(size, rpl string) (buffer int64, err error) {
	const (
		_ = 1 << (iota * 10)
		KB
		MB
		GB
		TB
	)

	var (
		lmt = len(size) - 2
		s   = []byte(size)
	)

	if empt.Match(s) {
		size = rpl
		s = []byte(size)
	}

	// recover, especially when buffer size overflows int64 i.e ~8019PBs
	defer func() {
		if e, ok := recover().(error); ok {
			err = e.(error)
		}
	}()

	switch {
	case rB.Match(s):
		buffer, err = strconv.ParseInt(size, 0, 64)
	case rKB.Match(s):
		buffer, err = strconv.ParseInt(size[:lmt], 0, 64)
		buffer *= KB
	case rMB.Match(s):
		buffer, err = strconv.ParseInt(size[:lmt], 0, 64)
		buffer *= MB
	case rGB.Match(s):
		buffer, err = strconv.ParseInt(size[:lmt], 0, 64)
		buffer *= GB
	case rTB.Match(s):
		buffer, err = strconv.ParseInt(size[:lmt], 0, 64)
		buffer *= TB
	default:
		return 0, fmt.Errorf("invalid buffer %q", size)
	}
	return
}
