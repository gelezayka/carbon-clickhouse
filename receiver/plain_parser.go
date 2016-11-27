package receiver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"
	"unsafe"
)

// https://github.com/golang/go/issues/2632#issuecomment-66061057
func unsafeString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

var ZeroDay = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

func DaysFrom1970(t time.Time) int {
	return int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Sub(ZeroDay) / (24 * time.Hour))
}

// PlainParser with local values cache
// Not thread-safe!
type PlainParser struct {
	In  chan *Buffer
	Out chan *WriteBuffer

	// cache current day days count from 1970
	todayStartTimestamp uint32
	todayEndTimestamp   uint32
	todayDays           uint16
}

func (pp *PlainParser) Days(timestamp uint32, now uint32) uint16 {
	if timestamp < pp.todayStartTimestamp {
		return uint16(DaysFrom1970(time.Unix(int64(timestamp), 0)))
	}

	// timestamp >= pp.todayStartTimestamp
	if timestamp <= pp.todayEndTimestamp {
		return pp.todayDays
	}

	// timestamp > pp.todayEndTimestamp
	// check now date
	if now > pp.todayEndTimestamp {
		// update "today" required
		d := time.Unix(int64(now), 0)
		pp.todayStartTimestamp = uint32(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local).Unix())
		pp.todayEndTimestamp = uint32(time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local).Unix())
		pp.todayDays = uint16(DaysFrom1970(time.Unix(int64(now), 0)))
	}

	return uint16(DaysFrom1970(time.Unix(int64(timestamp), 0)))
}

func ParsePlainLine(p []byte) ([]byte, float64, uint32, error) {
	i1 := bytes.IndexByte(p, ' ')
	if i1 < 1 {
		return nil, 0, 0, fmt.Errorf("bad message: %#v", string(p))
	}

	i2 := bytes.IndexByte(p[i1+1:], ' ')
	if i2 < 1 {
		return nil, 0, 0, fmt.Errorf("bad message: %#v", string(p))
	}
	i2 += i1 + 1

	i3 := len(p)
	if p[i3-1] == '\n' {
		i3--
	}

	value, err := strconv.ParseFloat(unsafeString(p[i1+1:i2]), 64)
	if err != nil || math.IsNaN(value) {
		return nil, 0, 0, fmt.Errorf("bad message: %#v", string(p))
	}

	tsf, err := strconv.ParseFloat(unsafeString(p[i2+1:i3]), 64)
	if err != nil || math.IsNaN(tsf) {
		return nil, 0, 0, fmt.Errorf("bad message: %#v", string(p))
	}

	return p[:i1], value, uint32(tsf), nil
}

func (pp *PlainParser) Buffer(b *Buffer, wb *WriteBuffer) {
	offset := 0

	version := make([]byte, 4)
	binary.LittleEndian.PutUint32(version, b.Time)

MainLoop:
	for offset < b.Used {
		lineEnd := bytes.IndexByte(b.Body[offset:b.Used], '\n')
		if lineEnd < 0 {
			// @TODO: log unfinished line
			break MainLoop
		} else if lineEnd == 0 {
			// skip empty line
			offset++
			continue MainLoop
		}

		name, value, timestamp, err := ParsePlainLine(b.Body[offset : offset+lineEnd+1])
		offset += lineEnd + 1

		// @TODO: check required buffer size, get new

		if err != nil {
			// @TODO: log error
			continue MainLoop
		}

		// write result to buffer for clickhouse
		wb.RowBinaryWriteBytes(name)
		wb.RowBinaryWriteFloat64(value)
		wb.RowBinaryWriteUint32(timestamp)
		wb.RowBinaryWriteUint16(pp.Days(timestamp, b.Time))
		wb.Write(version)
	}
}

func (pp *PlainParser) Worker(exit chan struct{}) {
	for {
		select {
		case <-exit:
			return
		case b := <-pp.In:
			w := GetWriteBuffer()
			pp.Buffer(b, w)
			b.Release()

			select {
			case pp.Out <- w:
				// pass
			case <-exit:
				return
			}

		}
	}
}
