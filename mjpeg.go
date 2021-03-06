package mjpeg

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
	log "github.com/sirupsen/logrus"
)

// Decoder decode motion jpeg
type Decoder struct {
	r *multipart.Reader
	m sync.Mutex
}

// NewDecoder return new instance of Decoder
func NewDecoder(r io.Reader, b string) *Decoder {
	d := new(Decoder)
	d.r = multipart.NewReader(r, b)
	return d
}

// NewDecoderFromResponse return new instance of Decoder from http.Response
func NewDecoderFromResponse(res *http.Response) (*Decoder, error) {
	_, param, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	return NewDecoder(res.Body, strings.Trim(param["boundary"], "-")), nil
}

// NewDecoderFromURL return new instance of Decoder from response which specified URL
func NewDecoderFromURL(u string) (*Decoder, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return NewDecoderFromResponse(res)
}

// Decode do decoding
func (d *Decoder) Decode() (image.Image, error) {
	p, err := d.r.NextPart()
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(p)
}

type Stream struct {
	m        sync.Mutex
	s        map[chan []byte]struct{}
	Interval time.Duration
}

func NewStream() *Stream {
	return &Stream{
		s: make(map[chan []byte]struct{}),
	}
}

func NewStreamWithInterval(interval time.Duration) *Stream {
	return &Stream{
		s:        make(map[chan []byte]struct{}),
		Interval: interval,
	}
}

func (s *Stream) Close() error {
	log.Warn("[MJPEG] Closing stream")

	s.m.Lock()
	defer s.m.Unlock()
	for c := range s.s {
		close(c)
		delete(s.s, c)
	}
	s.s = nil
	return nil
}

func (s *Stream) Update(b []byte) error {
	s.m.Lock()
	defer s.m.Unlock()
	if s.s == nil {
		return errors.New("stream was closed")
	}
	for c := range s.s {
		select {
		case c <- b:
		default:
		}
	}
	return nil
}

func (s *Stream) add(c chan []byte) {
	s.m.Lock()
	s.s[c] = struct{}{}
	s.m.Unlock()
}

func (s *Stream) destroy(c chan []byte) {
	s.m.Lock()
	if s.s != nil {
		close(c)
		delete(s.s, c)
	}
	s.m.Unlock()
}

func (s *Stream) NWatch() int {
	return len(s.s)
}

func (s *Stream) Current() []byte {
	c := make(chan []byte)
	s.add(c)
	defer s.destroy(c)

	return <-c
}

func (s *Stream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := make(chan []byte)
	s.add(c)
	defer s.destroy(c)

	m := multipart.NewWriter(w)
	defer m.Close()

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+m.Boundary())
	w.Header().Set("Connection", "close")
	header := textproto.MIMEHeader{}
	starttime := fmt.Sprint(time.Now().Unix())

	for {
		time.Sleep(s.Interval)

		b, errch := <-c
		if !errch {
			log.Errorf("[MJPEG] Channel error: %s", errch)
			continue
		}

		header.Set("Content-Type", "image/jpeg")
		header.Set("Content-Length", fmt.Sprint(len(b)))
		header.Set("X-StartTime", starttime)
		header.Set("X-TimeStamp", fmt.Sprint(time.Now().Unix()))
		mw, err := m.CreatePart(header)
		if err != nil {
			log.Errorf("[MJPEG] Enc err: %s", err)
			continue
		}
		_, err = mw.Write(b)
		if err != nil {
			log.Errorf("[MJPEG] Write err: %s", err)

			if flusher, ok := mw.(http.Flusher); ok {
				flusher.Flush()
			}
			break // Stop and close if the writer is not available any more
		}
		if flusher, ok := mw.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	log.Debug("[MJPEG] exiting stream")
}
