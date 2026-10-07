package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Sway's IPC (the i3 protocol): every message is the magic "i3-ipc", a
// little-endian uint32 payload length, a uint32 type, then the JSON payload.
// Events have the high bit of the type set.
const (
	ipcRunCommand    = 0
	ipcGetWorkspaces = 1
	ipcSubscribe     = 2
	ipcGetOutputs    = 3
	ipcGetTree       = 4
	ipcGetVersion    = 7

	eventFlag       = 1 << 31
	eventWorkspace  = eventFlag // | 0
	eventOutput     = eventFlag | 1
	eventWindow     = eventFlag | 3
	eventShutdown   = eventFlag | 6
	maxPayload      = 64 << 20
	ipcHeaderLength = 14
)

var ipcMagic = []byte("i3-ipc")

// writeMessage sends one message.
func writeMessage(w io.Writer, typ uint32, payload []byte) error {
	buf := make([]byte, ipcHeaderLength+len(payload))
	copy(buf, ipcMagic)
	binary.LittleEndian.PutUint32(buf[6:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(buf[10:], typ)
	copy(buf[ipcHeaderLength:], payload)
	_, err := w.Write(buf)
	return err
}

// readMessage reads one whole message, however the bytes arrive.
func readMessage(r io.Reader) (typ uint32, payload []byte, err error) {
	var hdr [ipcHeaderLength]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if string(hdr[:6]) != string(ipcMagic) {
		return 0, nil, errors.New("sway ipc: bad magic")
	}
	n := binary.LittleEndian.Uint32(hdr[6:])
	if n > maxPayload {
		return 0, nil, fmt.Errorf("sway ipc: payload of %d bytes is too large", n)
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return binary.LittleEndian.Uint32(hdr[10:]), payload, nil
}
