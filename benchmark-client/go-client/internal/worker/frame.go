package worker

import (
	"bytes"
	"io"
)

var TimestampKey = []byte(`"timestamp":`)

func ReadFrameReusable(r io.Reader, dst []byte) ([]byte, error) {
	dst = dst[:0]
	var scratch [32 * 1024]byte

	for {
		n, err := r.Read(scratch[:])
		if n > 0 {
			dst = append(dst, scratch[:n]...)
		}
		if err == io.EOF {
			return dst, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func ExtractTimestampInt64(msg []byte) int64 {
	idx := bytes.Index(msg, TimestampKey)
	if idx < 0 {
		return 0
	}

	i := idx + len(TimestampKey)
	for i < len(msg) {
		c := msg[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		break
	}

	var n int64
	found := false
	for i < len(msg) {
		c := msg[i]
		if c < '0' || c > '9' {
			break
		}
		found = true
		n = n*10 + int64(c-'0')
		i++
	}

	if !found {
		return 0
	}
	return n
}
