// package utils
package utils

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"time"

	"google.golang.org/protobuf/proto"
)

type V2DelimitedWriter struct {
	w *bufio.Writer
	c net.Conn
}

func NewV2DelimitedWriter(c net.Conn) *V2DelimitedWriter {
	return &V2DelimitedWriter{
		w: bufio.NewWriter(c),
		c: c,
	}
}

func (vw *V2DelimitedWriter) WriteMsgWithTimeout(m proto.Message, d time.Duration) (int, error) {
	if err := vw.c.SetWriteDeadline(time.Now().Add(d)); err != nil {
		return 0, err
	}
	return vw.WriteMsg(m)
}

func (vw *V2DelimitedWriter) WriteMsg(m proto.Message) (int, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return 0, err
	}
	// varint length prefix
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(b)))
	if _, err := vw.w.Write(hdr[:n]); err != nil {
		return 0, err
	}
	if _, err := vw.w.Write(b); err != nil {
		return 0, err
	}
	return n + len(b), vw.w.Flush()
}

type V2DelimitedReader struct {
	r *bufio.Reader
	c net.Conn
}

func NewV2DelimitedReader(c net.Conn) *V2DelimitedReader {
	return &V2DelimitedReader{
		r: bufio.NewReader(c),
		c: c,
	}
}

func (vr *V2DelimitedReader) ReadMsgWithTimeout(m proto.Message, d time.Duration) error {
	if err := vr.c.SetReadDeadline(time.Now().Add(d)); err != nil {
		return err
	}
	return vr.ReadMsg(m)
}

func (vr *V2DelimitedReader) ReadMsg(m proto.Message) error {
	// read varint length
	l, err := binary.ReadUvarint(vr.r)
	if err != nil {
		return err
	}
	if l == 0 {
		return nil
	}
	buf := make([]byte, l)
	if _, err := io.ReadFull(vr.r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, m)
}
