package tcp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/buger/goreplay/capture"
	"github.com/buger/goreplay/size"
)

// Stats every message carry its own stats object
type Stats struct {
	LostData   int
	Length     int       // length of the data
	Start      time.Time // first packet's timestamp
	End        time.Time // last packet's timestamp
	SrcAddr    string
	DstAddr    string
	IsIncoming bool
	TimedOut   bool // timeout before getting the whole message
	Truncated  bool // last packet truncated due to max message size
	IPversion  byte
}

// Message is the representation of a tcp message
type Message struct {
	packets  []*Packet
	parser   *MessageParser
	buf      *bytes.Buffer
	feedback interface{}
	Stats
}

// NewMessage ...
func NewMessage(srcAddr, dstAddr string, ipVersion uint8) (m *Message) {
	m = new(Message)
	m.DstAddr = dstAddr
	m.SrcAddr = srcAddr
	m.IPversion = ipVersion
	m.buf = &bytes.Buffer{}
	return
}

// UUID returns the UUID of a TCP request and its response.
func (m *Message) UUID() []byte {
	var key uint64
	pckt := m.packets[0]

	// check if response or request have generated the ID before.
	if m.parser.uuids != nil {
		lst := len(pckt.SrcIP) - 4
		if m.IsIncoming {
			key = uint64(pckt.SrcPort)<<48 | uint64(pckt.DstPort)<<32 |
				uint64(_uint32(pckt.SrcIP[lst:]))
		} else {
			key = uint64(pckt.DstPort)<<48 | uint64(pckt.SrcPort)<<32 |
				uint64(_uint32(pckt.DstIP[lst:]))
		}
		if uuidHex, ok := m.parser.uuids[key]; ok {
			delete(m.parser.uuids, key)
			return uuidHex
		}
	}

	id := make([]byte, 12)
	binary.BigEndian.PutUint32(id, pckt.Seq)
	now := m.End.UnixNano()
	binary.BigEndian.PutUint64(id[4:], uint64(now))
	uuidHex := make([]byte, 24)
	hex.Encode(uuidHex[:], id[:])
	if m.parser.uuids != nil {
		if len(m.parser.uuids) >= 1000 {
			m.parser.cleanUUIDs()
		}
		m.parser.uuids[key] = uuidHex
	}
	return uuidHex
}

func (m *Message) add(pckt *Packet) {
	m.Length += len(pckt.Payload)
	m.LostData += int(pckt.Lost)
	m.packets = append(m.packets, pckt)
	m.End = pckt.Timestamp
	m.buf.Write(pckt.Payload)
}

// Packets returns packets of the message
func (m *Message) Packets() []*Packet {
	return m.packets
}

// Data returns data in this message
func (m *Message) Data() []byte {
	return m.buf.Bytes()
}

// SetProtocolState set feedback/data that can be used later, e.g with End or Start hint
func (m *Message) SetProtocolState(feedback interface{}) {
	m.feedback = feedback
}

// ProtocolState returns feedback associated to this message
func (m *Message) ProtocolState() interface{} {
	return m.feedback
}

// Sort a helper to sort packets
func (m *Message) Sort() {
	sort.SliceStable(m.packets, func(i, j int) bool { return m.packets[i].Seq < m.packets[j].Seq })
}

// hold message locks before calling this function
func (m *Message) doDone(key uint64) {
	m.parser.emit(m)
	delete(m.parser.m, key)
	m.parser.msgs--
}

// Emitter message handler
type Emitter func(*Message)

// Debugger is the debugger function. first params is the indicator of the issue's priority
// the higher the number, the lower the priority. it can be 4 <= level <= 6.
type Debugger func(int, ...interface{})

// HintEnd hints the parser to stop the session, see MessageParser.End
// when set, it will be executed before checking FIN or RST flag
type HintEnd func(*Message) bool

// HintStart hints the parser to start the reassembling the message, see MessageParser.Start
// when set, it will be called after checking SYN flag
type HintStart func(*Packet) (IsIncoming, IsOutgoing bool)

// MessageParser holds data of all tcp messages in progress(still receiving/sending packets).
// message is identified by its source port and dst port, and last 4bytes of src IP.
type MessageParser struct {
	debug         Debugger
	maxSize       size.Size // maximum message size, default 5mb
	m             map[uint64]*Message
	uuids         map[uint64][]byte
	emit          Emitter
	messageExpire time.Duration // the maximum time to wait for the final packet, minimum is 100ms
	End           HintEnd
	Start         HintStart
	ticker        *time.Ticker
	packets       chan *capture.Packet
	msgs          int32         // messages in the parser
	close         chan struct{} // to signal that we are able to close
}

