package gomemcached

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MCResponse is memcached response
type MCResponse struct {
	// The command opcode of the command that sent the request
	Opcode CommandCode
	// The status of the response
	Status Status
	// The opaque sent in the request
	Opaque uint32
	// The CAS identifier (if applicable)
	Cas uint64
	// Extras, key, and body for this response
	Extras, Key, Body []byte
	// If true, this represents a fatal condition and we should hang up
	Fatal bool
}

// A debugging string representation of this response
func (res MCResponse) String() string {
	return fmt.Sprintf("{MCResponse status=%v keylen=%d, extralen=%d, bodylen=%d}",
		res.Status, len(res.Key), len(res.Extras), len(res.Body))
}

// Response as an error.
func (res *MCResponse) Error() string {
	return fmt.Sprintf("MCResponse status=%v, opcode=%v, opaque=%v, msg: %s",
		res.Status, res.Opcode, res.Opaque, string(res.Body))
}

func errStatus(e error) Status {
	status := Status(0xffff)
	if res, ok := e.(*MCResponse); ok {
		status = res.Status
	}
	return status
}

// IsNotFound is true if this error represents a "not found" response.
func IsNotFound(e error) bool {
	return errStatus(e) == KEY_ENOENT
}

// IsFatal is false if this error isn't believed to be fatal to a connection.
func IsFatal(e error) bool {
	if e == nil {
		return false
	}
	switch errStatus(e) {
	case KEY_ENOENT, KEY_EEXISTS, NOT_STORED, TMPFAIL:
		return false
	}
	return true
}

// Size is number of bytes this response consumes on the wire.
func (res *MCResponse) Size() int {
	return HDR_LEN + len(res.Extras) + len(res.Key) + len(res.Body)
}

func (res *MCResponse) fillHeaderBytes(data []byte) int {
	pos := 0
	data[pos] = RES_MAGIC
	pos++
	data[pos] = byte(res.Opcode)
	pos++
	binary.BigEndian.PutUint16(data[pos:pos+2],
		uint16(len(res.Key)))
	pos += 2

	// 4
	data[pos] = byte(len(res.Extras))
	pos++
	data[pos] = 0
	pos++
	binary.BigEndian.PutUint16(data[pos:pos+2], uint16(res.Status))
	pos += 2

	// 8
	binary.BigEndian.PutUint32(data[pos:pos+4],
		uint32(len(res.Body)+len(res.Key)+len(res.Extras)))
	pos += 4

	// 12
	binary.BigEndian.PutUint32(data[pos:pos+4], res.Opaque)
	pos += 4

	// 16
	binary.BigEndian.PutUint64(data[pos:pos+8], res.Cas)
	pos += 8

	if len(res.Extras) > 0 {
		copy(data[pos:pos+len(res.Extras)], res.Extras)
		pos += len(res.Extras)
	}

	if len(res.Key) > 0 {
		copy(data[pos:pos+len(res.Key)], res.Key)
		pos += len(res.Key)
	}

	return pos
}

// HeaderBytes will get just the header bytes for this response.
func (res *MCResponse) HeaderBytes() []byte {
	data := make([]byte, HDR_LEN+len(res.Extras)+len(res.Key))

	res.fillHeaderBytes(data)

	return data
}

// Bytes will return the actual bytes transmitted for this response.
func (res *MCResponse) Bytes() []byte {
	data := make([]byte, res.Size())

	pos := res.fillHeaderBytes(data)

	copy(data[pos:pos+len(res.Body)], res.Body)

	return data
}

// Transmit will send this response message across a writer.
func (res *MCResponse) Transmit(w io.Writer) (n int, err error) {
	if len(res.Body) < 128 {
		n, err = w.Write(res.Bytes())
	} else {
		n, err = w.Write(res.HeaderBytes())
		if err == nil {
			m := 0
			m, err = w.Write(res.Body)
			m += n
		}
	}
	return
}

// Receive will fill this MCResponse with the data from this reader.
func (res *MCResponse) Receive(r io.Reader, hdrBytes []byte) (n int, err error) {
	if len(hdrBytes) < HDR_LEN {
		hdrBytes = []byte{
			0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, 0, 0}
	}
	n, err = io.ReadFull(r, hdrBytes)
	if err != nil {
		return n, err
	}

	if hdrBytes[0] != RES_MAGIC && hdrBytes[0] != REQ_MAGIC {
		return n, fmt.Errorf("bad magic: 0x%02x", hdrBytes[0])
	}

	klen := int(binary.BigEndian.Uint16(hdrBytes[2:4]))
	elen := int(hdrBytes[4])

	res.Opcode = CommandCode(hdrBytes[1])
	res.Status = Status(binary.BigEndian.Uint16(hdrBytes[6:8]))
	res.Opaque = binary.BigEndian.Uint32(hdrBytes[12:16])
	res.Cas = binary.BigEndian.Uint64(hdrBytes[16:24])

	bodyLen := int(binary.BigEndian.Uint32(hdrBytes[8:12])) - (klen + elen)

	//defer function to debug the panic seen with MB-15557
	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf(`Panic in Receive. Response %v \n
                        key len %v extra len %v bodylen %v`, res, klen, elen, bodyLen)
		}
	}()

	buf := make([]byte, klen+elen+bodyLen)
	m, err := io.ReadFull(r, buf)
	if err == nil {
		res.Extras = buf[0:elen]
		res.Key = buf[elen : klen+elen]
		res.Body = buf[klen+elen:]
	}

	return n + m, err
}
