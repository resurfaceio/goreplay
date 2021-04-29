package tcp

import (
	"bytes"
	"encoding/binary"
	// "runtime"
	"testing"
	"time"

	"github.com/buger/goreplay/capture"
	"github.com/buger/goreplay/proto"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func generateHeader(seq uint32, length uint16) []byte {
	hdr := make([]byte, 4+24+24)
	binary.BigEndian.PutUint32(hdr, uint32(layers.ProtocolFamilyIPv4))

	ip := hdr[4:]
	ip[0] = 4<<4 | 6
	binary.BigEndian.PutUint16(ip[2:4], length+24+24)
	ip[9] = uint8(layers.IPProtocolTCP)
	copy(ip[12:16], []byte{127, 0, 0, 1})
	copy(ip[16:], []byte{127, 0, 0, 1})

	// set tcp header
	tcp := ip[24:]
	tcp[12] = 6 << 4
	binary.BigEndian.PutUint16(tcp, 5535)
	binary.BigEndian.PutUint16(tcp[2:], 8000)
	binary.BigEndian.PutUint32(tcp[4:], seq)
	return hdr
}

func GetPackets(start uint32, _len int, payload []byte) []*capture.Packet {
	var packets = make([]*capture.Packet, _len)
	for i := start; i < start+uint32(_len); i++ {
		d := append(generateHeader(i, uint16(len(payload))), payload...)
		ci := &gopacket.CaptureInfo{Length: len(d), CaptureLength: len(d), Timestamp: time.Now()}
		packets[i-start] = capture.NewPacket(d, int(layers.LinkTypeLoop), 4, ci)
	}
	return packets
}

func TestMessageParserWithHint(t *testing.T) {
	var mssg = make(chan *Message, 3)
	parser := NewMessageParser(1<<20, time.Second, nil, func(m *Message) { mssg <- m })
	parser.Start = func(pckt *Packet) (bool, bool) {
		return proto.HasRequestTitle(pckt.Payload), proto.HasResponseTitle(pckt.Payload)
	}
	parser.End = func(m *Message) bool {
		return proto.HasFullPayload(m.Data(), m)
	}
	packets := GetPackets(1, 30, nil)
	packets[0].TransLayer[13] = 2 // SYN flag
	packets[4] = GetPackets(1, 1, []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n7"))[0]
	packets[5] = GetPackets(1, 1, []byte("\r\nMozilla\r\n9\r\nDeveloper\r"))[0]
	packets[6] = GetPackets(1, 1, []byte("\n7\r\nNetwork\r\n0\r\n\r\n"))[0]
	packets[10].TransLayer[13] = 2 // SYN flag
	packets[14] = GetPackets(1, 1, []byte("POST / HTTP/1.1\r\nContent-Type: text/plain\r\nContent-Length: 23\r\n\r\n"))[0]
	packets[15] = GetPackets(1, 1, []byte("MozillaDeveloper"))[0]
	packets[16] = GetPackets(1, 1, []byte("Network"))[0]
	packets[24] = GetPackets(1, 1, []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 0\r\n\r"))[0]
	packets[29].TransLayer[13] = 1 // FIN flag

	for i := 0; i < 30; i++ {
		parser.PacketHandler(packets[i])
	}
	var m *Message
	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if !bytes.HasSuffix(m.Data(), []byte("\n7\r\nNetwork\r\n0\r\n\r\n")) {
		t.Errorf("expected to %q to have suffix %q", m.Data(), []byte("\n7\r\nNetwork\r\n0\r\n\r\n"))
	}

	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if !bytes.HasSuffix(m.Data(), []byte("Network")) {
		t.Errorf("expected to %q to have suffix %q", m.Data(), []byte("Network"))
	}

	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if !bytes.HasSuffix(m.Data(), []byte("Content-Length: 0\r\n\r")) {
		t.Errorf("expected to %q to have suffix %q", m.Data(), []byte("Content-Length: 0\r\n\r"))
	}

}

func TestMessageParserWithoutHint(t *testing.T) {
	var mssg = make(chan *Message, 1)
	var data [63 << 10]byte
	packets := GetPackets(1, 10, data[:])
	packets[0].TransLayer[13] = 2 // SYN flag
	packets[9].TransLayer[13] = 1 // FIN flag
	p := NewMessageParser(63<<10*10, time.Second, nil, func(m *Message) { mssg <- m })
	for _, v := range packets {
		p.PacketHandler(v)
	}
	var m *Message
	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if m.Length != 63<<10*10 {
		t.Errorf("expected %d to equal %d", m.Length, 63<<10*10)
	}
}

func TestMessageMaxSizeReached(t *testing.T) {
	var mssg = make(chan *Message, 2)
	var data [63 << 10]byte
	packets := GetPackets(1, 2, data[:])
	packets = append(packets, GetPackets(1, 1, make([]byte, 63<<10+10))...)
	packets[0].TransLayer[13] = 2 // SYN flag
	packets[2].TransLayer[13] = 2 // SYN flag
	packets[2].NetLayer[15] = 3   // changing address
	p := NewMessageParser(63<<10+10, time.Second, nil, func(m *Message) { mssg <- m })
	for _, v := range packets {
		p.PacketHandler(v)
	}
	var m *Message
	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if m.Length != 63<<10+10 {
		t.Errorf("expected %d to equal %d", m.Length, 63<<10+10)
	}
	if !m.Truncated {
		t.Error("expected message to be truncated")
	}

	select {
	case <-time.After(time.Second):
		t.Errorf("can't parse packets fast enough")
		return
	case m = <-mssg:
	}
	if m.Length != 63<<10+10 {
		t.Errorf("expected %d to equal %d", m.Length, 63<<10+10)
	}
	if m.Truncated {
		t.Error("expected message to not be truncated")
	}
}

func TestMessageTimeoutReached(t *testing.T) {
	var mssg = make(chan *Message, 2)
	var data [63 << 10]byte
	packets := GetPackets(1, 2, data[:])
	packets[0].TransLayer[13] = 2 // SYN flag
	p := NewMessageParser(1<<20, 0, nil, func(m *Message) { mssg <- m })
	p.PacketHandler(packets[0])
	time.Sleep(time.Millisecond * 400)
	p.PacketHandler(packets[1])
	m := <-mssg
	if m.Length != 63<<10 {
		t.Errorf("expected %d to equal %d", m.Length, 63<<10)
	}
	if !m.TimedOut {
		t.Error("expected message to be timeout")
	}
}

func TestMessageUUID(t *testing.T) {
	packets := GetPackets(1, 10, nil)
	packets[0].TransLayer[13] = 2 // SYN flag
	packets[4].TransLayer[13] = 1 // FIN flag
	packets[5].TransLayer[13] = 2 // SYN flag
	packets[9].TransLayer[13] = 1 // FIN flag
	var uuid, uuid1 []byte
	parser := NewMessageParser(0, 0, nil, func(msg *Message) {
		if len(uuid) == 0 {
			uuid = msg.UUID()
			return
		}
		uuid1 = msg.UUID()
	})
	parser.MatchUUID(true)
	for _, p := range packets {
		parser.PacketHandler(p)
	}

	if string(uuid) != string(uuid1) {
		t.Errorf("expected %s, to equal %s", uuid, uuid1)
	}
}

func BenchmarkMessageUUID(b *testing.B) {
	packets := GetPackets(1, 5, nil)
	packets[0].TransLayer[13] = 2 // SYN flag
	packets[4].TransLayer[13] = 1 // FIN flag
	var uuid []byte
	var msg *Message
	parser := NewMessageParser(0, 0, nil, func(m *Message) {
		msg = m
	})
	parser.MatchUUID(true)
	for _, p := range packets {
		parser.PacketHandler(p)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		uuid = msg.UUID()
	}
	_ = uuid
}

func BenchmarkPacketParseAndSort(b *testing.B) {
	m := new(Message)
	m.packets = make([]*Packet, 100)
	for i, v := range GetPackets(1, 100, nil) {
		m.packets[i], _ = ParsePacket(v)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Sort()
	}
}

func BenchmarkMessageParserWithoutHint(b *testing.B) {
	// runtime.GOMAXPROCS(8)
	var mssg = make(chan *Message, 1)
	var chunk = []byte("111111111111111111111111111111")
	packets := GetPackets(1, 1000, chunk)
	packets[0].TransLayer[13] = 2      // SYN flag
	packets[1000-1].TransLayer[13] = 1 // FIN flag
	p := NewMessageParser(1<<20, time.Second*2, nil, func(m *Message) {
		mssg <- m
	})
	b.ResetTimer()
	b.ReportMetric(float64(1000), "packets/op")
	for i := 0; i < b.N; i++ {
		for _, v := range packets {
			p.PacketHandler(v)
		}
		<-mssg
	}
}

func BenchmarkMessageParserWithHint(b *testing.B) {
	var buf [1002][]byte
	var chunk = []byte("1e\r\n111111111111111111111111111111\r\n")
	buf[0] = []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n")
	for i := 1; i < 1000; i++ {
		buf[i] = chunk
	}
	buf[1001] = []byte("0\r\n\r\n")
	packets := make([]*capture.Packet, len(buf))
	for i := 0; i < len(buf); i++ {
		packets[i] = GetPackets(1, 1, buf[i])[0]
	}
	var mssg = make(chan *Message, 1)
	parser := NewMessageParser(1<<30, time.Second*10, nil, func(m *Message) { mssg <- m })
	parser.Start = func(pckt *Packet) (bool, bool) {
		return false, proto.HasResponseTitle(pckt.Payload)
	}
	parser.End = func(m *Message) bool {
		return proto.HasFullPayload(m.Data(), m)
	}
	b.ResetTimer()
	b.ReportMetric(float64(len(packets)), "packets/op")
	b.ReportMetric(float64(1000), "chunks/op")
	for i := 0; i < b.N; i++ {
		for j := range packets {
			parser.PacketHandler(packets[j])
		}
		<-mssg
	}
}

func BenchmarkNewAndParsePacket(b *testing.B) {
	data := append(generateHeader(1024, 10), make([]byte, 10)...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParsePacket(capture.NewPacket(data, int(layers.LinkTypeLoop), 4, &gopacket.CaptureInfo{}))
	}
}

func BenchmarkNewPacket(b *testing.B) {
	packet := capture.NewPacket(
		append(generateHeader(1024, 10), make([]byte, 10)...),
		int(layers.LinkTypeLoop),
		4,
		&gopacket.CaptureInfo{},
	)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParsePacket(packet)
	}
}
