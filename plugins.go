package main

import (
	"io"
	"reflect"
	"strings"
	"sync"
)

// InOutPlugins struct for holding references to plugins
type InOutPlugins struct {
	Inputs  []io.Reader
	Outputs []io.Writer
	All     []interface{}
}

var pluginMu sync.Mutex

// Plugins holds all the plugin objects
var plugins *InOutPlugins = new(InOutPlugins)

// extractLimitOptions detects if plugin get called with limiter support
// Returns address and limit
func extractLimitOptions(options string) (string, string) {
	split := strings.Split(options, "|")

	if len(split) > 1 {
		return split[0], split[1]
	}

	return split[0], ""
}

// Automatically detects type of plugin and initialize it
//
// See this article if curious about relfect stuff below: http://blog.burntsushi.net/type-parametric-functions-golang
func registerPlugin(constructor interface{}, options ...interface{}) {
	var path, limit string
	vc := reflect.ValueOf(constructor)

	// Pre-processing options to make it work with reflect
	vo := []reflect.Value{}
	for _, oi := range options {
		vo = append(vo, reflect.ValueOf(oi))
	}

	if len(vo) > 0 {
		// Removing limit options from path
		path, limit = extractLimitOptions(vo[0].String())

		// Writing value back without limiter "|" options
		vo[0] = reflect.ValueOf(path)
	}

	// Calling our constructor with list of given options
	plugin := vc.Call(vo)[0].Interface()
	pluginWrapper := plugin

	if limit != "" {
		pluginWrapper = NewLimiter(plugin, limit)
	} else {
		pluginWrapper = plugin
	}

	_, isR := plugin.(io.Reader)
	_, isW := plugin.(io.Writer)

	// Some of the output can be Readers as well because return responses
	if isR && !isW {
		plugins.Inputs = append(plugins.Inputs, pluginWrapper.(io.Reader))
	}

	if isW {
		plugins.Outputs = append(plugins.Outputs, pluginWrapper.(io.Writer))
	}

	plugins.All = append(plugins.All, plugin)
}

// InitPlugins specify and initialize all available plugins
func InitPlugins() *InOutPlugins {
	pluginMu.Lock()
	defer pluginMu.Unlock()

	for _, options := range Settings.InputDummy {
		registerPlugin(NewDummyInput, options)
	}

	for range Settings.OutputDummy {
		registerPlugin(NewDummyOutput)
	}

	if Settings.OutputStdout {
		registerPlugin(NewDummyOutput)
	}

	if Settings.OutputNull {
		registerPlugin(NewNullOutput)
	}

	engine := EnginePcap
	if Settings.InputRAWConfig.Engine == "raw_socket" {
		engine = EngineRawSocket
	} else if Settings.InputRAWConfig.Engine == "pcap_file" {
		engine = EnginePcapFile
	}

	for _, options := range Settings.InputRAW {
		registerPlugin(NewRAWInput, options, engine, Settings.InputRAWConfig.TrackResponse, Settings.InputRAWConfig.Expire, Settings.InputRAWConfig.RealIPHeader, Settings.InputRAWConfig.Protocol, Settings.InputRAWConfig.BpfFilter, Settings.InputRAWConfig.TimestampType, Settings.InputRAWConfig.bufferSize)
	}

	for _, options := range Settings.InputTCP {
		registerPlugin(NewTCPInput, options, &Settings.InputTCPConfig)
	}

	for _, options := range Settings.OutputTCP {
		registerPlugin(NewTCPOutput, options, &Settings.OutputTCPConfig)
	}

	for _, options := range Settings.InputFile {
		registerPlugin(NewFileInput, options, Settings.InputFileLoop)
	}

	for _, path := range Settings.OutputFile {
		if strings.HasPrefix(path, "s3://") {
			registerPlugin(NewS3Output, path, &Settings.OutputFileConfig)
		} else {
			registerPlugin(NewFileOutput, path, &Settings.OutputFileConfig)
		}
	}

	for _, options := range Settings.InputHTTP {
		registerPlugin(NewHTTPInput, options)
	}

	// If we explicitly set Host header http output should not rewrite it
	// Fix: https://github.com/buger/gor/issues/174
	for _, header := range Settings.ModifierConfig.Headers {
		if header.Name == "Host" {
			Settings.OutputHTTPConfig.OriginalHost = true
			break
		}
	}

	for _, options := range Settings.OutputHTTP {
		registerPlugin(NewHTTPOutput, options, &Settings.OutputHTTPConfig)
	}

	for _, options := range Settings.OutputBinary {
		registerPlugin(NewBinaryOutput, options, &Settings.OutputBinaryConfig)
	}

	if Settings.OutputKafkaConfig.Host != "" && Settings.OutputKafkaConfig.Topic != "" {
		registerPlugin(NewKafkaOutput, "", &Settings.OutputKafkaConfig)
	}

	if Settings.InputKafkaConfig.Host != "" && Settings.InputKafkaConfig.Topic != "" {
		registerPlugin(NewKafkaInput, "", &Settings.InputKafkaConfig)
	}

	return plugins
}
