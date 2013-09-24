package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"sync"
	"testing"
	"time"

	. "launchpad.net/gocheck"
)

// ----------- Gocheck config ----------------

func Test(t *testing.T) { TestingT(t) }

type GostSuite struct{}

var _ = Suite(&GostSuite{})

// ----------- Setup ----------------

var (
	rec         = &recorder{}
	testUDPConn *net.UDPConn
	when        = time.Time{}
	flushChan   = make(chan time.Time)
)

func (s *GostSuite) SetUpSuite(c *C) {
	now = func() time.Time { return when }
	flushTicker = func() <-chan time.Time { return flushChan }
	go flush()
	go aggregate()
}

func (s *GostSuite) SetUpTest(c *C) {
	rec.Start()
	conf = &Conf{
		GraphiteAddr:    rec.Addr,
		Namespace:       "com.example",
		FlushIntervalMS: 2000, // Fake
	}
	udp, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		c.Fatal(err)
	}
	testUDPConn, err = net.ListenUDP("udp", udp)
	if err != nil {
		c.Fatal(err)
	}
	go clientServer(testUDPConn)
}

func (s *GostSuite) TearDownTest(c *C) {
	testUDPConn.Close()
	rec.Close()
}

type recorder struct {
	W        *sync.WaitGroup
	Addr     string
	Listener net.Listener
	Messages chan []byte
}

func (r *recorder) waitForMessage() []byte {
	ticker := time.NewTicker(time.Millisecond)
	for {
		select {
		case <-ticker.C:
			flushChan <- when
		case msg := <-r.Messages:
			return msg
		}
	}
}

func (r *recorder) Start() {
	r.W = &sync.WaitGroup{}
	// Listen on a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	r.Listener = listener
	r.Addr = listener.Addr().String()
	r.Messages = make(chan []byte)
	go func() {
		for {
			c, err := r.Listener.Accept()
			if err != nil {
				return
			}
			go r.Handle(c)
		}
	}()
}

func (r *recorder) Handle(c net.Conn) {
	r.W.Add(1)
	defer r.W.Done()
	msg, err := ioutil.ReadAll(c)
	if err != nil {
		return
	}
	r.Messages <- msg
}

func (r *recorder) Close() {
	r.Listener.Close()
	r.W.Wait()
}

func sendGostMessage(msg []byte) error {
	conn, err := net.Dial("udp", testUDPConn.LocalAddr().String())
	if err != nil {
		return err
	}
	conn.Write(msg)
	conn.Close()
	return nil
}

// ----------- Tests ----------------

func (s *GostSuite) TestCount(c *C) {
	if err := sendGostMessage([]byte("foobar:3|c")); err != nil {
		c.Fatal(err)
	}
	msg := rec.waitForMessage()
	fmt.Println(string(msg))
}
