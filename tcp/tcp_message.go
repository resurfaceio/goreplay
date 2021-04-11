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
	pool     *MessagePool
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
	if m.pool.uuids != nil {
		lst := len(pckt.SrcIP) - 4
		if m.IsIncoming {
			key = uint64(pckt.SrcPort)<<48 | uint64(pckt.DstPort)<<32 |
				uint64(_uint32(pckt.SrcIP[lst:]))
		} else {
			key = uint64(pckt.DstPort)<<48 | uint64(pckt.SrcPort)<<32 |
				uint64(_uint32(pckt.DstIP[lst:]))
		}
		if uuidHex, ok := m.pool.uuids[key]; ok {
			delete(m.pool.uuids, key)
			return uuidHex
		}
	}

	id := make([]byte, 12)
	binary.BigEndian.PutUint32(id, pckt.Seq)
	tStamp := m.End.UnixNano()
	binary.BigEndian.PutUint64(id[4:], uint64(tStamp))
	uuidHex := make([]byte, 24)
	hex.Encode(uuidHex[:], id[:])
	if m.pool.uuids != nil {
		if len(m.pool.uuids) >= 1000 {
			m.pool.cleanUUIDs()
		}
		m.pool.uuids[key] = uuidHex
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

// SetFeedback set feedback/data that can be used later, e.g with End or Start hint
func (m *Message) SetFeedback(feedback interface{}) {
	m.feedback = feedback
}

// Feedback returns feedback associated to this message
func (m *Message) Feedback() interface{} {
	return m.feedback
}

// Sort a helper to sort packets
func (m *Message) Sort() {
	sort.SliceStable(m.packets, func(i, j int) bool { return m.packets[i].Seq < m.packets[j].Seq })
}

// hold message locks before calling this function
func (m *Message) doDone(key uint64) {
	m.pool.handler(m)
	delete(m.pool.m, key)
	m.pool.msgs--
}

// Handler message handler
type Handler func(*Message)

// Debugger is the debugger function. first params is the indicator of the issue's priority
// the higher the number, the lower the priority. it can be 4 <= level <= 6.
type Debugger func(int, ...interface{})

// HintEnd hints the pool to stop the session, see MessagePool.End
// when set, it will be executed before checking FIN or RST flag
type HintEnd func(*Message) bool

// HintStart hints the pool to start the reassembling the message, see MessagePool.Start
// when set, it will be called after checking SYN flag
type HintStart func(*Packet) (IsIncoming, IsOutgoing bool)

// MessagePool holds data of all tcp messages in progress(still receiving/sending packets).
// message is identified by its source port and dst port, and last 4bytes of src IP.
type MessagePool struct {
	debug         Debugger
	maxSize       size.Size // maximum message size, default 5mb
	m             map[uint64]*Message
	uuids         map[uint64][]byte
	handler       Handler
	messageExpire time.Duration // the maximum time to wait for the final packet, minimum is 100ms
	End           HintEnd
	Start         HintStart
	ticker        *time.Ticker
	packets       chan *capture.Packet
	ticks         time.Duration // the rate of ticks
	msgs          int32         // messages in the pool
	close         chan struct{} // to signal that we are able to close
}

// NewMessagePool returns a new instance of message pool
func NewMessagePool(maxSize size.Size, messageExpire time.Duration, debugger Debugger, handler Handler) (pool *MessagePool) {
	pool = new(MessagePool)
	pool.debug = debugger
	pool.handler = handler
	pool.messageExpire = time.Millisecond * 100
	if pool.messageExpire < messageExpire {
		pool.messageExpire = messageExpire
	}
	pool.maxSize = maxSize
	if pool.maxSize < 1 {
		pool.maxSize = 5 << 20
	}
	pool.packets = make(chan *capture.Packet, 1e4)
	pool.m = make(map[uint64]*Message)
	pool.ticker = time.NewTicker(minTicks << 3)
	pool.ticks = minTicks << 3
	pool.close = make(chan struct{}, 1)
	go pool.wait()
	return pool
}

// Handler returns packet handler
func (pool *MessagePool) Handler(packet *capture.Packet) {
	pool.packets <- packet
}

func (pool *MessagePool) wait() {
	var (
		pckt *capture.Packet
		now  time.Time
	)
	for {
		select {
		case pckt = <-pool.packets:
			pool.parsePacket(pckt)
		case now = <-pool.ticker.C:
			pool.timer(now)
		case <-pool.close:
			pool.ticker.Stop()
			// pool.Close should wait for this function to return
			pool.close <- struct{}{}
			return
		}
	}
}

func (pool *MessagePool) parsePacket(packet *capture.Packet) {
	var in, out bool
	pckt, err := ParsePacket(packet)
	if err != nil {
		pool.say(4, fmt.Sprintf("error decoding packet(%dBytes):%s\n", packet.Info.CaptureLength, err))
		return
	}
	lst := len(pckt.SrcIP) - 4
	key := uint64(pckt.SrcPort)<<48 | uint64(pckt.DstPort)<<32 |
		uint64(_uint32(pckt.SrcIP[lst:]))
	m, ok := pool.m[key]
	if pckt.RST {
		if ok {
			m.doDone(key)
		}
		key = uint64(pckt.DstPort)<<48 | uint64(pckt.SrcPort)<<32 |
			uint64(_uint32(pckt.DstIP[lst:]))
		m, ok = pool.m[key]
		if ok {
			m.doDone(key)
		}
		pool.say(4, fmt.Sprintf("RST flag from %s to %s", pckt.Src(), pckt.Dst()))
		return
	}
	switch {
	case ok:
		pool.addPacket(key, m, pckt)
		return
	case pckt.SYN:
		in = !pckt.ACK
	case pool.Start != nil:
		if in, out = pool.Start(pckt); !(in || out) {
			return
		}
	default:
		return
	}
	m = NewMessage(pckt.Src(), pckt.Dst(), pckt.Version)
	m.IsIncoming = in
	pool.m[key] = m
	m.Start = pckt.Timestamp
	m.pool = pool
	pool.msgs++
	if pool.msgs > 2000 {
		// if pool has a lot of un-dispacthed messages, timer is probably ticking very slow.
		// try to decrease tick duration(tick faster)
		pool.decreaseTicks()
	}
	pool.addPacket(key, m, pckt)
}

// MatchUUID instructs the pool to use same UUID for request and responses
// this function should be called at initial stage of the pool
func (pool *MessagePool) MatchUUID(match bool) {
	if match {
		pool.uuids = make(map[uint64][]byte)
		return
	}
	pool.uuids = nil
}

// run GC on UUID map
func (pool *MessagePool) cleanUUIDs() {
	var tStamp int64
	now := time.Now().UnixNano()
	for k, v := range pool.uuids {
		// there is a timestamp wrapped in every ID
		// this 1 millisecond, is here to account for a time a message
		// before it being dispatched
		tStamp = int64(binary.BigEndian.Uint64(v[4:])) - int64(time.Millisecond)
		if time.Duration(now-tStamp) > pool.messageExpire {
			delete(pool.uuids, k)
		}
	}
}

func (pool *MessagePool) addPacket(key uint64, m *Message, pckt *Packet) {
	trunc := m.Length + len(pckt.Payload) - int(pool.maxSize)
	if trunc > 0 {
		m.Truncated = true
		pckt.Payload = pckt.Payload[:int(pool.maxSize)-m.Length]
	}
	m.add(pckt)
	switch {
	// if one of this cases matches, we dispatch the message
	case trunc >= 0:
	case pckt.FIN:
	case pool.End != nil && pool.End(m):
	default:
		// continue to receive packets
		return
	}
	m.doDone(key)
}

func (pool *MessagePool) timer(now time.Time) {
	found := false
	for k, m := range pool.m {
		if now.Sub(m.End) > pool.messageExpire {
			m.TimedOut = true
			m.doDone(k)
			found = true
		}
	}
	// if we did not find any expired message, timer is probably ticking at a very fast rate.
	// try to increase tick duration(tick slower).
	if !found {
		pool.increaseTicks()
	}
}

const (
	minTicks = time.Millisecond * 50
	maxTicks = minTicks << 7
)

func (pool *MessagePool) increaseTicks() {
	if t := pool.ticks << 1; t <= maxTicks {
		pool.ticks = t
		pool.ticker.Reset(t)
	}
}

func (pool *MessagePool) decreaseTicks() {
	if t := pool.ticks >> 1; t >= minTicks {
		pool.ticks = t
		pool.ticker.Reset(t)
	}
}

// this function should not block other pool operations
func (pool *MessagePool) say(level int, args ...interface{}) {
	if pool.debug != nil {
		pool.debug(level, args...)
	}
}

func (pool *MessagePool) Close() error {
	pool.close <- struct{}{}
	<-pool.close // wait for timer to be closed!
	return nil
}

func _uint32(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}
