package main

import "net"

// A PConn is a persistent TCP connection. It opens a connection lazily when used and reopens the connection
// on errors.
// It can also be thought of as a connection pool of size 1.
type PConn struct {
	c    *net.TCPConn
	addr string
}

func DialPConn(addr string) *PConn {
	return &PConn{addr: addr}
}

func (c *PConn) connect() error {
	conn, err := net.Dial("tcp", c.addr)
	if err != nil {
		return err
	}
	c.c = conn.(*net.TCPConn)
	if err := c.c.SetKeepAlivePeriod(tcpKeepAlivePeriod); err != nil {
		return err
	}
	return c.c.SetKeepAlive(true)
}

func (c *PConn) Write(b []byte) (int, error) {
	// For now, just do one retry -- we could introduce more with backoff, etc later.
	hadConn := c.c != nil
	if c.c == nil {
		if err := c.connect(); err != nil {
			return 0, err
		}
	}
	n, err := c.c.Write(b)
	if err != nil {
		// TODO: I could convert to net.Error and check Timeout() and/or Temporary() -- is that useful?
		if hadConn {
			c.c.Close()
			c.c = nil
			return c.Write(b)
		}
	}
	return n, err
}

func (c *PConn) Close() error {
	if c.c != nil {
		return c.c.Close()
	}
	return nil
}
