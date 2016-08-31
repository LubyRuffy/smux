package smux

import (
	"io"
	"sync"

	"github.com/pkg/errors"
)

// Stream implements io.ReadWriteCloser
type Stream struct {
	id             uint32
	chNotifyReader chan struct{}
	sess           *Session
	frameSize      uint32
	die            chan struct{}
	mu             sync.Mutex
	buffer         []byte
}

func newStream(id uint32, frameSize uint32, chNotifyReader chan struct{}, sess *Session) *Stream {
	s := new(Stream)
	s.id = id
	s.chNotifyReader = chNotifyReader
	s.frameSize = frameSize
	s.sess = sess
	s.die = make(chan struct{})
	return s
}

// Read implements io.ReadWriteCloser
func (s *Stream) Read(b []byte) (n int, err error) {
READ:
	s.mu.Lock()
	if len(s.buffer) > 0 {
		n = copy(b, s.buffer)
		s.buffer = s.buffer[n:]
		return n, nil
	}

	f := s.sess.nioread(s.id)
	if f != nil {
		switch f.cmd {
		case cmdPSH:
			n = copy(b, f.data)
			if len(f.data) > n {
				s.buffer = make([]byte, len(f.data)-n)
				copy(s.buffer, f.data[n:])
			}
			s.mu.Unlock()
			return n, nil
		default:
			s.mu.Unlock()
			return 0, io.EOF
		}
	}

	s.mu.Unlock()
	select {
	case <-s.chNotifyReader:
		goto READ
	case <-s.die:
		return 0, errors.New("broken pipe")
	}
}

// Write implements io.ReadWriteCloser
func (s *Stream) Write(b []byte) (n int, err error) {
	frames := s.split(b, cmdPSH, s.id)
	for k := range frames {
		bts, _ := frames[k].MarshalBinary()
		if _, err = s.sess.lw.Write(bts); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// Close implements io.ReadWriteCloser
func (s *Stream) Close() error {
	select {
	case <-s.die:
		return errors.New("broken pipe")
	default:
		close(s.die)
		s.sess.streamClose(s.id)
		f := newFrame(cmdRST, s.id)
		bts, _ := f.MarshalBinary()
		s.sess.lw.Write(bts)
	}
	return nil
}

func (s *Stream) split(bts []byte, cmd byte, sid uint32) []Frame {
	var frames []Frame
	for uint32(len(bts)) > s.frameSize {
		frame := newFrame(cmd, sid)
		frame.data = make([]byte, s.frameSize)
		n := copy(frame.data, bts)
		bts = bts[n:]
		frames = append(frames, frame)
	}
	if len(bts) > 0 {
		frame := newFrame(cmd, sid)
		frame.data = make([]byte, len(bts))
		copy(frame.data, bts)
		frames = append(frames, frame)
	}
	return frames
}
