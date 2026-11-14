// Package preview prepares local-video seek images without persistent storage.
package preview

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
)

const (
	MaxImageBytes = 256 * 1024
	MaxVideoBytes = 8 * 1024 * 1024
	MaxCacheBytes = 64 * 1024 * 1024
	TotalSamples  = 101
)

type Event struct {
	Kind       string  `json:"kind"`
	Duration   float64 `json:"duration,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	Rotation   int     `json:"rotation,omitempty"`
	Total      int     `json:"total,omitempty"`
	Progress   int     `json:"progress,omitempty"`
	Time       float64 `json:"time"`
	SampleTime float64 `json:"sampleTime,omitempty"`
}

type Record struct {
	Event
	Image []byte
}

func ReadRecord(r io.Reader) (Record, error) {
	var record Record
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return record, err
	}
	n, m := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
	if n == 0 || n > 1024 || m > MaxImageBytes {
		return record, errors.New("invalid preview record length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return record, err
	}
	if err := json.Unmarshal(body, &record.Event); err != nil {
		return record, err
	}
	if err := record.validate(int(m)); err != nil {
		return record, err
	}
	record.Image = make([]byte, m)
	_, err := io.ReadFull(r, record.Image)
	return record, err
}

func (r Record) validate(imageBytes int) error {
	finite := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
	ok := false
	switch r.Kind {
	case "meta":
		ok = r.Duration > 0 && finite(r.Duration) && r.Width >= 2 && r.Height >= 2 &&
			r.Width <= 320 && r.Height <= 320 && r.Width*r.Height <= 62000 &&
			r.Total == TotalSamples && r.Rotation >= 0 && r.Rotation < 360 && r.Rotation%90 == 0 && imageBytes == 0
	case "frame", "progress":
		ok = r.Progress >= 1 && r.Progress <= TotalSamples && finite(r.Time) && r.Time >= 0 &&
			finite(r.SampleTime) && r.SampleTime >= 0 && ((r.Kind == "frame" && imageBytes > 0) || (r.Kind == "progress" && imageBytes == 0))
	case "done", "unavailable":
		ok = imageBytes == 0
	}
	if !ok {
		return errors.New("invalid preview record")
	}
	return nil
}

func WriteRecord(w io.Writer, r Record) error {
	body, err := json.Marshal(r.Event)
	if err != nil {
		return err
	}
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(body)))
	binary.BigEndian.PutUint32(header[4:], uint32(len(r.Image)))
	for _, part := range [][]byte{header[:], body, r.Image} {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return nil
}