// NewMessageParser returns a new instance of message parser
func NewMessageParser(maxSize size.Size, messageExpire time.Duration, debugger Debugger, emitHandler Emitter) (parser *MessageParser) {
	parser = new(MessageParser)
	parser.debug = debugger
	parser.emit = emitHandler
	parser.messageExpire = time.Millisecond * 100
	if parser.messageExpire < messageExpire {
		parser.messageExpire = messageExpire
	}
	parser.maxSize = maxSize
	if parser.maxSize < 1 {
		parser.maxSize = 5 << 20
	}
	parser.packets = make(chan *capture.Packet, 1e4)
	parser.m = make(map[uint64]*Message)
	parser.ticker = time.NewTicker(time.Millisecond * 50)
	parser.close = make(chan struct{}, 1)
	go parser.wait()
	return parser
}

// Packet returns packet handler
func (parser *MessageParser) PacketHandler(packet *capture.Packet) {
	parser.packets <- packet
}

func (parser *MessageParser) wait() {
	var (
		pckt *capture.Packet
		now  time.Time
	)
	for {
		select {
		case pckt = <-parser.packets:
			parser.parsePacket(pckt)
		case now = <-parser.ticker.C:
			parser.timer(now)
		case <-parser.close:
			parser.ticker.Stop()
			// parser.Close should wait for this function to return
			parser.close <- struct{}{}
			return
		}
	}
}

func (parser *MessageParser) parsePacket(packet *capture.Packet) {
	var in, out bool
	pckt, err := ParsePacket(packet)
	if err != nil {
		parser.debug(4, fmt.Sprintf("error decoding packet(%dBytes):%s\n", packet.Info.CaptureLength, err))
		return
	}
	lst := len(pckt.SrcIP) - 4
	key := uint64(pckt.SrcPort)<<48 | uint64(pckt.DstPort)<<32 |
		uint64(_uint32(pckt.SrcIP[lst:]))
	m, ok := parser.m[key]
	if pckt.RST {
		if ok {
			m.doDone(key)
		}
		key = uint64(pckt.DstPort)<<48 | uint64(pckt.SrcPort)<<32 |
			uint64(_uint32(pckt.DstIP[lst:]))
		m, ok = parser.m[key]
		if ok {
			m.doDone(key)
		}
		parser.debug(4, fmt.Sprintf("RST flag from %s to %s", pckt.Src(), pckt.Dst()))
		return
	}
	switch {
	case ok:
		parser.addPacket(key, m, pckt)
		return
	case pckt.SYN:
		in = !pckt.ACK
	case parser.Start != nil:
		if in, out = parser.Start(pckt); !(in || out) {
			return
		}
	default:
		return
	}
	m = NewMessage(pckt.Src(), pckt.Dst(), pckt.Version)
	m.IsIncoming = in
	parser.m[key] = m
	m.Start = pckt.Timestamp
	m.parser = parser
	parser.addPacket(key, m, pckt)
}

// MatchUUID instructs the parser to use same UUID for request and responses
// this function should be called at initial stage of the parser
func (parser *MessageParser) MatchUUID(match bool) {
	if match {
		parser.uuids = make(map[uint64][]byte)
		return
	}
	parser.uuids = nil
}

// run GC on UUID map
func (parser *MessageParser) cleanUUIDs() {
	var now int64
	before := time.Now().UnixNano()
	for k, v := range parser.uuids {
		// there is a timestamp wrapped in every ID
		// this 1 millisecond, is here to account for a time a message
		// before it being dispatched
		now = int64(binary.BigEndian.Uint64(v[4:])) - int64(time.Millisecond)
		if time.Duration(before-now) > parser.messageExpire {
			delete(parser.uuids, k)
		}
	}
}

func (parser *MessageParser) addPacket(key uint64, m *Message, pckt *Packet) {
	trunc := m.Length + len(pckt.Payload) - int(parser.maxSize)
	if trunc > 0 {
		m.Truncated = true
		pckt.Payload = pckt.Payload[:int(parser.maxSize)-m.Length]
	}
	m.add(pckt)
	switch {
	// if one of this cases matches, we dispatch the message
	case trunc >= 0:
	case pckt.FIN:
	case parser.End != nil && parser.End(m):
	default:
		// continue to receive packets
		return
	}
	m.doDone(key)
}

func (parser *MessageParser) timer(now time.Time) {
	for k, m := range parser.m {
		if now.Sub(m.End) > parser.messageExpire {
			m.TimedOut = true
			m.doDone(k)
		}
	}
}

// this function should not block other parser operations
func (parser *MessageParser) Debug(level int, args ...interface{}) {
	if parser.debug != nil {
		parser.debug(level, args...)
	}
}

func (parser *MessageParser) Close() error {
	parser.close <- struct{}{}
	<-parser.close // wait for timer to be closed!
	return nil
}

func _uint32(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}
